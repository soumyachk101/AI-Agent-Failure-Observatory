import { describe, expect, it, vi } from "vitest";
import { ObservatoryClient } from "../src/client";
import { EventEnvelope } from "../src/types";

function env(i: number): EventEnvelope {
  return {
    schema_version: "1",
    event_id: `e-${i}`,
    idempotency_key: "",
    project_id: "acme",
    run_id: "run-1",
    sequence: i + 1,
    event_type: i % 2 === 0 ? "run.started" : "run.completed",
    timestamp: "2026-01-01T00:00:00.000Z",
    attributes: {},
    payload_ref: null,
  };
}

/** Advance microtasks so coalesced auto-flushes run. */
async function tick(n = 8): Promise<void> {
  for (let i = 0; i < n; i++) await new Promise((r) => setImmediate(r));
}

/** Record request bodies and script responses per call. */
function makeFetch(
  respond: (body: unknown, call: number) => Response | Response[]
) {
  const calls: { url: string; body: string }[] = [];
  let i = 0;
  const fake = vi.fn(async (url: string, init?: RequestInit) => {
    i++;
    const body = init?.body ? JSON.parse(String(init.body)) : undefined;
    calls.push({ url: String(url), body: String(init?.body ?? "") });
    const next = respond(body, i);
    if (Array.isArray(next)) {
      return next[Math.min(i - 1, next.length - 1)]!;
    }
    return next!;
  });
  return { fake, calls };
}

const ok200 = () => new Response(JSON.stringify({ accepted: 1 }), { status: 200 });

describe("buffering: bounded buffer", () => {
  it("drops the oldest events when the buffer overflows", async () => {
    const { fake } = makeFetch(ok200);
    const client = new ObservatoryClient({
      endpoint: "http://x",
      apiKey: "k",
      maxBufferSize: 3,
      maxBatchSize: 10,
      flushIntervalMs: 0,
      fetchImpl: fake,
      baseRetryMs: 1,
    });
    for (let i = 0; i < 10; i++) client.emit(env(i));
    await tick();
    const s = client.stats();
    expect(s.dropped).toBe(7); // 10 emitted - 3 buffered
    expect(s.buffered).toBe(0); // flushed
    const last = fake.mock.calls[fake.mock.calls.length - 1] as [string, RequestInit];
    const events = (JSON.parse(last[1].body as string).events ?? []) as EventEnvelope[];
    expect(events.map((e) => e.event_id)).toEqual(["e-7", "e-8", "e-9"]);
    client.shutdown();
  });
});

describe("buffering: non-blocking, batched delivery", () => {
  it("coalesces a burst into a single batch request", async () => {
    const { fake } = makeFetch(ok200);
    const client = new ObservatoryClient({
      endpoint: "http://observatory:8080",
      apiKey: "k",
      maxBatchSize: 100,
      flushIntervalMs: 0,
      fetchImpl: fake,
      baseRetryMs: 1,
    });
    for (let i = 0; i < 50; i++) client.emit(env(i));
    await tick();
    expect(fake).toHaveBeenCalledTimes(1);
    const call = fake.mock.calls[0] as unknown as { 0: string; 1: RequestInit };
    expect(call.0).toBe("http://observatory:8080/v1/ingest/events");
    const parsed = JSON.parse(call.1.body as string);
    expect(parsed.events).toHaveLength(50);
    expect(parsed.idempotency_key).toMatch(/^batch-/);
    const headers = call.1.headers as Record<string, string>;
    expect(headers.Authorization).toBe("Bearer k");
    expect(headers["X-Obs-Key"]).toBe("k");
    expect(client.stats().sent).toBe(50);
    client.shutdown();
  });

  it("splits large buffers into maxBatchSize chunks", async () => {
    const { fake } = makeFetch(ok200);
    const client = new ObservatoryClient({
      endpoint: "http://x",
      apiKey: "k",
      maxBatchSize: 4,
      flushIntervalMs: 0,
      fetchImpl: fake,
      baseRetryMs: 1,
    });
    for (let i = 0; i < 9; i++) client.emit(env(i));
    await tick();
    await client.flush();
    expect(fake).toHaveBeenCalledTimes(3); // 4 + 4 + 1
    expect(client.stats().sent).toBe(9);
    client.shutdown();
  });
});

