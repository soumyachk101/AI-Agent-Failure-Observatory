package api

import (
	"encoding/json"
	"net/http"

	"github.com/ensembyte/observatory/internal/event"
	"github.com/ensembyte/observatory/internal/evalsvc"
	"github.com/ensembyte/observatory/internal/store"
)

// createDataset registers a versioned dataset with cases.
func (r *Router) createDataset(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()
	pid, err := r.ctxProject(ctx, req, req.PathValue("pid"))
	if err != nil {
		writeErr(w, http.StatusUnauthorized, err)
		return
	}
	var body struct {
		ID          string   `json:"id"`
		Name        string   `json:"name"`
		Version     string   `json:"version"`
		Description string   `json:"description"`
		Cases       []struct {
			ID               string `json:"id"`
			Task             string `json:"task"`
			ExpectedTool     string `json:"expected_tool"`
			ExpectCompletion bool   `json:"expect_completion"`
			Rubric           string `json:"rubric"`
		} `json:"cases"`
	}
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if body.ID == "" {
		body.ID = "ds-" + randomID()
	}
	if body.Version == "" {
		body.Version = "v1"
	}
	ds := &store.Dataset{
		ID: body.ID, Project: pid, Name: body.Name, Version: body.Version,
		Description: body.Description, CaseCount: len(body.Cases),
	}
	if err := r.App.Store.UpsertDataset(ctx, ds); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	for _, c := range body.Cases {
		_ = r.App.Store.UpsertCase(ctx, pid, c.ID, body.ID, c.Task,
			c.ExpectedTool, c.ExpectCompletion, c.Rubric)
	}
	// Refresh case count.
	_ = r.App.Store.UpsertDataset(ctx, &store.Dataset{
		ID: body.ID, Project: pid, Name: body.Name, Version: body.Version,
		CaseCount: len(body.Cases),
	})
	writeJSON(w, http.StatusCreated, map[string]any{
		"id": body.ID, "version": body.Version, "case_count": len(body.Cases),
	})
}

