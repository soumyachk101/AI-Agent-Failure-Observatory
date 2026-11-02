package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/ensembyte/observatory/internal/event"
	"github.com/ensembyte/observatory/internal/pipeline"
	"github.com/ensembyte/observatory/internal/queue"
	"github.com/ensembyte/observatory/internal/store"
)

// Router builds the HTTP handler tree with project-key auth.
type Router struct {
	App       *App
	limiter   *rateLimiter
}

// NewRouter wires the router.
func NewRouter(app *App) *Router {
	return &Router{App: app, limiter: newRateLimiter()}
}

// Handler returns the mux.
func (r *Router) Handler() http.Handler {
	mux := http.NewServeMux()

	// Ingestion (authenticated by project key).
	mux.HandleFunc("POST /v1/ingest/events", r.ingest)

	// Admin / bootstrap (no auth in dev; real deployments gate by service
	// key or mTLS). These create projects and keys so the demo works.
	mux.HandleFunc("POST /v1/projects", r.createProject)
	mux.HandleFunc("POST /v1/projects/{pid}/keys", r.createKey)

	// Run queries (authenticated by project key; tenant-scoped).
	mux.HandleFunc("GET /v1/projects/{pid}/runs", r.listRuns)
	mux.HandleFunc("GET /v1/runs/{run_id}", r.getRun)
	mux.HandleFunc("GET /v1/runs/{run_id}/timeline", r.runTimeline)
	mux.HandleFunc("GET /v1/projects/{pid}/failures", r.failures)
	mux.HandleFunc("GET /v1/projects/{pid}/failures/{sig}", r.failureDetail)

	// Datasets / evaluation.
	mux.HandleFunc("POST /v1/projects/{pid}/datasets", r.createDataset)
	mux.HandleFunc("GET /v1/projects/{pid}/datasets", r.listDatasets)
	mux.HandleFunc("GET /v1/projects/{pid}/datasets/{did}", r.getDataset)
	mux.HandleFunc("POST /v1/projects/{pid}/evaluations", r.createEvaluation)
	mux.HandleFunc("GET /v1/projects/{pid}/evaluations", r.listEvaluations)
	mux.HandleFunc("GET /v1/projects/{pid}/evaluations/{eid}", r.getEvaluation)

	// Replay.
	mux.HandleFunc("POST /v1/projects/{pid}/replays", r.createReplay)
	mux.HandleFunc("GET /v1/projects/{pid}/replays/{rid}", r.getReplay)

	// Alerts.
	mux.HandleFunc("POST /v1/projects/{pid}/alerts", r.createAlertRule)
	mux.HandleFunc("GET /v1/projects/{pid}/alerts", r.listAlertRules)
	mux.HandleFunc("POST /v1/projects/{pid}/alerts/evaluate", r.evaluateAlerts)

	// Usage.
	mux.HandleFunc("GET /v1/projects/{pid}/usage", r.usage)

	// Health (no auth).
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /v1/readyz", r.ready)

	return withCORS(mux)
}

// ready is a liveness probe that also reports queue depth.
func (r *Router) ready(w http.ResponseWriter, _ *http.Request) {
	depth := 0
	if r.App.Queue != nil {
		depth = r.App.Queue.Depth()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ready", "queue_depth": depth,
	})
}

// ---- auth middleware ----

// projectKey extracts the Bearer token and resolves the owning project.
func (r *Router) projectKey(ctx context.Context, req *http.Request) (string, error) {
	auth := req.Header.Get("Authorization")
	key := strings.TrimPrefix(auth, "Bearer ")
	if key == "" {
		// Also allow an X-Obs-Key header for SDK convenience.
		key = req.Header.Get("X-Obs-Key")
	}
	if key == "" {
		return "", errors.New("missing project key")
	}
	project, ok, err := r.App.APIKeys.LookupProjectForKey(ctx, key)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", errors.New("invalid project key")
	}
	return project, nil
}

// ctxProject resolves the authenticated project for a route that has a
// {pid} path parameter, and verifies the key owns that project (tenant
// isolation: a key cannot read another tenant's project).
func (r *Router) ctxProject(ctx context.Context, req *http.Request, pid string) (string, error) {
	project, err := r.projectKey(ctx, req)
	if err != nil {
		return "", err
	}
	if pid != "" && project != pid {
		return "", errors.New("project key does not scope to this project")
	}
	return pid, nil
}

