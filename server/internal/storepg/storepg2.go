package storepg

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ensembyte/observatory/internal/store"
)

// ---- failure signatures (spec 8.4 / FR-04) ----

// RecordSignature upserts a failure-signature cluster (same semantics as
// the SQLite store).
func (s *PG) RecordSignature(ctx context.Context, project, sig, kind, tool, agentVersion, sampleEvent, sampleRun string, seen time.Time, countDelta int64) error {
	now := seen.UTC().Format(time.RFC3339)
	_, err := s.pool.Exec(ctx, `
		INSERT INTO failure_signatures(
		  project_id, signature, kind, tool_name, count, run_count,
		  first_seen, last_seen, agent_versions, sample_event, sample_run)
		VALUES ($1,$2,$3,$4,1,1,$5,$6,'',$7,$8)
		ON CONFLICT(project_id, signature) DO UPDATE SET
		  count = failure_signatures.count + 1,
		  last_seen = MAX(failure_signatures.last_seen, EXCLUDED.last_seen),
		  sample_event = COALESCE(NULLIF(EXCLUDED.sample_event,''), failure_signatures.sample_event),
		  sample_run = COALESCE(NULLIF(EXCLUDED.sample_run,''), failure_signatures.sample_run)`,
		project, sig, kind, nullStr(tool), now, now,
		nullStr(sampleEvent), nullStr(sampleRun))
	if err != nil {
		return err
	}
	if agentVersion != "" {
		var versions *string
		_ = s.pool.QueryRow(ctx,
			`SELECT agent_versions FROM failure_signatures WHERE project_id=$1 AND signature=$2`,
			project, sig).Scan(&versions)
		vs := ""
		if versions != nil {
			vs = *versions
		}
		if !containsVersion(vs, agentVersion) {
			if vs == "" {
				vs = agentVersion
			} else {
				vs += "," + agentVersion
			}
			_, _ = s.pool.Exec(ctx,
				`UPDATE failure_signatures SET agent_versions = $1 WHERE project_id=$2 AND signature=$3`,
				vs, project, sig)
		}
	}
	return nil
}

// ListSignatures returns the signature clusters for a project, most active
// first (count desc, then last_seen desc).
func (s *PG) ListSignatures(ctx context.Context, project string) ([]store.Signature, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT signature, kind, COALESCE(tool_name,''), count, run_count,
		       first_seen, last_seen, COALESCE(agent_versions,''),
		       COALESCE(sample_event,''), COALESCE(sample_run,'')
		FROM failure_signatures WHERE project_id = $1
		ORDER BY count DESC, last_seen DESC LIMIT 500`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Signature
	for rows.Next() {
		var sg store.Signature
		var first, last string
		if err := rows.Scan(&sg.Signature, &sg.Kind, &sg.ToolName, &sg.Count,
			&sg.RunCount, &first, &last, &sg.AgentVersions, &sg.SampleEvent, &sg.SampleRun); err != nil {
			return nil, err
		}
		sg.Project = project
		if v, perr := time.Parse(time.RFC3339, first); perr == nil {
			sg.FirstSeen = v
		}
		if v, perr := time.Parse(time.RFC3339, last); perr == nil {
			sg.LastSeen = v
		}
		out = append(out, sg)
	}
	return out, rows.Err()
}

// ---- findings ----

// RecordFinding persists a run-scoped finding (idempotent on project+id).
func (s *PG) RecordFinding(ctx context.Context, f *store.FindingRow) error {
	if f.CreatedAt.IsZero() {
		f.CreatedAt = time.Now().UTC()
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO findings(id, project_id, run_id, signature, kind,
		                     severity, message, evidence, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT(project_id, id) DO NOTHING`,
		f.ID, f.Project, f.Run, nullStr(f.Signature), f.Kind, f.Severity,
		f.Message, f.Evidence, f.CreatedAt.UTC().Format(time.RFC3339))
	return err
}