func (r *Router) listDatasets(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()
	pid, err := r.ctxProject(ctx, req, req.PathValue("pid"))
	if err != nil {
		writeErr(w, http.StatusUnauthorized, err)
		return
	}
	dss, err := r.App.Store.ListDatasets(ctx, pid)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]map[string]any, 0, len(dss))
	for _, d := range dss {
		out = append(out, map[string]any{
			"id": d.ID, "name": d.Name, "version": d.Version,
			"description": d.Description, "case_count": d.CaseCount,
			"created_at": d.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (r *Router) getDataset(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()
	pid, err := r.ctxProject(ctx, req, req.PathValue("pid"))
	if err != nil {
		writeErr(w, http.StatusUnauthorized, err)
		return
	}
	did := req.PathValue("did")
	cases, err := r.App.Store.ListCases(ctx, pid, did)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// Dataset-level lookup is best-effort in dev (single row per version).
	var ds *store.Dataset
	if dss, derr := r.App.Store.ListDatasets(ctx, pid); derr == nil {
		for _, d := range dss {
			if d.ID == did {
				ds = &d
				break
			}
		}
	}
	out := map[string]any{"id": did, "cases": cases}
	if ds != nil {
		out["name"] = ds.Name
		out["version"] = ds.Version
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- evaluation ----

// createEvaluation runs a dataset against an agent version and records a
// versioned evaluation (spec FR-05). The agent runs are already ingested;
// we pair each dataset case with the most recent matching run.
func (r *Router) createEvaluation(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()
	pid, err := r.ctxProject(ctx, req, req.PathValue("pid"))
	if err != nil {
		writeErr(w, http.StatusUnauthorized, err)
		return
	}
	var body struct {
		DatasetID    string `json:"dataset_id"`
		DatasetVer   string `json:"dataset_version"`
		AgentName    string `json:"agent_name"`
		AgentVersion string `json:"agent_version"`
		Model        string `json:"model"`
		EvaluatorID  string `json:"evaluator_id"`
		EvaluatorVer string `json:"evaluator_version"`
	}
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if body.DatasetID == "" || body.AgentName == "" || body.AgentVersion == "" {
		writeErr(w, http.StatusBadRequest, errMissingFields)
		return
	}
	if body.EvaluatorVer == "" {
		body.EvaluatorVer = "v1"
	}
	if body.DatasetVer == "" {
		body.DatasetVer = "v1"
	}

	raw, err := r.App.Store.ListCases(ctx, pid, body.DatasetID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	cases := evalsvc.ParseCases(raw)
	if len(cases) == 0 {
		writeErr(w, http.StatusBadRequest, errNoCases)
		return
	}

	ev := evalsvc.NewEvaluator(r.App.Store)
	run := "eval-" + randomID()

	// Pair each case with a matching ingested run (best-effort: use the
	// newest completed run for this agent version). Deterministic scoring
	// is over the case's expected outcome.
	runs, _ := r.App.Store.ListRuns(ctx, pid, "completed", body.AgentName, body.AgentVersion, body.Model, "", 1000, 0)
	passed, total := 0, len(cases)
	sumScore := 0.0
	for i, dc := range cases {
		// If there are fewer runs than cases, reuse the closest one.
		var tl *store.Timeline
		if i < len(runs) {
			tl, _ = r.App.Store.RunTimeline(ctx, pid, runs[i].RunID)
		}
		var cr evalsvc.CaseResult
		if tl != nil {
			cr = ev.RunCase(ctx, pid, dc, tl)
		} else {
			// No ingested run for this case: mark error, do not guess.
			cr = evalsvc.CaseResult{CaseID: dc.ID, Status: "error",
				Detail: "no matching ingested run for this case"}
		}
		_ = r.App.Store.InsertEvalResult(ctx, pid, run, dc.ID,
			cr.Passed, cr.Score, cr.Status, cr.Detail, originRunFor(runs, i))
		if cr.Passed {
			passed++
		}
		sumScore += cr.Score
	}

	avg := 0.0
	if total > 0 {
		avg = sumScore / float64(total)
	}
	passRate := float64(passed) / float64(total)
	gate := evalsvc.DefaultGate()
	gateFailed := gate.Require && (passRate < gate.MinPassRate || (gate.MinScore > 0 && avg < gate.MinScore))

	er := &store.EvalRun{
		ID: run, Project: pid, DatasetID: body.DatasetID,
		DatasetVersion: body.DatasetVer,
		AgentName: body.AgentName, AgentVersion: body.AgentVersion,
		Model: body.Model, EvaluatorID: body.EvaluatorID,
		EvaluatorVersion: body.EvaluatorVer,
		Status: "completed", TotalCases: total, Passed: passed,
		AvgScore: avg, ScoreSource: "deterministic", GateFailed: gateFailed,
		GateJSON: mustJSON(gate),
	}
	if err := r.App.Store.CreateEvalRun(ctx, er); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	_ = r.App.Store.FinishEvalRun(ctx, er)

	writeJSON(w, http.StatusCreated, map[string]any{
		"id": run, "agent_version": body.AgentVersion,
		"model": body.Model, "dataset_version": body.DatasetVer,
		"total_cases": total, "passed": passed, "avg_score": avg,
		"pass_rate": passRate, "gate_failed": gateFailed,
		"gate": gate, "score_source": "deterministic",
	})
}

func originRunFor(runs []*store.RunAggregate, i int) string {
	if i >= 0 && i < len(runs) {
		return runs[i].RunID
	}
	return ""
}

// ---- evaluation listing / detail ----

func (r *Router) listEvaluations(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()
	pid, err := r.ctxProject(ctx, req, req.PathValue("pid"))
	if err != nil {
		writeErr(w, http.StatusUnauthorized, err)
		return
	}
	// A project's evaluations are grouped per dataset; the UI passes the
	// dataset id in the query.
	did := req.URL.Query().Get("dataset_id")
	if did == "" {
		// Fall back to the first dataset.
		dss, _ := r.App.Store.ListDatasets(ctx, pid)
		if len(dss) == 0 {
			writeJSON(w, http.StatusOK, []any{})
			return
		}
		did = dss[0].ID
	}
	ers, err := r.App.Store.ListEvalRuns(ctx, pid, did)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]map[string]any, 0, len(ers))
	for _, e := range ers {
		out = append(out, evalRunAsMap(e))
	}
	writeJSON(w, http.StatusOK, out)
}

func (r *Router) getEvaluation(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()
	pid, err := r.ctxProject(ctx, req, req.PathValue("pid"))
	if err != nil {
		writeErr(w, http.StatusUnauthorized, err)
		return
	}
	eid := req.PathValue("eid")
	er, err := r.App.Store.GetEvalRun(ctx, pid, eid)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if er == nil {
		writeErr(w, http.StatusNotFound, errNotFound)
		return
	}
	results, _ := r.App.Store.ListEvalResults(ctx, pid, eid)
	out := evalRunAsMap(*er)
	out["results"] = evalResultsAsMaps(results)
	writeJSON(w, http.StatusOK, out)
}

func evalRunAsMap(e store.EvalRun) map[string]any {
	m := map[string]any{
		"id": e.ID, "dataset_id": e.DatasetID, "dataset_version": e.DatasetVersion,
		"agent_name": e.AgentName, "agent_version": e.AgentVersion,
		"model": e.Model, "evaluator_id": e.EvaluatorID,
		"evaluator_version": e.EvaluatorVersion,
		"status": e.Status, "total_cases": e.TotalCases, "passed": e.Passed,
		"avg_score": e.AvgScore, "score_source": e.ScoreSource,
		"gate_failed": e.GateFailed, "gate_json": e.GateJSON,
		"started_at": e.StartedAt,
	}
	if e.FinishedAt != nil {
		m["finished_at"] = *e.FinishedAt
	}
	return m
}

func evalResultsAsMaps(rs []store.EvalResult) []map[string]any {
	out := make([]map[string]any, 0, len(rs))
	for _, r := range rs {
		out = append(out, map[string]any{
			"case_id": r.CaseID, "passed": r.Passed, "score": r.Score,
			"status": r.Status, "detail": r.Detail, "origin_run": r.OriginRun,
		})
	}
	return out
}

// keep event referenced (attribute names are used by evals indirectly).
var _ = event.ToolCallKey
