// Batched, non-blocking telemetry client for the AI Agent Failure Observatory
// control plane (spec FR-01 / NFR Reliability / section 10 "SDK requirements").
//
// Design goals, all satisfied here:
//   - bounded in-memory buffer (a flood can't grow memory without bound)
//   - non-blocking delivery (emit() never does I/O; it queues and returns)
//   - batched transport with a deterministic, content-addressed idempotency key
//     so a retried batch is deduped end-to-end (NATS JetStream MessageID + the
//     pipeline's per-event keys)
//   - retry with full jitter on transient failures
//   - flush-on-shutdown hook (await client.flush())
//   - redaction before content ever leaves the process (first line of defence;
//     the server redacts again as a second line)
//   - telemetry failure NEVER crashes the instrumented agent: every error is
//     absorbed into counters and an optional log sink.
//
// Idempotency model: the *per-event* key is computed by the control plane from
// the envelope's identity fields (project|run|span|type|sequence|event_id);
// it is intentionally not a wire field. The *batch* key (below) is the stable
// transport-level key the SDK resubmits when a batch is redelivered, so the
// queue's server-side MessageID dedup and the pipeline's idempotency table both
// collapse a duplicate to a no-op => effective-once processing.

import { EventEnvelope, SCHEMA_VERSION } from "./types";
import { RedactionConfig, applyHook, redact, RedactionHook } from "./redact";

export interface ClientOptions {
  /** Control-plane base URL, e.g. "http://localhost:8080". The client POSTs to
   *  "<endpoint>/v1/ingest/events". */
  endpoint: string;
  /** Project-scoped ingestion key. Sent as Bearer + X-Obs-Key. */
  apiKey: string;
  /** Default project scope stamped onto envelopes that omit it. */
  projectId?: string;

  // Buffering.
  /** Hard cap on buffered (unsent) events; oldest are dropped on overflow. */
  maxBufferSize?: number;
  /** Maximum events per HTTP batch. */
  maxBatchSize?: number;
  /** How often the client auto-flushes (ms). 0 disables the interval timer. */
  flushIntervalMs?: number;

  // Retry.
  /** Transient retries before a batch is returned to the buffer. */
  maxRetries?: number;
  baseRetryMs?: number;
  maxRetryMs?: number;
  /** Cap on auto-flush attempts so a downed server can't spin the timer. */
  maxConsecutiveFailures?: number;

  // Redaction.
  /** Built-in + extra-key redaction applied to captured attributes. */
  redaction?: RedactionConfig;
  /** User hook run per (key,value) before capture; return undefined to drop. */
  redactionHook?: RedactionHook;

// Transport / observability.
  /** Inject a fetch-compatible implementation (tests / browsers / Node). The
   *  global fetch satisfies this; so does any recorded stub. */
  fetchImpl?: FetchLike;
  /** Diagnostic sink; never throws. */
  log?: (msg: string, ...rest: unknown[]) => void;
  /** When false, emit() is a no-op. Default true. */
  enabled?: boolean;
}

/** Minimal fetch-compatible transport; the global fetch satisfies it. */
export type FetchLike = (url: string, init?: RequestInit) => Promise<Response>;

type Status = "ok" | "retryable" | "permanent";

export interface ClientStats {
  emitted: number;
  buffered: number;
  dropped: number;
  sent: number;
  retries: number;
  batchFailures: number;
}

const DEFAULTS = {
  maxBufferSize: 5000,
  maxBatchSize: 100,
  flushIntervalMs: 1000,
  maxRetries: 5,
  baseRetryMs: 200,
  maxRetryMs: 8000,
  maxConsecutiveFailures: 10,
};

interface Cfg {
  maxBufferSize: number;
  maxBatchSize: number;
  flushIntervalMs: number;
  maxRetries: number;
  baseRetryMs: number;
  maxRetryMs: number;
  maxConsecutiveFailures: number;
}

export class ObservatoryClient {
  private readonly endpoint: string;
  private readonly apiKey: string;
  private readonly projectId?: string;
  private readonly fetchImpl?: FetchLike;
  private readonly log?: (msg: string, ...rest: unknown[]) => void;
  private readonly redaction: RedactionConfig;
  private readonly hook?: RedactionHook;
  private readonly enabled: boolean;
  private readonly cfg: Cfg;

  private readonly pending: EventEnvelope[] = [];
  private flushing = false;
  private dirty = false;
  private timer: ReturnType<typeof setInterval> | null = null;
  private stopped = false;
  private consecutiveFailures = 0;

  private readonly counters: ClientStats = {
    emitted: 0,
    buffered: 0,
    dropped: 0,
    sent: 0,
    retries: 0,
    batchFailures: 0,
  };
  private activeDrain: Promise<unknown> | null = null;

