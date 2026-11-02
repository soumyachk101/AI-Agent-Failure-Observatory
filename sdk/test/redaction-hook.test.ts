import { describe, expect, it, vi } from "vitest";
import { Observatory } from "../src/instrument";
import { ObservatoryClient } from "../src/client";
import { EventEnvelope } from "../src/types";
import { Attr } from "../src/types";

/** A fetch stub that records bodies and always succeeds. */
function recorder() {
  const fake = vi.fn(async (_url: string, _init?: RequestInit) => {
    return new Response(JSON.stringify({ accepted: 1 }), { status: 200 });
  });
  return { fake };
}

describe("redactionHook: capture path", () => {
  it("runs the hook on emitted events, not just client.scrub", async () => {
    const seen: string[] = [];
    const rec = recorder();
    const client = new ObservatoryClient({
      endpoint: "http://x",
      apiKey: "k",
      flushIntervalMs: 0,
      baseRetryMs: 1,
      fetchImpl: rec.fake,
      redactionHook: (key, _value) => {
        seen.push(key);
        return key === "internal.note" ? undefined : _value;
      },
    });
    const obs = new Observatory(client, "acme");
    const run = obs.startRun();
    run.event("evaluation.started", { "internal.note": "drop me", tool_name: "keep me" });
    run.complete();
    await client.flush();

    // The hook saw the attribute of the emitted event (capture path).
    expect(seen).toContain("internal.note");
    const body = JSON.parse(String(rec.fake.mock.calls[0]![1].body));
    const ev = body.events.find(
      (e: EventEnvelope) => e.event_type === "evaluation.started"
    ) as EventEnvelope;
    // Hook output replaces attributes before buffering...
    expect(ev.attributes["internal.note"]).toBeUndefined();
    // ...while untouched attributes are preserved.
    expect(ev.attributes.tool_name).toBe("keep me");
    client.shutdown();
  });

  it("hook-transformed values reach the buffered event, not the originals", async () => {
    const rec = recorder();
    const client = new ObservatoryClient({
      endpoint: "http://x",
      apiKey: "k",
      flushIntervalMs: 0,
      baseRetryMs: 1,
      fetchImpl: rec.fake,
      redactionHook: (key, value) =>
        key === Attr.contextTokensDropped ? `masked(${value})` : value,
    });
    const obs = new Observatory(client, "acme");
    obs.startRun().state("f1", { progress: true }).retrieval({
      truncated: true,
      tokensDropped: 99,
    });
    await client.flush();

    const body = JSON.parse(String(rec.fake.mock.calls[0]![1].body));
    const ev = body.events.find(
      (e: EventEnvelope) => e.event_type === "retrieval.completed"
    ) as EventEnvelope;
    expect(ev.attributes[Attr.contextTokensDropped]).toBe("masked(99)");
    client.shutdown();
  });

  it("a throwing hook is absorbed: logged, original value kept, no crash", async () => {
    const logs: unknown[][] = [];
    const rec = recorder();
    const client = new ObservatoryClient({
      endpoint: "http://x",
      apiKey: "k",
      flushIntervalMs: 0,
      baseRetryMs: 1,
      fetchImpl: rec.fake,
      log: (...args: unknown[]) => logs.push(args),
      redactionHook: () => {
        throw new Error("bad hook");
      },
    });
    const obs = new Observatory(client, "acme");
    const run = obs.startRun();
    expect(() => {
      run.event("evaluation.started", { state_fingerprint: "abc" });
      run.complete();
    }).not.toThrow();
    await client.flush(); // resolves, run not crashed

    const body = JSON.parse(String(rec.fake.mock.calls[0]![1].body));
    const ev = body.events.find(
      (e: EventEnvelope) => e.event_type === "evaluation.started"
    ) as EventEnvelope;
    expect(ev.attributes.state_fingerprint).toBe("abc");
    expect(logs.some((l) => String(l[0]).includes("redaction hook error"))).toBe(
      true
    );
    client.shutdown();
  });
});

describe("redactionHook: manual escape hatch still works", () => {
  it("client.scrub uses the built-in redaction without the user hook", () => {
    const client = new ObservatoryClient({
      endpoint: "http://x",
      apiKey: "k",
      flushIntervalMs: 0,
      redaction: { extraKeys: ["tenant_hint"] },
      redactionHook: () => "never called by scrub",
    });
    const out = client.scrub({ tenant_hint: "acme", tool: "x" }) as Record<
      string,
      unknown
    >;
    expect(out.tenant_hint).toBe("[REDACTED]");
    expect(out.tool).toBe("x");
    client.shutdown();
  });
});
