// Package logx provides structured logs (slog) whose attributes are
// redacted before writing (spec NFR: structured logs redact sensitive
// payloads).
package logx

import (
	"context"
	"io"
	"log/slog"
	"strings"

	"github.com/ensembyte/observatory/internal/event"
)

// NewLogger builds a JSON logger on out at level. Attribute keys that look
// sensitive are replaced with a redaction marker; map values pass through a
// recursive scrub of string leaves.
func NewLogger(out io.Writer, level slog.Leveler, extraSensitive []string) *slog.Logger {
	h := &redactingHandler{
		inner:     slog.NewJSONHandler(out, &slog.HandlerOptions{Level: level}),
		sensitive: newSensitiveSet(extraSensitive),
	}
	return slog.New(h)
}

type sensitiveSet map[string]bool

func newSensitiveSet(extra []string) sensitiveSet {
	s := sensitiveSet{}
	for _, k := range event.SensitiveKeys {
		s[k] = true
	}
	for _, k := range extra {
		s[strings.ToLower(k)] = true
	}
	return s
}

type redactingHandler struct {
	inner     slog.Handler
	sensitive sensitiveSet
}

func (h *redactingHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

func (h *redactingHandler) Handle(ctx context.Context, rec slog.Record) error {
	// Copy the record's attributes, redacting sensitive keys and scrubbing
	// string/map leaves, so no secret reaches the log sink.
	var cleaned []slog.Attr
	rec.Attrs(func(a slog.Attr) bool {
		cleaned = append(cleaned, h.cleanAttr(a))
		return true
	})
	// Build a new record with the same time/level/message/PC but the
	// cleaned attribute set (Record attrs are immutable after creation).
	nrec := slog.NewRecord(rec.Time, rec.Level, rec.Message, rec.PC)
	nrec.AddAttrs(cleaned...)
	return h.inner.Handle(ctx, nrec)
}

func (h *redactingHandler) cleanAttr(a slog.Attr) slog.Attr {
	if h.isSensitive(a.Key) {
		return slog.Attr{Key: a.Key, Value: slog.StringValue("[REDACTED]")}
	}
	if m, ok := a.Value.Any().(map[string]any); ok {
		return slog.Attr{Key: a.Key, Value: slog.AnyValue(h.scrubMap(m))}
	}
	if s, ok := a.Value.Any().(string); ok {
		return slog.Attr{Key: a.Key, Value: slog.StringValue(event.ScrubString(s))}
	}
	return a
}

func (h *redactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	cleaned := make([]slog.Attr, 0, len(attrs))
	for _, a := range attrs {
		cleaned = append(cleaned, h.cleanAttr(a))
	}
	return &redactingHandler{inner: h.inner.WithAttrs(cleaned), sensitive: h.sensitive}
}

func (h *redactingHandler) WithGroup(name string) slog.Handler {
	return &redactingHandler{inner: h.inner.WithGroup(name), sensitive: h.sensitive}
}

func (h *redactingHandler) isSensitive(key string) bool {
	lk := strings.ToLower(key)
	for s := range h.sensitive {
		if strings.Contains(lk, s) {
			return true
		}
	}
	return false
}

func (h *redactingHandler) scrubMap(m map[string]any) any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		switch val := v.(type) {
		case string:
			if h.isSensitive(k) {
				out[k] = "[REDACTED]"
			} else {
				out[k] = event.ScrubString(val)
			}
		default:
			if h.isSensitive(k) {
				out[k] = "[REDACTED]"
			} else {
				out[k] = v
			}
		}
	}
	return out
}
