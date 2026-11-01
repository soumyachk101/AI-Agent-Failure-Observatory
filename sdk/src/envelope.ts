// Envelope helpers shared by the instrumentation layer.
//
// The control plane (server/internal/event) computes each event's idempotency
// key as a pipe-join of its identity fields and uses it to make at-least-once
// delivery collapse to effective-once. `eventKey` reproduces that key exactly,
// byte-for-byte, so a redelivered logical event always produces the same key
// and the pipeline dedups it. It is deterministic in the identity fields and
// deliberately independent of the wall-clock timestamp.

import { AgentInfo, EventEnvelope, ProviderInfo, SCHEMA_VERSION } from "./types";
import { newId } from "./id";

/** A deterministic RFC3339 UTC timestamp (ms precision, 'Z'). */
export function nowRfc3339(d: Date = new Date()): string {
  return d.toISOString();
}

// Fields that make up an event's identity (spec 7). Same logical event =>
// same key; the wall-clock timestamp is NOT part of it.
export interface EventIdentity {
  project_id: string;
  run_id: string;
  span_id?: string;
  event_type: string;
  sequence: number;
  event_id: string;
}

/**
 * Per-event idempotency key, identical to the server's
 * `event.Envelope.IdempotencyKey()`:
 *   project_id | run_id | span_id | event_type | sequence | event_id
 *
 * Redelivering the same logical event (same event_id + sequence) yields the
 * same key, so the ingestion pipeline processes it exactly once even though
 * the transport is at-least-once (spec NFR: idempotent event processing).
 */
export function eventKey(e: EventIdentity): string {
  return [
    e.project_id,
    e.run_id,
    e.span_id ?? "",
    e.event_type,
    String(e.sequence),
    e.event_id,
  ].join("|");
}

export interface EnvelopeDraft {
  type: EventEnvelope["event_type"];
  attrs: Record<string, unknown>;
  sequence: number;
  spanId?: string;
  parentSpanId?: string;
  timestamp?: string;
  agent?: AgentInfo;
  provider?: ProviderInfo;
  eventId?: string;
  payloadRef?: string;
}

/**
 * Build a wire envelope with every shared field stamped. The caller supplies
 * the authoritative per-run `sequence`. `event_id` is generated when omitted
 * (the control plane rejects envelopes without one), and `idempotency_key` is
 * derived from the identity fields so the envelope is fully self-describing.
 */
export function makeEnvelope(projectId: string, runId: string, d: EnvelopeDraft): EventEnvelope {
  const event_id = d.eventId ?? newId();
  const env: EventEnvelope = {
    schema_version: SCHEMA_VERSION,
    event_id,
    project_id: projectId,
    run_id: runId,
    sequence: d.sequence,
    event_type: d.type,
    timestamp: d.timestamp ?? nowRfc3339(),
    attributes: d.attrs,
    payload_ref: d.payloadRef ?? null,
    idempotency_key: "",
  };
  if (d.spanId) env.span_id = d.spanId;
  if (d.parentSpanId) env.parent_span_id = d.parentSpanId;
  if (d.agent) env.agent = d.agent;
  if (d.provider) env.provider = d.provider;
  env.idempotency_key = eventKey(env);
  return env;
}
