package evalsvc

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/ensembyte/observatory/internal/event"
	"github.com/ensembyte/observatory/internal/store"
)

// The deterministic scoring path reads only the passed-in timeline, never
// ev.Store, so a nil engine is enough to exercise the verdict logic.
func passTimeline(t *testing.T) *store.Timeline {
	t.Helper()
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	return &store.Timeline{
		Run:   &store.RunAggregate{RunID: "run-pass", Status: "completed"},
		Spans: []*store.SpanRow{{ID: "t1", ToolName: "order.lookup", Status: "completed",
			StartedAt: base, EndedAt: base.Add(time.Second), DurationMS: 1000}},
		Events: []*store.EventRow{{ID: "e1", Seq: 1, Type: event.RunCompleted, TS: base,
			Attrs: map[string]any{event.AttrTaskCompleted: true}}},
	}
}

// Used the wrong tool and never reported completion.
func failTimeline(t *testing.T) *store.Timeline {
	t.Helper()
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	return &store.Timeline{
		Run:   &store.RunAggregate{RunID: "run-fail", Status: "completed"},
		Spans: []*store.SpanRow{{ID: "t1", ToolName: "some.other.tool", Status: "completed",
			StartedAt: base, EndedAt: base.Add(time.Second), DurationMS: 1000}},
		Events: []*store.EventRow{{ID: "e1", Seq: 1, Type: event.RunCompleted, TS: base}},
	}
}

// MVP DoD: an evaluation dataset can compare two agent versions — the verdict
// is a deterministic function of the case's expected outcome.
func TestDeterministicScoringPass(t *testing.T) {
	ev := NewEvaluator(nil)
	dc := DatasetCaseRaw{ID: "c1", Task: "look up order", ExpectedTool: "order.lookup", ExpectCompletion: true}
	res := ev.RunCase(context.Background(), "p", dc, passTimeline(t))
	if !res.Passed {
		t.Fatalf("expected pass, got %+v", res)
	}
	if res.Score < 1.0 {
		t.Fatalf("expected perfect score, got %v", res.Score)
	}
}

func TestDeterministicScoringFailOnToolSelection(t *testing.T) {
	ev := NewEvaluator(nil)
	dc := DatasetCaseRaw{ID: "c2", Task: "look up order", ExpectedTool: "order.lookup", ExpectCompletion: true}
	res := ev.RunCase(context.Background(), "p", dc, failTimeline(t))
	if res.Passed {
		t.Fatalf("expected a tool-selection failure, got pass")
	}
	if res.Score >= 1.0 {
		t.Fatalf("a failing case must score < 1.0, got %v", res.Score)
	}
	if len(res.FailedChecks) == 0 {
		t.Fatal("failed case has no evidence checks (explainability)")
	}
}

// Reproducibility: same input => same verdict (spec 14).
func TestReproducible(t *testing.T) {
	ev := NewEvaluator(nil)
	dc := DatasetCaseRaw{ID: "c3", ExpectedTool: "order.lookup", ExpectCompletion: true}
	a := ev.RunCase(context.Background(), "p", dc, passTimeline(t))
	b := ev.RunCase(context.Background(), "p", dc, passTimeline(t))
	if a.Score != b.Score || a.Passed != b.Passed {
		t.Fatalf("non-deterministic verdicts: %+v vs %+v", a, b)
	}
}

// The supplementary versioned judge is recorded but never the source of truth
// for pass/fail: a deterministic failure is not flipped by the judge.
type recordingJudge struct{}

func (recordingJudge) Judge(_ context.Context, version, caseID, task, runID string, _ []store.EventRow) (float64, bool, string, error) {
	if version != "judge-v1" {
		return 0, false, fmt.Sprintf("unexpected judge version %q", version), nil
	}
	return 0.5, true, "ok", nil // even a passing judge cannot override
}

func TestJudgeNeverOverridesDeterministicFail(t *testing.T) {
	ev := NewEvaluator(nil)
	ev.Judge = recordingJudge{}
	dc := DatasetCaseRaw{ID: "c4", ExpectedTool: "order.lookup", ExpectCompletion: true}
	res := ev.RunCase(context.Background(), "p", dc, failTimeline(t))
	// Deterministic checks already failed; the judge only adds notes and the
	// case must still be a failure.
	if res.Passed {
		t.Fatalf("judge must not override a deterministic failure: %+v", res)
	}
}