// ListFindingsByRun returns findings for one run.
func (s *PG) ListFindingsByRun(ctx context.Context, project, run string) ([]store.FindingRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, COALESCE(signature,''), kind, severity, message, evidence, created_at
		FROM findings WHERE project_id = $1 AND run_id = $2 ORDER BY created_at`, project, run)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.FindingRow
	for rows.Next() {
		var f store.FindingRow
		var created string
		if err := rows.Scan(&f.ID, &f.Signature, &f.Kind, &f.Severity, &f.Message,
			&f.Evidence, &created); err != nil {
			return nil, err
		}
		f.Project, f.Run = project, run
		f.CreatedAt, _ = time.Parse(time.RFC3339, created)
		out = append(out, f)
	}
	return out, rows.Err()
}

// FindingsBySignature returns the distinct runs for a signature.
func (s *PG) FindingsBySignature(ctx context.Context, project, signature string) ([]store.FindingRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, COALESCE(signature,''), kind, severity, message, evidence, created_at, run_id
		FROM findings WHERE project_id = $1 AND signature = $2 ORDER BY created_at
		LIMIT 200`, project, signature)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.FindingRow
	for rows.Next() {
		var f store.FindingRow
		var created string
		if err := rows.Scan(&f.ID, &f.Signature, &f.Kind, &f.Severity, &f.Message,
			&f.Evidence, &created, &f.Run); err != nil {
			return nil, err
		}
		f.Project = project
		f.CreatedAt, _ = time.Parse(time.RFC3339, created)
		out = append(out, f)
	}
	return out, rows.Err()
}

// ---- datasets / cases / evaluators ----

// UpsertDataset upserts a dataset and its case count.
func (s *PG) UpsertDataset(ctx context.Context, d *store.Dataset) error {
	if d.CreatedAt.IsZero() {
		d.CreatedAt = time.Now().UTC()
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO datasets(id, project_id, name, version, description, case_count, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT(project_id, id, version) DO UPDATE SET
		  name = EXCLUDED.name,
		  description = COALESCE(NULLIF(EXCLUDED.description,''), datasets.description)`,
		d.ID, d.Project, d.Name, d.Version, nullStr(d.Description), d.CaseCount,
		d.CreatedAt.UTC().Format(time.RFC3339))
	return err
}

// UpsertCase adds or updates a single case.
func (s *PG) UpsertCase(ctx context.Context, project, caseID, datasetID, task, expectedTool string, expectCompletion bool, rubric string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO dataset_cases(id, project_id, dataset_id, task, expected_tool,
		                           expect_completion, rubric)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT(project_id, id) DO UPDATE SET
		  task = EXCLUDED.task,
		  expected_tool = COALESCE(NULLIF(EXCLUDED.expected_tool,''), dataset_cases.expected_tool),
		  expect_completion = EXCLUDED.expect_completion,
		  rubric = EXCLUDED.rubric`,
		caseID, project, nullStr(datasetID), task, nullStr(expectedTool),
		boolInt(expectCompletion), nullStr(rubric))
	return err
}

// ListCases returns the cases for a dataset.
func (s *PG) ListCases(ctx context.Context, project, datasetID string) ([]map[string]any, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, task, COALESCE(expected_tool,''), expect_completion,
		       COALESCE(rubric,'{}')
		FROM dataset_cases WHERE project_id = $1 AND dataset_id = $2
		ORDER BY id`, project, datasetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var id, task, expectedTool, rubric string
		var exp int64
		if err := rows.Scan(&id, &task, &expectedTool, &exp, &rubric); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{
			"id": id, "task": task, "expected_tool": expectedTool,
			"expect_completion": exp != 0, "rubric": rubric,
		})
	}
	return out, rows.Err()
}

// ListDatasets returns a project's datasets with case counts.
func (s *PG) ListDatasets(ctx context.Context, project string) ([]store.Dataset, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, name, version, COALESCE(description,''), case_count, created_at
		FROM datasets WHERE project_id = $1 ORDER BY name, version DESC`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Dataset
	for rows.Next() {
		var d store.Dataset
		var created string
		if err := rows.Scan(&d.ID, &d.Name, &d.Version, &d.Description,
			&d.CaseCount, &created); err != nil {
			return nil, err
		}
		d.Project = project
		d.CreatedAt, _ = time.Parse(time.RFC3339, created)
		out = append(out, d)
	}
	return out, rows.Err()
}

// UpsertEvaluator stores a versioned evaluator.
func (s *PG) UpsertEvaluator(ctx context.Context, e *store.Evaluator) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO evaluators(id, project_id, name, version, kind, prompt, calibrated)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT(project_id, id, version) DO UPDATE SET
		  name = EXCLUDED.name, kind = EXCLUDED.kind,
		  prompt = COALESCE(NULLIF(EXCLUDED.prompt,''), evaluators.prompt),
		  calibrated = EXCLUDED.calibrated`,
		e.ID, e.Project, e.Name, e.Version, e.Kind, nullStr(e.Prompt), boolInt(e.Calibrated))
	return err
}

// ---- evaluation runs / results ----

// CreateEvalRun inserts a running evaluation run.
func (s *PG) CreateEvalRun(ctx context.Context, r *store.EvalRun) error {
	if r.Status == "" {
		r.Status = "running"
	}
	if r.StartedAt.IsZero() {
		r.StartedAt = time.Now().UTC()
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO eval_runs(
		  id, project_id, dataset_id, dataset_version, agent_name, agent_version,
		  model, evaluator_id, evaluator_version, status,
		  total_cases, passed, avg_score, score_source, gate_failed, gate_json,
		  started_at, finished_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,0,0,$12,0,$13,$14,NULL)
		ON CONFLICT(project_id, id) DO NOTHING`,
		r.ID, r.Project, nullStr(r.DatasetID), nullStr(r.DatasetVersion),
		r.AgentName, r.AgentVersion, nullStr(r.Model), nullStr(r.EvaluatorID),
		nullStr(r.EvaluatorVersion), "running", r.TotalCases,
		nullStr(r.ScoreSource), nullStr(r.GateJSON),
		r.StartedAt.UTC().Format(time.RFC3339))
	return err
}

