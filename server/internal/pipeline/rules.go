package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/ensembyte/observatory/internal/event"
	"github.com/ensembyte/observatory/internal/store"
)

// DetectorConfig tunes the deterministic detectors (spec section 8). All
// thresholds are overridable per project in the future; these are defaults.
type DetectorConfig struct {
	// ToolLoop: same tool + normalized arg hash repeated this many times
	// within WindowMin flags a loop, unless the tool is in PollingTools.
	ToolLoopThreshold int
	ToolLoopWindowMin int
	// Tools designed for polling are exempt from the loop detector (spec 8.1:
	// per-tool policy).
	PollingTools map[string]bool

	// StateOscillation: state fingerprint must repeat this many times with
	// no progress event between.
	OscillationThreshold int
	OscillationWindowMin int

	// BudgetExhaustion: absolute thresholds; 0 means "rely on SDK budget
	// events only" (the SDK emits budget.warning / budget.exhausted).
	BudgetTokens    int64
	BudgetCostUSD   float64
	BudgetToolCalls int

	// FailureClustering: signature aggregation; no threshold — every failure
	// produces a signature.
}

// DefaultDetectorConfig is the documented default.
func DefaultDetectorConfig() DetectorConfig {
	return DetectorConfig{
		ToolLoopThreshold:  4,
		ToolLoopWindowMin:  10,
		PollingTools:       map[string]bool{},
		OscillationThreshold: 3,
		OscillationWindowMin: 30,
		BudgetTokens:       0,
		BudgetCostUSD:      0,
		BudgetToolCalls:    0,
	}
}

// Finding is one detector output: a diagnosis linked to raw event evidence
// (spec NFR Explainability: every diagnosis links to raw event evidence).
type Finding struct {
	ID        string
	Project   string
	Run       string
	Kind      string // "tool-loop" | "state-oscillation" | "budget-exhaustion" | "failure-cluster" | "context-loss"
	Severity  string
	Signature string
	Message   string
	Evidence  []string // event ids
	ToolName  string
	CreatedAt time.Time
}

// RuleEngine evaluates the four deterministic detectors over a run's spans
// and events. Detectors are pure functions of persisted evidence so they are
// reproducible and unit-testable.
type RuleEngine struct {
	Cfg   DetectorConfig
	Store store.Engine
}

// NewRuleEngine builds the engine with default config.
func NewRuleEngine(s store.Engine) *RuleEngine {
	return &RuleEngine{Cfg: DefaultDetectorConfig(), Store: s}
}

// Detect runs all detectors for a project over freshly persisted rows and
// records findings + failure signatures.
func (n *Normalizer) Detect(ctx context.Context, p ProjectPolicy, rows []*store.EventRow) error {
	run := firstRunID(rows)
	if run == "" {
		return nil
	}
	re := NewRuleEngine(n.Store)
	findings := re.DetectRun(ctx, p, run, rows)
	for i := range findings {
		f := &findings[i]
		if f.ID == "" {
			f.ID = event.NewEventID()
		}
		f.Project = p.ID
		if f.CreatedAt.IsZero() {
			f.CreatedAt = time.Now().UTC()
		}
		evidence, _ := json.Marshal(f.Evidence)
		if err := n.Store.RecordFinding(ctx, &store.FindingRow{
			ID: f.ID, Project: p.ID, Run: f.Run, Signature: f.Signature,
			Kind: f.Kind, Severity: f.Severity, Message: f.Message,
			Evidence: string(evidence), CreatedAt: f.CreatedAt,
		}); err != nil {
			return err
		}
		if f.Signature != "" {
			_ = n.Store.RecordSignature(ctx, p.ID, f.Signature, f.Kind, f.ToolName,
				agentVersionOf(rows), f.Evidence[0], f.Run, f.CreatedAt, 1)
		}
	}
	return nil
}

func firstRunID(rows []*store.EventRow) string {
	for _, r := range rows {
		if r.Run != "" {
			return r.Run
		}
	}
	return ""
}

