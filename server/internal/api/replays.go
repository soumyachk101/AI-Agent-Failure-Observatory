package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/ensembyte/observatory/internal/evalsvc"
)

// createReplay executes a SAFE replay: mocked tools by default, no live
// side effects. The isolation boundary is documented in docs/architecture.md
// and enforced by the executor choice.
func (r *Router) createReplay(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()
	pid, err := r.ctxProject(ctx, req, req.PathValue("pid"))
	if err != nil {
		writeErr(w, http.StatusUnauthorized, err)
		return
	}
	var body struct {
		OriginRun string `json:"origin_run"`
		ReplayRun string `json:"replay_run"`
		// Mode is accepted but only "mocked" is permitted through this
		// endpoint. A "live" request is refused: live replay is a
		// separate, explicitly configured executor (spec FR-06).
		Mode string `json:"mode"`
	}
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if body.OriginRun == "" {
		writeErr(w, http.StatusBadRequest, errMissingFields)
		return
	}
	if body.Mode == "live" {
		// Refuse live replay through the default path (spec: never silently
		// re-run payment, email, deletion or production mutations).
		writeErr(w, http.StatusMethodNotAllowed,
			fmt.Errorf("live replay is not permitted through the default path; "+
				"configure a live executor with an allowlist and dry-run checks"))
		return
	}
	if body.ReplayRun == "" {
		body.ReplayRun = "replay-" + randomID()
	}

	// Only the mock executor is wired here. This is the isolation boundary:
	// no outbound sockets, no production credentials, destructive tools
	// refused.
	tools := evalsvc.NewMockTool()
	rs := &evalsvc.ReplayService{Store: r.App.Store, Tools: tools, Project: pid}

	// Validate the origin run exists in this project (tenant isolation).
	origin, err := r.App.Store.RunTimeline(ctx, pid, body.OriginRun)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if origin == nil || len(origin.Events) == 0 {
		writeErr(w, http.StatusNotFound, errNotFound)
		return
	}

	out, err := rs.Rebuild(ctx, body.OriginRun, body.ReplayRun)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	jobID := "rp-" + randomID()
	_ = rs.Record(ctx, jobID, out)
	_ = r.App.Store.LogAudit(ctx, pid, "replay", "api",
		"origin="+body.OriginRun+" mode=mocked")

	writeJSON(w, http.StatusCreated, map[string]any{
		"id":          jobID,
		"origin_run":  out.OriginRun,
		"replay_run":  out.ReplayRun,
		"status":      out.Status,
		"mode":        "mocked",
		"isolation":   out.Isolation,
		"step_count":  out.StepCount,
		"completed":   out.CompletedSteps,
		"failed":      out.FailedSteps,
		"refused":     out.RefusedSteps,
		"provenance":  out.Provenance,
	})
}

func (r *Router) getReplay(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()
	pid, err := r.ctxProject(ctx, req, req.PathValue("pid"))
	if err != nil {
		writeErr(w, http.StatusUnauthorized, err)
		return
	}
	rid := req.PathValue("rid")
	j, err := r.App.Store.GetReplayJob(ctx, pid, rid)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if j == nil {
		writeErr(w, http.StatusNotFound, errNotFound)
		return
	}
	replays, _ := r.App.Store.ListReplayJobs(ctx, pid)
	_ = replays
	writeJSON(w, http.StatusOK, map[string]any{
		"id": j.ID, "origin_run": j.OriginRun, "replay_run": j.ReplayRun,
		"status": j.Status, "mode": j.Mode,
		"step_count": j.StepCount, "completed_steps": j.CompletedSteps,
		"failed_steps": j.FailedSteps, "mock_ref": j.MockRef,
		"created_at": j.CreatedAt,
	})
}
