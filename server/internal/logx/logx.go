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
	handler := &redactingHandler{
		inner:     slog.NewJSONHandler(out, &slog.HandlerOptions{Level: level}),
		sensitive: sensitiveSet(extraSensitive),
	}
	return slog.New(handler)
}

type sensitiveSet map[string]bool

func sensitiveSet(extra []string) sensitiveSet {
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
	group     string
}

func (h *redactingHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

func (h *redactingHandler) Handle(ctx context.Context, rec slog.Record) (bool, error) {
	cleaned := slog.NewRecord(rec.Time, rec.Level, rec.Message, rec.PC)
	var keep []slog.Attr
	rec.Attrs(func(a slog.Attr) bool {
		if h.isSensitive(a.Key) {
			keep = append(keep, slog.Attr{Key: a.Key, Value: slog.StringValue("[REDACTED]")})
		} else if m, ok := a.Value.Any().(map[string]any); ok {
			keep = append(keep, slog.Attr{Key: a.Key, Value: slog.AnyValue(h.scrubMap(m))})
		} else {
			keep = append(keep, a)
		}
		return true
	})
	cleaned.AddAttrs(keep...)
	if h.group != "" {
		cleaned.AddAttrs() // group prefix is applied by inner.WithGroup; see WithGroup
	}
	return h.inner.Handle(ctx, cleaned)
}

func (h *redactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := &redactingHandler{inner: h.inner, sensitive: h.sensitive}
	attrs = h.filterAttrs(attrs)
	return next.withAttrs(attrs)
}

func (h *redactingHandler) WithGroup(name string) slog.Handler {
	return &redactingHandler{inner: h.inner.WithGroup(name), sensitive: h.sensitive, group: name}
}

func (h *redactingHandler) filterAttrs(attrs []slog.Attr) []slog.Attr {
	out := make([]slog.Attr, 0, len(attrs))
	for _, a := range attrs {
		if h.isSensitive(a.Key) {
			out = append(out, slog.Attr{Key: a.Key, Value: slog.StringValue("[REDACTED]")})
		} else {
			out = append(out, a)
		}
	}
	return out
}

func (h *redactingHandler) withAttrs(attrs []slog.Attr) slog.Handler {
	c := h.inner
	for _, a := range attrs {
		c = c.WithAttrs([]slog.Attr{a})
	}
	return &redactingHandler{inner: c, sensitive: h.sensitive}
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
		if h.isSensitive(k) {
			out[k] = "[REDACTED]"
			continue
		}
		if s, ok := v.(string); ok {
			out[k] = event.ScrubString(s)
			continue
		}
		out[k] = v
	}
	return out
}
