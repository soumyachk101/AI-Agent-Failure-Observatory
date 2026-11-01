# Requirement Traceability

Maps each requirement in the product blueprint
(`02-AI-Agent-Failure-Observatory.md`) to where it is implemented. "In
production target" items are DDL/env-selection that exists as
`infrastructure/` reference, not yet wired into the running binary
(`deploy/NOTES-AND-GAPS.md` G2).

## Functional

| ID | Requirement | Implementation |
| --- | --- | --- |
| FR-01 | Instrumentation SDK (TS first) | `sdk/` (`@ensembyte/observatory`): run/span lifecycle, model- and tool-call spans, handoff, retrieval, state, budget events. Async-boundary propagation via `withRun`/`activeRun`. Manual `Run.event()`. OpenTelemetry-compatible export is an open decision (spec §19.4), not yet implemented. |
| FR-02 | Event model | `event.Envelope` (tenant/project/run/parent/type/ts/schema), sequence-numbered ordering (`sequence`), model/provider + token + latency + retries + error class, tool name/version/schema/result, opt-in redacted payloads. |
| FR-03 | Run explorer | `api.RunTimeline` (ordered events + spans + run aggregate + findings), `GET /v1/runs/{id}/timeline`; frontend `RunsWorkspace`/`TimelineCanvas`/`RunInspector` (separate head). Filters by project/environment/agent/model/outcome via `ListRuns`. |
| FR-04 | Failure intelligence | `pipeline.RuleEngine`: 8.1 repeated-tool loop (polling-policy exempt), 8.2 state oscillation, 8.3 budget exhaustion, 8.4 failure clustering, 8.5 context loss. Clusters persisted in `failure_signatures`; findings link to evidence event ids (Explainability NFR). |
| FR-05 | Evaluation | `evalsvc`: versioned datasets + cases, deterministic checks (tool selection, schema validity, task completion), supplementary versioned LLM judge (never the source of truth), `GateConfig` CI threshold, cross-version comparison via `eval_runs`. |
| FR-06 | Safe replay | `evalsvc.ReplayService` + `MockTool`: mocked tools by default, destructive deny-list refused even with fixtures, side-effect-free override is explicit, `CallOrder` provenance, original sequence preserved. |
| FR-07 | Alerts and budgets | `alertsvc`: `AlertRule` (detector + window + threshold + agent + severity + webhook), deterministic evaluation over persisted evidence, HMAC-signed delivery with monotonic nonce (replay protection), per-window `alert_evaluations` records. |

## Non-functional

| Area | Requirement | Implementation |
| --- | --- | --- |
| Ingestion | At-least-once + idempotent | Durable queue (`queue`) + per-event/batch idempotency keys ⇒ effective-once (`processed` table + `MessageID`). |
| Query | Fast run lookup / timeline | `RunTimeline` ordered by `sequence`; ClickHouse analytics target in `infrastructure/`. |
| Privacy | Redacted by default, configurable | `payload_capture` default `off`; `redact_fields` per project; client- and server-side `Redact`. |
| Isolation | Strict tenant + scoped keys | Every row carries `project_id`; every scoped query filters by it; keys are tenant-scoped and hashed. |
| Reliability | Buffer on transient failure | `ObservatoryClient` bounded buffer, non-blocking, retry with jitter, flush-on-shutdown; server 429 + `Retry-After`. |
| Evaluation | Versioned datasets/prompts/evaluators/settings | `datasets`/`evaluators`/`eval_runs` keyed by `(project, id, version)`. |
| Explainability | Every diagnosis links to evidence | `Finding.Evidence` = event ids; evaluation `FailedChecks` cite event ids. |
| Cost | Separate metadata from payloads | `payload_ref` in analytics rows; payloads live in the object store. |

## MVP definition of done (spec §18)

| Acceptance | Where |
| --- | --- |
| TypeScript SDK instruments one agent run with model + tool spans | `sdk` `Observatory.startRun` → `run.model` / `run.tool`; `sdk/test/buffering` + `pipeline` tests. |
| Run Explorer displays correct hierarchy + ordering | `store.RunTimeline` + `api.runTimeline`; assembler builds parent/child by `parent_span_id`; order by `sequence`. |
| Loop detector catches a known repeated-tool case | `pipeline.SampleLoopRun` seed + `TestRepeatedToolLoopDetects`. |
| Token/latency labelled provider vs estimate | `usage.source` attribute; `TestUsageSourceLabelEstimate`; `estimateUsage` always labelled `estimate`. |
| Payload capture opt-in + redaction tested | `projects.payload_capture` default `off`; `event.Redact` + `TestRedact*`; `TestPayloadCaptureOffDropsRef`. |
| Evaluation dataset compares two agent versions | `evalsvc.RunCase` over `eval_runs` per `agent_version`; `TestDeterministicScoring*`. |
| Replay uses mocked tools by default | `evalsvc.MockTool` default executor; `TestMockTool*`. |
| UI is a purpose-built trace workbench | `frontend/` (separate head) — Run Explorer three-region workbench, not a generic dashboard. |
