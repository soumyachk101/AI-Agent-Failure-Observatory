// Client-side redaction. Payload capture is OFF by default (spec FR-02 /
// NFR Privacy); when a project opts in, captured attributes are redacted here
// before they ever leave the process, and the server redacts again as a
// second layer. This is the first line of defence: even a compromised server
// only ever receives already-redacted content.

/** Lowercased key substrings that mark an attribute sensitive. */
export const SENSITIVE_KEY_SUBSTRINGS: readonly string[] = [
  "api_key", "apikey", "authorization", "access_token", "token", "secret",
  "password", "credential", "cookie", "session", "email", "phone", "ssn",
  "card_number", "api-key", "private_key", "bearer",
];

/** Built-in value scrubbers applied to string values. */
const SCRUBBERS: ReadonlyArray<{ re: RegExp; label: string }> = [
  { re: /AKIA[0-9A-Z]{16}/g, label: "[REDACTED:aws-key]" },
  { re: /\beyJ[A-Za-z0-9_-]+\.eyJ[A-Za-z0-9_-]+/g, label: "[REDACTED:jwt]" },
  { re: /\bbearer\s+[A-Za-z0-9._\-+/=]{8,}/gi, label: "[REDACTED:bearer]" },
  { re: /\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}\b/g, label: "[REDACTED:email]" },
  { re: /\b(?:\d[ -]?){13,19}\d\b/g, label: "[REDACTED:card]" },
];

/** Extra sensitive keys a project can configure beyond the built-ins. */
export type RedactionConfig = {
  extraKeys?: string[];
  /** Replace values with this marker. */
  marker?: string;
};

export type RedactionResult = {
  value: unknown;
  redacted: boolean;
};

/** True when a key looks sensitive. */
export function isSensitiveKey(key: string, extraKeys: readonly string[] = []): boolean {
  const lk = key.toLowerCase();
  for (const s of SENSITIVE_KEY_SUBSTRINGS) {
    if (lk.includes(s)) return true;
  }
  for (const s of extraKeys) {
    if (lk.includes(s.toLowerCase())) return true;
  }
  return false;
}

/** Scrub a string value with built-in patterns. */
export function scrubString(s: string): string {
  let out = s;
  for (const { re, label } of SCRUBBERS) {
    out = out.replace(re, label);
  }
  return out;
}

/**
 * Deep-redact a value. Sensitive keys are replaced with the marker; other
 * string values are scrubbed. Structure is preserved so diagnostics keep
 * working (spec NFR Explainability).
 */
export function redact(value: unknown, cfg: RedactionConfig = {}): RedactionResult {
  const marker = cfg.marker ?? "[REDACTED]";
  const extra = cfg.extraKeys ?? [];
  let anyRedacted = false;

  const walk = (v: unknown): unknown => {
    if (v === null || typeof v !== "object") {
      if (typeof v === "string") {
        const scrubbed = scrubString(v);
        if (scrubbed !== v) anyRedacted = true;
        return scrubbed;
      }
      return v;
    }
    if (Array.isArray(v)) {
      return v.map((item) => {
        if (typeof item === "string") {
          const scrubbed = scrubString(item);
          if (scrubbed !== item) anyRedacted = true;
          return scrubbed;
        }
        return item;
      });
    }
    const out: Record<string, unknown> = {};
    for (const [k, val] of Object.entries(v as Record<string, unknown>)) {
      if (isSensitiveKey(k, extra)) {
        anyRedacted = true;
        out[k] = marker;
      } else {
        out[k] = walk(val);
      }
    }
    return out;
  };

  const result = walk(value);
  return { value: result, redacted: anyRedacted };
}

/**
 * A redaction hook is a user-supplied function that can inspect/transform an
 * attribute value before it is captured. Return `undefined` to drop the
 * field entirely. This is the explicit payload-capture hook (spec FR-02).
 */
export type RedactionHook = (
  key: string,
  value: unknown
) => unknown | undefined;

/** Apply a hook to a value; `undefined` drops the field. */
export function applyHook(
  key: string,
  value: unknown,
  hook: RedactionHook | undefined
): { value: unknown; dropped: boolean } {
  if (!hook) return { value, dropped: false };
  const out = hook(key, value);
  return { value: out, dropped: out === undefined };
}
