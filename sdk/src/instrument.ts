// Instrumentation layer: run / span lifecycle, model-call and tool-call
// instrumentation with usage attribution, and agent handoff tracking
// (spec FR-01 / FR-02).
//
// Every emitted event conforms to the control plane's envelope
// (server/internal/event):
//   - field names match exactly (snake_case wire names),
//   - per-event idempotency key = pipe-join of
//       project_id | run_id | span_id | event_type | sequence | event_id
//     computed identically client- and server-side,
//   - usage attribution is explicit: token counts always carry
//     usage.source ("provider" | "estimate") so an estimate is never read as
//     provider data (MVP DoD),
//   - tool arguments are redacted client-side and the SDK does NOT send
//     tool.args_hash: the control plane derives it from tool.args with its
//     canonical-JSON hashing so loop detection sees one stable key.
//
// Redaction hook: the client's `redactionHook` (ClientOptions) runs
// automatically over the final attribute map of every emitted event, in
// emitEvent(), before the envelope enters the client buffer — so anything
// the hook drops or transforms never leaves the process. This mirrors what
// the control plane does server-side as a second layer. A hook that throws
// is logged via the client's log sink and the attribute is kept as-is;
// telemetry must never crash the instrumented agent.

import { ObservatoryClient } from "./client";
import { eventKey, makeEnvelope, nowRfc3339, EnvelopeDraft } from "./envelope";
import { newId } from "./id";
import {
  AgentInfo,
  Attr,
  EventEnvelope,
  EventType,
  ProviderInfo,
  Usage,
} from "./types";

export interface ObservatoryOptions {
  /** Agent identity stamped on every event of its runs. */
  agent?: AgentInfo;
  /** Default model provider stamped on model-call envelopes. */
  provider?: ProviderInfo;
  /** environment attribute on run.started (spec 7 metadata namespace). */
  environment?: string;
  /** Mark every event as sample data (demo/seed runs; never real analytics). */
  sample?: boolean;
}

// ---------------------------------------------------------------- agent spans

/**
 * A started agent span (agent.started ... agent.completed). Nesting is
 * expressed by passing the parent span's id to Run.span(); the server
 * assembler builds the hierarchy from parent_span_id.
 */
export class AgentSpan {
  constructor(
    private readonly run: Run,
    readonly id: string,
    readonly name: string,
    private readonly parentSpanId?: string
  ) {}

  /**
   * Emit agent.completed on this span. Callers are expected to end spans in
   * reverse open order; end() is idempotent per call site.
   */
  end(): Run {
    this.run.finishSpan(this.id, this.parentSpanId);
    return this.run;
  }
}

// ---------------------------------------------------------------- model call

export interface ModelCallOptions {
  provider?: ProviderInfo;
  /** Measured call latency (ms); when omitted the SDK measures it. */
  latencyMs?: number;
  /** Retries inside this call (exposed as model.retries). */
  retries?: number;
  providerCode?: string;
  /** Containing agent span; defaults to the run's current innermost span. */
  parentSpanId?: string;
}

/**
 * A model-call span. Usage attribution is explicit: pass the provider's
 * reported usage (source "provider") or use estimateUsage() for SDK-derived
 * numbers (source "estimate"). The source label is what the MVP requires.
 */
export class ModelCall {
  private readonly spanId: string;
  private readonly startedAt: number;
  private readonly parent: string | undefined;
  private closed = false;

  constructor(private readonly run: Run, private readonly opts: ModelCallOptions) {
    this.spanId = newId();
    this.startedAt = Date.now();
    this.parent = this.run.resolveSpanParent(this.opts.parentSpanId);
    this.run.openCall(this.spanId, "model.call.started", {}, this.parent);
  }

  end(usage?: Usage, extra?: { costUsd?: number; costEstimated?: boolean; latencyMs?: number }): Run {
    if (this.closed) return this.run;
    this.closed = true;
    this.run.closeCall(
      this.spanId,
      "model.call.completed",
      usageAttrs(
        usage,
        this.opts,
        extra?.costUsd,
        extra?.costEstimated,
        extra?.latencyMs ?? Math.max(0, Date.now() - this.startedAt)
      ),
      this.parent
    );
    return this.run;
  }

