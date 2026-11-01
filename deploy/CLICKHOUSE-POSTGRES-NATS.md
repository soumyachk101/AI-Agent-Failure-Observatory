# Production targets: ClickHouse, Postgres, NATS JetStream

> `infrastructure/` does not exist yet (only `frontend/`, `sdk/`, `server/`
> are present in the tree today). This document is therefore written against
> what the code's interfaces expect, not against `infrastructure/` files.

These three are the *migration targets* for the dev-mode fallbacks. None of
them is wired into the default binary today: the Go control plane is
hardcoded to embedded SQLite, the file queue, and the local blob dir
(`server/cmd/observatory/main.go`). Postgres/ClickHouse/NATS/S3 exist only as
build-tagged reference code (`internal/queue/nats.go`, the `store3.go`
upserts). This document describes the intended production shape so the
`infrastructure/` head and a future non-embedded build can agree on names.

## Postgres
- Holds: `projects`, `api_keys`, `processed`, and the relational span/run
  aggregates that outgrow SQLite single-writer.
- DSN: `postgres://user:pass@host:5432/observatory`.
- The SQLite schema in `internal/store/schema.go` is the reference; column
  names are intentionally kept portable (the file notes it mirrors
  `infrastructure/001_postgres.sql`). Provision that DDL first.

## ClickHouse
- Holds: high-volume append-only analytics — raw events, `spans`, and run
  aggregates that are written once and queried for dashboards.
- DSN: `clickhouse://user:pass@host:8123/observatory`.
- Use `MergeTree` families keyed by `(project_id, toYYYYMM(ts))`. The
  `UpsertSpan` upserts in `store3.go` (with `MAX`/`MIN` merge logic) map onto
  a `ReplacingMergeTree` rather than in-place updates.

## NATS JetStream
- Replaces the file queue (`internal/queue/local.go`) with at-least-once,
  ordered, durable delivery.
- Stream: `observatory.events`, subjects `obs.<project>.events`, file
  retention, durable consumer `obs-workers` (see `internal/queue/nats.go`).
- JetStream's server-side `MessageID` dedup + the pipeline's own idempotency
  keys give effective-once processing.

## How the server is pointed at each (when a non-embedded build lands)
The compose file currently sets `PRODUCTION_OBS_*` placeholders. These are NOT
read by the default binary — they are reserved names that the future
build-tagged builds should read. Record of the mapping is in
`deploy/NOTES-AND-GAPS.md`. Do not treat `PRODUCTION_OBS_*` as live config.