// FinishEvalRun closes a run with results and a CI gate verdict.
func (s *PG) FinishEvalRun(ctx context.Context, r *store.EvalRun) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.pool.Exec(ctx, `
		UPDATE eval_runs SET status = $1, passed = $2, avg_score = $3, score_source = $4,
		  gate_failed = $5, gate_json = $6, finished_at = $7
		WHERE project_id = $8 AND id = $9`,
		r.Status, r.Passed, r.AvgScore, nullStr(r.ScoreSource), boolInt(r.GateFailed),
		nullStr(r.GateJSON), now, r.Project, r.ID)
	return err
}

// GetEvalRun fetches a single run.
func (s *PG) GetEvalRun(ctx context.Context, project, id string) (*store.EvalRun, error) {
	var r store.EvalRun
	var started string
	var finished *string
	var total, passed int64
	var avg float64
	var gateFailed int64
	err := s.pool.QueryRow(ctx, `
		SELECT dataset_id, dataset_version, agent_name, agent_version, model,
		       evaluator_id, evaluator_version, status,
		       total_cases, passed, avg_score, score_source, gate_failed,
		       COALESCE(gate_json,''), started_at, finished_at
		FROM eval_runs WHERE project_id = $1 AND id = $2`, project, id).
		Scan(&r.DatasetID, &r.DatasetVersion, &r.AgentName, &r.AgentVersion,
			&r.Model, &r.EvaluatorID, &r.EvaluatorVersion, &r.Status,
			&total, &passed, &avg, &r.ScoreSource, &gateFailed, &r.GateJSON,
			&started, &finished)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	r.ID, r.Project = id, project
	r.TotalCases, r.Passed, r.AvgScore, r.GateFailed = int(total), int(passed), avg, gateFailed != 0
	if v, perr := time.Parse(time.RFC3339, started); perr == nil {
		r.StartedAt = v
	}
	if finished != nil {
		if v, perr := time.Parse(time.RFC3339, *finished); perr == nil {
			r.FinishedAt = &v
		}
	}
	return &r, nil
}

