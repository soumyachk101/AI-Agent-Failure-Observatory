package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// UpsertSpan writes or updates a span aggregate. Callers build the partial
// value and upsert semantics fill only non-empty fields.
func (s *Store) UpsertSpan(ctx context.Context, sp *SpanRow) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO spans(
		  id, project_id, run_id, parent_span_id, name, kind, status,
		  agent_name, agent_version, tool_name, tool_version, tool_args_hash,
		  tool_schema_version, model, provider, usage_source,
		  tokens_in, tokens_out, cost_usd, cost_estimated, retries, error_class,
		  started_at, ended_at, duration_ms, seq_start, seq_end)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id, project_id) DO UPDATE SET
		  run_id = excluded.run_id,
		  parent_span_id = COALESCE(NULLIF(excluded.parent_span_id,''), spans.parent_span_id),
		  name = COALESCE(NULLIF(excluded.name,''), spans.name),
		  kind = COALESCE(NULLIF(excluded.kind,''), spans.kind),
		  status = COALESCE(NULLIF(excluded.status,''), spans.status),
		  agent_name = COALESCE(NULLIF(excluded.agent_name,''), spans.agent_name),
		  agent_version = COALESCE(NULLIF(excluded.agent_version,''), spans.agent_version),
		  tool_name = COALESCE(NULLIF(excluded.tool_name,''), spans.tool_name),
		  tool_version = COALESCE(NULLIF(excluded.tool_version,''), spans.tool_version),
		  tool_args_hash = COALESCE(NULLIF(excluded.tool_args_hash,''), spans.tool_args_hash),
		  tool_schema_version = COALESCE(NULLIF(excluded.tool_schema_version,''), spans.tool_schema_version),
		  model = COALESCE(NULLIF(excluded.model,''), spans.model),
		  provider = COALESCE(NULLIF(excluded.provider,''), spans.provider),
		  usage_source = COALESCE(NULLIF(excluded.usage_source,''), spans.usage_source),
		  tokens_in = MAX(spans.tokens_in, excluded.tokens_in),
		  tokens_out = MAX(spans.tokens_out, excluded.tokens_out),
		  cost_usd = MAX(spans.cost_usd, excluded.cost_usd),
		  cost_estimated = MAX(spans.cost_estimated, excluded.cost_estimated),
		  retries = MAX(spans.retries, excluded.retries),
		  error_class = COALESCE(NULLIF(excluded.error_class,''), spans.error_class),
		  started_at = COALESCE(NULLIF(excluded.started_at,''), spans.started_at),
		  ended_at = COALESCE(NULLIF(excluded.ended_at,''), spans.ended_at),
		  duration_ms = MAX(spans.duration_ms, excluded.duration_ms),
		  seq_start = CASE WHEN spans.seq_start = 0 THEN excluded.seq_start
		                  ELSE MIN(spans.seq_start, excluded.seq_start) END,
		  seq_end = MAX(spans.seq_end, excluded.seq_end)`,
		sp.ID, sp.Project, sp.RunID, nullStr(sp.Parent), nullStr(sp.Name), nullStr(sp.Kind),
		nullStr(sp.Status), nullStr(sp.AgentName), nullStr(sp.AgentVersion),
		nullStr(sp.ToolName), nullStr(sp.ToolVersion), nullStr(sp.ToolArgsHash),
		nullStr(sp.ToolSchemaVer), nullStr(sp.Model), nullStr(sp.Provider),
		nullStr(sp.UsageSource), sp.TokensIn, sp.TokensOut, sp.CostUSD,
		bi(sp.CostEstimated), sp.Retries, nullStr(sp.ErrorClass),
		spanTS(sp.StartedAt), spanTS(sp.EndedAt), sp.DurationMS, sp.SeqStart, sp.SeqEnd)
	return err
}

// bi converts a bool to an int64 for SQLite.
func bi(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// spanTS formats a time for storage (empty for zero).
func spanTS(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// ListRuns pages the run index for a project, most recent first.
func (s *Store) ListRuns(ctx context.Context, project, status, agentName, agentVersion, model, environment string, limit, offset int) ([]*RunAggregate, error) {
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT id, status FROM runs WHERE project_id = ?`
	args := []any{project}
	if status != "" {
		q += ` AND status = ?`
		args = append(args, status)
	}
	if agentName != "" {
		q += ` AND agent_name = ?`
		args = append(args, agentName)
	}
	if agentVersion != "" {
		q += ` AND agent_version = ?`
		args = append(args, agentVersion)
	}
	if model != "" {
		q += ` AND model = ?`
		args = append(args, model)
	}
	if environment != "" {
		q += ` AND environment = ?`
		args = append(args, environment)
	}
	q += ` ORDER BY started_at DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*RunAggregate
	for rows.Next() {
		var id, status string
		if err := rows.Scan(&id, &status); err != nil {
			return nil, err
		}
		if r, err := s.GetRun(ctx, project, id); err == nil && r != nil {
			out = append(out, r)
		}
	}
	return out, rows.Err()
}

// ---- failure signatures (spec 8.4 / FR-04) ----

// RecordSignature upserts a failure-signature cluster. agentVersion is folded
// into the agent_versions set for cross-version attribution.
func (s *Store) RecordSignature(ctx context.Context, project, sig, kind, tool, agentVersion, sampleEvent, sampleRun string, seen time.Time, countDelta int64) error {
	now := seen.UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO failure_signatures(
		  project_id, signature, kind, tool_name, count, run_count,
		  first_seen, last_seen, agent_versions, sample_event, sample_run)
		VALUES (?,?,?,?,1,1,?,?,'',?,?)
		ON CONFLICT(project_id, signature) DO UPDATE SET
		  count = failure_signatures.count + 1,
		  last_seen = MAX(failure_signatures.last_seen, excluded.last_seen),
		  sample_event = COALESCE(NULLIF(excluded.sample_event,''), failure_signatures.sample_event),
		  sample_run = COALESCE(NULLIF(excluded.sample_run,''), failure_signatures.sample_run)`,
		//  ? project_id, ? signature, ? kind, ? tool_name,
		//  1 count, 1 run_count,
		//  ? first_seen, ? last_seen, '' agent_versions,
		//  ? sample_event, ? sample_run
		project, sig, kind, nullStr(tool), now, now,
		nullStr(sampleEvent), nullStr(sampleRun))
	if err != nil {
		return err
	}
	if agentVersion != "" {
		var versions string
		_ = s.db.QueryRowContext(ctx,
			`SELECT agent_versions FROM failure_signatures WHERE project_id=? AND signature=?`,
			project, sig).Scan(&versions)
		if !containsVersion(versions, agentVersion) {
			if versions == "" {
				versions = agentVersion
			} else {
				versions += "," + agentVersion
			}
			_, _ = s.db.ExecContext(ctx,
				`UPDATE failure_signatures SET agent_versions = ? WHERE project_id=? AND signature=?`,
				versions, project, sig)
		}
	}
	return nil
}

