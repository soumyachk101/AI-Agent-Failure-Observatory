package evalsvc

import (
	"context"
	"testing"
)

// Replay safety: mocked tools by default, destructive tools refused even when
// fixtures exist (spec FR-06 / 12: "never silently re-run payment, email,
// file deletion or production mutations").

func TestMockToolReturnsFixture(t *testing.T) {
	mt := NewMockTool()
	mt.Fixtures["order.lookup"] = []Fixture{{Result: `{"id":42}`}}
	got, err := mt.Execute(context.Background(), "order.lookup", map[string]any{"id": 42})
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("expected a fixture result")
	}
	// Provenance is recorded for replay (spec FR-06: record replay provenance).
	if len(mt.CallOrder) != 1 || mt.CallOrder[0].Tool != "order.lookup" {
		t.Fatalf("provenance not recorded: %+v", mt.CallOrder)
	}
}

// Destructive tools are refused in mock mode even with a fixture configured:
// the deny-list is a hard guarantee.
func TestMockToolRefusesDestructiveWithFixture(t *testing.T) {
	mt := NewMockTool()
	mt.Fixtures["payment.charge"] = []Fixture{{Result: "charged"}}
	// A fixture is present, but the tool is destructive and NOT side-effect-free.
	_, err := mt.Execute(context.Background(), "payment.charge", map[string]any{"amount": 1})
	if err == nil {
		t.Fatal("destructive tool executed even though it must be refused")
	}
	if len(mt.CallOrder) == 0 || !mt.CallOrder[0].Refused {
		t.Fatalf("refusal not recorded in provenance: %+v", mt.CallOrder)
	}
}

// Side-effect-free override lets a specific destructive tool be replayed when
// explicitly configured (spec: "require explicit configuration").
func TestMockToolSideEffectFreeOverride(t *testing.T) {
	mt := NewMockTool()
	mt.Fixtures["email.send"] = []Fixture{{Result: "queued"}}
	mt.SideEffectFree["email.send"] = true
	if _, err := mt.Execute(context.Background(), "email.send", map[string]any{"to": "a@b.c"}); err != nil {
		t.Fatalf("side-effect-free override should allow replay: %v", err)
	}
}

// A missing fixture returns a neutral empty result, not an error, so the
// replay stays deterministic and the gap is visible in provenance.
func TestMockToolMissingFixtureIsNeutral(t *testing.T) {
	mt := NewMockTool()
	got, err := mt.Execute(context.Background(), "no-such-tool", map[string]any{})
	if err != nil {
		t.Fatalf("missing fixture must not error: %v", err)
	}
	m, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("expected a map result, got %T", got)
	}
	if m["mock"] != true {
		t.Fatalf("result not marked as a mock: %v", m)
	}
}

// A fixture that is an error returns an error carrying the class.
func TestMockToolFixtureError(t *testing.T) {
	mt := NewMockTool()
	mt.Fixtures["flaky.call"] = []Fixture{{Result: "boom", IsError: true, ErrClass: "tool_error"}}
	_, err := mt.Execute(context.Background(), "flaky.call", nil)
	if err == nil {
		t.Fatal("expected an error result")
	}
	if err.Error() == "" {
		t.Fatalf("error not surfaced: %v", err)
	}
}