// ListEvalRuns lists runs for a dataset (used by the comparison view).
func (s *PG) ListEvalRuns(ctx context.Context, project, datasetID string) ([]store.EvalRun, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, dataset_id, dataset_version, agent_name, agent_version, model,
		       evaluator_id, evaluator_version, status, total_cases, passed,
		       avg_score, score_source, gate_failed, COALESCE(gate_json,''), started_at
		FROM eval_runs WHERE project_id = $1 AND dataset_id = $2
		ORDER BY started_at DESC LIMIT 200`, project, datasetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.EvalRun
	for rows.Next() {
		var r store.EvalRun
		var started string
		var total, passed, gateFailed int64
		var avg float64
		if err := rows.Scan(&r.ID, &r.DatasetID, &r.DatasetVersion, &r.AgentName,
			&r.AgentVersion, &r.Model, &r.EvaluatorID, &r.EvaluatorVersion, &r.Status,
			&total, &passed, &avg, &r.ScoreSource, &gateFailed, &r.GateJSON,
			&started); err != nil {
			return nil, err
		}
		r.Project = project
		r.TotalCases, r.Passed, r.AvgScore, r.GateFailed = int(total), int(passed), avg, gateFailed != 0
		if v, perr := time.Parse(time.RFC3339, started); perr == nil {
			r.StartedAt = v
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// InsertEvalResult records a per-case outcome.
func (s *PG) InsertEvalResult(ctx context.Context, project, runID, caseID string, passed bool, score float64, status, detail, originRun string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO eval_results(run_id, project_id, case_id, passed, score, status,
		                         detail, origin_run)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT(project_id, run_id, case_id) DO UPDATE SET
		  passed = EXCLUDED.passed, score = EXCLUDED.score, status = EXCLUDED.status,
		  detail = EXCLUDED.detail, origin_run = EXCLUDED.origin_run`,
		runID, project, caseID, boolInt(passed), score, nullStr(status), nullStr(detail),
		nullStr(originRun))
	return err
}

// ListEvalResults returns per-case results for a run.
func (s *PG) ListEvalResults(ctx context.Context, project, runID string) ([]store.EvalResult, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT case_id, passed, score, COALESCE(status,''), COALESCE(detail,''), COALESCE(origin_run,'')
		FROM eval_results WHERE project_id = $1 AND run_id = $2 ORDER BY case_id`,
		project, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.EvalResult
	for rows.Next() {
		var r store.EvalResult
		var passed int64
		if err := rows.Scan(&r.CaseID, &passed, &r.Score, &r.Status, &r.Detail,
			&r.OriginRun); err != nil {
			return nil, err
		}
		r.Passed = passed != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

// ---- replay ----

// CreateReplayJob inserts a replay job.
func (s *PG) CreateReplayJob(ctx context.Context, j *store.ReplayJob) error {
	if j.Status == "" {
		j.Status = "running"
	}
	if j.Mode == "" {
		j.Mode = "mocked"
	}
	if j.CreatedAt.IsZero() {
		j.CreatedAt = time.Now().UTC()
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO replay_jobs(
		  id, project_id, origin_run, replay_run, status, mode,
		  step_count, completed_steps, failed_steps, mock_ref, created_at, finished_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,NULL)
		ON CONFLICT(project_id, id) DO NOTHING`,
		j.ID, j.Project, j.OriginRun, nullStr(j.ReplayRun), j.Status, j.Mode,
		j.StepCount, j.CompletedSteps, j.FailedSteps, nullStr(j.MockRef),
		j.CreatedAt.UTC().Format(time.RFC3339))
	return err
}