func containsVersion(set, v string) bool {
	if v == "" || set == "" {
		return false
	}
	for _, x := range splitComma(set) {
		if x == v {
			return true
		}
	}
	return false
}

func splitComma(s string) []string {
	var out []string
	for _, p := range splitRunes(s) {
		out = append(out, p)
	}
	return out
}

func splitRunes(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == ',' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

// ListSignatures returns the signature clusters for a project, most active
// first (count desc, then last_seen desc).
func (s *Store) ListSignatures(ctx context.Context, project string) ([]Signature, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT signature, kind, COALESCE(tool_name,''), count, run_count,
		       first_seen, last_seen, COALESCE(agent_versions,''),
		       COALESCE(sample_event,''), COALESCE(sample_run,'')
		FROM failure_signatures WHERE project_id = ?
		ORDER BY count DESC, last_seen DESC LIMIT 500`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Signature
	for rows.Next() {
		var sg Signature
		var first, last string
		if err := rows.Scan(&sg.Signature, &sg.Kind, &sg.ToolName, &sg.Count,
			&sg.RunCount, &first, &last, &sg.AgentVersions, &sg.SampleEvent, &sg.SampleRun); err != nil {
			return nil, err
		}
		sg.Project = project
		if v, _ := time.Parse(time.RFC3339, first); !v.IsZero() {
			sg.FirstSeen = v
		}
		if v, _ := time.Parse(time.RFC3339, last); !v.IsZero() {
			sg.LastSeen = v
		}
		out = append(out, sg)
	}
	return out, rows.Err()
}

// RecordFinding persists a run-scoped finding (evidence-linked diagnosis).
func (s *Store) RecordFinding(ctx context.Context, f *FindingRow) error {
	if f.CreatedAt.IsZero() {
		f.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO findings(id, project_id, run_id, signature, kind,
		                               severity, message, evidence, created_at)
		VALUES (?,?,?,?,?,?,?,?,?)`,
		f.ID, f.Project, f.Run, nullStr(f.Signature), f.Kind, f.Severity,
		f.Message, f.Evidence, f.CreatedAt.UTC().Format(time.RFC3339))
	return err
}