// ---- ingest ----

type IngestRequest struct {
	// Events are raw envelopes. Batched transport (spec: SDK batches).
	Events []json.RawMessage `json:"events"`
	// IdempotencyKey lets the SDK de-dup a retried batch end-to-end.
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

// ingest authenticates, rate-limits, and enqueues a batch. In dev mode we
// also process synchronously so the UI reflects events immediately; the
// queue + worker remain the at-least-once path.
func (r *Router) ingest(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()
	project, err := r.projectKey(ctx, req)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, err)
		return
	}
	body, _ := io.ReadAll(io.LimitReader(req.Body, 32<<20))
	var ir IngestRequest
	if err := json.Unmarshal(body, &ir); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	pp, err := ProjectPolicyView(ctx, r.App.Store, project)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if !r.limiter.allow(project, pp.RatePerMin, pp.RateBurst, nowUTC()) {
		// 429 with retry guidance (spec: buffer SDK events during transient
		// failures — the SDK retries with jitter).
		w.Header().Set("Retry-After", "1")
		writeErr(w, http.StatusTooManyRequests, errors.New("rate limit exceeded; back off and retry"))
		return
	}

	// Decode envelopes.
	envelopes := make([]*event.Envelope, 0, len(ir.Events))
	for _, raw := range ir.Events {
		var env event.Envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			continue
		}
		envelopes = append(envelopes, &env)
	}
	if len(envelopes) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{
			"accepted": 0, "rejected": 0, "project": project,
		})
		return
	}

	// Synchronous pipeline (dev immediacy). The store dedups, so the async
	// worker processing the same batch later is a no-op.
	norm := &pipeline.Normalizer{Store: r.App.Store, RedactKeys: pp.RedactFields}
	res, err := norm.Run(ctx, pp, envelopes)
	if err != nil {
		// Still enqueue for durability so nothing is lost on a transient
		// DB error.
		r.enqueue(ctx, project, ir, envelopes)
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	r.enqueue(ctx, project, ir, envelopes)

	// Audit payload access when capture is on (spec 12).
	if pp.PayloadCapture == "on" {
		_ = r.App.Store.LogAudit(ctx, project, "ingest", "sdk", "payload capture enabled")
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"project":  project,
		"accepted": len(res.Accepted),
		"rejected": len(res.Rejected),
		"rejected_reasons": res.RejectedReasons,
	})
}

// enqueue durably enqueues the batch for the worker path.
func (r *Router) enqueue(ctx context.Context, project string, ir IngestRequest, envelopes []*event.Envelope) {
	raw := make([]json.RawMessage, 0, len(envelopes))
	for _, e := range envelopes {
		b, _ := json.Marshal(e)
		raw = append(raw, b)
	}
	payload, _ := json.Marshal(queue.BatchPayload{Project: project, Events: raw})
	m := queue.Message{ProjectID: project, Payload: payload}
	if ir.IdempotencyKey != "" {
		m.ID = "idem." + ir.IdempotencyKey
	}
	_ = r.App.Queue.Enqueue(ctx, m)
}

// ---- admin ----

func (r *Router) createProject(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()
	var body struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Environment string `json:"environment"`
	}
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if body.ID == "" {
		body.ID = "project-" + randomID()
	}
	if err := r.App.Store.CreateProject(ctx, body.ID, body.Name, body.Environment); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// Seed a key so the project is immediately usable.
	key, _ := NewKey()
	if key != "" {
		_ = r.App.Store.CreateAPIKey(ctx, body.ID, "initial", key)
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id": body.ID, "name": body.Name, "environment": body.Environment,
		"api_key": key,
		"note": "store this key now; it is shown once",
	})
}

func (r *Router) createKey(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()
	pid := req.PathValue("pid")
	if _, err := r.ctxProject(ctx, req, pid); err != nil {
		writeErr(w, http.StatusUnauthorized, err)
		return
	}
	key, err := NewKey()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if err := r.App.Store.CreateAPIKey(ctx, pid, "manual", key); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"api_key": key, "project": pid})
}

// ---- run queries ----

