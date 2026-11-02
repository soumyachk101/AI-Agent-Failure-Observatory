# AI Agent Failure Observatory

Turn an opaque agent run into an evidence-backed execution timeline. The
Observatory captures structured traces for multi-step agent runs — model
calls, tool calls, handoffs, state transitions, retries and evaluation
outcomes — so you can find the *first failing step*, spot tool loops and
retry storms, compare agent/model/tool versions, and replay runs against
mocked tools. Payload capture is opt-in and redacted by default; the whole
point is diagnosis without indiscriminately collecting secrets.

Spec: [`../../02-AI-Agent-Failure-Observatory.md`](../../02-AI-Agent-Failure-Observatory.md)
(event schema §7, failure detection §8, API §10, MVP definition of done §18).

## Three components

| Component | Path | What it is |
| --- | --- | --- |
| **Server** (Go control plane) | [`server/`](server/) | Ingestion API + durable queue + worker + failure detectors + alert scheduler. Dev mode uses embedded **SQLite + file queue + local blob dir** — no external services. Production backends (Postgres / ClickHouse / NATS / S3) switch on via env vars, each independently. |
| **SDK** (TypeScript) | [`sdk/`](sdk/) | Instrumentation: runs, spans, model/tool calls, handoffs. Bounded in-memory buffer, non-blocking batched delivery, jittered retry, content-addressed idempotency, client-side redaction. |
| **Frontend** (React Run Explorer) | [`frontend/`](frontend/) | The workbench UI: **Run Explorer**, **Failures**, **Evaluations** over the Go API. Vite + React + TypeScript. |

All three build and test independently; the server seeds a labelled-sample
demo project on first launch so the frontend and SDK have real structure to
point at.

## Quick start

### 1. Server (Go control plane)

```sh
cd server
go run ./cmd/observatory
```

Listens on `:8080`, creates the embedded store under `./data/` (SQLite + file
queue + local blobs), and — by default — seeds the demo project.

Real flags (each has an env-var override):

| Flag | Env | Default | Meaning |
| --- | --- | --- | --- |
| `-addr` | `OBS_ADDR` | `:8080` | listen address |
| `-data-dir` | `OBS_DATA_DIR` | `./data` | embedded data directory |
| `-log-level` | `OBS_LOG_LEVEL` | `info` | log level |
| `-seed-demo` | `OBS_SEED_DEMO` | `true` | seed a demo project + sample run |
| `-webhook-secret` | `OBS_WEBHOOK_SECRET` | *(empty)* | HMAC secret for signed alert webhooks |
| `-no-worker` | — | `false` | disable the background queue worker |

> **SAMPLE data.** `--seed-demo` creates the `demo` project, the key
> **`demo-key`**, a sample dataset and a synthetic run. Every seeded event
> carries `sample: "true"`. **`demo-key` is a development placeholder, not a
> credential** — do not use it against any real deployment, and never
> present seeded data as real analytics.

Production backends are opt-in, each selected independently (see
[`docs/PRODUCTION-BACKENDS.md`](docs/PRODUCTION-BACKENDS.md)):

```sh
OBS_DATABASE_URL=postgres://…      # Postgres metadata store
OBS_CLICKHOUSE_DSN=clickhouse://…  # ClickHouse analytics mirror
OBS_NATS_URL=nats://…             # NATS JetStream durable queue
OBS_S3_ENDPOINT=…                 # S3/MinIO object store
```

Leave them unset and you get the dev fallbacks above.

### 2. SDK (TypeScript)

```sh
cd sdk
npm install
npm test            # vitest
```

Instrument a run (real exports from `sdk/src/index.ts`):

```ts
import { Observatory, ObservatoryClient, estimateUsage } from "@ensembyte/observatory";

const client = new ObservatoryClient({
  endpoint: "http://localhost:8080",
  apiKey: "demo-key",          // the seeded sample key
  projectId: "demo",
});

const obs = new Observatory(client, "demo", {
  agent: { name: "support-agent", version: "1.4.0" },
  environment: "dev",
});

const run = obs.startRun({ sample: true });
const s = run.span("planner");
run.model({ provider: { name: "acme", model: "gpt-4o" } })
  .end(estimateUsage("user: order stuck", "your order is in transit"));
run.tool({ name: "order.lookup", version: "v3", schemaVersion: "schema-v3",
           args: { order_id: 42 } }).end({ resultRef: "blob://orders/42.json" });
s.end();
run.complete({ taskCompleted: true });

await client.flush();          // graceful shutdown: drain the buffer before exit
```

The client buffers non-blockingly, retries with full jitter, dedupes via a
content-addressed batch key, and redacts client-side before anything leaves
the process. See [`sdk/README.md`](sdk/README.md).

### 3. Frontend (React Run Explorer)

```sh
cd frontend
npm install
npm run dev            # http://localhost:5173, /v1 proxied to the server
```

Start the Go server first (it serves `/v1/*` and the seeded demo data the
workspaces render). `npm test` runs the vitest suites; `npm run build` does
type-check + production bundle. See [`frontend/README.md`](frontend/README.md).

## Docs

- [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) — system shape, data flow, env-based backend selection.
- [`docs/EVENT-SCHEMA.md`](docs/EVENT-SCHEMA.md) — the event envelope, attribute namespaces, sequence/idempotency model.
- [`docs/PRODUCTION-BACKENDS.md`](docs/PRODUCTION-BACKENDS.md) — Postgres/ClickHouse/NATS/S3 wiring, consumer topology, delivery semantics.
- [`docs/REQUIREMENT-TRACEABILITY.md`](docs/REQUIREMENT-TRACEABILITY.md) — spec requirement → implementation mapping.
- [`docs/KNOWN-LIMITATIONS.md`](docs/KNOWN-LIMITATIONS.md) — documented approximations and follow-ups.
- [`infrastructure/001_postgres.sql`](infrastructure/001_postgres.sql),
  [`infrastructure/001_clickhouse.sql`](infrastructure/001_clickhouse.sql) — production DDL.
- [`deploy/CLICKHOUSE-POSTGRES-NATS.md`](deploy/CLICKHOUSE-POSTGRES-NATS.md),
  [`deploy/NOTES-AND-GAPS.md`](deploy/NOTES-AND-GAPS.md) — deployment notes and open gaps.

## CI

[`.github/workflows/ci.yml`](.github/workflows/ci.yml) runs three independent
jobs mirroring the three components — a gap in one does not block the others:

- **server** — `go build` / `go vet` / `go test ./...`
- **sdk** — `npm run typecheck` (tsc) + `npm test` (vitest)
- **frontend** — `npm run build` (tsc -b + vite) + `npm test` (vitest)
