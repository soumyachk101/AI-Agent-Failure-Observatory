package pipeline

import (
	"testing"
	"time"

	"github.com/ensembyte/observatory/internal/event"
)

func mkRunEnvelope(seq uint64, typ string, ts time.Time) *event.Envelope {
	return &event.Envelope{
		ProjectID: "p", RunID: "run-x", EventID: "e-" + typ,
		EventType: typ, Sequence: seq,
		Timestamp: ts, Attributes: map[string]any{},
	}
}

func TestRunRejectsInvalidEnvelope(t *testing.T) {
	s := openTestStore(t)
	n := &Normalizer{Store: s}
	pp := ProjectPolicy{ID: "p"}
	_ = s.CreateProject(ctx0(), "p", "P", "dev")

	bad := &event.Envelope{
		ProjectID: "p", RunID: "r", EventID: "bad",
		EventType: "not.a.type", Sequence: 1, Timestamp: time.Now().UTC(),
	}
	res, err := n.Run(ctx0(), pp, []*event.Envelope{bad})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rejected) != 1 {
		t.Fatalf("expected the invalid envelope rejected, res=%+v", res)
	}
}

// Idempotency: the same logical event (same idempotency key) is processed
// exactly once even under at-least-once delivery (spec NFR Ingestion).
func TestRunDeduplicatesBySequence(t *testing.T) {
	s := openTestStore(t)
	n := &Normalizer{Store: s}
	pp := ProjectPolicy{ID: "p"}
	_ = s.CreateProject(ctx0(), "p", "P", "dev")

	// First delivery persists.
	if res, err := n.Run(ctx0(), pp, []*event.Envelope{mkRunEnvelope(1, event.RunStarted, time.Now().UTC())}); err != nil || len(res.Accepted) != 1 {
		t.Fatalf("first delivery: res=%+v err=%v", res, err)
	}
	// A duplicate batch with the same logical event must be ignored.
	res, err := n.Run(ctx0(), pp, []*event.Envelope{mkRunEnvelope(1, event.RunStarted, time.Now().UTC())})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Accepted) != 0 {
		t.Fatalf("duplicate event must be ignored, accepted=%v", res.Accepted)
	}
}

// Out-of-order delivery: sequence is authoritative, not arrival order
// (spec FR-02).
func TestRunOrdersOutOfSequence(t *testing.T) {
	s := openTestStore(t)
	n := &Normalizer{Store: s}
	_ = s.CreateProject(ctx0(), "p", "P", "dev")

	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	e1 := mkRunEnvelope(1, event.RunStarted, base)
	e1.RunID = "run-y"
	e2 := mkRunEnvelope(2, event.RunCompleted, base.Add(time.Second))
	e2.RunID = "run-y"

	// Deliver 2 before 1 (out of order); both must be accepted.
	res, err := n.Run(ctx0(), ProjectPolicy{ID: "p"}, []*event.Envelope{e2, e1})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Accepted) != 2 {
		t.Fatalf("out-of-order events must both be accepted, got %+v", res)
	}
	tl, err := s.RunTimeline(ctx0(), "p", "run-y")
	if err != nil || len(tl.Events) != 2 {
		t.Fatalf("timeline: %v (%d events)", err, len(tl.Events))
	}
	// Ordering is by sequence, not delivery order.
	if tl.Events[0].Seq != 1 || tl.Events[1].Seq != 2 {
		t.Fatalf("events not ordered by sequence: %d, %d", tl.Events[0].Seq, tl.Events[1].Seq)
	}
}

// The assembler must build the span hierarchy (parent links) and fold
// labelled usage.
func TestAssemblerBuildsSpanHierarchy(t *testing.T) {
	s := openTestStore(t)
	n := &Normalizer{Store: s}
	_ = s.CreateProject(ctx0(), "p", "P", "dev")

	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	mk := func(span, parent, typ string, seq uint64, ts time.Time) *event.Envelope {
		return &event.Envelope{
			ProjectID: "p", RunID: "run-z", SpanID: span, ParentSpanID: parent,
			EventID: "e" + span, EventType: typ, Sequence: seq,
			Timestamp: ts, Agent: &event.AgentInfo{Name: "a", Version: "1"},
			Attributes: map[string]any{},
		}
	}
	modelDone := func() *event.Envelope {
		e := mk("model-1", "agent-1", event.ModelCompleted, 4, base.Add(5*time.Second))
		e.Attributes = withUsage("provider", 100, 50)
		return e
	}

	_, err := n.Run(ctx0(), ProjectPolicy{ID: "p"}, []*event.Envelope{
		mk("run", "", event.RunStarted, 1, base),
		mk("agent-1", "run", event.AgentStarted, 2, base),
		mk("model-1", "agent-1", event.ModelStarted, 3, base.Add(time.Second)),
		modelDone(),
		mk("agent-1", "run", event.AgentCompleted, 6, base.Add(10*time.Second)),
	})
	if err != nil {
		t.Fatal(err)
	}
	tl, err := s.RunTimeline(ctx0(), "p", "run-z")
	if err != nil {
		t.Fatal(err)
	}
	for _, sp := range tl.Spans {
		if sp.ID == "model-1" {
			if sp.Parent != "agent-1" {
				t.Fatalf("model span parent = %q, want agent-1", sp.Parent)
			}
			if sp.UsageSource != "provider" {
				t.Fatalf("model span usage source = %q, want provider", sp.UsageSource)
			}
			if sp.TokensIn != 100 || sp.TokensOut != 50 {
				t.Fatalf("model span usage not folded: in=%d out=%d", sp.TokensIn, sp.TokensOut)
			}
		}
	}
}

// Usage source must be labelled estimate when the SDK reports an estimate
// (MVP DoD: "labelled provider-reported or estimated").
func TestUsageSourceLabelEstimate(t *testing.T) {
	s := openTestStore(t)
	n := &Normalizer{Store: s}
	_ = s.CreateProject(ctx0(), "p", "P", "dev")

	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	env := &event.Envelope{
		ProjectID: "p", RunID: "run-e", SpanID: "m", EventID: "e",
		EventType: event.ModelCompleted, Sequence: 1, Timestamp: base,
		Attributes: withUsage("estimate", 12, 8),
	}
	if _, err := n.Run(ctx0(), ProjectPolicy{ID: "p"}, []*event.Envelope{env}); err != nil {
		t.Fatal(err)
	}
	tl, err := s.RunTimeline(ctx0(), "p", "run-e")
	if err != nil {
		t.Fatal(err)
	}
	for _, sp := range tl.Spans {
		if sp.ID == "m" && sp.UsageSource != "estimate" {
			t.Fatalf("usage source = %q, want estimate", sp.UsageSource)
		}
	}
}

func withUsage(src string, in, out int) map[string]any {
	return map[string]any{
		event.AttrModelTokensIn:  in,
		event.AttrModelTokensOut: out,
		event.AttrUsageSource:    src,
	}
}