  constructor(opts: ClientOptions) {
    this.endpoint = opts.endpoint;
    this.apiKey = opts.apiKey;
    this.projectId = opts.projectId;
    this.fetchImpl = opts.fetchImpl ?? (typeof fetch === "function" ? fetch : undefined);
    this.log = opts.log;
    this.redaction = opts.redaction ?? {};
    this.hook = opts.redactionHook;
    this.enabled = opts.enabled ?? true;
    this.cfg = {
      ...DEFAULTS,
      maxBufferSize: opts.maxBufferSize ?? DEFAULTS.maxBufferSize,
      maxBatchSize: opts.maxBatchSize ?? DEFAULTS.maxBatchSize,
      flushIntervalMs: opts.flushIntervalMs ?? DEFAULTS.flushIntervalMs,
      maxRetries: opts.maxRetries ?? DEFAULTS.maxRetries,
      baseRetryMs: opts.baseRetryMs ?? DEFAULTS.baseRetryMs,
      maxRetryMs: opts.maxRetryMs ?? DEFAULTS.maxRetryMs,
      maxConsecutiveFailures: opts.maxConsecutiveFailures ?? DEFAULTS.maxConsecutiveFailures,
    };

    if (this.cfg.flushIntervalMs > 0 && typeof setInterval === "function") {
      this.timer = setInterval(() => {
        void this.autoFlush();
      }, this.cfg.flushIntervalMs);
      // Don't keep a process alive just for the telemetry timer.
      (this.timer as unknown as { unref?: () => void }).unref?.();
    }
  }

  /** True when emit() should be a no-op. */
  get disabled(): boolean {
    return !this.enabled || this.cfg.maxBufferSize <= 0;
  }

  /** The content-addressed key for a batch; stable across resubmits. */
  static batchKey(batch: EventEnvelope[]): string {
    return batchIdempotencyKey(batch);
  }

  /**
   * Queue an event. Non-blocking: this method performs no I/O. Per-event
   * dedup happens on the control plane; the wire envelope needs no key field.
   */
  emit(envelope: EventEnvelope): void {
    if (this.disabled || this.stopped) return;
    this.counters.emitted++;
    this.pending.push({
      ...envelope,
      schema_version: envelope.schema_version || SCHEMA_VERSION,
      project_id: envelope.project_id || this.projectId || "",
    });
    this.counters.buffered = this.pending.length;
    this.enforceCap();
    this.scheduleFlush();
  }

  /** Apply built-in + configured redaction to a value the caller will capture. */
  scrub(value: unknown): unknown {
    return redact(value, this.redaction).value;
  }

  /**
   * Run the user redaction hook for one attribute. Returns the value to
   * capture, or undefined to drop the field entirely.
   *
   * This hook now runs automatically over every attribute of every emitted
   * event in the instrumentation capture path (Run.emitEvent), so a value
   * the hook drops or transforms never reaches the client buffer. This is
   * the escape hatch for manual capture: values you redact yourself with
   * scrub()/applyHook still go through the same hook. A hook that throws
   * is logged via the client's log sink and the original value is kept —
   * telemetry must never crash the instrumented agent.
   */
  redactAttr(key: string, value: unknown): unknown {
    try {
      const { value: v, dropped } = applyHook(key, value, this.hook);
      return dropped ? undefined : v;
    } catch (err) {
      this.log?.("redaction hook error; attribute kept as-is", err);
      return value;
    }
  }

  /**
   * Run the optional user redaction hook for one attribute. Returns the value
   * to capture, or undefined to drop the field entirely.
   */
  protected applyHook(key: string, value: unknown): unknown {
    const { value: v, dropped } = applyHook(key, value, this.hook);
    return dropped ? undefined : v;
  }

  private enforceCap(): void {
    const over = this.pending.length - this.cfg.maxBufferSize;
    if (over > 0) {
      this.pending.splice(0, over);
      this.counters.dropped += over;
      this.counters.buffered = this.pending.length;
      this.log?.("buffer overflow; dropped %d oldest event(s)", over);
    }
  }

  // Coalesce bursts into one drain via a microtask so emit() stays non-blocking
  // and we don't fire a request per event.
  private scheduleFlush(): void {
    if (this.flushing || this.dirty) return;
    this.dirty = true;
    const f = () => {
      this.dirty = false;
      void this.autoFlush();
    };
    if (typeof queueMicrotask === "function") queueMicrotask(f);
    else if (typeof setImmediate === "function") setImmediate(f);
    else setTimeout(f, 0);
  }

  private async autoFlush(): Promise<void> {
    // Backed off because the server kept failing: stop hammering it.
    if (this.consecutiveFailures >= this.cfg.maxConsecutiveFailures) return;
    const p = this.drainBatch();
    this.activeDrain = p;
    try {
      await p;
    } finally {
      if (this.activeDrain === p) this.activeDrain = null;
    }
  }

