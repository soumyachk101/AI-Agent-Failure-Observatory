# Architecture

The AI Agent Failure Observatory is a Go control plane with a TypeScript
instrumentation SDK, a dense trace-explorer frontend, and pluggable storage.
Everything is organised around one idea: an agent run is a stream of
structured events, and every diagnosis must be explainable from that
evidence.

## Data flow

```
Agent application
  ├── @ensembyte/observatory (TypeScript SDK)
  │     bounded buffer → batched POST /v1/ingest/events (Bearer project key)
  └── (future) Python / OpenTelemetry adapters
            |
            v
   Go ingestion API (internal/api, internal/router)
     auth: project key (hashed, tenant-scoped) → project
     rate limit: per-project token bucket (burst + per-minute)
            |
   ┌────────┴────────┐
   │  synchronous     │  durable queue
   │  pipeline        │  (at-least-once)
   │  (dev immediacy) │
   └────────┬─────────┘
            v
   pipeline.Normalizer
     1. validate + normalise envelopes        (event.Envelope)
     2. idempotency: drop already-processed   (store.processed)
     3. payload policy: redact / drop refs    (event.Redact)
     4. persist event rows                    (store.events)
     5. assemble span hierarchy + usage       (assembler.go)
     6. deterministic detectors               (rules.go)
            |
            v
   storage
     ClickHouse-family: events, spans, run aggregates (analytics facts)
     Postgres-family:   projects, keys, datasets, alerts (transactional)
     object store:      opt-in redacted payloads (metadata vs payload split)
            |
            v
   failure intelligence + evaluation + alerts
     internal/pipeline/rules.go   four detectors + context-loss signal
     internal/evalsvc             datasets, deterministic scoring, safe replay
     internal/alertsvc            rules → signed webhook deliveries
            |
            v
   Query API → Run Explorer / Failures / Evaluations frontend
```

At-least-once delivery plus idempotent processing yields **effective-once**:
a batch is content-addressed (queue `MessageID` + pipeline idempotency keys),
so retries and the dev-mode dual path (synchronous pipeline *and* queue
worker) collapse to one persisted result.

## Components

| Component | Path | Responsibility |
| --- | --- | --- |
| Event contract | `server/internal/event` | Envelope, validation, canonical-JSON hashing, deterministic failure signatures, redaction policy |
| Pipeline | `server/internal/pipeline` | Normalise → dedup → persist → assemble → detect |
| Detectors | `pipeline/rules.go` | 8.1 repeated-tool loop, 8.2 state oscillation, 8.3 budget, 8.4 failure clustering, 8.5 context loss |
| Assembler | `pipeline/assembler.go` | Parent/child span hierarchy, per-span duration/usage, `usage.source` labelling |
| Storage | `server/internal/store` | `Engine` interface + embedded SQLite dev store; column names mirror `infrastructure/` DDL |
| Queue | `server/internal/queue` | Durable at-least-once contract; file queue (dev) + NATS JetStream reference (`//go:build nats`) |
| Blob | `server/internal/blob` | `ObjectStore` contract; local dir (dev) + S3-compatible target (`infrastructure/`) |
| Config | `server/internal/config` | Reads every `OBS_*` env var; selects dev vs production adapters |
| API | `server/internal/api` | Ingest, run/timeline/failure queries, datasets, evaluations, replays, alerts, usage; worker loop |
| Evaluation | `server/internal/evalsvc` | Versioned datasets, deterministic scoring (LLM judge supplementary), mocked-tool replay |
| Alerts | `server/internal/alertsvc` | Rule evaluation over persisted evidence, HMAC-signed webhook delivery with monotonic nonce |
| SDK | `sdk/` | `ObservatoryClient` (bounded buffer, jittered retry, batch idempotency) + `Observatory`/`Run`/span instrumentation |

## Ordering model

Wall-clock time is never authoritative. The SDK assigns strictly increasing
`sequence` numbers per run; the assembler and timeline sort by `sequence`, so
out-of-order delivery (async agents, multiple processes) is safe. Timestamps
are metadata; `sequence` is the ordering key.

## Idempotency model

Two layers, both content-addressed:

1. **Per-event key** — `project_id | run_id | span_id | event_type |
   sequence | event_id` (pipe-join, see `event.Envelope.IdempotencyKey`).
   Recomputed by the pipeline; the `processed` table makes redeliveries
   no-ops. The SDK computes the same key so redelivered logical events are
   self-describing.
2. **Batch/transport key** — the SDK's `batchIdempotencyKey` (FNV-1a over the
   sorted identity tuples) is sent as the request's `idempotency_key` and
   becomes the queue `MessageID` / NATS `jetstream.MessageID`, so the queue
   layer dedupes a retried *batch* before it ever reaches the pipeline.

## Tenancy and privacy

- Every row carries `project_id`; every project-scoped query in `store`,
  `api`, `alertsvc` and `evalsvc` filters by it. A key from one tenant cannot
  read another's data (verified by `store` tests).
- Keys are hashed (SHA-256) at rest; plaintext is shown once and never
  persisted.
- Payload capture is **off by default**; when a project opts in, both the SDK
  (client-side) and the pipeline (server-side) redact before anything is
  persisted. Log output is redacted too (`logx`).

## Storage split (NFR "separate metadata from large payloads")

- **Analytics facts** (events, spans, run aggregates): high-volume,
  append-oriented → ClickHouse in production (dev: SQLite in one embedded
  file).
- **Transactional metadata** (projects, keys, datasets, alert rules):
  Postgres in production (dev: the same embedded file; column names are kept
  portable across both).
- **Large payloads**: S3-compatible object store; the analytics rows keep
  only a `payload_ref`.

## Production backends and env selection

`internal/config` selects the backend family from the environment:

| Reserved env (compose `PRODUCTION_OBS_*`) | Config field | Adapter |
| --- | --- | --- |
| `PRODUCTION_OBS_DATABASE_URL` | `OBS_DATABASE_URL` → `PostgresDSN` | Postgres metadata store |
| `PRODUCTION_OBS_CLICKHOUSE_DSN` | `OBS_CLICKHOUSE_DSN` → `ClickHouseDSN` | ClickHouse analytics |
| `PRODUCTION_OBS_NATS_URL` | `OBS_NATS_URL` → `NATSURL` | NATS JetStream queue |
| `PRODUCTION_OBS_S3_{ENDPOINT,BUCKET,ACCESS_KEY,SECRET_KEY}` | `OBS_S3_*` → `S3*` | S3/MinIO object store |

See `infrastructure/DEPLOYMENT-NOTES.md` and `deploy/NOTES-AND-GAPS.md` for
the reserved-name record and current wiring gaps.
