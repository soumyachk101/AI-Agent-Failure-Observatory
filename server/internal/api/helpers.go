package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/ensembyte/observatory/internal/store"
)

// nowUTC is a seam for tests.
func nowUTC() time.Time { return time.Now().UTC() }

// writeJSON encodes v with a JSON content type.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeErr writes a JSON error body.
func writeErr(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

// fmtAtoi parses an int; returns 0 on error (callers check err).
func fmtAtoi(s string) (int64, error) { return strconv.ParseInt(s, 10, 64) }

// randomID is a short random hex id.
func randomID() string {
	b := make([]byte, 6)
	rand.Read(b) //nolint:errcheck // non-fatal
	return hex.EncodeToString(b)
}

// withCORS adds permissive CORS for the dev UI (same-origin in prod).
func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, X-Obs-Key, Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ---- serializers ----

// runAsMap shapes a run aggregate for the API. Usage source is always
// present so the UI can label provider-reported vs estimated (MVP DoD).
func runAsMap(r *store.RunAggregate) map[string]any {
	if r == nil {
		return map[string]any{"id": ""}
	}
	return map[string]any{
		"id":           r.RunID,
		"status":       r.Status,
		"agent_name":   r.AgentName,
		"agent_version": r.AgentVersion,
		"model":        r.Model,
		"provider":     r.Provider,
		"environment":  r.Environment,
		"started_at":   r.StartedAt,
		"ended_at":     r.EndedAt,
		"duration_ms":  r.DurationMS,
		"tokens_in":    r.TokensIn,
		"tokens_out":   r.TokensOut,
		"usage_source": r.UsageSource,
		"cost_usd":     r.CostUSD,
		"cost_estimated": r.CostEstimated,
		"retries":      r.Retries,
		"event_count":  r.EventCount,
		"seq_max":      r.SeqMax,
	}
}

func spansAsMaps(spans []*store.SpanRow) []map[string]any {
	out := make([]map[string]any, 0, len(spans))
	for _, sp := range spans {
		out = append(out, spanAsMap(sp))
	}
	return out
}

func spanAsMap(sp *store.SpanRow) map[string]any {
	if sp == nil {
		return nil
	}
	return map[string]any{
		"id": sp.ID, "run_id": sp.RunID, "parent_span_id": sp.Parent,
		"name": sp.Name, "kind": sp.Kind, "status": sp.Status,
		"agent_name": sp.AgentName, "agent_version": sp.AgentVersion,
		"tool_name": sp.ToolName, "tool_version": sp.ToolVersion,
		"tool_args_hash": sp.ToolArgsHash, "tool_schema_version": sp.ToolSchemaVer,
		"model": sp.Model, "provider": sp.Provider, "usage_source": sp.UsageSource,
		"tokens_in": sp.TokensIn, "tokens_out": sp.TokensOut,
		"cost_usd": sp.CostUSD, "cost_estimated": sp.CostEstimated,
		"retries": sp.Retries, "error_class": sp.ErrorClass,
		"started_at": sp.StartedAt, "ended_at": sp.EndedAt, "duration_ms": sp.DurationMS,
		"seq_start": sp.SeqStart, "seq_end": sp.SeqEnd,
	}
}

func eventsAsMaps(evs []*store.EventRow) []map[string]any {
	out := make([]map[string]any, 0, len(evs))
	for _, e := range evs {
		out = append(out, eventAsMap(e))
	}
	return out
}

func eventAsMap(e *store.EventRow) map[string]any {
	if e == nil {
		return nil
	}
	m := map[string]any{
		"id": e.ID, "run_id": e.Run, "span_id": e.Span,
		"parent_span_id": e.Parent, "seq": e.Seq, "type": e.Type,
		"ts": e.TS, "received_at": e.ReceivedAt,
		"agent_name": e.AgentName, "agent_version": e.AgentVersion,
		"model": e.Model, "provider": e.Provider,
		"attributes": e.Attrs,
	}
	if e.PayloadRef != "" {
		m["payload_ref"] = e.PayloadRef
	}
	return m
}

func findingsAsMaps(fs []store.FindingRow) []map[string]any {
	out := make([]map[string]any, 0, len(fs))
	for _, f := range fs {
		out = append(out, findingAsMap(f))
	}
	return out
}

func findingAsMap(f store.FindingRow) map[string]any {
	var evidence []any
	if f.Evidence != "" {
		_ = json.Unmarshal([]byte(f.Evidence), &evidence)
	}
	return map[string]any{
		"id": f.ID, "run_id": f.Run, "signature": f.Signature,
		"kind": f.Kind, "severity": f.Severity, "message": f.Message,
		"evidence": evidence, "created_at": f.CreatedAt,
	}
}

func sigAsMap(s store.Signature) map[string]any {
	return sigAsMapOpt(&s)
}

func sigAsMapOpt(s *store.Signature) any {
	if s == nil {
		return nil
	}
	return map[string]any{
		"signature": s.Signature, "kind": s.Kind, "tool_name": s.ToolName,
		"count": s.Count, "run_count": s.RunCount,
		"first_seen": s.FirstSeen, "last_seen": s.LastSeen,
		"agent_versions": s.AgentVersions,
		"sample_event": s.SampleEvent, "sample_run": s.SampleRun,
	}
}

// worstSeverity ranks findings by highest severity.
func worstSeverity(fs []store.FindingRow) string {
	levels := map[string]int{"error": 3, "warning": 2, "info": 1, "": 0}
	worst := ""
	for _, f := range fs {
		if levels[f.Severity] > levels[worst] {
			worst = f.Severity
		}
	}
	if worst == "" {
		worst = "info"
	}
	return worst
}

// keep store referenced (some serializers use its types).
var _ store.Signature

// ---- shared errors / json helpers ----

var (
	errMissingFields = errors.New("missing required fields")
	errNoCases       = errors.New("dataset has no cases")
	errNotFound      = errors.New("not found")
)

// mustJSON marshals v for storage (best-effort; empty on error).
func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}
