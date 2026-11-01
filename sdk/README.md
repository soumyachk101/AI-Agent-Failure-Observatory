# @ensembyte/observatory

TypeScript instrumentation SDK for the AI Agent Failure Observatory.

It turns an opaque agent run into structured, idempotent events that the
Go control plane ingests, assembles into spans, and runs the deterministic
failure detectors over. The SDK is dependency-free: no runtime packages,
just Node 20+ / modern browser globals.

## Install

```bash
npm install @ensembyte/observatory
```

## Quick start

```ts
import { Observatory, ObservatoryClient, estimateUsage } from "@ensembyte/observatory";

const client = new ObservatoryClient({
  endpoint: "http://observatory:8080",
  apiKey: process.env.OBS_API_KEY!,   // project-scoped key
  projectId: "acme-support",
});

const obs = new Observatory(client, "acme-support", {
  agent: { name: "support-agent", version: "1.4.0" },
  environment: "production",
});

// One run with an agent span, a model call and a tool call.
const run = obs.startRun({ environment: "dev", sample: true });
const planner = run.span("planner");
run.tool({ name: "order.lookup", version: "v3", schemaVersion: "schema-v3", args: { order_id: 42 } })
  .end({ resultRef: "blob://orders/42.json" });

const m = run.model({ provider: { name: "acme", model: "gpt-4o" } });
m.end(estimateUsage("user: my order stuck", "your order is in transit"));
planner.end();

// Agent handoff: agent.handoff with agent.from / agent.to.
run.handoff("verifier", "planner");

run.complete({ taskCompleted: true });

// Graceful shutdown: flush what is buffered, then exit.
process.on("SIGTERM", async () => {
  await client.flush();
});
```

## API

| Export | Purpose |
| --- | --- |
| `ObservatoryClient` | Bounded in-memory buffer + non-blocking batched delivery to `POST /v1/ingest/events`, retry with full jitter, content-addressed batch idempotency key, `flush()` on shutdown, `stats()` counters. |
| `Observatory` | Entry point: `startRun()` / `attachRun()`, carries agent/provider/environment metadata. |
| `Run` | `span`, `model`, `tool`, `handoff`, `retrieval`, `state`, `budget`, `event` (manual) and terminal `complete` / `fail` / `cancel`. Every emitted envelope matches the control plane's schema (field names, sequence, idempotency key). |
| `ModelCall` / `ToolCall` | Open on `started`, close on `completed` / `failed`; usage attribution is explicit. |
| `estimateUsage` | char/4 token estimate; **always labelled `usage.source = "estimate"`** so consumers never read it as provider data. |
| `withRun` / `activeRun` | Propagate the current run across async boundaries without threading it through every signature. |
| `redact` / `scrubString` / `applyHook` | Client-side redaction: sensitive keys are replaced before content leaves the process; the server redacts again as a second layer. |

### Instrumentation model

- **Sequence numbers are authoritative.** Each run assigns strictly increasing
  `sequence` values; the pipeline orders by them, never by wall clock.
- **Idempotency is content-addressed.** A logical event is identified by
  `(project_id, run_id, span_id, event_type, sequence, event_id)`. The
  control plane recomputes that key on ingest; the transport additionally
  carries a stable batch key so retried batches dedupe at the queue layer.
  Redelivery therefore collapses to effective-once processing.
- **Payload capture is off by default.** `payload_ref` only takes effect when
  the project opts in; tool arguments are captured in `tool.args` and
  redacted client-side when you pass them. Do not send `tool.args_hash` —
  the server derives it from `tool.args` with canonical-JSON hashing, which
  is what the loop detector keys on.
- **Usage is labelled.** Pass provider-reported usage with `source:
  "provider"`, or `estimateUsage()` with `source: "estimate"`. The run
  aggregate carries the source label end to end.

### Telemetry never crashes your agent

`emit()` does no I/O and throws nothing. Network failures, 429s and 5xx
responses are absorbed: transient failures are retried with full jitter and,
once retries are exhausted, the batch is returned to the buffer for later
redelivery; permanent 4xx responses are dropped and counted in `stats()`.
The overflow policy is oldest-dropped, bounded by `maxBufferSize`.

## Options

```ts
new ObservatoryClient({
  endpoint, apiKey, projectId,

  maxBufferSize: 5000,     // hard cap on unsent events
  maxBatchSize: 100,       // events per HTTP batch
  flushIntervalMs: 1000,   // auto-flush cadence (0 = manual only)
  maxRetries: 5,           // transient retries per batch
  baseRetryMs: 200,        // full-jitter backoff base
  maxRetryMs: 8000,        // backoff cap
  maxConsecutiveFailures: 10, // stop auto-flushing once the server is dead

  redaction: { extraKeys: ["sso", "tenant_hint"], marker: "[REDACTED]" },
  redactionHook: (key, value) => (key === "internal" ? undefined : value),
  fetchImpl: globalThis.fetch,
  log: console.debug,
})
```

## Development

```bash
npm install
npm run typecheck   # tsc --noEmit
npm test            # vitest: idempotency-key determinism, redaction, buffering
npm run build       # emits dist/ (ESM + .d.ts)
```

## Testing without a server

Every test can inject a `fetchImpl`. The buffering suites record request
bodies and script responses:

```ts
import { ObservatoryClient, batchIdempotencyKey } from "@ensembyte/observatory";

const bodies: string[] = [];
const client = new ObservatoryClient({
  endpoint: "http://unit",
  apiKey: "k",
  flushIntervalMs: 0,
  baseRetryMs: 1,
  fetchImpl: async (_url, init) => {
    bodies.push(String(init!.body));
    return new Response(JSON.stringify({ accepted: 1 }), { status: 200 });
  },
});

client.emit(/* ... */);
await client.flush();
expect(JSON.parse(bodies[0]).idempotency_key).toBe(batchIdempotencyKey(events));
```
