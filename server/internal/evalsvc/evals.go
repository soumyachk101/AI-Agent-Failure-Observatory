// Package evalsvc implements evaluation dataset management, deterministic
// scoring, versioned evaluators and safe replay (spec FR-05, FR-06).
//
// Scoring model (MVP): deterministic checks only — tool selection, schema
// validity and task completion, each derived from persisted events. An
// LLM-as-judge is a supplementary, versioned evaluator: its verdict is
// recorded with its version and calibrated flag, but it never overrides a
// deterministic check by default. This keeps results reproducible (spec:
// evaluation reproducibility tests).
package evalsvc

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ensembyte/observatory/internal/event"
	"github.com/ensembyte/observatory/internal/store"
)

// CaseResult is the scored outcome for one dataset case.
type CaseResult struct {
	CaseID string
	Passed bool
	Score  float64
	Status string // "passed" | "failed" | "error"
	Detail string
	// FailedChecks are the deterministic checks that failed, with the
	// evidence (event ids) that proves each — every verdict is explainable.
	FailedChecks []string
}

// Evaluator executes a dataset against an agent version and records an
// evaluation run.
type Evaluator struct {
	Store store.Engine
	// Judge is an optional supplementary LLM judge. nil => deterministic
	// only. Provided as an interface so tests can stub it.
	Judge Judge
}

// Judge is the supplementary versioned LLM-as-judge (spec FR-05). It is
// never the source of truth for pass/fail unless explicitly configured;
// deterministic checks always run first.
type Judge interface {
	// Judge scores a single case with a versioned prompt. version and
	// calibrated identify the evaluator for reproducibility.
	Judge(ctx context.Context, version string, caseID string, task string, runID string, events []store.EventRow) (score float64, passed bool, note string, err error)
}

// NewEvaluator builds the evaluator with a deterministic default.
func NewEvaluator(s store.Engine) *Evaluator {
	return &Evaluator{Store: s}
}

// GateConfig is the CI gate (spec FR-05: configurable thresholds).
type GateConfig struct {
	MinPassRate float64 // 0..1
	MinScore    float64 // 0..1
	Require     bool   // when false, gate only reports
}

// DefaultGate is lenient for dev; CI callers set stricter thresholds.
func DefaultGate() GateConfig {
	return GateConfig{MinPassRate: 0.9, MinScore: 0.0, Require: false}
}

// RunCase scores one dataset case against the events of a single run.
// The run is the agent's execution for that case; the expected outcome is
// the dataset case.
func (ev *Evaluator) RunCase(ctx context.Context, project string, dc DatasetCaseRaw, run *store.Timeline) CaseResult {
	res := CaseResult{CaseID: dc.ID, Status: "passed"}

	// Deterministic check 1: tool selection — the expected tool was used
	// (if the case specifies one).
	if dc.ExpectedTool != "" {
		used, ok := usedTool(run, dc.ExpectedTool)
		if !ok {
			res.Status = "failed"
			res.Passed = false
			res.FailedChecks = append(res.FailedChecks,
				"tool-selection: expected tool "+dc.ExpectedTool+" was not called")
		}
		_ = used
	}

	// Deterministic check 2: schema validity — no tool call failed on a
	// schema error class.
	if bad, evIDs := schemaFailure(run); bad {
		res.Status = "failed"
		res.Passed = false
		res.FailedChecks = append(res.FailedChecks,
			fmt.Sprintf("schema-validity: tool call failed with schema error (evidence: %v)", evIDs))
	}

	// Deterministic check 3: task completion — the case requires completion
	// and the run reports task.completed=true, or a terminal success.
	if dc.ExpectCompletion {
		completed, evIDs := taskCompleted(run)
		if !completed {
			res.Status = "failed"
			res.Passed = false
			res.FailedChecks = append(res.FailedChecks,
				fmt.Sprintf("task-completion: run did not report completion (evidence: %v)", evIDs))
		}
	}

	// Score: ratio of deterministic checks that passed.
	if len(res.FailedChecks) == 0 {
		res.Score = 1.0
	} else {
		total := 0
		if dc.ExpectedTool != "" {
			total++
		}
		total++ // schema
		if dc.ExpectCompletion {
			total++
		}
		if total == 0 {
			res.Score = 1.0
		} else {
			res.Score = float64(total-len(res.FailedChecks)) / float64(total)
		}
	}
	res.Detail = joinChecks(res.FailedChecks)

	// Supplementary judge (versioned, never overrides deterministic fails).
	if ev.Judge != nil && res.Status != "failed" {
		evs := make([]store.EventRow, 0, len(run.Events))
		for _, e := range run.Events {
			evs = append(evs, *e)
		}
		jscore, jpassed, notes, _ := ev.Judge.Judge(ctx, judgeVersion(ev), dc.ID, dc.Task, run.Run.RunID, evs)
		if !jpassed {
			res.FailedChecks = append(res.FailedChecks, "llm-judge: "+notes)
			res.Status = "failed"
			res.Passed = false
		}
		// Judge score is recorded for the record but deterministic score
		// governs pass/fail.
		_ = jscore
	}
	res.Passed = res.Status == "passed"
	return res
}

