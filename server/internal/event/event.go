// Package event defines the wire event envelope (spec section 7), validation,
// normalisation and deterministic hashing used by detectors.
package event

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

const SchemaVersion = "1"

// Event types (spec section 7).
const (
	RunStarted     = "run.started"
	RunCompleted   = "run.completed"
	RunFailed      = "run.failed"
	RunCancelled   = "run.cancelled"
	AgentStarted   = "agent.started"
	AgentHandoff   = "agent.handoff"
	AgentCompleted = "agent.completed"
	ModelStarted   = "model.call.started"
	ModelCompleted = "model.call.completed"
	ModelFailed    = "model.call.failed"
	ToolStarted    = "tool.call.started"
	ToolCompleted  = "tool.call.completed"
	ToolFailed     = "tool.call.failed"
	RetrievalStart = "retrieval.started"
	RetrievalDone  = "retrieval.completed"
	StateSnapshot  = "state.snapshot.created"
	BudgetWarning  = "budget.warning"
	BudgetExhaust  = "budget.exhausted"
	EvalStarted    = "evaluation.started"
	EvalCompleted  = "evaluation.completed"
)

var ValidEventTypes = map[string]bool{
	RunStarted: true, RunCompleted: true, RunFailed: true, RunCancelled: true,
	AgentStarted: true, AgentHandoff: true, AgentCompleted: true,
	ModelStarted: true, ModelCompleted: true, ModelFailed: true,
	ToolStarted: true, ToolCompleted: true, ToolFailed: true,
	RetrievalStart: true, RetrievalDone: true,
	StateSnapshot: true,
	BudgetWarning: true, BudgetExhaust: true,
	EvalStarted: true, EvalCompleted: true,
}

// Status terminal events, used by the assembler to close spans/runs.
var RunEndEvents = map[string]bool{RunCompleted: true, RunFailed: true, RunCancelled: true}
var SpanEndEvents = map[string]bool{
	AgentCompleted: true, ModelCompleted: true, ModelFailed: true,
	ToolCompleted: true, ToolFailed: true, RetrievalDone: true,
}

// Agent info (spec envelope field).
type AgentInfo struct {
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
}

type ProviderInfo struct {
	Name    string `json:"name,omitempty"`
	Model   string `json:"model,omitempty"`
	Version string `json:"version,omitempty"`
}

// Envelope is the core event envelope (spec section 7).
type Envelope struct {
	SchemaVersion string         `json:"schema_version"`
	EventID       string         `json:"event_id"`
	ProjectID     string         `json:"project_id"`
	RunID         string         `json:"run_id"`
	SpanID        string         `json:"span_id,omitempty"`
	ParentSpanID  string         `json:"parent_span_id,omitempty"`
	Sequence      uint64         `json:"sequence"`
	EventType     string         `json:"event_type"`
	Timestamp     time.Time      `json:"timestamp"`
	Agent         *AgentInfo     `json:"agent,omitempty"`
	Provider      *ProviderInfo  `json:"provider,omitempty"`
	Attributes    map[string]any `json:"attributes,omitempty"`
	PayloadRef    *string        `json:"payload_ref"`
}

// IdempotencyKey is stable across retries: same content => same key, so the
// pipeline can process duplicates exactly once (at-least-once + idempotent).
func (e Envelope) IdempotencyKey() string {
	return strings.Join([]string{
		e.ProjectID, e.RunID, e.SpanID, e.EventType,
		strconv.FormatUint(e.Sequence, 10), e.EventID,
	}, "|")
}

// Normalize fills defaults. projectID is the authoritative project scope
// (from the authenticated key); an envelope may omit it.
func (e *Envelope) Normalize(projectID string) {
	if e.SchemaVersion == "" {
		e.SchemaVersion = SchemaVersion
	}
	if e.ProjectID == "" {
		e.ProjectID = projectID
	}
	if e.Attributes == nil {
		e.Attributes = map[string]any{}
	}
}

// Validate checks structural invariants. Ordering (sequence) is enforced at
// assembly time, not here: out-of-order delivery is expected.
func (e *Envelope) Validate() error {
	if e.EventID == "" {
		return fmt.Errorf("event: missing event_id")
	}
	if e.RunID == "" {
		return fmt.Errorf("event: missing run_id")
	}
	if !ValidEventTypes[e.EventType] {
		return fmt.Errorf("event: unknown event_type %q", e.EventType)
	}
	if e.Sequence == 0 {
		return fmt.Errorf("event: sequence must be > 0")
	}
	if e.Timestamp.IsZero() {
		return fmt.Errorf("event: missing timestamp")
	}
	if e.SchemaVersion != "" && e.SchemaVersion != SchemaVersion {
		return fmt.Errorf("event: unsupported schema_version %q", e.SchemaVersion)
	}
	return nil
}

// NewEventID returns an RFC4122-style v4 ID with no external dependency.
func NewEventID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "evt-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return hex.EncodeToString(b)
}