  /**
   * Drain one batch with the retry budget. On transient exhaustion the batch
   * is returned to the buffer for later redelivery (at-least-once); on a
   * permanent 4xx it is dropped and counted.
   */
  private async drainBatch(): Promise<Status | "idle"> {
    if (this.flushing || this.pending.length === 0) return "idle";
    this.flushing = true;
    const batch = this.pending.splice(0, this.cfg.maxBatchSize);
    try {
      const status = await this.send(batch, ObservatoryClient.batchKey(batch));
      switch (status) {
        case "ok":
          this.consecutiveFailures = 0;
          break;
        case "retryable":
          this.pending.unshift(...batch);
          this.counters.buffered = this.pending.length;
          this.consecutiveFailures++;
          this.log?.("batch deferred for redelivery (transient)");
          break;
        case "permanent":
          this.counters.dropped += batch.length;
          this.counters.batchFailures++;
          this.consecutiveFailures++;
          this.log?.("batch dropped (permanent failure), n=%d", batch.length);
          break;
      }
      return status;
    } finally {
      this.flushing = false;
    }
  }

  private async send(batch: EventEnvelope[], key: string): Promise<Status> {
    for (let attempt = 0; attempt <= this.cfg.maxRetries; attempt++) {
      if (attempt > 0) {
        this.counters.retries++;
        await this.sleep(this.jitter(attempt));
      }
      try {
        const res = await this.post(batch, key);
        if (res.status >= 200 && res.status < 300) {
          this.counters.sent += batch.length;
          this.counters.buffered = this.pending.length;
          return "ok";
        }
        if (res.status === 429 || res.status >= 500) continue; // transient
        return "permanent"; // 4xx: retrying won't help
      } catch {
        continue; // network error: transient
      }
    }
    return "retryable";
  }

  private async post(batch: EventEnvelope[], key: string): Promise<Response> {
    if (!this.fetchImpl) {
      throw new Error("no fetch implementation available");
    }
    const url = this.endpoint.replace(/\/+$/, "") + "/v1/ingest/events";
    return this.fetchImpl(url, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        Authorization: `Bearer ${this.apiKey}`,
        "X-Obs-Key": this.apiKey,
      },
      body: JSON.stringify({ events: batch, idempotency_key: key }),
    });
  }

  // Full jitter: uniform in [0, min(cap, base * 2^attempt)].
  private jitter(attempt: number): number {
    const cap = Math.min(this.cfg.maxRetryMs, this.cfg.baseRetryMs * 2 ** attempt);
    return Math.floor(Math.random() * cap);
  }

  private sleep(ms: number): Promise<void> {
    return new Promise((resolve) => setTimeout(resolve, ms));
  }

  /**
   * Flush-on-shutdown hook: deliver all buffered events, then stop the timer.
   * Await this before a process or page exits. Unresolvable batches are
   * counted, never thrown — telemetry must not take the agent down with it.
   */
  async flush(): Promise<void> {
    this.stopTimer();
    // A coalesced auto-flush may still be in flight; wait for it to settle so
    // its retry outcome (requeued batch) is visible before we decide to drain.
    if (this.activeDrain) await this.activeDrain;
    let guard = 0;
    while (this.pending.length > 0 && guard++ < this.cfg.maxBufferSize) {
      const status = await this.drainBatch();
      if (status === "retryable" || status === "permanent") break;
    }
    if (this.activeDrain) await this.activeDrain;
  }

  /** Stop auto-flush timers without draining (call flush() to drain first). */
  shutdown(): void {
    this.stopTimer();
    this.stopped = true;
  }

  private stopTimer(): void {
    if (this.timer) {
      clearInterval(this.timer);
      this.timer = null;
    }
  }

  /** Counter snapshot for diagnostics and tests. */
  stats(): ClientStats {
    return { ...this.counters, buffered: this.pending.length };
  }
}

/**
 * Content-addressed, order-insensitive idempotency key for a batch.
 *
 * FNV-1a over the canonical (sorted) list of each event's identity tuple
 * (run_id | sequence | event_id). A resubmitted batch carries the identical
 * tuples, so it yields the identical key; the queue's MessageID dedup and the
 * pipeline's idempotency table collapse the duplicate. Sorting the tuples
 * means reordering within a batch cannot mask a duplicate.
 */
export function batchIdempotencyKey(batch: EventEnvelope[]): string {
  const tuples = batch
    .map((e) => [e.run_id, String(e.sequence), e.event_id] as const)
    .sort((a, b) => cmp(a[0], b[0]) || cmp(a[1], b[1]) || cmp(a[2], b[2]))
    .map((t) => t.join("|"))
    .join("\u0000");
  return "batch-" + fnv1a(tuples).toString(16).padStart(8, "0");
}

function cmp(a: string, b: string): number {
  if (a < b) return -1;
  if (a > b) return 1;
  return 0;
}

function fnv1a(s: string): number {
  let h = 0x811c9dc5;
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i);
    h = Math.imul(h, 0x01000193);
  }
  return h >>> 0;
}