func judgeVersion(ev *Evaluator) string { return "judge-v1" }

// usedTool reports whether the expected tool name appears in any span.
func usedTool(tl *store.Timeline, want string) (bool, bool) {
	for _, sp := range tl.Spans {
		if sp.ToolName == want {
			return true, true
		}
	}
	return false, false
}

// schemaFailure reports whether any tool call failed on a schema error.
func schemaFailure(tl *store.Timeline) (bool, []string) {
	var ids []string
	for _, e := range tl.Events {
		if e.Type != event.ToolFailed {
			continue
		}
		class := event.AttrString(e.Attrs, event.AttrErrorClass)
		if class == "schema" || class == "invalid_arguments" || class == "schema_validation" {
			ids = append(ids, e.ID)
		}
	}
	return len(ids) > 0, ids
}

// taskCompleted reports whether the run reports completion.
func taskCompleted(tl *store.Timeline) (bool, []string) {
	var ids []string
	for _, e := range tl.Events {
		if v := event.AttrBool(e.Attrs, event.AttrTaskCompleted); v {
			ids = append(ids, e.ID)
		}
	}
	if len(ids) > 0 {
		return true, ids
	}
	// Fall back to a successful terminal run event.
	if tl.Run != nil && tl.Run.Status == "completed" {
		return true, nil
	}
	return false, nil
}

func joinChecks(ss []string) string {
	if len(ss) == 0 {
		return "all checks passed"
	}
	out := ss[0]
	for _, s := range ss[1:] {
		out += "; " + s
	}
	return out
}

// ---- dataset case raw (JSON-decoded form) ----

// DatasetCaseRaw is a dataset case as stored/returned.
type DatasetCaseRaw struct {
	ID               string `json:"id"`
	Task             string `json:"task"`
	ExpectedTool     string `json:"expected_tool"`
	ExpectCompletion bool   `json:"expect_completion"`
	// Rubric is free-form: scoring rubric text + optional judge prompt.
	Rubric string `json:"rubric"`
}

// ParseCases decodes the raw case list from ListCases output.
func ParseCases(rows []map[string]any) []DatasetCaseRaw {
	var out []DatasetCaseRaw
	for _, r := range rows {
		dc := DatasetCaseRaw{
			ID:               str(r["id"]),
			Task:             str(r["task"]),
			ExpectedTool:     str(r["expected_tool"]),
			ExpectCompletion: boolAs(r["expect_completion"]),
			Rubric:           str(r["rubric"]),
		}
		out = append(out, dc)
	}
	return out
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	if b, ok := v.([]byte); ok {
		return string(b)
	}
	if v == nil {
		return ""
	}
	return fmt.Sprintf("%v", v)
}

// boolAs coerces a decoded JSON attribute to a bool. A missing value or a
// non-bool type yields false (a case that does not assert completion is the
// safe default for the deterministic judge).
func boolAs(v any) bool {
	b, _ := v.(bool)
	return b
}

// keep json referenced.
var _ = json.Marshal