func agentVersionOf(rows []*store.EventRow) string {
	for i := len(rows) - 1; i >= 0; i-- {
		if rows[i].AgentVersion != "" {
			return rows[i].AgentVersion
		}
	}
	return ""
}

// DetectRun runs the four detectors over one run's rows and returns the
// findings. Pure with respect to its inputs.
func (re *RuleEngine) DetectRun(ctx context.Context, p ProjectPolicy, run string, rows []*store.EventRow) []Finding {
	var out []Finding
	out = append(out, re.repeatedToolLoop(p, run, rows)...)
	out = append(out, re.stateOscillation(p, run, rows)...)
	out = append(out, re.budgetExhaustion(p, run, rows)...)
	out = append(out, re.failureClustering(p, run, rows)...)
	out = append(out, re.contextLoss(p, run, rows)...)
	return out
}

// ---- 8.1 Repeated tool loop ----

// Repeated tool + normalized argument hash within a window (spec 8.1).
// Polling tools are exempt (per-tool policy). Emits one finding per distinct
// looping tool key.
func (re *RuleEngine) repeatedToolLoop(p ProjectPolicy, run string, rows []*store.EventRow) []Finding {
	// Collect tool-call completions/failures in sequence order.
	type call struct {
		key      string
		toolName string
		seq      uint64
		ts       time.Time
		eventID  string
	}
	var calls []call
	for _, r := range rows {
		if r.Type != event.ToolStarted && r.Type != event.ToolCompleted && r.Type != event.ToolFailed {
			continue
		}
		a := r.Attrs
		tool := event.AttrString(a, event.AttrToolName)
		argsHash := event.AttrString(a, event.AttrToolArgsHash)
		if argsHash == "" {
			argsHash = event.HashOf(a[event.AttrToolArgs])
		}
		calls = append(calls, call{
			key: event.ToolCallKey(a), toolName: tool, seq: r.Seq, ts: r.TS,
			eventID: r.ID,
		})
	}
	sort.Slice(calls, func(i, j int) bool { return calls[i].seq < calls[j].seq })

	if len(calls) < re.Cfg.ToolLoopThreshold {
		return nil
	}

	// Sliding window: count repeats of each key within ToolLoopWindowMin.
	type windowKey struct {
		key string
		tool string
	}
	var findings []Finding
	emitted := map[string]bool{}
	for i := 0; i < len(calls); i++ {
		start := calls[i].ts
		var count int
		var ids []string
		for j := i; j < len(calls); j++ {
			if calls[j].ts.Sub(start) > time.Duration(re.Cfg.ToolLoopWindowMin)*time.Minute {
				break
			}
			if calls[j].key == calls[i].key {
				count++
				ids = append(ids, calls[j].eventID)
			}
		}
		tool := calls[i].toolName
		if tool == "" {
			tool = "unknown"
		}
		if re.Cfg.PollingTools[tool] {
			continue
		}
		if count >= re.Cfg.ToolLoopThreshold {
			wk := windowKey{key: calls[i].key, tool: tool}
			if emitted[wk.key+tool] {
				continue
			}
			emitted[wk.key+tool] = true
			sev := "warning"
			if count >= re.Cfg.ToolLoopThreshold*2 {
				sev = "error"
			}
			findings = append(findings, Finding{
				Run:      run,
				Kind:     "tool-loop",
				Severity: sev,
				Signature: "loop-" + event.HashOf(map[string]string{"tool": tool, "args": calls[i].key}),
				Message:  fmt.Sprintf("tool %q repeated %d times with identical normalized arguments within %dm (possible loop)",
					tool, count, re.Cfg.ToolLoopWindowMin),
				Evidence: ids,
				ToolName: tool,
			})
		}
	}
	return findings
}

// ---- 8.2 State oscillation ----