// FindingRow is a persisted finding.
type FindingRow struct {
	ID        string
	Project   string
	Run       string
	Signature string
	Kind      string
	Severity  string
	Message   string
	Evidence  string
	CreatedAt time.Time
}

// ListFindingsByRun returns findings for one run.
func (s *Store) ListFindingsByRun(ctx context.Context, project, run string) ([]FindingRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, signature, kind, severity, message, evidence, created_at
		FROM findings WHERE project_id = ? AND run_id = ? ORDER BY created_at`, project, run)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FindingRow
	for rows.Next() {
		var f FindingRow
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
func (s *Store) FindingsBySignature(ctx context.Context, project, signature string) ([]FindingRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, signature, kind, severity, message, evidence, created_at, run_id
		FROM findings WHERE project_id = ? AND signature = ? ORDER BY created_at
		LIMIT 200`, project, signature)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FindingRow
	for rows.Next() {
		var f FindingRow
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
func (s *Store) UpsertDataset(ctx context.Context, d *Dataset) error {
	if d.CreatedAt.IsZero() {
		d.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO datasets(id, project_id, name, version, description, case_count, created_at)
		VALUES (?,?,?,?,?,?,?)
		ON CONFLICT(project_id, id, version) DO UPDATE SET
		  name = excluded.name, description = COALESCE(NULLIF(excluded.description,''), datasets.description)`,
		d.ID, d.Project, d.Name, d.Version, nullStr(d.Description), d.CaseCount,
		d.CreatedAt.UTC().Format(time.RFC3339))
	return err
}

// Dataset is the metadata for a versioned dataset.
type Dataset struct {
	ID          string
	Project     string
	Name        string
	Version     string
	Description string
	CaseCount   int
	CreatedAt   time.Time
}

// UpsertCase adds or updates a single case.
func (s *Store) UpsertCase(ctx context.Context, project, caseID, datasetID, task, expectedTool string, expectCompletion bool, rubric string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO dataset_cases(id, project_id, dataset_id, task, expected_tool,
		                          expect_completion, rubric)
		VALUES (?,?,?,?,?,?,?)
		ON CONFLICT(project_id, id) DO UPDATE SET
		  task = excluded.task,
		  expected_tool = COALESCE(NULLIF(excluded.expected_tool,''), dataset_cases.expected_tool),
		  expect_completion = excluded.expect_completion,
		  rubric = excluded.rubric`,
		caseID, project, nullStr(datasetID), task, nullStr(expectedTool),
		bi(expectCompletion), nullStr(rubric))
	return err
}

// ListCases returns the cases for a dataset.
func (s *Store) ListCases(ctx context.Context, project, datasetID string) ([]map[string]any, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, task, COALESCE(expected_tool,''), expect_completion,
		       COALESCE(rubric,'{}')
		FROM dataset_cases WHERE project_id = ? AND dataset_id = ?
		ORDER BY id`, project, datasetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var id, task, expectedTool, rubric string
		var exp int
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
func (s *Store) ListDatasets(ctx context.Context, project string) ([]Dataset, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, version, COALESCE(description,''), case_count, created_at
		FROM datasets WHERE project_id = ? ORDER BY name, version DESC`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Dataset
	for rows.Next() {
		var d Dataset
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
func (s *Store) UpsertEvaluator(ctx context.Context, e *Evaluator) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO evaluators(id, project_id, name, version, kind, prompt, calibrated)
		VALUES (?,?,?,?,?,?,?)
		ON CONFLICT(project_id, id, version) DO UPDATE SET
		  name = excluded.name, kind = excluded.kind,
		  prompt = COALESCE(NULLIF(excluded.prompt,''), evaluators.prompt),
		  calibrated = excluded.calibrated`,
		e.ID, e.Project, e.Name, e.Version, e.Kind, nullStr(e.Prompt), bi(e.Calibrated))
	return err
}

// Evaluator is a versioned evaluator definition.
type Evaluator struct {
	ID         string
	Project    string
	Name       string
	Version    string
	Kind       string
	Prompt     string
	Calibrated bool
}

// ---- evaluation runs / results ----

// CreateEvalRun inserts a running evaluation run.
func (s *Store) CreateEvalRun(ctx context.Context, r *EvalRun) error {
	if r.Status == "" {
		r.Status = "running"
	}
	if r.StartedAt.IsZero() {
		r.StartedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO eval_runs(
		  id, project_id, dataset_id, dataset_version, agent_name, agent_version,
		  model, evaluator_id, evaluator_version, status,
		  total_cases, passed, avg_score, score_source, gate_failed, gate_json,
		  started_at, finished_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,NULL)`,
		r.ID, r.Project, nullStr(r.DatasetID), nullStr(r.DatasetVersion),
		r.AgentName, r.AgentVersion, nullStr(r.Model), nullStr(r.EvaluatorID),
		nullStr(r.EvaluatorVersion), "running", r.TotalCases, 0, 0,
		nullStr(r.ScoreSource), 0, nullStr(r.GateJSON),
		r.StartedAt.UTC().Format(time.RFC3339))
	return err
}

// EvalRun is an evaluation execution record.
type EvalRun struct {
	ID               string
	Project          string
	DatasetID        string
	DatasetVersion   string
	AgentName        string
	AgentVersion     string
	Model            string
	EvaluatorID      string
	EvaluatorVersion string
	Status           string
	TotalCases       int
	Passed           int
	AvgScore         float64
	ScoreSource      string
	GateFailed       bool
	GateJSON         string
	StartedAt        time.Time
	FinishedAt       *time.Time
}

// FinishEvalRun closes a run with results and a CI gate verdict.
func (s *Store) FinishEvalRun(ctx context.Context, r *EvalRun) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx, `
		UPDATE eval_runs SET status = ?, passed = ?, avg_score = ?, score_source = ?,
		  gate_failed = ?, gate_json = ?, finished_at = ?
		WHERE project_id = ? AND id = ?`,
		r.Status, r.Passed, r.AvgScore, nullStr(r.ScoreSource), bi(r.GateFailed),
		nullStr(r.GateJSON), now, r.Project, r.ID)
	return err
}

