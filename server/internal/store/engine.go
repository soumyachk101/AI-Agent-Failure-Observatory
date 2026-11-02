// Engine is the metadata-store contract the control plane (api, pipeline,
// evalsvc, alertsvc) depends on. *Store (embedded SQLite, dev mode) and
// *storepg.PG (Postgres, production) both implement it, so the pipeline and
// API layers are identical across backends. The method set is exactly the
// surface those layers call; anything richer stays on the concrete type.
//
// This interface was extracted from *Store so a Postgres adapter can serve
// the same call sites without changing the API layer (zero new methods; it
// mirrors the query surface of the SQLite store).
package store

import (
	"context"
	"time"
)

// Engine is the metadata store contract (see package comment).
type Engine interface {
	// Migrate is idempotent schema init for the backend.
	Migrate(ctx context.Context) error

	// projects & keys
	CreateProject(ctx context.Context, id, name, environment string) error
	GetProject(ctx context.Context, id string) (*Project, error)
	ListProjects(ctx context.Context) ([]Project, error)
	SetPayloadCapture(ctx context.Context, project, mode string) error
	SetRateLimits(ctx context.Context, project string, perMin, burst int) error
	CreateAPIKey(ctx context.Context, project, label, key string) error
	LookupProjectForKey(ctx context.Context, key string) (string, bool, error)
	ListKeys(ctx context.Context, project string) ([]string, error)
	RevokeKey(ctx context.Context, project, key string) error

	// events / dedup
	FilterUnseen(ctx context.Context, project string, ids []string) []string
	InsertEvents(ctx context.Context, evs []*EventRow) error

	// runs / spans / timeline
	UpsertSpan(ctx context.Context, sp *SpanRow) error
	ListRuns(ctx context.Context, project, status, agentName, agentVersion, model, environment string, limit, offset int) ([]*RunAggregate, error)
	RunTimeline(ctx context.Context, project, run string) (*Timeline, error)
	GetRun(ctx context.Context, project, run string) (*RunAggregate, error)

	// failure signatures
	RecordSignature(ctx context.Context, project, sig, kind, tool, agentVersion, sampleEvent, sampleRun string, seen time.Time, countDelta int64) error
	ListSignatures(ctx context.Context, project string) ([]Signature, error)

	// findings
	RecordFinding(ctx context.Context, f *FindingRow) error
	ListFindingsByRun(ctx context.Context, project, run string) ([]FindingRow, error)
	FindingsBySignature(ctx context.Context, project, signature string) ([]FindingRow, error)

	// datasets / cases / evaluators
	UpsertDataset(ctx context.Context, d *Dataset) error
	UpsertCase(ctx context.Context, project, caseID, datasetID, task, expectedTool string, expectCompletion bool, rubric string) error
	ListCases(ctx context.Context, project, datasetID string) ([]map[string]any, error)
	ListDatasets(ctx context.Context, project string) ([]Dataset, error)
	UpsertEvaluator(ctx context.Context, e *Evaluator) error

	// evaluation runs / results
	CreateEvalRun(ctx context.Context, r *EvalRun) error
	FinishEvalRun(ctx context.Context, r *EvalRun) error
	GetEvalRun(ctx context.Context, project, id string) (*EvalRun, error)
	ListEvalRuns(ctx context.Context, project, datasetID string) ([]EvalRun, error)
	InsertEvalResult(ctx context.Context, project, runID, caseID string, passed bool, score float64, status, detail, originRun string) error
	ListEvalResults(ctx context.Context, project, runID string) ([]EvalResult, error)

	// replay
	CreateReplayJob(ctx context.Context, j *ReplayJob) error
	FinishReplayJob(ctx context.Context, project, id string, completed, failed int, status string) error
	GetReplayJob(ctx context.Context, project, id string) (*ReplayJob, error)
	ListReplayJobs(ctx context.Context, project string) ([]ReplayJob, error)

	// alerts
	UpsertAlertRule(ctx context.Context, r *AlertRule) error
	ListAlertRules(ctx context.Context, project string) ([]AlertRule, error)
	RecordAlertEvaluation(ctx context.Context, e *AlertEvaluation) error

	// audit
	LogAudit(ctx context.Context, project, action, actor, detail string) error
}

// AlertMetrics is the optional surface used only by alertsvc for its
// built-in detectors (findings counts, failure rate, latency regressions).
// The dev SQLite store implements it (store/alertmetrics.go); the Postgres
// store implements it via parameterized Postgres SQL. alertsvc receives it
// as a separate interface so the pipeline/API layers never see
// engine-specific SQL.
type AlertMetrics interface {
	AlertFindingKind(ctx context.Context, project, kind string, since time.Time) int64
	AlertRunStatusCounts(ctx context.Context, project string, since time.Time) (total, failed int64)
	AlertLatencyRegressions(ctx context.Context, project string, since time.Time) int64
}

// Compile-time assertion that the dev SQLite store satisfies the contract.
var _ Engine = (*Store)(nil)