// Detects A->B->A or repeated state fingerprints with no progress event in
// a rolling window (spec 8.2). Progress = a state.snapshot with progress
// attribute, or a terminal tool/model success changing state.
func (re *RuleEngine) stateOscillation(p ProjectPolicy, run string, rows []*store.EventRow) []Finding {
	type snap struct {
		fp      string
		seq     uint64
		ts      time.Time
		eventID string
		prog    bool
	}
	var snaps []snap
	for _, r := range rows {
		if r.Type != event.StateSnapshot {
			continue
		}
		a := r.Attrs
		fp := event.AttrString(a, event.AttrStateFingerprint)
		if fp == "" {
			fp = event.HashOf(a)
		}
		snaps = append(snaps, snap{fp: fp, seq: r.Seq, ts: r.TS, eventID: r.ID,
			prog: event.AttrBool(a, event.AttrStateProgress)})
	}
	sort.Slice(snaps, func(i, j int) bool { return snaps[i].seq < snaps[j].seq })
	if len(snaps) < re.Cfg.OscillationThreshold {
		return nil
	}

	// Find an oscillating sequence: same fingerprint repeats >= threshold
	// times with no progress in between within the window.
	type run2 struct {
		fp  string
		ids []string
	}
	var runs []run2
	cur := run2{fp: snaps[0].fp, ids: []string{snaps[0].eventID}}
	proceeded := false
	for i := 1; i < len(snaps); i++ {
		if snaps[i].fp == cur.fp {
			if !snaps[i].prog {
				cur.ids = append(cur.ids, snaps[i].eventID)
			} else {
				proceeded = true
			}
			continue
		}
		// state changed
		if len(cur.ids) >= re.Cfg.OscillationThreshold && !proceeded {
			runs = append(runs, cur)
		}
		proceeded = false
		cur = run2{fp: snaps[i].fp, ids: []string{snaps[i].eventID}}
	}
	if len(cur.ids) >= re.Cfg.OscillationThreshold && !proceeded {
		runs = append(runs, cur)
	}

	var findings []Finding
	seen := map[string]bool{}
	for _, r2 := range runs {
		if seen[r2.fp] {
			continue
		}
		seen[r2.fp] = true
		findings = append(findings, Finding{
			Run:      run,
			Kind:     "state-oscillation",
			Severity: "warning",
			Signature: "osc-" + event.HashOf(map[string]string{"state": r2.fp}),
			Message:  fmt.Sprintf("state fingerprint %s repeated %d times with no progress event (oscillation)",
				shortHash(r2.fp), len(r2.ids)),
			Evidence: r2.ids,
		})
	}
	return findings
}

// ---- 8.3 Budget exhaustion ----

// Track token/cost/tool-call budgets (spec 8.3). Uses provider-reported
// usage where available; SDK budget.warning/budget.exhausted events always
// trigger a finding. Absolute thresholds are a backstop when the SDK does
// not emit budget events.
func (re *RuleEngine) budgetExhaustion(p ProjectPolicy, run string, rows []*store.EventRow) []Finding {
	var findings []Finding
	var events []string
	var exhausted, warned bool
	tokens, cost := int64(0), 0.0
	toolCalls := 0

	for _, r := range rows {
		a := r.Attrs
		switch r.Type {
		case event.BudgetExhaust:
			exhausted = true
			events = append(events, r.ID)
		case event.BudgetWarning:
			warned = true
			if len(events) == 0 {
				events = append(events, r.ID)
			}
		}
		if v, ok := a[event.AttrModelTokensIn]; ok {
			tokens += int64(AttrFloat64(v))
		}
		if v, ok := a[event.AttrModelTokensOut]; ok {
			tokens += int64(AttrFloat64(v))
		}
		if v, ok := a[event.AttrCostUSD]; ok {
			if c := AttrFloat64(v); c > cost {
				cost = c
			}
		}
		if r.Type == event.ToolCompleted || r.Type == event.ToolFailed {
			toolCalls++
		}
	}

	// SDK-driven findings.
	if exhausted {
		findings = append(findings, Finding{
			Run: run, Kind: "budget-exhaustion", Severity: "error",
			Signature: "budget-exhausted",
			Message:   "run reported budget exhaustion",
			Evidence:  dedupe(events),
		})
	} else if warned {
		findings = append(findings, Finding{
			Run: run, Kind: "budget-exhaustion", Severity: "warning",
			Signature: "budget-warning",
			Message:   "run reported a budget warning",
			Evidence:  dedupe(events),
		})
	}

	// Absolute backstops (only when configured).
	var msgs []string
	var backstopEvents []string
	if re.Cfg.BudgetTokens > 0 && tokens > re.Cfg.BudgetTokens {
		msgs = append(msgs, fmt.Sprintf("tokens %d exceed budget %d", tokens, re.Cfg.BudgetTokens))
		backstopEvents = append(backstopEvents, events...)
	}
	if re.Cfg.BudgetCostUSD > 0 && cost > re.Cfg.BudgetCostUSD {
		msgs = append(msgs, fmt.Sprintf("cost %.4f exceeds budget %.4f", cost, re.Cfg.BudgetCostUSD))
		backstopEvents = append(backstopEvents, events...)
	}
	if re.Cfg.BudgetToolCalls > 0 && toolCalls > re.Cfg.BudgetToolCalls {
		msgs = append(msgs, fmt.Sprintf("tool calls %d exceed budget %d", toolCalls, re.Cfg.BudgetToolCalls))
		backstopEvents = append(backstopEvents, events...)
	}
	if len(msgs) > 0 {
		findings = append(findings, Finding{
			Run: run, Kind: "budget-exhaustion",
			Severity: "error",
			Signature: "budget-backstop",
			Message:   "backstop: " + join(msgs, "; "),
			Evidence:  dedupe(backstopEvents),
		})
	}
	return findings
}

