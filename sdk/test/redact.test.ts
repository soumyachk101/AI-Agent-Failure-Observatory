import { describe, expect, it } from "vitest";
import {
  SENSITIVE_KEY_SUBSTRINGS,
  applyHook,
  isSensitiveKey,
  redact,
  scrubString,
} from "../src/redact";
import { ObservatoryClient } from "../src/client";

describe("isSensitiveKey", () => {
  it("matches the built-in sensitive substrings", () => {
    for (const s of SENSITIVE_KEY_SUBSTRINGS) {
      expect(isSensitiveKey("X" + s.toUpperCase())).toBe(true);
    }
  });

  it("respects extra configured keys", () => {
    expect(isSensitiveKey("sso_ticket", ["sso"])).toBe(true);
    expect(isSensitiveKey("sso_ticket", [])).toBe(false);
    expect(isSensitiveKey("request_id", ["sso"])).toBe(false);
  });
});

describe("scrubString", () => {
  it("redacts AWS keys, JWTs, bearer tokens, emails and card numbers", () => {
    expect(scrubString("key=AKIAABCDEFGHIJKLMNOP")).toBe("key=[REDACTED:aws-key]");
    expect(scrubString("x eyJhbGciOiJ.eyJzdWIiOiIxIn0 tail")).toContain("[REDACTED:jwt]");
    expect(scrubString("Authorization: Bearer abcdefghijkl")).toBe(
      "Authorization: [REDACTED:bearer]"
    );
    expect(scrubString("mail me at jane.doe@example.com please")).toContain("[REDACTED:email]");
    expect(scrubString("card 4111 1111 1111 1111")).toContain("[REDACTED:card]");
  });

  it("leaves ordinary strings untouched", () => {
    expect(scrubString("order id 42")).toBe("order id 42");
  });
});

describe("redact (deep, structure-preserving)", () => {
  it("replaces sensitive key values with the marker, keeping structure", () => {
    const out = redact({
      headers: { Authorization: "Bearer abcdefgh", "Content-Type": "text/plain" },
      count: 3,
      email: "jane@example.com",
    });
    expect(out.redacted).toBe(true);
    const v = out.value as Record<string, unknown>;
    const headers = v.headers as Record<string, unknown>;
    expect(headers.Authorization).toBe("[REDACTED]");
    // Non-sensitive key with a scrubbable value keeps structure but is scrubbed.
    expect(headers["Content-Type"]).toBe("text/plain");
    expect(v.count).toBe(3);
    expect(v.email).toBe("[REDACTED]");
  });

  it("honours the configured marker and extra keys", () => {
    const out = redact({ sso: "tok", user: "alice" }, {
      extraKeys: ["user"],
      marker: "<redacted>",
    });
    const v = out.value as Record<string, unknown>;
    expect(v.user).toBe("<redacted>");
    expect(v.sso).toBe("tok");
  });

  it("scrubs string items inside arrays", () => {
    const out = redact({ contacts: ["a@b.co", "ok"] });
    const v = out.value as { contacts: string[] };
    expect(v.contacts[0]).toContain("[REDACTED:email]");
    expect(v.contacts[1]).toBe("ok");
  });
});

describe("applyHook (user redaction hook)", () => {
  it("returns the value untouched without a hook", () => {
    expect(applyHook("k", 42, undefined)).toEqual({ value: 42, dropped: false });
  });

  it("transforms values via the hook", () => {
    const out = applyHook("k", "secret", (_k, v) => `hash(${String(v)})`);
    expect(out).toEqual({ value: "hash(secret)", dropped: false });
  });

  it("drops fields when the hook returns undefined", () => {
    expect(applyHook("k", 1, () => undefined)).toEqual({ value: undefined, dropped: true });
  });
});

describe("ObservatoryClient.scrub", () => {
  it("applies the client's configured redaction to captured values", () => {
    const client = new ObservatoryClient({
      endpoint: "http://localhost:8080",
      apiKey: "obs_test",
      redaction: { extraKeys: ["tenant_hint"] },
    });
    const out = client.scrub({
      tenant_hint: "acme",
      api_key: "AKIAABCDEFGHIJKLMNOP",
      tool: "order.lookup",
    }) as Record<string, unknown>;
    expect(out.tenant_hint).toBe("[REDACTED]");
    expect(out.api_key).toBe("[REDACTED]");
    expect(out.tool).toBe("order.lookup");
    client.shutdown();
  });
});