// GetEvalRun fetches a single run.
func (s *Store) GetEvalRun(ctx context.Context, project, id string) (*EvalRun, error) {
	var r EvalRun
	var started string
	var finished sql.NullString
	var total, passed int
	var avg float64
	var gateFailed int
	err := s.db.QueryRowContext(ctx, `
		SELECT dataset_id, dataset_version, agent_name, agent_version, model,
		       evaluator_id, evaluator_version, status,
		       total_cases, passed, avg_score, score_source, gate_failed,
		       COALESCE(gate_json,''), started_at, finished_at
		FROM eval_runs WHERE project_id = ? AND id = ?`, project, id).
		Scan(&r.DatasetID, &r.DatasetVersion, &r.AgentName, &r.AgentVersion,
			&r.Model, &r.EvaluatorID, &r.EvaluatorVersion, &r.Status,
			&total, &passed, &avg, &r.ScoreSource, &gateFailed, &r.GateJSON,
			&started, &finished)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r.ID, r.Project = id, project
	r.TotalCases, r.Passed, r.AvgScore, r.GateFailed = total, passed, avg, gateFailed != 0
	if v, _ := time.Parse(time.RFC3339, started); !v.IsZero() {
		r.StartedAt = v
	}
	if finished.Valid {
		if v, _ := time.Parse(time.RFC3339, finished.String); !v.IsZero() {
			r.FinishedAt = &v
		}
	}
	return &r, nil
}

// ListEvalRuns lists runs for a dataset (used by the comparison view).
func (s *Store) ListEvalRuns(ctx context.Context, project, datasetID string) ([]EvalRun, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, dataset_id, dataset_version, agent_name, agent_version, model,
		       evaluator_id, evaluator_version, status, total_cases, passed,
		       avg_score, score_source, gate_failed, COALESCE(gate_json,''), started_at
		FROM eval_runs WHERE project_id = ? AND dataset_id = ?
		ORDER BY started_at DESC LIMIT 200`, project, datasetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EvalRun
	for rows.Next() {
		var r EvalRun
		var started string
		var gateFailed int
		if err := rows.Scan(&r.ID, &r.DatasetID, &r.DatasetVersion, &r.AgentName,
			&r.AgentVersion, &r.Model, &r.EvaluatorID, &r.EvaluatorVersion, &r.Status,
			&r.TotalCases, &r.Passed, &r.AvgScore, &r.ScoreSource, &gateFailed,
			&r.GateJSON, &started); err != nil {
			return nil, err
		}
		r.Project = project
		r.GateFailed = gateFailed != 0
		if v, _ := time.Parse(time.RFC3339, started); !v.IsZero() {
			r.StartedAt = v
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// InsertEvalResult records a per-case outcome.
func (s *Store) InsertEvalResult(ctx context.Context, project, runID, caseID string, passed bool, score float64, status, detail, originRun string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO eval_results(run_id, project_id, case_id, passed, score, status,
		                         detail, origin_run)
		VALUES (?,?,?,?,?,?,?,?)`,
		runID, project, caseID, bi(passed), score, nullStr(status), nullStr(detail),
		nullStr(originRun))
	return err
}

