package event

import (
	"strings"
	"testing"
)

// Redaction and secret-leak tests (spec 14). Payloads are redacted by default;
// the server is the second line of defence after the client-side redaction.

func TestIsSensitiveKey(t *testing.T) {
	cases := map[string]bool{
		"api_key":        true,
		"Authorization":  true,
		"access_token":   true,
		"user_password":  true,
		"cookie":         true,
		"client_secret":  true,
		"order_id":       false,
		"request_id":     false,
		"model_calls":    false, // no sensitive substring
	}
	for k, want := range cases {
		if got := IsSensitiveKey(k); got != want {
			t.Errorf("IsSensitiveKey(%q) = %v, want %v", k, got, want)
		}
	}
}

// A value that looks like a secret in an innocuous key is scrubbed.
func TestScrubStringPatterns(t *testing.T) {
	if s := ScrubString("use AKIAABCDEFGHIJKLMNOP"); !strings.Contains(s, "[REDACTED:aws-key]") {
		t.Errorf("AWS key not scrubbed: %q", s)
	}
	if s := ScrubString("mail j.doe@example.com"); !strings.Contains(s, "[REDACTED:email]") {
		t.Errorf("email not scrubbed: %q", s)
	}
	if s := ScrubString("Authorization: Bearer abcdefghijkl"); !strings.Contains(s, "[REDACTED:bearer]") {
		t.Errorf("bearer token not scrubbed: %q", s)
	}
	// An ordinary value is untouched.
	if s := ScrubString("status ok"); s != "status ok" {
		t.Errorf("ordinary string was altered: %q", s)
	}
}

func TestRedactSensitiveValueReplaced(t *testing.T) {
	in := map[string]any{
		"api_key":   "AKIASECRET",
		"order_id":  42,
		"nested":    map[string]any{"session": "s-x", "tool": "ok"},
		"tool_name": "search",
	}
	out := Redact(in, nil)
	if out["api_key"] != "[REDACTED]" {
		t.Errorf("api_key not redacted: %v", out["api_key"])
	}
	if out["order_id"] != 42 {
		t.Errorf("non-sensitive value mangled: %v", out["order_id"])
	}
	if out["tool_name"] != "search" {
		t.Errorf("tool_name mangled: %v", out["tool_name"])
	}
	nested, _ := out["nested"].(map[string]any)
	if nested["session"] != "[REDACTED]" {
		t.Errorf("nested sensitive key not redacted: %v", nested["session"])
	}
	if nested["tool"] != "ok" {
		t.Errorf("nested safe value mangled: %v", nested["tool"])
	}
}

// Extra project-configured keys are redacted too.
func TestRedactExtraKeys(t *testing.T) {
	in := map[string]any{"customer_ref": "C-1", "note": "hello@world.com"}
	out := Redact(in, []string{"customer_ref"})
	if out["customer_ref"] != "[REDACTED]" {
		t.Errorf("configured extra key not redacted: %v", out["customer_ref"])
	}
	// A string value that merely looks like an email is scrubbed, not dropped.
	if !strings.Contains(out["note"].(string), "[REDACTED:email]") {
		t.Errorf("email in a value not scrubbed: %v", out["note"])
	}
}

// The most important property: no secret survives.
func TestRedactNeverLeaksSecret(t *testing.T) {
	secret := "AKIA0123456789ABCDEF"
	in := map[string]any{
		"secret_field": secret,
		"plain":        "totally fine",
	}
	out := Redact(in, []string{"secret_field"})
	if out["secret_field"] == secret {
		t.Fatalf("secret value leaked through redaction")
	}
}
