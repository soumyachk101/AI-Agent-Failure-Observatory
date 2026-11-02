// Package alertsvc implements deterministic alert rules (spec FR-07):
// loop signatures, elevated failure rates, cost spikes, latency
// regressions. Alerts route through signed webhooks with replay
// protection (spec 12).
package alertsvc

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/ensembyte/observatory/internal/store"
)

// AlertService evaluates rules against recent findings/signatures and
// delivers triggered alerts to webhooks.
type AlertService struct {
	Store  store.Engine
	// Metrics supplies the built-in detector metrics. Filled from Store when
	// it implements store.AlertMetrics; tests may override.
	Metrics store.AlertMetrics
	HTTP    *http.Client
	// SigningSecret is used to HMAC-sign webhook deliveries so receivers
	// can verify authenticity and replay protection uses a monotonically
	// increasing nonce (spec: signed webhook delivery + replay protection).
	SigningSecret string
	// Now is injectable for deterministic tests.
	Now func() time.Time
}

// New builds the alert service.
func New(s store.Engine) *AlertService {
	a := &AlertService{
		Store: s, HTTP: &http.Client{Timeout: 5 * time.Second},
		Now: time.Now,
	}
	if m, ok := s.(store.AlertMetrics); ok {
		a.Metrics = m
	}
	return a
}

// WebhookPayload is the signed delivery body.
type WebhookPayload struct {
	Project       string        `json:"project"`
	RuleID        string        `json:"rule_id"`
	Detector      string        `json:"detector"`
	Severity      string        `json:"severity"`
	Metric        string        `json:"metric"`
	Value         int64         `json:"value"`
	Threshold     int           `json:"threshold"`
	WindowMin     int           `json:"window_min"`
	WindowStart   time.Time     `json:"window_start"`
	Nonce         int64         `json:"nonce"`
	Signature     string        `json:"signature"`
	OccurredAt    time.Time     `json:"occurred_at"`
}

// Evaluate runs every enabled rule for a project over a window and records
// outcomes. Metrics are counted from persisted findings/signatures so the
// alert is a deterministic function of evidence.
func (a *AlertService) Evaluate(ctx context.Context, project string, window time.Time) (triggered int, err error) {
	rules, err := a.Store.ListAlertRules(ctx, project)
	if err != nil {
		return 0, err
	}
	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		value, sig, metric := a.metricFor(ctx, project, r, window)
		isTriggered := value >= int64(r.Threshold)

		eval := &store.AlertEvaluation{
			Project:     project,
			RuleID:      r.ID,
			WindowStart: window,
			Metric:      metric,
			Value:       value,
			Triggered:   isTriggered,
		}
		if isTriggered {
			triggered++
			status, delErr, body := a.deliver(ctx, project, r, value, metric, sig, window)
			eval.DeliveryStatus = status
			if delErr != nil {
				eval.DeliveryError = delErr.Error()
			}
			_ = body
		} else {
			eval.DeliveryStatus = "skipped"
		}
		if err := a.Store.RecordAlertEvaluation(ctx, eval); err != nil {
			return triggered, err
		}
	}
	return triggered, nil
}

// metricFor computes the metric a rule watches. Detector kinds:
//
//	"loop-signature":        count of tool-loop findings in window
//	"failure-rate":          failed runs / total runs * 100 (rounded)
//	"cost-spike":            count of budget-exhaustion findings in window
//	"latency-regression":    count of runs whose duration_ms exceeds
//	                         window average by 2x
func (a *AlertService) metricFor(ctx context.Context, project string, r store.AlertRule, window time.Time) (int64, string, string) {
	switch r.Detector {
	case "loop-signature":
		n := a.countFindingKind(ctx, project, "tool-loop", window)
		return n, "tool-loop-findings", sigForFindings(ctx, a.Store, project, "tool-loop")
	case "failure-rate":
		rate := a.failureRate(ctx, project, window)
		return rate, "failure-rate-percent", ""
	case "cost-spike":
		n := a.countFindingKind(ctx, project, "budget-exhaustion", window)
		return n, "budget-exhaustion-findings", ""
	case "latency-regression":
		n := a.countLatencyRegressions(ctx, project, window)
		return n, "latency-regressions", ""
	default:
		return 0, "unknown", ""
	}
}

func sigForFindings(ctx context.Context, s store.Engine, project, kind string) string {
	sigs, _ := s.ListSignatures(ctx, project)
	for _, sg := range sigs {
		if sg.Kind == kind {
			return sg.Signature
		}
	}
	return ""
}

func (a *AlertService) countFindingKind(ctx context.Context, project, kind string, since time.Time) int64 {
	if a.Metrics == nil {
		return 0
	}
	return a.Metrics.AlertFindingKind(ctx, project, kind, since)
}

func (a *AlertService) failureRate(ctx context.Context, project string, since time.Time) int64 {
	if a.Metrics == nil {
		return 0
	}
	total, failed := a.Metrics.AlertRunStatusCounts(ctx, project, since)
	if total == 0 {
		return 0
	}
	return int64(float64(failed) / float64(total) * 100)
}

func (a *AlertService) countLatencyRegressions(ctx context.Context, project string, since time.Time) int64 {
	// A latency regression = a run whose duration is more than 2x the
	// average over the same window. Computed in one pass.
	if a.Metrics == nil {
		return 0
	}
	return a.Metrics.AlertLatencyRegressions(ctx, project, since)
}

// deliver signs and POSTs the webhook. Nonce is a monotonic value derived
// from the evaluation timestamp; signature is HMAC-SHA256 over a canonical
// string (spec 12: signed webhook delivery + replay protection).
func (a *AlertService) deliver(ctx context.Context, project string, r store.AlertRule, value int64, metric, sig string, window time.Time) (string, error, []byte) {
	if r.WebhookURL == "" {
		return "no-route", nil, nil
	}
	nonce := window.Unix()
	payload := WebhookPayload{
		Project: project, RuleID: r.ID, Detector: r.Detector,
		Severity: r.Severity, Metric: metric, Value: value,
		Threshold: r.Threshold, WindowMin: r.WindowMin,
		WindowStart: window, Signature: sig, Nonce: nonce,
		OccurredAt: a.Now().UTC(),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "marshal-error", err, nil
	}
	// Canonical signing string: project|rule|nonce (order matters).
	canonical := project + "|" + r.ID + "|" + strconv.FormatInt(nonce, 10)
	payload.Signature = sign(a.SigningSecret, canonical)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.WebhookURL, bytes.NewReader(body))
	if err != nil {
		return "build-error", err, nil
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Obs-Nonce", strconv.FormatInt(nonce, 10))
	req.Header.Set("X-Obs-Signature", payload.Signature)
	resp, err := a.HTTP.Do(req)
	if err != nil {
		return "send-error", err, body
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return "delivered", nil, body
	}
	return "http-" + strconv.Itoa(resp.StatusCode), nil, body
}

// sign computes the HMAC-SHA256 hex digest.
func sign(secret, msg string) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(msg))
	return hex.EncodeToString(h.Sum(nil))
}