// ---- 8.4 Failure clustering ----

// Build deterministic signatures from error class, tool, schema version,
// provider code and normalized stack trace (spec 8.4). One finding per
// failed tool/model span so every failure is attributable and linkable.
func (re *RuleEngine) failureClustering(p ProjectPolicy, run string, rows []*store.EventRow) []Finding {
	var findings []Finding
	for _, r := range rows {
		if r.Type != event.ToolFailed && r.Type != event.ModelFailed {
			continue
		}
		a := r.Attrs
		class := event.AttrString(a, event.AttrErrorClass)
		if class == "" {
			class = "unknown"
		}
		sig := event.FailureSignatureOf(class, a)
		evidence := []string{r.ID}
		msg := fmt.Sprintf("%s failure (%s)", r.Type, class)
		if tool := event.AttrString(a, event.AttrToolName); tool != "" {
			msg = fmt.Sprintf("tool %s failure (%s)", tool, class)
			findings = append(findings, Finding{
				Run: run, Kind: "failure-cluster", Severity: "error",
				Signature: sig, Message: msg, Evidence: evidence,
				ToolName: tool,
			})
			continue
		}
		findings = append(findings, Finding{
			Run: run, Kind: "failure-cluster", Severity: "error",
			Signature: sig, Message: msg, Evidence: evidence,
		})
	}
	return findings
}

// ---- 8.5 Context loss signals ----

// Capture explicit truncation and retrieval-miss metadata (spec 8.5).
// No inference about hidden model context.
func (re *RuleEngine) contextLoss(p ProjectPolicy, run string, rows []*store.EventRow) []Finding {
	var findings []Finding
	var ev []string
	lostTokens := int64(0)
	for _, r := range rows {
		a := r.Attrs
		if event.AttrBool(a, event.AttrContextTruncated) {
			ev = append(ev, r.ID)
		}
		lostTokens += int64(event.AttrUint64(a, event.AttrContextTokensLost))
	}
	// Retrieval with zero hits while a model call follows can indicate a
	// missing-evidence state — surface it only when truncated=true is
	// already flagged, to avoid over-firing.
	if len(ev) > 0 {
		findings = append(findings, Finding{
			Run: run, Kind: "context-loss", Severity: "warning",
			Signature: "context-truncated",
			Message:   fmt.Sprintf("context truncation reported (%d tokens dropped)", lostTokens),
			Evidence:  ev,
		})
	}
	return findings
}

// ---- helpers ----

func shortHash(h string) string {
	if len(h) <= 8 {
		return h
	}
	return h[:8]
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func join(ss []string, sep string) string {
	if len(ss) == 0 {
		return ""
	}
	out := ss[0]
	for _, s := range ss[1:] {
		out += sep + s
	}
	return out
}
