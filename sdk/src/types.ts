// Event envelope types (spec section 7). The base schema is provider-agnostic;
// provider/model specifics live in namespaced attributes so the platform can
// evolve without breaking the contract.

export const SCHEMA_VERSION = "1";

/** All event types the platform understands (spec section 7). */
export type EventType =
  | "run.started"
  | "run.completed"
  | "run.failed"
  | "run.cancelled"
  | "agent.started"
  | "agent.handoff"
  | "agent.completed"
  | "model.call.started"
  | "model.call.completed"
  | "model.call.failed"
  | "tool.call.started"
  | "tool.call.completed"
  | "tool.call.failed"
  | "retrieval.started"
  | "retrieval.completed"
  | "state.snapshot.created"
  | "budget.warning"
  | "budget.exhausted"
  | "evaluation.started"
  | "evaluation.completed";

export interface AgentInfo {
  name: string;
  version: string;
}

export interface ProviderInfo {
  name: string;
  model: string;
  version?: string;
}

/**
 * The wire event envelope. Every event carries tenant, project, run id,
 * parent span id, type, timestamp and schema version. `sequence` is the
 * authoritative ordering key (spec FR-02) — do not assume wall-clock
 * ordering. `idempotency_key` makes at-least-once delivery idempotent.
 */
export interface EventEnvelope {
  schema_version: string;
  event_id: string;
  idempotency_key: string;
  project_id: string;
  run_id: string;
  span_id?: string;
  parent_span_id?: string;
  sequence: number;
  event_type: EventType;
  timestamp: string; // RFC3339
  agent?: AgentInfo;
  provider?: ProviderInfo;
  attributes: Record<string, unknown>;
  payload_ref?: string | null;
}

/** Namespaced attribute keys (spec 7: namespaced attributes). */
export const Attr = {
  // model
  modelTokensIn: "model.tokens_in",
  modelTokensOut: "model.tokens_out",
  modelLatencyMs: "model.latency_ms",
  modelRetries: "model.retries",
  usageSource: "usage.source", // "provider" | "estimate"
  costUsd: "cost.usd",
  costEstimated: "cost.estimated",
  // tool
  toolName: "tool.name",
  toolVersion: "tool.version",
  toolArgs: "tool.args",
  toolArgsHash: "tool.args_hash",
  toolSchemaVersion: "tool.schema_version",
  toolResultRef: "tool.result_ref",
  schemaVersion: "schema.version",
  // error
  errorClass: "error.class",
  providerCode: "provider.response_code",
  stack: "error.stack",
  retryCount: "retry.count",
  // state
  stateFingerprint: "state.fingerprint",
  stateProgress: "state.progress",
  // context
  contextTruncated: "context.truncated",
  contextTokensDropped: "context.tokens_dropped",
  retrievalHits: "retrieval.hit_count",
  // task / eval
  taskCompleted: "task.completed",
  evalCaseId: "eval.case_id",
  // replay
  replayOriginRun: "replay.origin_run",
  replayMock: "replay.mock",
  safetySideEffect: "safety.side_effect",
  // metadata
  sample: "sample",
  environment: "environment",
} as const;

/**
 * Usage source: provider-reported vs estimated (MVP DoD: labelled).
 * When the provider reports token counts, source is "provider"; when the SDK
 * estimates (e.g. char/4 heuristic for a provider that doesn't expose
 * usage), source is "estimate" and consumers must not treat it as exact.
 */
export type UsageSource = "provider" | "estimate";

/** A usage block on a model call. Always carries an explicit source label. */
export interface Usage {
  inputTokens: number;
  outputTokens: number;
  source: UsageSource;
  /** Estimated USD cost when the provider does not report it. */
  costUsd?: number;
  costEstimated?: boolean;
}
