# Deployment notes

How to bring up the control plane with a local dev profile, and what changes
for the single-host prod profile. The `deploy/` folder holds the user-facing
runbook (`GUIDE.md`) and the packaging gaps log (`NOTES-AND-GAPS.md`); this
file is the DDL/infra-specific counterpart.

## What you are deploying

- **Server** — the Go control plane (`server/`), built by `server/Dockerfile`
  (multi-stage, CGO off, distroless static, non-root).
- **Storage** — three backends, dev vs prod:

  | Subsystem | Dev (default, embedded) | Prod (reserved env) |
  | --- | --- | --- |
  | Metadata store | SQLite at `$OBS_DATA_DIR/observatory.db` | Postgres (`PRODUCTION_OBS_DATABASE_URL` → `OBS_DATABASE_URL`) |
  | Analytics | SQLite (same file) | ClickHouse (`PRODUCTION_OBS_CLICKHOUSE_DSN` → `OBS_CLICKHOUSE_DSN`) |
  | Queue | file queue at `$OBS_DATA_DIR/queue` | NATS JetStream (`PRODUCTION_OBS_NATS_URL` → `OBS_NATS_URL`) |
  | Object store | local dir at `$OBS_DATA_DIR/blobs` | S3/MinIO (`PRODUCTION_OBS_S3_*` → `OBS_S3_*`) |

## Provisioning order (prod profile)

Run the DDL **before** starting the server against prod backends:

```bash
# 1. Postgres metadata + dedup (transactional data).
psql "$PRODUCTION_OBS_DATABASE_URL" -f infrastructure/001_postgres.sql

# 2. ClickHouse analytics (events / spans / run aggregates).
clickhouse client --dsn "$PRODUCTION_OBS_CLICKHOUSE_DSN" \
  < infrastructure/001_clickhouse.sql

# 3. NATS JetStream stream + durable consumer.
#    (provisioning commands in infrastructure/QUEUE-AND-OBJECTSTORE.md)

# 4. S3/MinIO bucket.
mc mb -p local/observatory        # or equivalent
```

The ClickHouse DDL is idempotent (`CREATE TABLE IF NOT EXISTS`); re-running
it after a schema change is safe but does not evolve existing tables — use
ALTERs for those.

## Dev profile (no provision needed)

`docker compose up server` in the default profile uses the embedded SQLite
and file queue; the server creates its schema on boot (`store.Migrate`) and
seeds the demo project/key + sample run when `OBS_SEED_DEMO=true`. No DDL
files are required in this mode.

## Data flow and idempotency

Regardless of backend, ingestion is **at-least-once + idempotent ⇒
effective-once**: a batch is content-addressed at the queue layer
(`MessageID` / NATS `jetstream.MessageID`) and again at the pipeline layer
(per-event idempotency key, `processed` table). Retrying a redelivered batch
is a no-op, so you can run the dev-mode synchronous pipeline *and* the queue
worker without double-counting.

## Retention and payload policy

- Events/spans TTL in ClickHouse (`TTL ts + INTERVAL 30 DAY`, override per
  project via `projects.retention_days`).
- Payloads are opt-in and redacted; `payload_ref` is nulled when a project's
  capture is off, so raw content is never persisted.

## Sample data

Seeded demo data (project `demo`, key `demo-key`, `SampleLoopRun`) is
labelled `sample: "true"` on every event. It exists for first-launch UI
structure and must never be treated as real analytics — the UI and any export
must respect the `sample` marker. See `docs/KNOWN-LIMITATIONS.md` §3.

## Gaps you will hit (read before wiring prod)

- **G1** — `server/go.sum` is absent; run `cd server && go mod tidy` (owned
  by the head that owns `server/go.mod`) before `server/Dockerfile`'s
  `go mod download` works on a clean build.
- **G2 / G5** — the default binary is dev-path only; `PRODUCTION_OBS_*` are
  reserved placeholders, not yet read. See `deploy/NOTES-AND-GAPS.md`.