describe("buffering: retry with jitter and safe redelivery", () => {
  it("retries transient 5xx and succeeds", async () => {
    const responses = [
      new Response("boom", { status: 503 }),
      new Response("boom", { status: 503 }),
      ok200(),
    ];
    const { fake } = makeFetch((_b, i) => responses[Math.min(i - 1, responses.length - 1)]!);
    const client = new ObservatoryClient({
      endpoint: "http://x",
      apiKey: "k",
      maxRetries: 5,
      flushIntervalMs: 0,
      fetchImpl: fake,
      baseRetryMs: 1,
      maxRetryMs: 2,
    });
    client.emit(env(0));
    await tick();
    await client.flush();
    expect(client.stats().sent).toBe(1);
    expect(client.stats().retries).toBeGreaterThanOrEqual(2);
    client.shutdown();
  });

  it("defers a batch for redelivery when transient retries are exhausted", async () => {
    const { fake } = makeFetch(() => new Response("boom", { status: 500 }));
    const client = new ObservatoryClient({
      endpoint: "http://x",
      apiKey: "k",
      maxRetries: 1,
      flushIntervalMs: 0,
      fetchImpl: fake,
      baseRetryMs: 1,
      maxRetryMs: 2,
    });
    client.emit(env(0));
    client.emit(env(1));
    await tick();
    await client.flush(); // must resolve even with a dead server
    expect(client.stats().sent).toBe(0);
    // The batch went back into the buffer for a later redelivery.
    expect(client.stats().buffered).toBe(2);
    client.shutdown();
  });

  it("drops a batch on a permanent 4xx (auth/config error) and does not loop", async () => {
    const { fake } = makeFetch(() => new Response("nope", { status: 401 }));
    const client = new ObservatoryClient({
      endpoint: "http://x",
      apiKey: "k",
      maxRetries: 3,
      flushIntervalMs: 0,
      fetchImpl: fake,
      baseRetryMs: 1,
      maxRetryMs: 2,
    });
    client.emit(env(0));
    await tick();
    await client.flush();
    expect(client.stats().sent).toBe(0);
    expect(client.stats().dropped).toBe(1);
    expect(client.stats().batchFailures).toBe(1);
    expect(client.stats().buffered).toBe(0);
    client.shutdown();
  });

  it("a network failure is treated as transient and redelivered later", async () => {
    const fake = vi.fn(async () => {
      throw new Error("socket hang up");
    });
    const client = new ObservatoryClient({
      endpoint: "http://x",
      apiKey: "k",
      maxRetries: 1,
      flushIntervalMs: 0,
      fetchImpl: fake,
      baseRetryMs: 1,
      maxRetryMs: 2,
    });
    client.emit(env(0));
    await tick();
    await client.flush(); // resolves; nothing thrown out of telemetry
    expect(client.stats().buffered).toBe(1);
    client.shutdown();
  });
});

describe("buffering: flush-on-shutdown hook", () => {
  it("flush() drains the buffer and stops the interval timer", async () => {
    const { fake } = makeFetch(ok200);
    const client = new ObservatoryClient({
      endpoint: "http://x",
      apiKey: "k",
      maxBatchSize: 2,
      flushIntervalMs: 0,
      fetchImpl: fake,
      baseRetryMs: 1,
    });
    for (let i = 0; i < 5; i++) client.emit(env(i));
    await client.flush();
    expect(client.stats().sent).toBe(5);
    expect(client.stats().buffered).toBe(0);
    client.shutdown();
  });

  it("emit() after shutdown() is a no-op", async () => {
    const { fake } = makeFetch(ok200);
    const client = new ObservatoryClient({
      endpoint: "http://x",
      apiKey: "k",
      flushIntervalMs: 0,
      fetchImpl: fake,
      baseRetryMs: 1,
    });
    client.emit(env(0));
    await tick();
    client.shutdown();
    const emitted = client.stats().emitted;
    client.emit(env(1));
    expect(client.stats().emitted).toBe(emitted);
    client.shutdown();
  });

  it("the interval timer auto-flushes buffered events", async () => {
    const { fake } = makeFetch(ok200);
    const client = new ObservatoryClient({
      endpoint: "http://x",
      apiKey: "k",
      flushIntervalMs: 20,
      fetchImpl: fake,
      baseRetryMs: 1,
    });
    client.emit(env(0));
    await new Promise((r) => setTimeout(r, 60));
    expect(fake).toHaveBeenCalledTimes(1);
    expect(client.stats().sent).toBe(1);
    client.shutdown();
  });
});