  fail(err: unknown, extra?: { retries?: number; providerCode?: string }): Run {
    if (this.closed) return this.run;
    this.closed = true;
    const attrs: Record<string, unknown> = {
      ...usageAttrs(
        undefined,
        this.opts,
        undefined,
        undefined,
        Math.max(0, Date.now() - this.startedAt),
        extra?.retries
      ),
      ...errorAttrs(err, extra?.providerCode ?? this.opts.providerCode),
    };
    this.run.closeCall(this.spanId, "model.call.failed", attrs, this.parent);
    return this.run;
  }
}

// ------------------------------------------------------------------ tool call

export interface ToolCallOptions {
  name: string;
  version?: string;
  /** Tool argument schema version (tool.schema_version + schema.version). */
  schemaVersion?: string;
  /** Arguments, redacted before capture. Do NOT hash here (see file note). */
  args?: Record<string, unknown>;
  retries?: number;
  /** Containing agent span; defaults to the run's current innermost span. */
  parentSpanId?: string;
}

/**
 * A tool-call span. The started event carries the tool's identity and the
 * redacted argument blob; the terminal event carries the outcome. Args are
 * captured once, on start, which is where their shape is authoritative.
 */
export class ToolCall {
  private readonly spanId: string;
  private readonly parent: string | undefined;
  private closed = false;

  constructor(private readonly run: Run, private readonly opts: ToolCallOptions) {
    this.spanId = newId();
    this.parent = this.run.resolveSpanParent(this.opts.parentSpanId);
    this.run.openCall(this.spanId, "tool.call.started", this.attrs(), this.parent);
  }

  end(result?: { resultRef?: string }): Run {
    if (this.closed) return this.run;
    this.closed = true;
    const attrs: Record<string, unknown> = {};
    if (result?.resultRef) attrs[Attr.toolResultRef] = result.resultRef;
    this.run.closeCall(this.spanId, "tool.call.completed", attrs, this.parent);
    return this.run;
  }

  fail(err: unknown, extra?: { providerCode?: string }): Run {
    if (this.closed) return this.run;
    this.closed = true;
    const attrs: Record<string, unknown> = {
      ...this.attrs(),
      ...errorAttrs(err, extra?.providerCode),
    };
    this.run.closeCall(this.spanId, "tool.call.failed", attrs, this.parent);
    return this.run;
  }

  private attrs(): Record<string, unknown> {
    const attrs: Record<string, unknown> = { [Attr.toolName]: this.opts.name };
    if (this.opts.version) attrs[Attr.toolVersion] = this.opts.version;
    if (this.opts.schemaVersion) {
      attrs[Attr.toolSchemaVersion] = this.opts.schemaVersion;
      attrs[Attr.schemaVersion] = this.opts.schemaVersion;
    }
    if (this.opts.args) attrs[Attr.toolArgs] = this.run.scrub(this.opts.args);
    if (this.opts.retries && this.opts.retries > 0) attrs[Attr.retryCount] = this.opts.retries;
    return attrs;
  }
}

// ----------------------------------------------------------------------- run

export interface StartRunOptions {
  agent?: AgentInfo;
  provider?: ProviderInfo;
  environment?: string;
  sample?: boolean;
  runId?: string;
}

export class Run {
  readonly id: string;
  private seq = 0;
  private readonly base: Record<string, unknown>;
  private readonly agent?: AgentInfo;
  private readonly provider?: ProviderInfo;
  /** Innermost open span, used as the default parent for calls. */
  private spanStack: string[] = [];