// EvalResult is a per-case outcome.
type EvalResult struct {
	CaseID  string
	Passed  bool
	Score   float64
	Status  string
	Detail  string
	OriginRun string
}

// ListEvalResults returns per-case results for a run.
func (s *Store) ListEvalResults(ctx context.Context, project, runID string) ([]EvalResult, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT case_id, passed, score, status, detail, origin_run
		FROM eval_results WHERE project_id = ? AND run_id = ? ORDER BY case_id`,
		project, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EvalResult
	for rows.Next() {
		var r EvalResult
		var passed int
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

// CreateReplayJob inserts a replay job. mode MUST be "mocked" unless
// explicitly configured live; the API layer enforces that.
func (s *Store) CreateReplayJob(ctx context.Context, j *ReplayJob) error {
	if j.Status == "" {
		j.Status = "running"
	}
	if j.Mode == "" {
		j.Mode = "mocked"
	}
	if j.CreatedAt.IsZero() {
		j.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO replay_jobs(
		  id, project_id, origin_run, replay_run, status, mode,
		  step_count, completed_steps, failed_steps, mock_ref, created_at, finished_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		j.ID, j.Project, j.OriginRun, nullStr(j.ReplayRun), j.Status, j.Mode,
		j.StepCount, j.CompletedSteps, j.FailedSteps, nullStr(j.MockRef),
		j.CreatedAt.UTC().Format(time.RFC3339), nil)
	return err
}

// ReplayJob is a replay execution record.
type ReplayJob struct {
	ID             string
	Project        string
	OriginRun      string
	ReplayRun      string
	Status         string
	Mode           string
	StepCount      int
	CompletedSteps int
	FailedSteps    int
	MockRef        string
	CreatedAt      time.Time
	FinishedAt     *time.Time
}

// FinishReplayJob closes a replay with step tallies.
func (s *Store) FinishReplayJob(ctx context.Context, project, id string, completed, failed int, status string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx, `
		UPDATE replay_jobs SET status = ?, completed_steps = ?, failed_steps = ?,
		  finished_at = ? WHERE project_id = ? AND id = ?`,
		status, completed, failed, now, project, id)
	return err
}

// GetReplayJob fetches a replay job.
func (s *Store) GetReplayJob(ctx context.Context, project, id string) (*ReplayJob, error) {
	var j ReplayJob
	var created string
	var finished sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT origin_run, replay_run, status, mode, step_count, completed_steps,
		       failed_steps, COALESCE(mock_ref,''), created_at, finished_at
		FROM replay_jobs WHERE project_id = ? AND id = ?`, project, id).
		Scan(&j.OriginRun, &j.ReplayRun, &j.Status, &j.Mode, &j.StepCount,
			&j.CompletedSteps, &j.FailedSteps, &j.MockRef, &created, &finished)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	j.ID, j.Project = id, project
	if v, _ := time.Parse(time.RFC3339, created); !v.IsZero() {
		j.CreatedAt = v
	}
	if finished.Valid {
		if v, _ := time.Parse(time.RFC3339, finished.String); !v.IsZero() {
			j.FinishedAt = &v
		}
	}
	return &j, nil
}