func (r *Router) listRuns(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()
	pid := req.PathValue("pid")
	if _, err := r.ctxProject(ctx, req, pid); err != nil {
		writeErr(w, http.StatusUnauthorized, err)
		return
	}
	q := req.URL.Query()
	status := q.Get("status")
	agent := q.Get("agent")
	agentVersion := q.Get("agent_version")
	model := q.Get("model")
	environment := q.Get("environment")
	limit := 50
	if l := q.Get("limit"); l != "" {
		if v, err := fmtAtoi(l); err == nil {
			limit = int(v)
		}
	}
	offset := 0
	if o := q.Get("offset"); o != "" {
		if v, err := fmtAtoi(o); err == nil {
			offset = int(v)
		}
	}
	runs, err := r.App.Store.ListRuns(ctx, pid, status, agent, agentVersion, model, environment, limit, offset)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// Attach findings counts so the UI can surface failure status.
	out := make([]map[string]any, 0, len(runs))
	for _, run := range runs {
		row := runAsMap(run)
		if findings, _ := r.App.Store.ListFindingsByRun(ctx, pid, run.RunID); len(findings) > 0 {
			row["finding_count"] = len(findings)
			row["worst_severity"] = worstSeverity(findings)
		}
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, out)
}

func (r *Router) getRun(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()
	pid, err := r.projectKey(ctx, req)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, err)
		return
	}
	runID := req.PathValue("run_id")
	tl, err := r.App.Store.RunTimeline(ctx, pid, runID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if tl == nil || (len(tl.Events) == 0 && tl.Run == nil) {
		writeErr(w, http.StatusNotFound, errNotFound)
		return
	}
	findings, _ := r.App.Store.ListFindingsByRun(ctx, pid, runID)
	writeJSON(w, http.StatusOK, map[string]any{
		"run":      runAsMap(tl.Run),
		"findings": findingsAsMaps(findings),
	})
}

func (r *Router) runTimeline(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()
	pid, err := r.projectKey(ctx, req)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, err)
		return
	}
	runID := req.PathValue("run_id")
	tl, err := r.App.Store.RunTimeline(ctx, pid, runID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	findings, _ := r.App.Store.ListFindingsByRun(ctx, pid, runID)
	writeJSON(w, http.StatusOK, map[string]any{
		"project":  pid,
		"run":      runAsMap(tl.Run),
		"spans":    spansAsMaps(tl.Spans),
		"events":   eventsAsMaps(tl.Events),
		"findings": findingsAsMaps(findings),
	})
}

// ---- failures ----

func (r *Router) failures(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()
	pid, err := r.ctxProject(ctx, req, req.PathValue("pid"))
	if err != nil {
		writeErr(w, http.StatusUnauthorized, err)
		return
	}
	sigs, err := r.App.Store.ListSignatures(ctx, pid)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]map[string]any, 0, len(sigs))
	for _, s := range sigs {
		out = append(out, sigAsMap(s))
	}
	writeJSON(w, http.StatusOK, out)
}

func (r *Router) failureDetail(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()
	pid, err := r.ctxProject(ctx, req, req.PathValue("pid"))
	if err != nil {
		writeErr(w, http.StatusUnauthorized, err)
		return
	}
	sig := req.PathValue("sig")
	findings, _ := r.App.Store.FindingsBySignature(ctx, pid, sig)
	var sigInfo *store.Signature
	if sigs, _ := r.App.Store.ListSignatures(ctx, pid); sigs != nil {
		for i := range sigs {
			if sigs[i].Signature == sig {
				sigInfo = &sigs[i]
				break
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"signature": sig, "info": sigAsMapOpt(sigInfo),
		"findings":  findingsAsMaps(findings),
	})
}

// ---- usage ----

func (r *Router) usage(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()
	pid, err := r.projectKey(ctx, req)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, err)
		return
	}
	// Usage is aggregated over the project's recent runs (bounded).
	runs, _ := r.App.Store.ListRuns(ctx, pid, "", "", "", "", "", 1000, 0)
	var eventCount int64
	for _, rr := range runs {
		eventCount += rr.EventCount
	}
	depth := 0
	if r.App.Queue != nil {
		depth = r.App.Queue.Depth()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"project":     pid,
		"run_count":   len(runs),
		"event_count": eventCount,
		"queue_depth": depth,
		"note": "aggregated over the most recent 1000 runs; live per-second counters are a production metric",
	})
}
