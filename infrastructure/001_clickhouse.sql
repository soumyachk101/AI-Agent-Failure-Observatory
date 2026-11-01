-- infrastructure/001_clickhouse.sql
-- Production analytics target. Column names mirror server/internal/store's
-- SQLite schema (schema.go) so query code ports between dev and prod.
--
-- Split (spec 9): ClickHouse holds the high-volume event *facts*;
-- Postgres holds transactional metadata. Do NOT use ClickHouse as the only
-- source of truth for accounts, keys or billing.
--
-- Upsert model: the dev store upserts run/span aggregates in place. On
-- ClickHouse these are ReplacingMergeTree tables keyed on the (project, id)
-- grain, with MAX()/MIN() merge functions so out-of-order, at-least-once
-- redelivery converges. The pipeline's idempotency keys guarantee a batch is
-- processed once, so duplicates collapse to the last (or merged) row.

-- ---------------------------------------------------------------------------
-- events: raw, append-only. Insert-once, never updated.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS observatory.events
(
    project_id      String,
    id              String,
    run_id          String,
    seq             UInt64,
    type            String,
    ts              DateTime64(9, 'UTC'),
    span_id         String,
    parent_span_id  String,
    agent_name      String,
    agent_version   String,
    model           String,
    provider        String,
    attrs           String,          -- JSON blob (matches SQLite `attrs TEXT`)
    payload_ref     String,
    received_at     DateTime64(9, 'UTC')
)
ENGINE = MergeTree
PARTITION BY toYYYYMM(ts)
ORDER BY (project_id, run_id, seq, id)
TTL ts + INTERVAL 30 DAY;           -- default retention; override per project

-- ---------------------------------------------------------------------------
-- runs: per-run aggregate, written by the normalizer. ReplacingMergeTree so
-- concurrent/out-of-order writes on the same run converge. version = seq_max
-- so the most advanced row wins.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS observatory.runs
(
    id               String,
    project_id       String,
    status           String DEFAULT 'open',
    agent_name       String,
    agent_version    String,
    model            String,
    provider         String,
    environment      String,
    started_at       DateTime64(9, 'UTC') DEFAULT toDateTime64(0, 9),
    ended_at         DateTime64(9, 'UTC') DEFAULT toDateTime64(0, 9),
    duration_ms      UInt64,
    tokens_in        UInt64,
    tokens_out       UInt64,
    usage_source     String,         -- 'provider' | 'estimate' | ''
    cost_usd         Float64,
    cost_estimated   UInt8,
    retries          UInt64,
    event_count      UInt64,
    seq_max          UInt64,
    created_at       DateTime64(9, 'UTC')
)
ENGINE = ReplacingMergeTree(seq_max)
PARTITION BY toYYYYMM(created_at)
ORDER BY (project_id, id);

-- ---------------------------------------------------------------------------
-- spans: materialised span aggregates (assembler). ReplacingMergeTree keyed on
-- (project_id, id); version = seq_end mirrors the SQLite upsert's
-- MIN(seq_start)/MAX(seq_end) merge logic in store3.go.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS observatory.spans
(
    id                  String,
    project_id          String,
    run_id              String,
    parent_span_id      String,
    name                String,
    kind                String,      -- agent | model | tool | retrieval | run
    status               String,
    agent_name          String,
    agent_version       String,
    tool_name           String,
    tool_version        String,
    tool_args_hash      String,
    tool_schema_version String,
    model               String,
    provider            String,
    usage_source        String,
    tokens_in           UInt64,
    tokens_out          UInt64,
    cost_usd            Float64,
    cost_estimated      UInt8,
    retries             UInt64,
    error_class         String,
    started_at          DateTime64(9, 'UTC') DEFAULT toDateTime64(0, 9),
    ended_at            DateTime64(9, 'UTC') DEFAULT toDateTime64(0, 9),
    duration_ms         UInt64,
    seq_start           UInt64,
    seq_end             UInt64
)
ENGINE = ReplacingMergeTree(seq_end)
PARTITION BY toYYYYMM(started_at)
ORDER BY (project_id, id)
TTL started_at + INTERVAL 30 DAY;

-- Optional materialised view for timeline latency dashboards. Uncomment when
-- a dashboard needs pre-agg rather than scanning spans.
--
-- CREATE MATERIALIZED VIEW observatory.span_latency_hourly
--   ENGINE = SummingMergeTree
--   PARTITION BY toYYYYMMH(ts)
--   ORDER BY (project_id, model) AS
--   SELECT project_id, model, toStartOfHour(started_at) AS ts,
--          avg(duration_ms) AS avg_ms, count() AS n
--   FROM observatory.spans GROUP BY project_id, model, ts;

-- Queries to remember:
--   * Timeline for a run:
--     SELECT * FROM observatory.spans
--     WHERE project_id = ? AND run_id = ? ORDER BY seq_start, id
--   * Latest run aggregate (dedup the Replacing tree before finalise):
--     SELECT * FROM observatory.runs
--     WHERE project_id = ? AND id = ? ORDER BY seq_max DESC LIMIT 1
--   * Force merge for small tables / before a report:
--     OPTIMIZE TABLE observatory.runs FINAL
