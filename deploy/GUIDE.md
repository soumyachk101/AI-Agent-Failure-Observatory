# Deployment guide

Three shapes, in order of ambition.

## 1. Local dev (no Docker required)

The default Go build is fully self-contained: embedded SQLite + file queue +
local blob dir, all under `OBS_DATA_DIR` (default `./data`).

```sh
cd server
go run ./cmd/observatory --data-dir ./data --addr :8080
# server on :8080; /healthz returns 200 when ready.
```

Every env var the server actually reads (from `server/cmd/observatory/main.go`):

| Var | Default | Purpose |
| --- | --- | --- |
| `OBS_ADDR` | `:8080` | listen address |
| `OBS_DATA_DIR` | `./data` | where SQLite/queue/blobs live |
| `OBS_LOG_LEVEL` | `info` | debug/info/warn/error |
| `OBS_SEED_DEMO` | `true` | seed the `demo` project + sample run |
| `OBS_WEBHOOK_SECRET` | `` | HMAC secret for signed webhooks |

## 2. Single-host Docker Compose

One host, one compose file. The dev server runs as the default service; the
four backends (Postgres, ClickHouse, NATS, MinIO) run under the `prod`
profile so a plain `docker compose up server` does not start a pile of
empty databases.

```sh
# Dev: just the server, data in a named volume.
docker compose up server

# Dev + all the (currently unused) backends, to pre-provision them:
docker compose --profile prod up
```

Ports on the host: `8080` (server), plus `5432`/`8123`/`9000`/`4222`/`9001`
for the prod-profile services. The `server` service exposes `OBS_DATA_DIR` as
`/observatory/data` (a named volume, `observatory-data`), so SQLite/queue/
blobs survive restarts.

> Note: the server build is packaged in `server/Dockerfile` (multi-stage Go,
> CGO off, distroless static, non-root). It runs the **default** binary,
> which is the dev path regardless of which backends are up. See
> `NOTES-AND-GAPS.md` for why.

## 3. Split managed-services production — *target shape, not provisioned
### in this environment*

> This section is a **target shape, not provisioned in this environment.**
> No external Postgres/ClickHouse/NATS/S3 was stood up for this product; the
> `infrastructure/` head owns the DDL and any managed resource definitions.
> Nothing here claims a managed service exists.

In a managed split, each backend is a hosted resource and the server is a
stateless service that points at them purely via env:

- **Postgres** → `OBS_DATABASE_URL` (reserved) with a managed instance; the
  relational tables only.
- **ClickHouse** → `OBS_CLICKHOUSE_DSN` (reserved); analytics tables.
- **NATS JetStream** → `OBS_NATS_URL` (reserved); the durable event stream.
- **S3-compatible bucket** → `OBS_S3_ENDPOINT` / `OBS_S3_BUCKET` /
  `OBS_S3_ACCESS_KEY` / `OBS_S3_SECRET_KEY` (reserved); opt-in payloads.

Until the non-embedded builds read those `OBS_*` names, the compose file
carries `PRODUCTION_OBS_*` placeholders that map to each of the above. The
server itself remains stateless: no local volume, `OBS_DATA_DIR` unused, and
all config via env. This is the shape the build-tagged code in
`internal/queue/nats.go` and `store3.go` is preparing for.
