# Production backends

How to stand up each production backend against this server, and what to
verify after deployment. Backend selection is env-driven
(`server/internal/config`): each production target is independent — set
`OBS_NATS_URL` alone and you get the real queue with SQLite + local blobs;
set them all and you get the full production shape. Any target left unset
keeps its dev fallback (embedded SQLite at `$OBS_DATA_DIR/observatory.db`,
durable file queue, local blob dir, no analytics sink), so a plain startup
with no env vars is exactly the dev mode.

The env-var table below duplicates `server/.env.example`, grouped by
backend.

## Postgres (metadata store, `storepg`)

- **Env:** `OBS_DATABASE_URL` — Postgres DSN,
  `postgres://user:pass@host:5432/observatory`. Unset = dev fallback
  (embedded SQLite).
- **Schema:** the Go-embedded DDL in `internal/storepg/schema.go` runs the
  migration at startup (`st.Migrate`). `infrastructure/001_postgres.sql`
  mirrors that same schema for provisioning a fresh instance by hand.
- Postgres holds the transactional metadata (projects, keys, processed/idempotency
  table, events, runs, spans, datasets, evaluations, alerts). Do not treat
  it as the only source of truth for high-volume event facts — that is the
  ClickHouse half.

## ClickHouse (analytics sink, `analyticsch`)

- **Env:** `OBS_CLICKHOUSE_DSN` — DSN naming the analytics database,
  `clickhouse://user:pass@host:8123/observatory`. Unset = dev fallback (no
  analytics mirroring).
- **Tuning knobs (optional, dev-safe defaults when unset):**
  `OBS_CH_FLUSH_INTERVAL` (default `2s`), `OBS_CH_FLUSH_MAX_ROWS` (default
  `500`), `OBS_CH_BUFFER_MAX_ROWS` (default `100000`). The sink batches and
  buffers inserts in bounded memory; on a ClickHouse outage rows buffer and
  overflow drops the oldest rows (logged, `ClickHouse.Dropped`), never
  blocking ingestion.
- **Schema:** the runtime authority is the DDL embedded in
  `server/internal/analyticsch/analytics.go` (`ChSchema`); the server
  executes it against the database in the DSN at startup.
  `infrastructure/001_clickhouse.sql` is a static deployment copy that
  mirrors it table-by-table (events: MergeTree; runs/spans:
  ReplacingMergeTree, version-keyed for at-least-once convergence).
  If the two ever diverge, the Go code wins — resync the SQL file from
  `ChSchema`.

## NATS JetStream (queue, `queuenats`)

- **Env:** `OBS_NATS_URL` — NATS server URL, `nats://host:4222`. Unset =
  dev fallback (durable file queue at `$OBS_DATA_DIR/queue`).
  `OBS_NATS_STREAM` — stream name, default `observatory.events`.
- **Topology (created lazily and idempotently by the server):** stream
  `obs.*.events` (file retention/storage); durable **pull consumer**
  `obs-workers`, explicit ack, 5-minute `ack_wait`, 1000 max-acking.
  Subjects are `obs.<project_id>.events`.
- **Delivery semantics: at-least-once, effective-once.** JetStream
  guarantees redelivery (a restarted process simply resumes consuming,
  unacked messages held server-side). Dedup is two-layer: JetStream
  `MessageID` (server-side idempotent publish of the content-addressed
  batch id) plus the pipeline's processed-table idempotency keys — so a
  redelivered batch is processed exactly once end-to-end, matching the
  file-queue contract.

## S3/MinIO (blob store, `blobs3`)

- **Env:** `OBS_S3_ENDPOINT` + `OBS_S3_BUCKET` (both required to enable),
  `OBS_S3_ACCESS_KEY`, `OBS_S3_SECRET_KEY`, `OBS_S3_REGION` (default
  `us-east-1`), `OBS_S3_PATH_STYLE=true` (path-style addressing for
  self-hosted MinIO; leave unset/false for hosted S3). Either endpoint or
  bucket unset = dev fallback (local blob dir).
- **Redaction:** objects are stored **already-redacted** by the pipeline's
  payload policy (capture off ⇒ the payload ref is dropped entirely;
  capture on ⇒ attributes are redacted before the row is written).
  `blobs3` never re-introduces raw content. The bucket is created if
  missing.

## Env-var table

| Backend | Var | Meaning / default | Unset (fail-safe) |
|---|---|---|---|
| — | `OBS_ADDR` | listen address, `:8080` | dev default |
| — | `OBS_DATA_DIR` | embedded data dir, `./data` | dev default |
| — | `OBS_LOG_LEVEL` | `info` | dev default |
| — | `OBS_SEED_DEMO` | seed demo project + sample run, `true` | dev default |
| — | `OBS_WEBHOOK_SECRET` | HMAC secret for signed webhooks, empty | dev (no signing check) |
| Postgres | `OBS_DATABASE_URL` | DSN of the metadata store | dev: embedded SQLite |
| ClickHouse | `OBS_CLICKHOUSE_DSN` | DSN of the analytics database | dev: no analytics sink |
| ClickHouse | `OBS_CH_FLUSH_INTERVAL` | periodic flush interval, `2s` | dev-safe default |
| ClickHouse | `OBS_CH_FLUSH_MAX_ROWS` | rows per flush trigger, `500` | dev-safe default |
| ClickHouse | `OBS_CH_BUFFER_MAX_ROWS` | bounded in-memory buffer, `100000` | dev-safe default |
| NATS | `OBS_NATS_URL` | NATS server URL | dev: durable file queue |
| NATS | `OBS_NATS_STREAM` | stream name, `observatory.events` | default applies |
| S3 | `OBS_S3_ENDPOINT` | S3/MinIO endpoint | dev: local blob dir |
| S3 | `OBS_S3_BUCKET` | bucket name | dev: local blob dir |
| S3 | `OBS_S3_ACCESS_KEY` | access key | (unset is valid for the dev fallback) |
| S3 | `OBS_S3_SECRET_KEY` | secret key | (as above) |
| S3 | `OBS_S3_REGION` | region, `us-east-1` | default applies |
| S3 | `OBS_S3_PATH_STYLE` | `true` for self-hosted MinIO | default applies (hosted S3) |

## Validation after deploy

The following must be confirmed against live services **after deployment**;
none of them is possible from the development environment and none of them
is claimed done here:

- **Postgres:** insert + read round-trip — create a project through the API,
  confirm it lands in the `projects` table; crash-restart the server and
  confirm state survives (metadata durability).
- **NATS JetStream:** confirm the stream and the `obs-workers` consumer were
  created; watch the consumer's pending/acked counters; confirm
  **consumer lag** returns to zero under sustained ingestion and that a
  redelivered batch (kill the worker mid-ack-window) is not processed twice
  (check the `processed` table for duplicate ids).
- **S3:** list the bucket and confirm each object key matches the event
  `payload_ref` the API returns; confirm objects contain redacted payloads
  only (spot-check against the source attributes).
- **ClickHouse:** confirm the three tables exist in the DSN's database and
  rows flow into `events`; force merges on `runs`/`spans`
  (`OPTIMIZE TABLE runs FINAL`) and confirm
  **ReplacingMergeTree dedup** collapses out-of-order/at-least-once
  duplicates to the most advanced row (highest `seq_max` / `version`);
  confirm buffer-overflow drops are logged when the sink is paused.
