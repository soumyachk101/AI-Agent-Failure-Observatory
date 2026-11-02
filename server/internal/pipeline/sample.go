package pipeline

import (
	"time"

	"github.com/ensembyte/observatory/internal/event"
)

// SampleLoopRun returns a synthetic run that exhibits the four detector
// conditions plus a clean context-loss and budget signal. It is ingested
// through the normal pipeline so dedup, assembly and detection all run on
// real data. Every event is marked sample (spec: demo data labelled).
func SampleLoopRun(project string) []*event.Envelope {
	run := "run-sample-loop"
	spanAgent := "span-agent-1"
	spanModel := "span-model-1"
	spanTool := "span-tool-1"
	spanRet := "span-retr-1"
	spanState := "span-state-1"
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	agent := &event.AgentInfo{Name: "support-agent", Version: "1.4.0"}
	provider := &event.ProviderInfo{Name: "acme", Model: "gpt-4o", Version: "2026-01"}

	seq := uint64(0)
	next := func() uint64 { seq++; return seq }
	at := func(min, sec int) time.Time {
		return base.Add(time.Duration(min)*time.Minute + time.Duration(sec)*time.Second)
	}
	sample := map[string]any{event.AttrSample: "true"}

	var out []*event.Envelope
	add := func(span string, parent, typ string, ts time.Time, attrs map[string]any) {
		if attrs == nil {
			attrs = map[string]any{}
		}
		for k, v := range sample {
			if _, ok := attrs[k]; !ok {
				attrs[k] = v
			}
		}
		e := &event.Envelope{
			SchemaVersion: event.SchemaVersion,
			EventID:       event.NewEventID(),
			ProjectID:     project,
			RunID:         run,
			SpanID:        span,
			ParentSpanID:  parent,
			Sequence:      next(),
			EventType:     typ,
			Timestamp:     ts,
			Agent:         agent,
			Provider:      provider,
			Attributes:    attrs,
		}
		out = append(out, e)
	}

	// run.started
	add("", "", event.RunStarted, at(0, 0), map[string]any{
		event.AttrEnvironment: "dev"})
	// agent.started
	add(spanAgent, "", event.AgentStarted, at(0, 0), nil)
	// retrieval with truncation (context-loss signal)
	add(spanRet, spanAgent, event.RetrievalStart, at(0, 1), map[string]any{
		event.AttrRetrievalHits: 0})
	add(spanRet, spanAgent, event.RetrievalDone, at(0, 3), map[string]any{
		event.AttrContextTruncated:    true,
		event.AttrContextTokensLost:   2048,
		event.AttrRetrievalHits:       0})
	// model call (provider-reported usage)
	add(spanModel, spanAgent, event.ModelStarted, at(0, 4), nil)
	add(spanModel, spanAgent, event.ModelCompleted, at(0, 9), map[string]any{
		event.AttrModelTokensIn:  1200,
		event.AttrModelTokensOut: 300,
		event.AttrModelLatencyMS: 5000,
		event.AttrUsageSource:    "provider",
		event.AttrCostUSD:        0.021,
	})
	// Repeated tool loop: same tool + identical args x5 (8.1)
	for i := 0; i < 5; i++ {
		m := at(1, 0)
		add(spanTool, spanAgent, event.ToolStarted, m, map[string]any{
			event.AttrToolName:        "status.poll",
			event.AttrToolVersion:     "v3",
			event.AttrSchemaVersion:   "schema-v3",
			event.AttrToolArgs:        map[string]any{"job_id": "job-42"},
		})
		add(spanTool, spanAgent, event.ToolCompleted, m.Add(400*time.Millisecond), map[string]any{
			event.AttrToolName:        "status.poll",
			event.AttrToolVersion:     "v3",
			event.AttrSchemaVersion:   "schema-v3",
			event.AttrToolArgs:        map[string]any{"job_id": "job-42"},
			event.AttrToolArgsHash:    "hashA",
		})
	}
	// State oscillation: A->B->A (8.2)
	add(spanState, spanAgent, event.StateSnapshot, at(2, 0), map[string]any{
		event.AttrStateFingerprint: "A", event.AttrStateProgress: false})
	add(spanState, spanAgent, event.StateSnapshot, at(2, 30), map[string]any{
		event.AttrStateFingerprint: "B", event.AttrStateProgress: false})
	add(spanState, spanAgent, event.StateSnapshot, at(2, 60), map[string]any{
		event.AttrStateFingerprint: "A", event.AttrStateProgress: false})
	// A budget exhaustion (8.3)
	add(spanAgent, spanAgent, event.BudgetWarning, at(3, 0), map[string]any{})
	add(spanAgent, spanAgent, event.BudgetExhaust, at(3, 1), map[string]any{})
	// A failed tool call (8.4 failure clustering)
	spanToolF := "span-tool-fail"
	mf := at(4, 0)
	add(spanToolF, spanAgent, event.ToolStarted, mf, map[string]any{
		event.AttrToolName:      "payment.charge",
		event.AttrToolVersion:   "v1",
		event.AttrSchemaVersion: "schema-v1"})
	add(spanToolF, spanAgent, event.ToolFailed, mf.Add(800*time.Millisecond), map[string]any{
		event.AttrToolName:      "payment.charge",
		event.AttrToolVersion:   "v1",
		event.AttrSchemaVersion: "schema-v1",
		event.AttrErrorClass:    "schema",
		event.AttrProviderCode:  "422",
		event.AttrStack:         "panic: invalid argument schema in tool.run\nat observatory/tools/payment.go:88"})
	// agent.completed + run.failed
	add(spanAgent, "", event.AgentCompleted, at(5, 0), nil)
	add("", "", event.RunFailed, at(5, 1), map[string]any{
		event.AttrErrorClass: "tool_failure"})

	return out
}