// FinishReplayJob closes a replay with step tallies.
func (s *PG) FinishReplayJob(ctx context.Context, project, id string, completed, failed int, status string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.pool.Exec(ctx, `
		UPDATE replay_jobs SET status = $1, completed_steps = $2, failed_steps = $3,
		  finished_at = $4 WHERE project_id = $5 AND id = $6`,
		status, completed, failed, now, project, id)
	return err
}

// GetReplayJob fetches a replay job.
func (s *PG) GetReplayJob(ctx context.Context, project, id string) (*store.ReplayJob, error) {
	var j store.ReplayJob
	var created string
	var finished *string
	err := s.pool.QueryRow(ctx, `
		SELECT origin_run, replay_run, status, mode, step_count, completed_steps,
		       failed_steps, COALESCE(mock_ref,''), created_at, finished_at
		FROM replay_jobs WHERE project_id = $1 AND id = $2`, project, id).
		Scan(&j.OriginRun, &j.ReplayRun, &j.Status, &j.Mode, &j.StepCount,
			&j.CompletedSteps, &j.FailedSteps, &j.MockRef, &created, &finished)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	j.ID, j.Project = id, project
	if v, perr := time.Parse(time.RFC3339, created); perr == nil {
		j.CreatedAt = v
	}
	if finished != nil {
		if v, perr := time.Parse(time.RFC3339, *finished); perr == nil {
			j.FinishedAt = &v
		}
	}
	return &j, nil
}

// ListReplayJobs lists replays for a project.
func (s *PG) ListReplayJobs(ctx context.Context, project string) ([]store.ReplayJob, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, origin_run, replay_run, status, mode, step_count, completed_steps,
		       failed_steps, COALESCE(mock_ref,''), created_at
		FROM replay_jobs WHERE project_id = $1 ORDER BY created_at DESC LIMIT 200`,
		project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.ReplayJob
	for rows.Next() {
		var j store.ReplayJob
		var created string
		if err := rows.Scan(&j.ID, &j.OriginRun, &j.ReplayRun, &j.Status, &j.Mode,
			&j.StepCount, &j.CompletedSteps, &j.FailedSteps, &j.MockRef, &created); err != nil {
			return nil, err
		}
		j.Project = project
		if v, perr := time.Parse(time.RFC3339, created); perr == nil {
			j.CreatedAt = v
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// ---- alerts ----

// UpsertAlertRule stores an alert rule.
func (s *PG) UpsertAlertRule(ctx context.Context, r *store.AlertRule) error {
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now().UTC()
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO alert_rules(
		  id, project_id, name, detector, window_min, threshold, agent, severity,
		  webhook_url, enabled, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		ON CONFLICT(project_id, id) DO UPDATE SET
		  name = EXCLUDED.name, detector = EXCLUDED.detector,
		  window_min = EXCLUDED.window_min, threshold = EXCLUDED.threshold,
		  agent = COALESCE(NULLIF(EXCLUDED.agent,''), alert_rules.agent),
		  severity = EXCLUDED.severity,
		  webhook_url = COALESCE(NULLIF(EXCLUDED.webhook_url,''), alert_rules.webhook_url),
		  enabled = EXCLUDED.enabled`,
		r.ID, r.Project, r.Name, r.Detector, r.WindowMin, r.Threshold,
		nullStr(r.Agent), nullStr(r.Severity), nullStr(r.WebhookURL),
		boolInt(r.Enabled), r.CreatedAt.UTC().Format(time.RFC3339))
	return err
}

