# Event Schema

This is the wire contract between the SDK and the Go control plane. It is
implemented by `server/internal/event` (the source of truth) and
`sdk/src/types.ts` (the mirror). If the two ever diverge, the server wins and
the SDK must be re-conformed.

## Envelope

Every event carries the same envelope. Field names are snake_case on the
wire; the SDK's TypeScript `EventEnvelope` matches them one-to-one.

```json
{
  "schema_version": "1",
  "event_id": "3f2504e0-4f89-11d3-9a0c-0305e82c3301",
  "project_id": "acme",
  "run_id": "run-9f1c",
  "span_id": "span-model-1",
  "parent_span_id": "span-agent-1",
  "sequence": 12,
  "event_type": "model.call.completed",
  "timestamp": "2026-01-01T12:00:00.000Z",
  "agent": { "name": "support-agent", "version": "1.4.0" },
  "provider": { "name": "acme", "model": "gpt-4o", "version": "2026-01" },
  "attributes": { },
  "payload_ref": null
}
```

| Field | Type | Required | Notes |
| --- | --- | --- | --- |
| `schema_version` | string | yes | `"1"`. The pipeline rejects any value other than the known version. |
| `event_id` | string | yes | RFC4122-shaped v4 id. Part of the identity key. |
| `project_id` | string | yes* | Tenant scope. The SDK may omit it; auth fills it from the key (the authoritative source). |
| `run_id` | string | yes | The agent run this event belongs to. |
| `span_id` | string | no | Set on span-scoped events; empty for run-scoped events. |
| `parent_span_id` | string | no | Builds the span hierarchy. Empty for top-level spans. |
| `sequence` | uint64 | yes, >0 | Per-run monotonically increasing ordering key. **Authoritative** — wall clock is not. |
| `event_type` | string | yes | One of the values below. |
| `timestamp` | string | yes | RFC3339 UTC. Metadata only; ordering uses `sequence`. |
| `agent` | object | no | `{ name, version }` of the acting agent. |
| `provider` | object | no | `{ name, model, version }` of the model provider. |
| `attributes` | map | yes | Namespaced attributes; see below. |
| `payload_ref` | string \| null | yes | Reference to an opt-in captured payload. Nulled by the pipeline when payload capture is off. |

## Event types (spec section 7)

| Type | Scope | Meaning |
| --- | --- | --- |
| `run.started` | run | The agent run begins. |
| `run.completed` | run | Terminal success. |
| `run.failed` | run | Terminal failure (often carries `error.class`). |
| `run.cancelled` | run | Terminal cancellation. |
| `agent.started` | span | An agent (sub-)span begins. |
| `agent.handoff` | span | Control passes between agents (`agent.from`, `agent.to`). |
| `agent.completed` | span | An agent span ends. |
| `model.call.started` | span | A model call begins. |
| `model.call.completed` | span | A model call succeeds (usage folded here). |
| `model.call.failed` | span | A model call fails (`error.class`, `provider.response_code`). |
| `tool.call.started` | span | A tool call begins (tool identity + redacted `tool.args`). |
| `tool.call.completed` | span | A tool call succeeds. |
| `tool.call.failed` | span | A tool call fails (feeds failure clustering). |
| `retrieval.started` | span | A retrieval step begins. |
| `retrieval.completed` | span | Retrieval ends (`retrieval.hit_count`, truncation signals). |
| `state.snapshot.created` | span | A state fingerprint is recorded (oscillation detection). |
| `budget.warning` | span | A budget threshold is approached. |
| `budget.exhausted` | span | A budget is exceeded. |
| `evaluation.started` | run | An evaluation run begins. |
| `evaluation.completed` | run | An evaluation run ends. |

Run-terminal events (`run.completed/failed/cancelled`) and span-terminal
events close their scopes in the assembler; a span without a terminal event
is marked `open` (partial telemetry).

## Attribute namespaces

Provider/model specifics stay out of the base envelope; they live in
namespaced attributes so the schema can evolve.

| Namespace | Keys |
| --- | --- |
| agent | `agent.from`, `agent.to` |
| model | `model.tokens_in`, `model.tokens_out`, `model.latency_ms`, `model.retries` |
| usage | `usage.source` (`"provider"` \| `"estimate"`), `cost.usd`, `cost.estimated` |
| tool | `tool.name`, `tool.version`, `tool.args`, `tool.args_hash`, `tool.schema_version`, `schema.version`, `tool.result_ref` |
| error | `error.class`, `provider.response_code`, `error.stack`, `retry.count` |
| state | `state.fingerprint`, `state.progress` |
| context | `context.truncated`, `context.tokens_dropped`, `retrieval.hit_count` |
| task/eval | `task.completed`, `eval.case_id` |
| replay | `replay.origin_run`, `replay.mock`, `safety.side_effect` |
| metadata | `sample`, `environment` |

Usage is always labelled (`usage.source`): provider-reported numbers are
`"provider"`, SDK-estimated numbers are `"estimate"` (MVP DoD).

## Idempotency keys

**Per-event key** (pipeline dedup, `processed` table). The exact, canonical
form is a pipe-join of the identity fields:

```
project_id | run_id | span_id | event_type | sequence | event_id
```

- Computed by `event.Envelope.IdempotencyKey()` on the server and
  `eventKey()` in the SDK — byte-identical by construction.
- Same logical event ⇒ same key ⇒ processed exactly once, even under
  at-least-once delivery.
- The wall-clock `timestamp` is intentionally **not** part of the key.

**Batch/transport key** (queue `MessageID`, NATS `jetstream.MessageID`).
The SDK's `batchIdempotencyKey()` is a FNV-1a over the sorted list of
`(run_id, sequence, event_id)` tuples in the batch. Reordering a batch does
not change the key; resubmitting the same batch does. The queue and the
pipeline dedupe independently, so a redelivered batch is a no-op.

**Argument hashing for loops (spec 8.1).** `tool.args_hash` is derived by the
control plane from `tool.args` via **canonical JSON** (keys sorted, slices of
maps sorted) then SHA-256-truncated. Key-order changes and numeric-format
differences therefore cannot mask a repeated call. The SDK does not send a
pre-computed hash — it sends the (redacted) `tool.args` and lets the server
derive the stable key. `ToolCallKey` = `tool.name | HashOf(tool.args) |
schema.version`.

**Failure signature (spec 8.4).** `FailureSignatureOf` hashes
`error.class | tool.name | schema.version | provider.response_code |
normalised stack` into a `fs-`-prefixed cluster id. Raw evidence stays on the
events; the signature is the stable group key.

## Redaction

Payload capture is off by default. When a project opts in, `event.Redact`
deep-copies attributes and replaces any value whose key matches a sensitive
substring (or a project-configured extra key) with `[REDACTED]`, and scrubs
secret-looking string values (AWS keys, JWTs, bearer tokens, emails, card
numbers). The SDK applies the same policy client-side, so the server is a
second line of defence. Structure is preserved so diagnostics still work.