  constructor(
    private readonly client: ObservatoryClient,
    private readonly projectId: string,
    opts: StartRunOptions,
    obs: ObservatoryOptions,
    emitStarted: boolean
  ) {
    this.id = opts.runId ?? newId();
    this.agent = obs.agent ?? opts.agent;
    this.provider = obs.provider ?? opts.provider;
    this.base = {};
    const environment = opts.environment ?? obs.environment;
    if (environment) this.base[Attr.environment] = environment;
    if (opts.sample ?? obs.sample) this.base[Attr.sample] = "true";
    if (emitStarted) {
      this.emitEvent("run.started", { attrs: { ...this.base } });
    }
  }

  get runId(): string {
    return this.id;
  }

  // ---- spans ----

  /** Start an agent span (agent.started); .end() emits agent.completed. */
  span(name: string, parentSpanId?: string): AgentSpan {
    const id = newId();
    this.spanStack.push(id);
    this.emitEvent("agent.started", { spanId: id, parentSpanId });
    return new AgentSpan(this, id, name, parentSpanId);
  }

  /**
   * Resolve the containing agent span for a call: explicit parent wins,
   * otherwise the innermost open span on this run.
   */
  resolveSpanParent(explicit?: string): string | undefined {
    return explicit ?? this.spanStack[this.spanStack.length - 1];
  }

  /** Model-call span; usage attribution happens on .end(). */
  model(opts: ModelCallOptions = {}): ModelCall {
    return new ModelCall(this, opts);
  }

  /** Tool-call span; args are redacted at construction. */
  tool(opts: ToolCallOptions): ToolCall {
    return new ToolCall(this, opts);
  }

  /**
   * Agent handoff tracking (spec FR-01). Emits agent.handoff with the
   * agent.from / agent.to attributes, scoped to the given span.
   */
  handoff(to: string, from?: string, opts: { spanId?: string; parentSpanId?: string } = {}): Run {
    const attrs: Record<string, unknown> = {
      [Attr.agentFrom]: from ?? "",
      [Attr.agentTo]: to,
    };
    this.emitEvent("agent.handoff", { attrs, spanId: opts.spanId, parentSpanId: opts.parentSpanId });
    return this;
  }

  /**
   * Retrieval pair (retrieval.started / retrieval.completed) with hit count
   * and explicit truncation signals (spec 8.5: capture what is instrumented,
   * never infer hidden context state).
   */
  retrieval(opts: {
    hits?: number;
    truncated?: boolean;
    tokensDropped?: number;
    spanId?: string;
    parentSpanId?: string;
  }): Run {
    const spanId = opts.spanId ?? newId();
    this.emitEvent("retrieval.started", { spanId, parentSpanId: opts.parentSpanId });
    const attrs: Record<string, unknown> = { [Attr.retrievalHits]: opts.hits ?? 0 };
    if (opts.truncated) {
      attrs[Attr.contextTruncated] = true;
      attrs[Attr.contextTokensDropped] = opts.tokensDropped ?? 0;
    }
    this.emitEvent("retrieval.completed", { spanId, parentSpanId: opts.parentSpanId, attrs });
    return this;
  }

  /** State snapshot (spec 8.2 state oscillation detection). */
  state(fingerprint: string, opts: { progress?: boolean; parentSpanId?: string } = {}): Run {
    const attrs: Record<string, unknown> = { [Attr.stateFingerprint]: fingerprint };
    if (opts.progress) attrs[Attr.stateProgress] = true;
    this.emitEvent("state.snapshot.created", {
      spanId: "state",
      parentSpanId: opts.parentSpanId,
      attrs,
    });
    return this;
  }

  /** Budget events (spec 8.3); the detector treats exhausted as an error. */
  budget(level: "warning" | "exhausted"): Run {
    this.emitEvent(level === "warning" ? "budget.warning" : "budget.exhausted", {});
    return this;
  }

  /** Terminal run.completed; optionally records task completion. */
  complete(opts: { taskCompleted?: boolean } = {}): Run {
    const attrs: Record<string, unknown> = {};
    if (opts.taskCompleted) attrs[Attr.taskCompleted] = true;
    this.emitEvent("run.completed", { attrs });
    return this;
  }