// ListReplayJobs lists replays for a project.
func (s *Store) ListReplayJobs(ctx context.Context, project string) ([]ReplayJob, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, origin_run, replay_run, status, mode, step_count, completed_steps,
		       failed_steps, COALESCE(mock_ref,''), created_at
		FROM replay_jobs WHERE project_id = ? ORDER BY created_at DESC LIMIT 200`,
		project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReplayJob
	for rows.Next() {
		var j ReplayJob
		var created string
		if err := rows.Scan(&j.ID, &j.OriginRun, &j.ReplayRun, &j.Status, &j.Mode,
			&j.StepCount, &j.CompletedSteps, &j.FailedSteps, &j.MockRef, &created); err != nil {
			return nil, err
		}
		j.Project = project
		if v, _ := time.Parse(time.RFC3339, created); !v.IsZero() {
			j.CreatedAt = v
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// ---- alerts ----

// UpsertAlertRule stores an alert rule.
func (s *Store) UpsertAlertRule(ctx context.Context, r *AlertRule) error {
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO alert_rules(
		  id, project_id, name, detector, window_min, threshold, agent, severity,
		  webhook_url, enabled, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(project_id, id) DO UPDATE SET
		  name = excluded.name, detector = excluded.detector,
		  window_min = excluded.window_min, threshold = excluded.threshold,
		  agent = COALESCE(NULLIF(excluded.agent,''), alert_rules.agent),
		  severity = excluded.severity,
		  webhook_url = COALESCE(NULLIF(excluded.webhook_url,''), alert_rules.webhook_url),
		  enabled = excluded.enabled`,
		r.ID, r.Project, r.Name, r.Detector, r.WindowMin, r.Threshold,
		nullStr(r.Agent), nullStr(r.Severity), nullStr(r.WebhookURL),
		bi(r.Enabled), r.CreatedAt.UTC().Format(time.RFC3339))
	return err
}

// AlertRule is a deterministic alert threshold.
type AlertRule struct {
	ID         string
	Project    string
	Name       string
	Detector   string
	WindowMin  int
	Threshold  int
	Agent      string
	Severity   string
	WebhookURL string
	Enabled    bool
	CreatedAt  time.Time
}

// ListAlertRules returns enabled rules for a project.
func (s *Store) ListAlertRules(ctx context.Context, project string) ([]AlertRule, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, detector, window_min, threshold, COALESCE(agent,''),
		       COALESCE(severity,'warning'), COALESCE(webhook_url,''), enabled,
		       created_at
		FROM alert_rules WHERE project_id = ? AND enabled = 1 ORDER BY id`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AlertRule
	for rows.Next() {
		var r AlertRule
		var enabled int
		var created string
		if err := rows.Scan(&r.ID, &r.Name, &r.Detector, &r.WindowMin,
			&r.Threshold, &r.Agent, &r.Severity, &r.WebhookURL, &enabled, &created); err != nil {
			return nil, err
		}
		r.Project = project
		r.Enabled = enabled != 0
		if v, _ := time.Parse(time.RFC3339, created); !v.IsZero() {
			r.CreatedAt = v
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RecordAlertEvaluation stores a rule evaluation and its delivery result.
func (s *Store) RecordAlertEvaluation(ctx context.Context, e *AlertEvaluation) error {
	if e.EvaluatedAt.IsZero() {
		e.EvaluatedAt = time.Now().UTC()
	}
	if e.DeliveryStatus == "" {
		e.DeliveryStatus = "none"
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO alert_evaluations(
		  project_id, rule_id, window_start, metric, value, triggered,
		  delivery_status, delivery_error, evaluated_at)
		VALUES (?,?,?,?,?,?,?,?,?)
		ON CONFLICT(project_id, rule_id, window_start) DO UPDATE SET
		  value = excluded.value, triggered = excluded.triggered,
		  delivery_status = excluded.delivery_status,
		  delivery_error = excluded.delivery_error,
		  evaluated_at = excluded.evaluated_at`,
		e.Project, e.RuleID, e.WindowStart.UTC().Format(time.RFC3339), e.Metric,
		e.Value, bi(e.Triggered), e.DeliveryStatus, nullStr(e.DeliveryError),
		e.EvaluatedAt.UTC().Format(time.RFC3339))
	return err
}

// AlertEvaluation is a rule evaluation record.
type AlertEvaluation struct {
	Project        string
	RuleID         string
	WindowStart    time.Time
	Metric         string
	Value          int64
	Triggered      bool
	DeliveryStatus string
	DeliveryError  string
	EvaluatedAt    time.Time
}

// LogAudit records a payload-access or policy action.
func (s *Store) LogAudit(ctx context.Context, project, action, actor, detail string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO audit_log(project_id, action, actor, detail, ts)
		VALUES (?,?,?,?,?)`,
		project, action, actor, nullStr(detail), time.Now().UTC().Format(time.RFC3339))
	return err
}

// keep a reference to json so it is not pruned from the import set.
var _ = json.Marshal
