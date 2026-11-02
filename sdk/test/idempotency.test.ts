import { describe, expect, it } from "vitest";
import { eventKey, makeEnvelope, nowRfc3339 } from "../src/envelope";
import { batchIdempotencyKey, ObservatoryClient } from "../src/client";
import { EventEnvelope } from "../src/types";

function env(over: Partial<EventEnvelope> = {}): EventEnvelope {
  const base: EventEnvelope = {
    schema_version: "1",
    event_id: "e-1",
    idempotency_key: "",
    project_id: "acme",
    run_id: "run-1",
    span_id: "span-1",
    sequence: 12,
    event_type: "tool.call.completed",
    timestamp: nowRfc3339(),
    attributes: {},
    payload_ref: null,
  };
  return { ...base, ...over };
}

describe("eventKey (per-event idempotency)", () => {
  it("matches the control plane's canonical form exactly", () => {
    // server/internal/event: project|run|span|type|seq|event_id
    const e = env();
    expect(eventKey(e)).toBe("acme|run-1|span-1|tool.call.completed|12|e-1");
  });

  it("treats a missing span as empty (server omits it from the join)", () => {
    const e = env({ event_id: "e-2", sequence: 1, span_id: undefined, event_type: "run.started" });
    expect(eventKey(e)).toBe("acme|run-1||run.started|1|e-2");
  });

  it("is deterministic: same logical event => same key", () => {
    const a = eventKey(env({ timestamp: "2026-01-01T00:00:00.000Z" }));
    const b = eventKey(env({ timestamp: "2026-01-02T00:00:00.000Z" }));
    expect(a).toBe(b); // timestamp is not an identity field
  });

  it("differs when any identity field changes", () => {
    const base = eventKey(env());
    expect(eventKey(env({ sequence: 13 }))).not.toBe(base);
    expect(eventKey(env({ event_id: "e-2" }))).not.toBe(base);
    expect(eventKey(env({ run_id: "run-2" }))).not.toBe(base);
    expect(eventKey(env({ event_type: "model.call.failed" }))).not.toBe(base);
    expect(eventKey(env({ project_id: "other" }))).not.toBe(base);
  });

  it("stamps the envelope's idempotency_key from its identity fields", () => {
    const e = makeEnvelope("acme", "run-1", {
      type: "run.started",
      attrs: {},
      sequence: 1,
      eventId: "evt-a",
    });
    expect(e.idempotency_key).toBe("acme|run-1||run.started|1|evt-a");
    expect(e.schema_version).toBe("1");
  });
});

describe("batchIdempotencyKey (transport-level redelivery key)", () => {
  it("is deterministic for a given batch", () => {
    const batch = [
      env(),
      env({ event_id: "e-2", sequence: 13, event_type: "run.completed" }),
    ];
    expect(batchIdempotencyKey(batch)).toBe(
      batchIdempotencyKey(batch.slice())
    );
  });

  it("is order-insensitive: reordering the batch does not change the key", () => {
    const a = [
      env(),
      env({ event_id: "e-2", sequence: 13 }),
      env({ event_id: "e-3", sequence: 14 }),
    ];
    expect(batchIdempotencyKey(a)).toBe(batchIdempotencyKey([a[2], a[0], a[1]]));
  });

  it("differs when batch content differs", () => {
    const a = batchIdempotencyKey([env()]);
    const b = batchIdempotencyKey([env({ event_id: "e-9" })]);
    expect(a).not.toBe(b);
  });

  it("is exposed on the client for tests and diagnostics", () => {
    const batch = [env()];
    expect(ObservatoryClient.batchKey(batch)).toBe(batchIdempotencyKey(batch));
  });
});
