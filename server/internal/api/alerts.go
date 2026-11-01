package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/ensembyte/observatory/internal/alertsvc"
	"github.com/ensembyte/observatory/internal/store"
)

// createAlertRule stores an alert rule.
func (r *Router) createAlertRule(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()
	pid, err := r.ctxProject(ctx, req, req.PathValue("pid"))
	if err != nil {
		writeErr(w, http.StatusUnauthorized, err)
		return
	}
	var body struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		Detector   string `json:"detector"`
		WindowMin  int    `json:"window_min"`
		Threshold  int    `json:"threshold"`
		Agent      string `json:"agent"`
		Severity   string `json:"severity"`
		WebhookURL string `json:"webhook_url"`
		Enabled    *bool  `json:"enabled"`
	}
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if body.Detector == "" || body.Threshold <= 0 {
		writeErr(w, http.StatusBadRequest, errMissingFields)
		return
	}
	if body.ID == "" {
		body.ID = "alert-" + randomID()
	}
	if body.WindowMin <= 0 {
		body.WindowMin = 10
	}
	if body.Severity == "" {
		body.Severity = "warning"
	}
	enabled := true
	if body.Enabled != nil {
		enabled = *body.Enabled
	}
	// Validate detector kinds against known detectors (spec 8).
	switch body.Detector {
	case "loop-signature", "failure-rate", "cost-spike", "latency-regression":
		// ok
	default:
		writeErr(w, http.StatusBadRequest,
			errors.New("unknown detector "+body.Detector))
		return
	}
	rule := &store.AlertRule{
		ID: body.ID, Project: pid, Name: body.Name,
		Detector: body.Detector, WindowMin: body.WindowMin,
		Threshold: body.Threshold, Agent: body.Agent,
		Severity: body.Severity, WebhookURL: body.WebhookURL, Enabled: enabled,
	}
	if err := r.App.Store.UpsertAlertRule(ctx, rule); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	_ = r.App.Store.LogAudit(ctx, pid, "alert-rule-create", "api", rule.ID)
	writeJSON(w, http.StatusCreated, ruleAsMap(*rule))
}

func (r *Router) listAlertRules(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()
	pid, err := r.ctxProject(ctx, req, req.PathValue("pid"))
	if err != nil {
		writeErr(w, http.StatusUnauthorized, err)
		return
	}
	rules, err := r.App.Store.ListAlertRules(ctx, pid)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]map[string]any, 0, len(rules))
	for _, rl := range rules {
		out = append(out, ruleAsMap(rl))
	}
	writeJSON(w, http.StatusOK, out)
}

// evaluateAlerts runs a single alert evaluation window now and reports which
// rules triggered. The scheduler (worker) runs this on an interval; this
// endpoint lets the UI / CI trigger it explicitly.
func (r *Router) evaluateAlerts(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()
	pid, err := r.projectKey(ctx, req)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, err)
		return
	}
	now := time.Now().UTC()
	// Default window is the most recent 10 minutes; the UI can pass one.
	q := req.URL.Query()
	window := 10
	if wm := q.Get("window_min"); wm != "" {
		if v, perr := fmtAtoi(wm); perr == nil {
			window = int(v)
		}
	}
	windowStart := now.Add(-time.Duration(window) * time.Minute)

	// Build the alert service with the configured signing secret.
	secret := r.App.SigningSecret // may be empty in dev
	svc := alertsvc.New(r.App.Store)
	svc.SigningSecret = secret
	triggered, err := svc.Evaluate(ctx, pid, windowStart)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"project":    pid,
		"window_min": window,
		"triggered":  triggered,
		"window_start": windowStart,
	})
}

func ruleAsMap(r store.AlertRule) map[string]any {
	return map[string]any{
		"id": r.ID, "name": r.Name, "detector": r.Detector,
		"window_min": r.WindowMin, "threshold": r.Threshold,
		"agent": r.Agent, "severity": r.Severity,
		"webhook_url": r.WebhookURL, "enabled": r.Enabled,
		"created_at": r.CreatedAt,
	}
}
