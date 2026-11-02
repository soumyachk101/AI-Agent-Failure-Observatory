package event

import (
	"regexp"
	"strings"
)

// Redaction policy. Payload capture is off by default; when a project opts in,
// captured attributes are redacted on the server side before persistence.
// This is a second layer: the SDK applies the same policy client-side.

// SensitiveKeys are lowercased substrings matched against attribute keys.
// A key containing any of these has its value replaced (structure kept).
var SensitiveKeys = []string{
	"api_key", "apikey", "authorization", "access_token", "token", "secret",
	"password", "credential", "cookie", "session", "email", "phone",
	"ssn", "card_number", "api-key", "private_key",
}

var scrubbers = []struct {
	pattern *regexp.Regexp
	label   string
}{
	{regexp.MustCompile(`AKIA[0-9A-Z]{16}`), "[REDACTED:aws-key]"},
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]+\.eyJ[A-Za-z0-9_-]+`), "[REDACTED:jwt]"},
	{regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._\-+/=]{8,}`), "[REDACTED:bearer]"},
	{regexp.MustCompile(`\b[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}\b`), "[REDACTED:email]"},
	{regexp.MustCompile(`\b(?:\d[ -]?){13,19}\d\b`), "[REDACTED:card]"},
}

// IsSensitiveKey reports whether k (any case) should be redacted.
func IsSensitiveKey(k string) bool {
	lk := strings.ToLower(k)
	for _, s := range SensitiveKeys {
		if strings.Contains(lk, s) {
			return true
		}
	}
	return false
}

// ScrubString applies built-in pattern scrubbing to a string value.
func ScrubString(s string) string {
	for _, sc := range scrubbers {
		s = sc.pattern.ReplaceAllString(s, sc.label)
	}
	return s
}

// Redact deep-copies m and redacts sensitive leaves. Structure is preserved so
// downstream diagnostics keep working; only values are replaced.
func Redact(m map[string]any, extra []string) map[string]any {
	extraSet := map[string]bool{}
	for _, e := range extra {
		extraSet[strings.ToLower(e)] = true
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = redactValue(k, v, extraSet)
	}
	return out
}

func redactValue(key string, v any, extra map[string]bool) any {
	lk := strings.ToLower(key)
	sensitive := IsSensitiveKey(lk) || extra[lk]
	switch val := v.(type) {
	case map[string]any:
		// Containers are redacted only if the key itself is sensitive, or
		// field-by-field when nested keys are sensitive.
		if sensitive {
			return "[REDACTED]"
		}
		return Redact(val, nil)
	case []any:
		if sensitive {
			return "[REDACTED]"
		}
		for i, item := range val {
			if s, ok := item.(string); ok {
				val[i] = ScrubString(s)
			}
		}
		return val
	case string:
		if sensitive {
			return "[REDACTED]"
		}
		return ScrubString(val)
	default:
		if sensitive {
			return "[REDACTED]"
		}
		return v
	}
}
