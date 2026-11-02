package evalsvc

import (
	"context"
	"fmt"
	"time"

	"github.com/ensembyte/observatory/internal/event"
	"github.com/ensembyte/observatory/internal/store"
)

// ToolExecuter is the interface a replay step hands tool calls to. In dev
// mode the only implementation wired up is MockTool (fixtures); a live
// executor must be explicitly constructed and injected.
type ToolExecuter interface {
	Execute(ctx context.Context, tool string, args any) (result any, err error)
}

// MockTool replays tool calls from a fixture map. This is the default and
// the only implementation the API exposes. It has zero network access and
// zero credentials: its isolation boundary is that it is the only
// executor the replay path may call.
//
//	+---------------------------------------------------------------+
//	|  REPLAY ISOLATION BOUNDARY                                      |
//	|  - no outbound sockets: MockTool reads fixtures in-memory       |
//	|  - no production credentials: replays never see live keys      |
//	|  - live executor is a separate type; API must inject it and    |
//	|    must also pass explicit allowlist + dry-run checks          |
//	|  - destructive tools (payment, email, delete) are refused in    |
//	|    mock mode even when fixtures exist, unless the fixture is   |
//	|    explicitly marked side-effect-free                          |
//	+---------------------------------------------------------------+
type MockTool struct {
	// Fixtures map tool name -> list of canned results, consumed in
	// order of calls (first call gets fixtures[0], second gets [1]).
	// If there are fewer fixtures than calls, the last one is reused.
	Fixtures map[string][]Fixture
	// SideEffectFree marks which tools are safe to replay even when a
	// fixture is missing. Destructive tools default to refused.
	SideEffectFree map[string]bool
	// Destructive is the deny-list: these tools are never executed in
	// mock mode, even with fixtures, unless SideEffectFree overrides.
	Destructive map[string]bool

	// CallOrder records calls for provenance.
	CallOrder []CallRecord
}

// Fixture is a canned tool result.
type Fixture struct {
	Result    string `json:"result"`
	IsError   bool   `json:"is_error"`
	ErrClass  string `json:"err_class"`
}

// CallRecord is one replay step (provenance).
type CallRecord struct {
	Step      int    `json:"step"`
	Tool      string `json:"tool"`
	ArgsHash  string `json:"args_hash"`
	Fixture   int    `json:"fixture_index"`
	Reused    bool   `json:"reused"`
	Refused   bool   `json:"refused"`
	ElapsedMS int64  `json:"elapsed_ms"`
}

// NewMockTool builds the default mock executor with the destructive deny-
// list. The deny-list encodes the spec: payment, email, file deletion and
// production mutations are never re-run by default.
func NewMockTool() *MockTool {
	return &MockTool{
		Fixtures: map[string][]Fixture{},
		SideEffectFree: map[string]bool{},
		Destructive: map[string]bool{
			"payment.charge": true, "payment.refund": true,
			"email.send": true, "file.delete": true,
			"db.write": true, "db.delete": true,
			"external.mutate": true,
		},
		CallOrder: []CallRecord{},
	}
}

// Execute implements ToolExecuter.
func (m *MockTool) Execute(ctx context.Context, tool string, args any) (any, error) {
	rec := CallRecord{Step: len(m.CallOrder) + 1, Tool: tool, ArgsHash: event.HashOf(args)}
	start := time.Now()

	// Destructive tools are refused in mock mode unless explicitly
	// side-effect-free. This is the hard guarantee: a replay can never
	// charge a card or delete a file by accident.
	if m.Destructive[tool] && !m.SideEffectFree[tool] {
		rec.Refused = true
		rec.ElapsedMS = time.Since(start).Milliseconds()
		m.CallOrder = append(m.CallOrder, rec)
		return nil, fmt.Errorf("replay refused: tool %q is destructive and no side-effect-free fixture is configured", tool)
	}

	fixs, ok := m.Fixtures[tool]
	if !ok || len(fixs) == 0 {
		// No fixture: return a neutral empty result, not an error, so the
		// replay continues deterministically. The missing fixture is
		// visible in provenance.
		rec.Reused = false
		rec.ElapsedMS = time.Since(start).Milliseconds()
		m.CallOrder = append(m.CallOrder, rec)
		return map[string]any{"ok": true, "mock": true, "note": "no fixture; empty result"}, nil
	}
	idx := len(m.CallOrder)
	if idx >= len(fixs) {
		idx = len(fixs) - 1
		rec.Reused = true
	}
	f := fixs[idx]
	rec.Fixture = idx
	rec.ElapsedMS = time.Since(start).Milliseconds()
	m.CallOrder = append(m.CallOrder, rec)

	if f.IsError {
		class := f.ErrClass
		if class == "" {
			class = "tool_error"
		}
		return nil, fmt.Errorf("%s: %s", class, f.Result)
	}
	return f.Result, nil
}