  /** Terminal run.failed with best-effort error classification. */
  fail(err: unknown): Run {
    this.emitEvent("run.failed", { attrs: errorAttrs(err) });
    return this;
  }

  /** Terminal run.cancelled. */
  cancel(): Run {
    this.emitEvent("run.cancelled", {});
    return this;
  }

  /** Manual event for framework adapters (spec FR-01). */
  event(
    type: EventType,
    attrs: Record<string, unknown> = {},
    opts: { spanId?: string; parentSpanId?: string } = {}
  ): Run {
    this.emitEvent(type, { attrs, spanId: opts.spanId, parentSpanId: opts.parentSpanId });
    return this;
  }

  /** Redact a value before capturing it as an attribute. */
  scrub(value: unknown): unknown {
    return this.client.scrub(value);
  }

  // ---- internals used by the span classes ----

  openCall(spanId: string, type: EventType, attrs: Record<string, unknown>, parentSpanId?: string): void {
    this.emitEvent(type, { spanId, attrs, parentSpanId });
  }

  closeCall(spanId: string, type: EventType, attrs: Record<string, unknown>, parentSpanId?: string): void {
    this.emitEvent(type, { spanId, attrs, parentSpanId });
  }

  /** Close an agent span; the server assembler marks unended spans "open". */
  finishSpan(spanId: string, parentSpanId?: string): void {
    this.spanStack = this.spanStack.filter((s) => s !== spanId);
    this.emitEvent("agent.completed", { spanId, parentSpanId });
  }

  private emitEvent(
    type: EventType,
    d: Partial<EnvelopeDraft> & { spanId?: string; parentSpanId?: string }
  ): Run {
    this.seq++;
    const env = makeEnvelope(this.projectId, this.id, {
      type,
      attrs: this.withDefaults(this.redactAttrs(d.attrs ?? {})),
      sequence: this.seq,
      spanId: d.spanId,
      parentSpanId: d.parentSpanId,
      agent: this.agent,
      provider: this.provider,
      timestamp: nowRfc3339(),
    });
    this.client.emit(env);
    return this;
  }

  private withDefaults(attrs: Record<string, unknown>): Record<string, unknown> {
    const out = { ...attrs };
    if (this.base[Attr.sample]) out[Attr.sample] = this.base[Attr.sample];
    return out;
  }

  /**
   * Run the client's redaction hook over every attribute before the event
   * enters the client buffer (mirror of the server-side redaction layer).
   * The hook's output replaces the attribute value; `undefined` drops it.
   */
  private redactAttrs(attrs: Record<string, unknown>): Record<string, unknown> {
    const out: Record<string, unknown> = {};
    for (const [k, v] of Object.entries(attrs)) {
      const hv = this.client.redactAttr(k, v);
      if (hv !== undefined) out[k] = hv;
    }
    return out;
  }
}

// ------------------------------------------------------------ observatory

/**
 * Top-level instrumentation entry point.
 *
 *   const client = new ObservatoryClient({ endpoint, apiKey, projectId: "acme" });
 *   const obs = new Observatory(client, "acme", { agent: { name: "support", version: "1.4.0" } });
 *   const run = obs.startRun({ environment: "dev" });
 *   run.span("planner").tool({ name: "order.lookup", args: { id: 42 } }).end();
 *   run.complete({ taskCompleted: true });
 *   await client.flush();            // graceful shutdown hook
 */
export class Observatory {
  constructor(
    private readonly client: ObservatoryClient,
    private readonly projectId: string,
    private readonly obs: ObservatoryOptions = {}
  ) {}

  /** Start a run: emits run.started and returns the run handle. */
  startRun(opts: StartRunOptions = {}): Run {
    return new Run(this.client, this.projectId, opts, this.obs, true);
  }

  /**
   * Adopt a known run id (propagated across a service/async boundary): no
   * run.started is emitted, so the owning side keeps the lifecycle
   * authoritative (spec FR-01 trace propagation).
   */
  attachRun(opts: StartRunOptions & { runId: string }): Run {
    return new Run(this.client, this.projectId, opts, this.obs, false);
  }

