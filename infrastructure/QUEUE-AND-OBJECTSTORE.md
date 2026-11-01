# NATS JetStream + S3-compatible object store

Production queue and object-store targets. The dev fallbacks are the file
queue (`server/internal/queue/local.go`) and the local directory blob
(`server/internal/blob/blob.go`). Both sit behind the same contracts
(`queue.Queue`, `blob.ObjectStore`), so switching is an adapter swap — the
pipeline code is unchanged.

## NATS JetStream (replaces the file queue)

Wired in `server/internal/queue/nats.go` behind the `//go:build nats` tag.
It is reference code that adds **no dependency to the default build**; build
with `-tags nats` and the NATS SDK to use it.

| Setting | Value | Env |
| --- | --- | --- |
| URL | `nats://host:4222` | `PRODUCTION_OBS_NATS_URL` → config `OBS_NATS_URL` |
| Stream | `observatory.events` | `OBS_NATS_STREAM` (default `observatory.events`) |
| Subjects | `obs.<project_id>.events` | — |
| Retention | WorkFileRetention | — |
| Storage | FileStorageType | — |
| Durable consumer | `obs-workers` (generous `ack_wait`, bounded `max_ack_pending`) | — |

### Provisioning

```bash
# Stand up a cluster (single-node example):
nats-server -c /etc/nats/nats-server.conf

# Create the stream:
nats stream add observatory.events \
  --subjects "obs.*.events" \
  --retention workfile \
  --storage file \
  --max-age 72h

# Create the durable consumer:
nats consumer add observatory.events obs-workers \
  --push \
  --ack-wait 2m \
  --max-ack-pending 2048
```

### How it gives effective-once

The dev file queue is at-least-once (in-flight messages are re-queued on
restart). JetStream is the same contract, durable. Two independent dedup
layers collapse it to effective-once:

1. **Queue layer** — publish with `jetstream.MessageID(m.ID)`. `m.ID` is
   content-addressed (`queue.MessageID`: `project + "." + sha256(payload)[:8]`),
   and the SDK's batch idempotency key is reused for resubmits, so a retried
   batch is deduped server-side.
2. **Pipeline layer** — the `processed` table's per-event idempotency key
   (`event.Envelope.IdempotencyKey`). Even if the queue redelivers, the
   normalizer's `FilterUnseen` drops already-processed events.

The `store.Engine` dedup is the final guard, so any of these layers catching
a duplicate is a no-op, not an error.

### Build-time note

To run the JetStream adapter: `go build -tags nats ./...` and point
`PRODUCTION_OBS_NATS_URL` (config `OBS_NATS_URL`) at the cluster. The
reference file leaves `Pull`/`Ack` to implement against `jetstream.PullAsync`
— see `local.go` for the exact semantics to match (at-least-once, re-queue
in-flight on restart).

## S3-compatible object store (replaces the local blob dir)

The `blob.ObjectStore` contract (`Put/Get/Has`, project-scoped) is implemented
by the local dir in dev. The production target is any S3-compatible store
(Amazon S3, MinIO). The compose file runs MinIO; use the S3 SDK with
`path-style` for MinIO.

| Setting | Value | Env |
| --- | --- | --- |
| Endpoint | `http://minio:9000` | `PRODUCTION_OBS_S3_ENDPOINT` → config `OBS_S3_ENDPOINT` |
| Region | `us-east-1` | `OBS_S3_REGION` |
| Bucket | `observatory` | `PRODUCTION_OBS_S3_BUCKET` → `OBS_S3_BUCKET` |
| Access key | — | `PRODUCTION_OBS_S3_ACCESS_KEY` → `OBS_S3_ACCESS_KEY` |
| Secret key | — | `PRODUCTION_OBS_S3_SECRET_KEY` → `OBS_S3_SECRET_KEY` |
| Path style (MinIO) | `true` | `OBS_S3_PATH_STYLE` |

### Object layout and tenancy

The local store lays objects out as `<root>/<project>/<ref>` with atomic
writes and a `sanitizeRef` that forbids traversal. Keep the same key shape in
S3: `project_id/ref`. A missing project scope is an **error, not a fallback to
a shared bucket** — that is the tenant-isolation guarantee (spec NFR
Isolation). Preserve it when you implement the S3 adapter.

### Opt-in payloads only

S3 stores **only** opt-in, already-redacted payloads (spec: separate
metadata from large payloads). The analytics rows carry a `payload_ref`; the
actual bytes live here and are only written when a project's
`payload_capture = 'on'`. When capture is off, the pipeline nulls the
`payload_ref` so raw content is never persisted anywhere.

## Reserved env names

The exact names above (`PRODUCTION_OBS_*`) are the reserved ones recorded in
`deploy/NOTES-AND-GAPS.md` (G5). They are placeholders: the running binary
does not read them today (G2). A sibling head is wiring the env selection in
`internal/config` / the adapters; keep these names so the compose file, the
config, and the DDL stay in agreement. Do not rename them unilaterally.
