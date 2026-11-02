-- infrastructure/001_clickhouse.sql
--
-- MIRRORS THE RUNTIME SCHEMA. This file is a static deployment copy of the
-- embedded DDL in server/internal/analyticsch/analytics.go (const ChSchema),
-- which is the runtime authority: the server executes ChSchema itself on
-- startup (ensureSchema) against the database named in OBS_CLICKHOUSE_DSN.
-- Keep the two in sync table-by-table; if they diverge, the Go code wins —
-- update this file from ChSchema, not the other way around.
--
-- Tables are unqualified here because the DSN carries the database
-- (e.g. clickhouse://user:pass@host:8123/observatory); the CREATE DATABASE
-- below just guarantees it exists on a fresh instance.
--
-- Split (spec 9): ClickHouse holds the high-volume event *facts*;
-- Postgres holds transactional metadata. Do NOT use ClickHouse as the only
-- source of truth for accounts, keys or billing.
--
-- Upsert model: runs and spans are ReplacingMergeTree so out-of-order,
-- at-least-once redelivery converges (version = seq_end / seq_max, newest
-- row wins); events are plain MergeTree, append-only, deduped by the
-- pipeline's processed-table idempotency keys, not by the table itself.

CREATE DATABASE IF NOT EXISTS observatory;

-- ---------------------------------------------------------------------------
-- events: raw, append-only. Insert-once, never updated.
-- event_month/ingested_month are UInt16 YYYYMM partition aids the Go writer
-- computes per row (ym helper) — required by PARTITION BY.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS events(
  project_id String,
  id String,
  run_id String,
  seq UInt64,
  type String,
  ts DateTime64(9),
  span_id String,
  parent_span_id String,
  agent_name String,
  agent_version String,
  model String,
  provider String,
  attrs String,
  payload_ref String,
  received_at DateTime64(9),
  event_month UInt16,
  ingested_month UInt16
) ENGINE = MergeTree
PARTITION BY event_month
ORDER BY (project_id, run_id, seq, ingested_month)
TTL toDateTime(ts) + INTERVAL 90 DAY;

-- ---------------------------------------------------------------------------
-- spans: materialised span aggregates. ReplacingMergeTree(version); version
-- is set to the ended_at unix time by the writer, so the most advanced row
-- wins at merge. span_month is a partition aid computed by the writer.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS spans(
  id String,
  project_id String,
  run_id String,
  parent_span_id String,
  name String,
  kind String,
  status String,
  agent_name String,
  agent_version String,
  tool_name String,
  tool_version String,
  tool_args_hash String,
  tool_schema_version String,
  model String,
  provider String,
  usage_source String,
  tokens_in Int64,
  tokens_out Int64,
  cost_usd Float64,
  cost_estimated UInt8,
  retries Int64,
  error_class String,
  started_at DateTime64(9),
  ended_at DateTime64(9),
  duration_ms Int64,
  seq_start UInt64,
  seq_end UInt64,
  version UInt32,
  span_month UInt16
) ENGINE = ReplacingMergeTree(version)
PARTITION BY (project_id, span_month)
ORDER BY (project_id, run_id, id);

-- ---------------------------------------------------------------------------
-- runs: per-run aggregates folded from events by the writer
-- (ReplacingMergeTree on seq_max; the highest seq_max row converges to the
-- most advanced fold). run_month is a partition aid.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS runs(
  id String,
  project_id String,
  status String,
  agent_name String,
  agent_version String,
  model String,
  provider String,
  environment String,
  started_at DateTime64(9),
  ended_at DateTime64(9),
  duration_ms Int64,
  tokens_in Int64,
  tokens_out Int64,
  usage_source String,
  cost_usd Float64,
  cost_estimated UInt8,
  retries Int64,
  event_count Int64,
  seq_max UInt64,
  created_at DateTime64(9),
  run_month UInt16
) ENGINE = ReplacingMergeTree(seq_max)
PARTITION BY (project_id, run_month)
ORDER BY (project_id, id);

-- Queries to remember (run against the observatory database):
--   * Timeline for a run:
--     SELECT * FROM spans WHERE project_id = ? AND run_id = ?
--       ORDER BY seq_start, id
--   * Latest run aggregate (dedup the Replacing tree before finalise):
--     SELECT * FROM runs WHERE project_id = ? AND id = ?
--       ORDER BY seq_max DESC LIMIT 1
--   * Force merge for small tables / before a report:
--     OPTIMIZE TABLE runs FINAL
