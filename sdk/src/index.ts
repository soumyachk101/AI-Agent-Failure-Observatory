// @ensembyte/observatory — TypeScript instrumentation SDK for the
// AI Agent Failure Observatory.
//
// Instrument an agent run:
//
//   import { ObservatoryClient, Observatory, estimateUsage } from
//     "@ensembyte/observatory";
//
//   const client = new ObservatoryClient({
//     endpoint: "http://observatory:8080",
//     apiKey: process.env.OBS_API_KEY!,
//     projectId: "acme-support",
//     redaction: { extraKeys: ["sso"] },
//   });
//   const obs = new Observatory(client, "acme-support", {
//     agent: { name: "support-agent", version: "1.4.0" },
//     environment: "production",
//   });
//
//   const run = obs.startRun();
//   const s = run.span("planner");
//   const m = run.model({ provider: { name: "acme", model: "gpt-4o" } });
//   m.end(estimateUsage("prompt...", "answer..."));
//   run.tool({ name: "order.lookup", version: "v3", schemaVersion: "schema-v3",
//               args: { order_id: 42 } }).end({ resultRef: "blob://o1" });
//   run.handoff("verifier", "planner", { spanId: s.id });
//   s.end();
//   run.complete({ taskCompleted: true });
//
//   // Graceful shutdown (never blocks forever; retries are bounded).
//   process.on("SIGTERM", async () => {
//     await client.flush();
//     process.exit(0);
//   });

// Envelope / wire contract.
export * from "./types";

// Event helpers: deterministic timestamps, per-event idempotency key
// (byte-identical to the control plane's), envelope construction.
export {
  eventKey,
  makeEnvelope,
  nowRfc3339,
  type EnvelopeDraft,
  type EventIdentity,
} from "./envelope";

// IDs.
export { newId, hex, idempotencyKey } from "./id";

// Redaction (client-side first line of defence).
export * from "./redact";

// Client: bounded buffer, non-blocking batched delivery, jittered retry,
// flush-on-shutdown, safe redelivery via content-addressed batch keys.
export {
  ObservatoryClient,
  batchIdempotencyKey,
  type ClientOptions,
  type ClientStats,
} from "./client";

// Instrumentation: runs, spans, model/tool calls, handoffs, retrieval,
// state and budget events; usage attribution; async-context helpers.
export {
  Observatory,
  Run,
  AgentSpan,
  ModelCall,
  ToolCall,
  withRun,
  activeRun,
  estimateUsage,
  errorClassOf,
  envelopeKey,
  type ObservatoryOptions,
  type StartRunOptions,
  type ModelCallOptions,
  type ToolCallOptions,
} from "./instrument";
