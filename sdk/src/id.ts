// Dependency-free ID generation. Prefers the Web Crypto API (Node 19+ and
// all modern browsers) and falls back to a Math.random-derived hex id so the
// SDK works in restricted runtimes without native deps.

type CryptoCtor = { getRandomValues(arr: Uint8Array): void };

function globalCrypto(): CryptoCtor | undefined {
  if (typeof globalThis !== "undefined") {
    const g = globalThis as { crypto?: CryptoCtor };
    if (g.crypto && typeof g.crypto.getRandomValues === "function") {
      return g.crypto;
    }
  }
  return undefined;
}

/** Generate a new event/span id (128-bit hex, uuid-shaped). */
export function newId(): string {
  const c = globalCrypto();
  if (c) {
    const buf = new Uint8Array(16);
    c.getRandomValues(buf);
    // RFC4122 v4 bits.
    buf[6] = (buf[6] & 0x0f) | 0x40;
    buf[8] = (buf[8] & 0x3f) | 0x80;
    const hex = Array.from(buf, (b) => b.toString(16).padStart(2, "0")).join("");
    return (
      hex.slice(0, 8) + "-" + hex.slice(8, 12) + "-" + hex.slice(12, 16) + "-" +
      hex.slice(16, 20) + "-" + hex.slice(20, 32)
    );
  }
  return "id-" + hex(16);
}

/** A short random hex string. */
export function hex(bytes: number): string {
  let out = "";
  let v = 0;
  for (let i = 0; i < bytes; i++) {
    if (i % 4 === 0) v = (Math.random() * 0x100000000) >>> 0;
    out += (v & 0xf).toString(16);
    v >>>= 4;
  }
  return out;
}

/**
 * Compute a stable content-addressed idempotency key for an event. The key is
 * derived from the envelope's identity fields so that a retried batch with the
 * same logical event produces the same key and the pipeline dedupes it
 * (spec NFR: at-least-once delivery with idempotent processing).
 */
export function idempotencyKey(
  projectId: string,
  runId: string,
  spanId: string | undefined,
  eventType: string,
  sequence: number,
  eventId: string
): string {
  const s = [projectId, runId, spanId ?? "", eventType, String(sequence), eventId].join("|");
  // FNV-1a over the joined string for a compact, stable key.
  let h = 0x811c9dc5;
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i);
    h = Math.imul(h, 0x01000193);
  }
  return "idem-" + (h >>> 0).toString(16) + "-" + sequence.toString(16);
}