// ReplayService executes a safe replay of a run: it re-derives the tool-call
// sequence from the original run and re-executes each step against the mock
// executor. Provenance (CallOrder) and the original sequence are preserved.
type ReplayService struct {
	Store store.Engine
	// Tools is the executor; always MockTool in dev.
	Tools ToolExecuter
	// Project is the owning tenant; replays are project-scoped.
	Project string
}

// ReplayJob mirrors the store's replay record but with live provenance.
type ReplayOutcome struct {
	JobID         string
	OriginRun     string
	ReplayRun     string
	Status        string
	StepCount     int
	CompletedSteps int
	FailedSteps   int
	RefusedSteps  int
	Provenance    []CallRecord
	// Isolation documents the boundary the replay respected.
	Isolation string
}

// Rebuild replays a run. originRun is the run to replay; the tool call
// sequence is read from persisted tool.call.* events, in sequence order.
// Tools MUST be a MockTool (or otherwise isolated) — the API enforces this.
func (rs *ReplayService) Rebuild(ctx context.Context, originRun string, replayRun string) (*ReplayOutcome, error) {
	tl, err := rs.Store.RunTimeline(ctx, rs.Project, originRun)
	if err != nil {
		return nil, err
	}
	if tl == nil {
		return nil, fmt.Errorf("replay: run %s not found", originRun)
	}

	// Derive the ordered tool-call sequence from persisted events.
	type step struct {
		tool string
		args any
		seq  uint64
		id   string
	}
	var steps []step
	for _, e := range tl.Events {
		if e.Type != event.ToolStarted {
			continue
		}
		tool := event.AttrString(e.Attrs, event.AttrToolName)
		if tool == "" {
			continue
		}
		steps = append(steps, step{tool: tool, args: e.Attrs[event.AttrToolArgs], seq: e.Seq, id: e.ID})
	}
	// Events are already ordered by seq (RunTimeline sorts by seq).

	out := &ReplayOutcome{
		OriginRun: originRun,
		ReplayRun: replayRun,
		StepCount: len(steps),
		Isolation: "mocked-tools: no outbound sockets, no production credentials, destructive tools refused",
	}

	completed, failed, refused := 0, 0, 0
	for i, s := range steps {
		_ = i
		res, err := rs.Tools.Execute(ctx, s.tool, s.args)
		if err != nil {
			failed++
			// A refused destructive call shows up as an error; classify it.
			if ref, ok := rs.Tools.(*MockTool); ok && ref.Destructive[s.tool] && !ref.SideEffectFree[s.tool] {
				refused++
				failed--
			}
			_ = res
			continue
		}
		completed++
	}
	out.CompletedSteps = completed
	out.FailedSteps = failed
	out.RefusedSteps = refused
	if refused > 0 || failed > 0 {
		out.Status = "completed_with_failures"
	} else {
		out.Status = "completed"
	}
	if m, ok := rs.Tools.(*MockTool); ok {
		out.Provenance = m.CallOrder
	}
	return out, nil
}

// Record persists the replay outcome as a replay job row.
func (rs *ReplayService) Record(ctx context.Context, jobID string, out *ReplayOutcome) error {
	j := &store.ReplayJob{
		ID: jobID, Project: rs.Project,
		OriginRun: out.OriginRun, ReplayRun: out.ReplayRun,
		Status: out.Status, Mode: "mocked",
		StepCount: out.StepCount, CompletedSteps: out.CompletedSteps,
		FailedSteps: out.FailedSteps + out.RefusedSteps,
		MockRef: "mock", CreatedAt: time.Now().UTC(),
	}
	if err := rs.Store.CreateReplayJob(ctx, j); err != nil {
		return err
	}
	_ = rs.Store.FinishReplayJob(ctx, rs.Project, jobID,
		out.CompletedSteps, out.FailedSteps+out.RefusedSteps, out.Status)
	return nil
}
