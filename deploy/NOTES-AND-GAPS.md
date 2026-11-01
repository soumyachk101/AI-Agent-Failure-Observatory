# Notes and gaps — packaging

Recorded by the deployment-packaging head. None of the code below was
modified; these are things the lead should know when wiring up.

## Gaps

### G1. No `server/go.sum` in the tree
`server/go.mod` requires `modernc.org/sqlite v1.29.10` but there is no
`go.sum` next to it. `server/Dockerfile` runs `go mod download`, which will
fail on a clean build without the lock file. The lead (or a head owning
server/) needs to generate `go.sum` — `cd server && go mod tidy` — before the
Dockerfile is smoke-tested. This is a pre-existing missing artifact, not
something I fabricated.

### G2. The default binary is dev-path only
`cmd/observatory/main.go` unconditionally calls `store.OpenSQLite`,
`queue.NewLocalQueue`, and `blob.NewLocalObjectStore`. There is no
`OBS_DATABASE_URL`, `OBS_CLICKHOUSE_DSN`, `OBS_NATS_URL`, or `OBS_S3_*`
reading in the running code. The Postgres/ClickHouse/NATS/S3 wiring exists
only as build-tagged/aspirational code (`internal/queue/nats.go` is
`//go:build nats`; `store3.go` upserts carry `MAX/MIN` comments that only
match ClickHouse `ReplacingMergeTree` semantics).

**Consequence:** the `prod` profile in `docker-compose.yml` brings up
postgres/clickhouse/nats/minio but the server does NOT talk to them — it
still uses the embedded SQLite at `OBS_DATA_DIR`. The compose files'
`PRODUCTION_OBS_*` vars are documented placeholders for a future non-embedded
build; renaming them to `OBS_*` is a follow-up the non-embedded-build head
owns. This is recorded, not fixed.

### G3. `infrastructure/` does not exist
The product tree has only `frontend/`, `sdk/`, `server/`. `deploy/CLICKHOUSE-POSTGRES-NATS.md`
is written against the code's interfaces (store/queue/blob contracts and the
SQLite schema in `internal/store/schema.go`), not against `infrastructure/`
files. The `infrastructure/` head owns the DDL; this document is a
predecessor, not a replacement.

### G4. Frontend is not containerizable today
`frontend/src/` has `main.tsx`, `styles.css`, `types.ts` only.
`main.tsx` imports `./App` which does not exist, and
`vite.config.ts` references `src/test/setup.ts` which does not exist. The
`npm run build` script (`tsc -b && vite build`) will fail. **No
`frontend/Dockerfile` was added.** The `web-frontend` block in
`docker-compose.yml` is a commented-out template with a placeholder comment,
as the brief requires. When the frontend head lands `App.tsx` and the test
setup, a `frontend/Dockerfile` (multi-stage node:22 → nginx:alpine with
`/v1` proxying to `http://server:8080`) should be added; the compose block
is ready to uncomment at that point.

### G5. `PRODUCTION_OBS_*` env var names are a naming gap
The compose file introduces `PRODUCTION_OBS_DATABASE_URL`,
`PRODUCTION_OBS_CLICKHOUSE_DSN`, `PRODUCTION_OBS_NATS_URL`,
`PRODUCTION_OBS_S3_{ENDPOINT,BUCKET,ACCESS_KEY,SECRET_KEY}`. These are
**placeholders** that the code does not read today. When the non-embedded
builds land, they will presumably be renamed to `OBS_*` (matching the
existing `OBS_ADDR`/`OBS_DATA_DIR`/etc. prefix pattern) and the compose
block rewritten to pass them through. That rename is a code change and is
not part of this task.

## Files this head created

- `server/Dockerfile` — multi-stage Go 1.27 build, CGO off, distroless
  static, non-root, EXPOSE 8080. Depends on G1 being closed.
- `server/.dockerignore` — companion to the Dockerfile.
- `docker-compose.yml` — dev profile (default) + prod profile
  (postgres/clickhouse/nats/minio) + commented web-frontend block.
- `deploy/CLICKHOUSE-POSTGRES-NATS.md` — production target setup notes,
  written against the code's interfaces (G3).
- `deploy/GUIDE.md` — local dev → single-host compose → managed-shape
  notes; the managed section is labelled *target shape, not provisioned in
  this environment*.
- `deploy/NOTES-AND-GAPS.md` — this file.

## Smoke-test command for the lead

Once G1 is closed (`go mod tidy` in `server/`), the smoke test is:

```sh
cd /Users/soumyachakraborty/Documents/Projects_Money/Developer-Products/AI-Agent-Failure-Observatory
docker compose build server
docker compose run --rm server /observatory --data-dir /observatory/data --seed-demo=true
# expect: "observatory listening on :8080"; then hit /healthz:
#   curl http://127.0.0.1:8080/healthz   (when run with `docker compose up server`)
```

This exercises the Dockerfile build + the default dev-path runtime +
`/healthz`, which is the cheapest end-to-end check that the container is
shippable. It does not exercise G2/G5 (the prod backends) — that needs the
non-embedded build first.
