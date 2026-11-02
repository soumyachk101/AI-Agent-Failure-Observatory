package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/ensembyte/observatory/internal/event"
	"github.com/ensembyte/observatory/internal/store"
)

// row builds a minimal persisted event for the detectors (they read only the
// fields they use, no DB access required).
func row(seq uint64, ts time.Time, typ string, attrs map[string]any) *store.EventRow {
	if attrs == nil {
		attrs = map[string]any{}
	}
	return &store.EventRow{
		ID: "e-" + time.Unix(int64(seq), 0).Format("0102"),
		Run: "run-1", Project: "p",
		Span: "span-1", Seq: seq, Type: typ, TS: ts, Attrs: attrs,
	}
}

func toolRows(n int, tool string) []*store.EventRow {
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	var out []*store.EventRow
	for i := 0; i < n; i++ {
		ts := base.Add(time.Duration(i) * 20 * time.Second)
		out = append(out, row(uint64(i*2), ts, event.ToolStarted, map[string]any{
			event.AttrToolName: tool,
			event.AttrToolArgs: map[string]any{"job_id": "job-42"},
		}))
		out = append(out, row(uint64(i*2+1), ts.Add(100*time.Millisecond), event.ToolCompleted, map[string]any{
			event.AttrToolName: tool, event.AttrToolArgsHash: "hashA",
		}))
	}
	return out
}

// MVP DoD: the loop detector catches the known repeated-tool test case.
func TestRepeatedToolLoopDetects(t *testing.T) {
	re := NewRuleEngine(nil)
	rows := toolRows(5, "status.poll")
	found := re.DetectRun(context.Background(), ProjectPolicy{ID: "p"}, "run-1", rows)
	var loop bool
	for _, f := range found {
		if f.Kind == "tool-loop" && f.ToolName == "status.poll" {
			loop = true
			if len(f.Evidence) == 0 {
				t.Fatal("tool-loop finding has no evidence event ids")
			}
		}
	}
	if !loop {
		t.Fatalf("expected a tool-loop finding, got %+v", found)
	}
}

func TestRepeatedToolLoopRespectsPollingPolicy(t *testing.T) {
	re := NewRuleEngine(nil)
	re.Cfg.PollingTools["status.poll"] = true // spec 8.1 per-tool policy
	found := re.DetectRun(context.Background(), ProjectPolicy{ID: "p"}, "run-1", toolRows(5, "status.poll"))
	for _, f := range found {
		if f.Kind == "tool-loop" {
			t.Fatalf("polling tool must be exempt from the loop detector: %+v", f)
		}
	}
}

func TestRepeatedToolLoopNoLoopForDistinctArgs(t *testing.T) {
	re := NewRuleEngine(nil)
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	var rows []*store.EventRow
	for i := 0; i < 5; i++ {
		// Same tool, different argument hash each time => no loop.
		rows = append(rows, row(uint64(i), base.Add(time.Duration(i)*time.Second), event.ToolCompleted, map[string]any{
			event.AttrToolName: "search", event.AttrToolArgsHash: "hash-" + string(rune('A'+i)),
		}))
	}
	found := re.DetectRun(context.Background(), ProjectPolicy{ID: "p"}, "run-1", rows)
	for _, f := range found {
		if f.Kind == "tool-loop" {
			t.Fatalf("distinct-args calls must not flag a loop: %+v", f)
		}
	}
}

func TestStateOscillation(t *testing.T) {
	re := NewRuleEngine(nil)
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	// Same fingerprint repeated with no progress (spec 8.2).
	var rows []*store.EventRow
	for i := 0; i < 3; i++ {
		rows = append(rows, row(uint64(i), base.Add(time.Duration(i)*time.Minute), event.StateSnapshot, map[string]any{
			event.AttrStateFingerprint: "A", event.AttrStateProgress: false,
		}))
	}
	found := re.DetectRun(context.Background(), ProjectPolicy{ID: "p"}, "run-1", rows)
	var osc bool
	for _, f := range found {
		if f.Kind == "state-oscillation" {
			osc = true
		}
	}
	if !osc {
		t.Fatalf("expected a state-oscillation finding, got %+v", found)
	}

	// The same state with a progress event is not oscillation.
	rows = nil
	for i := 0; i < 3; i++ {
		prog := i == 1
		rows = append(rows, row(uint64(i), base.Add(time.Duration(i)*time.Minute), event.StateSnapshot, map[string]any{
			event.AttrStateFingerprint: "A", event.AttrStateProgress: prog,
		}))
	}
	found = re.DetectRun(context.Background(), ProjectPolicy{ID: "p"}, "run-1", rows)
	for _, f := range found {
		if f.Kind == "state-oscillation" {
			t.Fatalf("progress event must suppress the oscillation finding: %+v", f)
		}
	}
}

func TestBudgetExhaustion(t *testing.T) {
	re := NewRuleEngine(nil)
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	rows := []*store.EventRow{row(1, base, event.BudgetExhaust, nil)}
	found := re.DetectRun(context.Background(), ProjectPolicy{ID: "p"}, "run-1", rows)
	var foundExhaust bool
	for _, f := range found {
		if f.Kind == "budget-exhaustion" && f.Severity == "error" {
			foundExhaust = true
		}
	}
	if !foundExhaust {
		t.Fatalf("budget.exhausted must yield an error finding, got %+v", found)
	}
}

func TestBudgetWarningSeverity(t *testing.T) {
	re := NewRuleEngine(nil)
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	found := re.DetectRun(context.Background(), ProjectPolicy{ID: "p"}, "run-1",
		[]*store.EventRow{row(1, base, event.BudgetWarning, nil)})
	for _, f := range found {
		if f.Kind == "budget-exhaustion" {
			if f.Severity != "warning" {
				t.Fatalf("budget.warning should be severity warning, got %q", f.Severity)
			}
		}
	}
}

func TestFailureClustering(t *testing.T) {
	re := NewRuleEngine(nil)
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	attrs := map[string]any{
		event.AttrToolName: "payment.charge", event.AttrErrorClass: "schema",
		event.AttrProviderCode: "422",
	}
	rows := []*store.EventRow{
		row(1, base, event.ToolFailed, attrs),
		row(2, base.Add(time.Second), event.ToolFailed, attrs),
	}
	found := re.DetectRun(context.Background(), ProjectPolicy{ID: "p"}, "run-1", rows)
	var sigs []string
	for _, f := range found {
		if f.Kind == "failure-cluster" {
			sigs = append(sigs, f.Signature)
		}
	}
	if len(sigs) != 2 {
		t.Fatalf("expected 2 failure-cluster findings, got %d", len(sigs))
	}
	// Same content must cluster to the same signature (spec 8.4).
	if sigs[0] != sigs[1] {
		t.Fatalf("identical failures produced different signatures: %v", sigs)
	}
}

func TestContextLoss(t *testing.T) {
	re := NewRuleEngine(nil)
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	rows := []*store.EventRow{row(1, base, event.RetrievalDone, map[string]any{
		event.AttrContextTruncated:   true,
		event.AttrContextTokensLost:  2048,
	})}
	found := re.DetectRun(context.Background(), ProjectPolicy{ID: "p"}, "run-1", rows)
	var foundLoss bool
	for _, f := range found {
		if f.Kind == "context-loss" {
			foundLoss = true
		}
	}
	if !foundLoss {
		t.Fatalf("expected a context-loss finding, got %+v", found)
	}
}