  /** Flush-on-shutdown hook (await before exit). */
  flush(): Promise<void> {
    return this.client.flush();
  }
}

// ------------------------------------------------------------------ misc

/**
 * Synchronous run context: withRun(run, fn) makes activeRun() == run inside
 * fn. Use it to propagate the current run across an async boundary without
 * threading it through every call signature.
 */
let currentRun: Run | null = null;

export function withRun<T>(run: Run, fn: () => T): T {
  const prev = currentRun;
  currentRun = run;
  try {
    return fn();
  } finally {
    currentRun = prev;
  }
}

export function activeRun(): Run | null {
  return currentRun;
}

/**
 * Char/4 token estimate for providers that do not report usage. Always
 * labelled "estimate" — never present these numbers as provider data.
 */
export function estimateUsage(input: string, output?: string, extra?: { costUsd?: number }): Usage {
  return {
    inputTokens: Math.ceil(input.length / 4),
    outputTokens: output ? Math.ceil(output.length / 4) : 0,
    source: "estimate",
    costUsd: extra?.costUsd,
    costEstimated: true,
  };
}

/** Best-effort, framework-agnostic error classification (spec 8.4). */
export function errorClassOf(err: unknown): string {
  if (!err) return "unknown";
  const e = err as { name?: string; code?: string; status?: number };
  if (typeof e.name === "string") {
    switch (e.name.toLowerCase()) {
      case "timeouterror":
        return "timeout";
      case "aborterror":
        return "cancelled";
      default:
        break;
    }
  }
  if (typeof e.code === "string" && e.code.toLowerCase().includes("timeout")) return "timeout";
  if (typeof e.status === "number" && e.status >= 500) return "provider_error";
  if (typeof e.status === "number" && e.status === 422) return "invalid_arguments";
  return "error";
}

function errorAttrs(err: unknown, providerCode?: string): Record<string, unknown> {
  const attrs: Record<string, unknown> = { [Attr.errorClass]: errorClassOf(err) };
  if (providerCode) attrs[Attr.providerCode] = providerCode;
  const stack = err instanceof Error ? err.stack : typeof err === "string" ? err : undefined;
  if (stack) {
    // Keep only the first frames: the server normalizes stacks for the
    // failure signature, and full traces risk capturing payloads.
    attrs[Attr.stack] = stack
      .split("\n")
      .slice(0, 8)
      .join("\n");
  }
  return attrs;
}

/** Usage attributes with an explicit source label (MVP DoD). */
function usageAttrs(
  usage: Usage | undefined,
  opts: ModelCallOptions,
  costUsd?: number,
  costEstimated?: boolean,
  latencyMs?: number,
  retries?: number
): Record<string, unknown> {
  const attrs: Record<string, unknown> = {};
  const latency = latencyMs ?? opts.latencyMs;
  if (latency !== undefined) attrs[Attr.modelLatencyMs] = latency;
  const r = retries ?? opts.retries;
  if (r && r > 0) attrs[Attr.modelRetries] = r;
  if (opts.providerCode) attrs[Attr.providerCode] = opts.providerCode;
  if (usage) {
    attrs[Attr.modelTokensIn] = usage.inputTokens;
    attrs[Attr.modelTokensOut] = usage.outputTokens;
    attrs[Attr.usageSource] = usage.source;
    if (usage.costUsd !== undefined) {
      attrs[Attr.costUsd] = usage.costUsd;
      attrs[Attr.costEstimated] = usage.costEstimated ?? usage.source === "estimate";
    }
  }
  if (costUsd !== undefined) {
    attrs[Attr.costUsd] = costUsd;
    attrs[Attr.costEstimated] = costEstimated ?? false;
  }
  return attrs;
}

/** Determinism check helper: the same identity fields yield the same key. */
export function envelopeKey(e: EventEnvelope): string {
  return eventKey(e);
}