// CanonicalJSON marshals v deterministically: Go's encoding/json sorts map
// keys, and we additionally sort slices of maps of unknown shape.
func CanonicalJSON(v any) (string, error) {
	if m, ok := v.(map[string]any); ok && len(m) > 0 {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := "{"
		for i, k := range keys {
			if i > 0 {
				out += ","
			}
			b, err := json.Marshal(k)
			if err != nil {
				return "", err
			}
			val, err := CanonicalJSON(m[k])
			if err != nil {
				return "", err
			}
			out += string(b) + ":" + val
		}
		return out + "}", nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// HashOf returns a short stable hash of a value's canonical form.
func HashOf(v any) string {
	s, err := CanonicalJSON(v)
	if err != nil {
		s = fmt.Sprintf("%v", v)
	}
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:12])
}

// ToolCallKey identifies a normalized tool call: tool name + stable argument
// hash + schema version (spec 8.1). Argument hashing uses canonical JSON so
// key ordering and numeric formatting cannot mask repeats.
func ToolCallKey(attrs map[string]any) string {
	tool := AttrString(attrs, AttrToolName)
	args := attrs[AttrToolArgs]
	if AttrString(attrs, AttrToolArgsHash) != "" {
		args = AttrString(attrs, AttrToolArgsHash)
	}
	schema := AttrString(attrs, AttrSchemaVersion)
	return fmt.Sprintf("%s|%s|%s", tool, HashOf(args), schema)
}

// FailureSignatureOf builds the deterministic cross-run failure signature
// (spec 8.4) from error class, tool, schema version, provider code and a
// normalized stack trace. Raw evidence stays on the events themselves.
func FailureSignatureOf(class string, attrs map[string]any) string {
	factors := []string{
		class,
		AttrString(attrs, AttrErrorClass),
		AttrString(attrs, AttrToolName),
		AttrString(attrs, AttrSchemaVersion),
		AttrString(attrs, AttrProviderCode),
		NormalizeStack(AttrString(attrs, AttrStack)),
	}
	return "fs-" + HashOf(map[string]string{"f": strings.Join(factors, "\x00")})
}

// NormalizeStack removes volatile parts of stack traces (hex addresses,
// line offsets beyond the first frame are kept verbatim but numbers are
// tokenized) so minor build differences do not split a cluster.
func NormalizeStack(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > 8 {
		lines = lines[:8]
	}
	for i, l := range lines {
		l = strings.ToLower(l)
		l = strings.ReplaceAll(l, "0x[0-9a-f]+", "0x…")
		l = strings.ReplaceAll(l, "[0-9]+", "N")
		lines[i] = l
	}
	return strings.Join(lines, "\n")
}

// Common namespaced attribute keys (spec 7: namespaced attributes, no
// provider-specific base fields).
const (
	AttrAgentFrom           = "agent.from"
	AttrAgentTo             = "agent.to"
	AttrModelTokensIn      = "model.tokens_in"
	AttrModelTokensOut     = "model.tokens_out"
	AttrModelLatencyMS     = "model.latency_ms"
	AttrModelRetries       = "model.retries"
	AttrUsageSource        = "usage.source" // "provider" | "estimate"
	AttrCostUSD            = "cost.usd"
	AttrCostEstimated      = "cost.estimated"
	AttrToolName           = "tool.name"
	AttrToolVersion        = "tool.version"
	AttrToolArgs           = "tool.args"
	AttrToolArgsHash       = "tool.args_hash"
	AttrToolSchemaVersion  = "tool.schema_version"
	AttrSchemaVersion      = "schema.version"
	AttrToolResultRef      = "tool.result_ref"
	AttrErrorClass         = "error.class"
	AttrProviderCode       = "provider.response_code"
	AttrStack              = "error.stack"
	AttrRetryCount         = "retry.count"
	AttrStateFingerprint   = "state.fingerprint"
	AttrStateProgress      = "state.progress"
	AttrContextTruncated   = "context.truncated"
	AttrContextTokensLost  = "context.tokens_dropped"
	AttrRetrievalHits      = "retrieval.hit_count"
	AttrEvalCaseID         = "eval.case_id"
	AttrTaskCompleted      = "task.completed"
	AttrReplayOrigin       = "replay.origin_run"
	AttrReplayMock         = "replay.mock"
	AttrSafetySideEffect   = "safety.side_effect"
	AttrSample             = "sample"
	AttrEnvironment        = "environment"
)

func AttrString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
		return fmt.Sprintf("%v", v)
	}
	return ""
}

func AttrUint64(m map[string]any, key string) uint64 {
	if m == nil {
		return 0
	}
	switch v := m[key].(type) {
	case float64:
		return uint64(v)
	case int:
		return uint64(v)
	case uint64:
		return v
	}
	return 0
}

func AttrBool(m map[string]any, key string) bool {
	if m == nil {
		return false
	}
	if v, ok := m[key].(bool); ok {
		return v
	}
	return false
}
