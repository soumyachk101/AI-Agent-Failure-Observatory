package pipeline

import (
	"context"

	"github.com/ensembyte/observatory/internal/event"
	"github.com/ensembyte/observatory/internal/store"
)

// Assemble materializes span hierarchy and timings from event rows.
//
// Ordering model (spec FR-02): we order by the SDK-assigned sequence number,
// NOT wall clock. Timestamps may be out of order across processes; sequence
// is the authoritative order. Spans are opened by "started" events and closed
// by their terminal event, or left open if telemetry is partial.
//
// The assembler also computes per-span duration, token usage and cost, and
// labels usage source (provider vs estimate).
func (n *Normalizer) Assemble(ctx context.Context, p ProjectPolicy, rows []*store.EventRow) error {
	bySpan := map[string]*store.SpanRow{}
	for _, r := range rows {
		if r.Span == "" {
			continue
		}
		sp, ok := bySpan[r.Span]
		if !ok {
			sp = &store.SpanRow{
				ID: r.Span, Project: r.Project, RunID: r.Run,
				AgentName: r.AgentName, AgentVersion: r.AgentVersion,
				Model: r.Model, Provider: r.Provider,
			}
			bySpan[r.Span] = sp
		}
		applyToSpan(sp, r)
	}
	for _, sp := range bySpan {
		if sp.Status == "" {
			sp.Status = "open"
		}
		if err := n.Store.UpsertSpan(ctx, sp); err != nil {
			return err
		}
	}
	return nil
}

// applyToSpan folds a single event into its span aggregate.
func applyToSpan(sp *store.SpanRow, r *store.EventRow) {
	a := r.Attrs
	// Parent + kind from the event type.
	if sp.Parent == "" && r.Parent != "" {
		sp.Parent = r.Parent
	}
	sp.Kind = kindFor(r.Type)

	// Name from event type / agent / tool.
	if sp.Name == "" {
		sp.Name = nameFor(r)
	}

	// Tool metadata.
	if sp.ToolName == "" {
		if t := event.AttrString(a, event.AttrToolName); t != "" {
			sp.ToolName = t
		}
	}
	if sp.ToolVersion == "" {
		if t := event.AttrString(a, event.AttrToolVersion); t != "" {
			sp.ToolVersion = t
		}
	}
	if sp.ToolSchemaVer == "" {
		if t := event.AttrString(a, event.AttrSchemaVersion); t != "" {
			sp.ToolSchemaVer = t
		}
	}
	if h := event.AttrString(a, event.AttrToolArgsHash); h != "" {
		sp.ToolArgsHash = h
	} else if sp.ToolArgsHash == "" {
		// Derive a stable hash of args so loop detection works even when
		// the SDK didn't pre-compute it.
		sp.ToolArgsHash = event.HashOf(a[event.AttrToolArgs])
	}

	// Timing: sequence is authoritative; track the envelope sequence range.
	if sp.SeqStart == 0 || r.Seq < sp.SeqStart {
		sp.SeqStart = r.Seq
	}
	if r.Seq > sp.SeqEnd {
		sp.SeqEnd = r.Seq
	}
	if sp.StartedAt.IsZero() || r.TS.Before(sp.StartedAt) {
		sp.StartedAt = r.TS
	}
	switch {
	case event.SpanEndEvents[r.Type] || event.RunEndEvents[r.Type]:
		if r.TS.After(sp.EndedAt) {
			sp.EndedAt = r.TS
		}
		if r.Type == event.ToolFailed || r.Type == event.ModelFailed {
			sp.Status = "failed"
		} else {
			sp.Status = "completed"
		}
	}

	// Status for span-level failures that aren't terminal events.
	if r.Type == event.ModelFailed || r.Type == event.ToolFailed {
		sp.Status = "failed"
		if sp.ErrorClass == "" {
			sp.ErrorClass = event.AttrString(a, event.AttrErrorClass)
		}
	}

	// Usage: provider-reported or estimated, labelled.
	if v, ok := a[event.AttrModelTokensIn]; ok {
		sp.TokensIn += int64(AttrFloat64(v))
	}
	if v, ok := a[event.AttrModelTokensOut]; ok {
		sp.TokensOut += int64(AttrFloat64(v))
	}
	if v, ok := a[event.AttrModelRetries]; ok {
		if n := int64(AttrFloat64(v)); n > sp.Retries {
			sp.Retries = n
		}
	}
	if v, ok := a[event.AttrCostUSD]; ok {
		if c := AttrFloat64(v); c > sp.CostUSD {
			sp.CostUSD = c
		}
	}
	if v, ok := a[event.AttrCostEstimated]; ok && asBoolVal(v) {
		sp.CostEstimated = true
	}
	if src, ok := a[event.AttrUsageSource]; ok {
		if s, ok := src.(string); ok && s != "" {
			sp.UsageSource = s
		}
	}
	// Default usage source when provider gave usage but no label.
	if sp.UsageSource == "" && (sp.TokensIn > 0 || sp.TokensOut > 0) {
		sp.UsageSource = "provider"
	}
	if sp.UsageSource == "" && sp.CostUSD > 0 && sp.CostEstimated {
		sp.UsageSource = "estimate"
	}

	// Derived duration from the envelope time range.
	if !sp.StartedAt.IsZero() && !sp.EndedAt.IsZero() && sp.EndedAt.After(sp.StartedAt) {
		if d := sp.EndedAt.Sub(sp.StartedAt).Milliseconds(); d > sp.DurationMS {
			sp.DurationMS = d
		}
	}
}

// kindFor maps an event type to a span kind.
func kindFor(t string) string {
	switch t {
	case event.AgentStarted, event.AgentHandoff, event.AgentCompleted:
		return "agent"
	case event.ModelStarted, event.ModelCompleted, event.ModelFailed:
		return "model"
	case event.ToolStarted, event.ToolCompleted, event.ToolFailed:
		return "tool"
	case event.RetrievalStart, event.RetrievalDone:
		return "retrieval"
	default:
		return "run"
	}
}

// nameFor derives a human span name.
func nameFor(r *store.EventRow) string {
	a := r.Attrs
	switch {
	case r.Type == event.AgentStarted:
		return "agent:" + r.AgentName
	case r.Type == event.AgentHandoff:
		from := event.AttrString(a, event.AttrAgentFrom)
		to := event.AttrString(a, event.AttrAgentTo)
		if from != "" && to != "" {
			return "handoff " + from + " -> " + to
		}
		return "handoff"
	case r.Type == event.ToolStarted, r.Type == event.ToolCompleted, r.Type == event.ToolFailed:
		if t := event.AttrString(a, event.AttrToolName); t != "" {
			return "tool:" + t
		}
		return "tool"
	case r.Type == event.ModelStarted, r.Type == event.ModelCompleted, r.Type == event.ModelFailed:
		if m := r.Model; m != "" {
			return "model:" + m
		}
		return "model"
	case r.Type == event.RetrievalStart, r.Type == event.RetrievalDone:
		return "retrieval"
	default:
		return "run"
	}
}

// AttrFloat64 is a safe float64 coercion helper.
func AttrFloat64(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case float32:
		return float64(n)
	case int:
		return float64(n)
	case int64:
		return float64(n)
	}
	return 0
}

func asBoolVal(v any) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	return false
}