// ListAlertRules returns enabled rules for a project.
func (s *PG) ListAlertRules(ctx context.Context, project string) ([]store.AlertRule, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, name, detector, window_min, threshold, COALESCE(agent,''),
		       COALESCE(severity,'warning'), COALESCE(webhook_url,''), enabled,
		       created_at
		FROM alert_rules WHERE project_id = $1 AND enabled = 1 ORDER BY id`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.AlertRule
	for rows.Next() {
		var r store.AlertRule
		var enabled int64
		var created string
		if err := rows.Scan(&r.ID, &r.Name, &r.Detector, &r.WindowMin,
			&r.Threshold, &r.Agent, &r.Severity, &r.WebhookURL, &enabled, &created); err != nil {
			return nil, err
		}
		r.Project = project
		r.Enabled = enabled != 0
		if v, perr := time.Parse(time.RFC3339, created); perr == nil {
			r.CreatedAt = v
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RecordAlertEvaluation stores a rule evaluation and its delivery result.
func (s *PG) RecordAlertEvaluation(ctx context.Context, e *store.AlertEvaluation) error {
	if e.EvaluatedAt.IsZero() {
		e.EvaluatedAt = time.Now().UTC()
	}
	if e.DeliveryStatus == "" {
		e.DeliveryStatus = "none"
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO alert_evaluations(
		  project_id, rule_id, window_start, metric, value, triggered,
		  delivery_status, delivery_error, evaluated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT(project_id, rule_id, window_start) DO UPDATE SET
		  value = EXCLUDED.value, triggered = EXCLUDED.triggered,
		  delivery_status = EXCLUDED.delivery_status,
		  delivery_error = EXCLUDED.delivery_error,
		  evaluated_at = EXCLUDED.evaluated_at`,
		e.Project, e.RuleID, e.WindowStart.UTC().Format(time.RFC3339), e.Metric,
		e.Value, boolInt(e.Triggered), e.DeliveryStatus, nullStr(e.DeliveryError),
		e.EvaluatedAt.UTC().Format(time.RFC3339))
	return err
}

// ---- audit ----

// LogAudit records a payload-access or policy action.
func (s *PG) LogAudit(ctx context.Context, project, action, actor, detail string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO audit_log(project_id, action, actor, detail, ts)
		VALUES ($1,$2,$3,$4,$5)`,
		project, action, actor, nullStr(detail), time.Now().UTC().Format(time.RFC3339))
	return err
}

// ---- alert metrics (store.AlertMetrics) ----

// AlertFindingKind counts findings of a kind since a window start.
func (s *PG) AlertFindingKind(ctx context.Context, project, kind string, since time.Time) int64 {
	var n int64
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(COUNT(*),0) FROM findings
		WHERE project_id = $1 AND kind = $2 AND created_at >= $3`,
		project, kind, since.UTC().Format(time.RFC3339)).Scan(&n)
	if err != nil {
		return 0
	}
	return n
}

// AlertRunStatusCounts reports open and failed runs since a window start.
func (s *PG) AlertRunStatusCounts(ctx context.Context, project string, since time.Time) (total, failed int64) {
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(CASE WHEN status='open' THEN 1 ELSE 0 END),0),
		       COALESCE(SUM(CASE WHEN status='failed' THEN 1 ELSE 0 END),0)
		FROM runs WHERE project_id = $1 AND started_at >= $2`,
		project, since.UTC().Format(time.RFC3339)).Scan(&total, &failed)
	if err != nil {
		return 0, 0
	}
	return total, failed
}

// AlertLatencyRegressions counts runs whose duration is more than 2x the
// average over the same window.
func (s *PG) AlertLatencyRegressions(ctx context.Context, project string, since time.Time) int64 {
	var n int64
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(CASE WHEN duration_ms > 2 * avg_ms THEN 1 ELSE 0 END),0)
		FROM (
			SELECT duration_ms, AVG(duration_ms) OVER () AS avg_ms
			FROM runs WHERE project_id = $1 AND started_at >= $2 AND duration_ms > 0
		)`, project, since.UTC().Format(time.RFC3339)).Scan(&n)
	if err != nil {
		return 0
	}
	return n
}

// containsVersion reports whether the comma-separated version set contains v.
// Local copy of the store package's helper (unexported there) so the
// Postgres adapter is self-contained.
func containsVersion(set, v string) bool {
	if v == "" || set == "" {
		return false
	}
	for _, x := range strings.Split(set, ",") {
		if x == v {
			return true
		}
	}
	return false
}
