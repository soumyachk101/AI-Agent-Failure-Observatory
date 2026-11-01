package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
)

// HashKey is the SHA-256 of a plaintext key. Plaintext keys are never
// persisted; Lookups hash the presented key and compare.
func HashKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// schema mirrors infrastructure/ (ClickHouse + Postgres DDL) in SQLite form
// for the embedded dev store. Column names match the production schemas so
// query code portability is preserved.
const schema = `
CREATE TABLE IF NOT EXISTS projects(
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  environment TEXT,
  payload_capture TEXT NOT NULL DEFAULT 'off',
  redact_fields TEXT NOT NULL DEFAULT '',
  retention_days INTEGER NOT NULL DEFAULT 30,
  rate_limit_per_min INTEGER NOT NULL DEFAULT 3000,
  rate_limit_burst INTEGER NOT NULL DEFAULT 500,
  created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS api_keys(
  key TEXT PRIMARY KEY,
  project_id TEXT NOT NULL REFERENCES projects(id),
  label TEXT NOT NULL DEFAULT '',
  active INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS processed(
  project_id TEXT NOT NULL,
  id TEXT NOT NULL,
  ts TEXT NOT NULL,
  PRIMARY KEY(project_id, id)
);

CREATE TABLE IF NOT EXISTS events(
  project_id TEXT NOT NULL,
  id TEXT NOT NULL,
  run_id TEXT NOT NULL,
  seq INTEGER NOT NULL,
  type TEXT NOT NULL,
  ts TEXT NOT NULL,
  span_id TEXT,
  parent_span_id TEXT,
  agent_name TEXT,
  agent_version TEXT,
  model TEXT,
  provider TEXT,
  attrs TEXT NOT NULL DEFAULT '{}',
  payload_ref TEXT,
  received_at TEXT NOT NULL,
  PRIMARY KEY(project_id, id)
);
CREATE INDEX IF NOT EXISTS idx_events_run ON events(project_id, run_id, seq);
CREATE INDEX IF NOT EXISTS idx_events_type ON events(project_id, type, ts);

CREATE TABLE IF NOT EXISTS runs(
  id TEXT NOT NULL,
  project_id TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'open',
  agent_name TEXT, agent_version TEXT, model TEXT, provider TEXT,
  environment TEXT,
  started_at TEXT, ended_at TEXT,
  duration_ms INTEGER NOT NULL DEFAULT 0,
  tokens_in INTEGER NOT NULL DEFAULT 0,
  tokens_out INTEGER NOT NULL DEFAULT 0,
  usage_source TEXT NOT NULL DEFAULT '',
  cost_usd REAL NOT NULL DEFAULT 0,
  cost_estimated INTEGER NOT NULL DEFAULT 0,
  retries INTEGER NOT NULL DEFAULT 0,
  event_count INTEGER NOT NULL DEFAULT 0,
  seq_max INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  PRIMARY KEY(project_id, id)
);

CREATE TABLE IF NOT EXISTS spans(
  id TEXT NOT NULL,
  project_id TEXT NOT NULL,
  run_id TEXT NOT NULL,
  parent_span_id TEXT,
  name TEXT, kind TEXT, status TEXT,
  agent_name TEXT, agent_version TEXT,
  tool_name TEXT, tool_version TEXT, tool_args_hash TEXT, tool_schema_version TEXT,
  model TEXT, provider TEXT,
  usage_source TEXT,
  tokens_in INTEGER NOT NULL DEFAULT 0,
  tokens_out INTEGER NOT NULL DEFAULT 0,
  cost_usd REAL NOT NULL DEFAULT 0,
  cost_estimated INTEGER NOT NULL DEFAULT 0,
  retries INTEGER NOT NULL DEFAULT 0,
  error_class TEXT,
  started_at TEXT, ended_at TEXT,
  duration_ms INTEGER NOT NULL DEFAULT 0,
  seq_start INTEGER NOT NULL DEFAULT 0,
  seq_end INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(id, project_id)
);
CREATE INDEX IF NOT EXISTS idx_spans_run ON spans(project_id, run_id);

CREATE TABLE IF NOT EXISTS failure_signatures(
  project_id TEXT NOT NULL,
  signature TEXT NOT NULL,
  kind TEXT NOT NULL,
  tool_name TEXT,
  count INTEGER NOT NULL DEFAULT 0,
  run_count INTEGER NOT NULL DEFAULT 0,
  first_seen TEXT NOT NULL,
  last_seen TEXT NOT NULL,
  agent_versions TEXT NOT NULL DEFAULT '',
  sample_event TEXT,
  sample_run TEXT,
  PRIMARY KEY(project_id, signature)
);

CREATE TABLE IF NOT EXISTS findings(
  id TEXT NOT NULL,
  project_id TEXT NOT NULL,
  run_id TEXT NOT NULL,
  signature TEXT,
  kind TEXT NOT NULL,
  severity TEXT NOT NULL,
  message TEXT NOT NULL,
  evidence TEXT NOT NULL,
  created_at TEXT NOT NULL,
  PRIMARY KEY(project_id, id)
);
CREATE INDEX IF NOT EXISTS idx_findings_run ON findings(project_id, run_id);
CREATE INDEX IF NOT EXISTS idx_findings_sig ON findings(project_id, signature);

CREATE TABLE IF NOT EXISTS datasets(
  id TEXT NOT NULL, project_id TEXT NOT NULL,
  name TEXT NOT NULL, version TEXT NOT NULL, description TEXT,
  case_count INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL,
  PRIMARY KEY(project_id, id, version)
);

CREATE TABLE IF NOT EXISTS dataset_cases(
  id TEXT NOT NULL, project_id TEXT NOT NULL, dataset_id TEXT NOT NULL,
  task TEXT NOT NULL, expected_tool TEXT,
  expect_completion INTEGER NOT NULL DEFAULT 0,
  rubric TEXT NOT NULL DEFAULT '{}',
  PRIMARY KEY(project_id, id)
);

CREATE TABLE IF NOT EXISTS evaluators(
  id TEXT NOT NULL, project_id TEXT NOT NULL,
  name TEXT NOT NULL, version TEXT NOT NULL, kind TEXT NOT NULL,
  prompt TEXT NOT NULL DEFAULT '', calibrated INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(project_id, id, version)
);

CREATE TABLE IF NOT EXISTS eval_runs(
  id TEXT NOT NULL, project_id TEXT NOT NULL,
  dataset_id TEXT NOT NULL, dataset_version TEXT NOT NULL,
  agent_name TEXT NOT NULL, agent_version TEXT NOT NULL,
  model TEXT NOT NULL, evaluator_id TEXT NOT NULL, evaluator_version TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'running',
  total_cases INTEGER NOT NULL DEFAULT 0, passed INTEGER NOT NULL DEFAULT 0,
  avg_score REAL NOT NULL DEFAULT 0, score_source TEXT NOT NULL,
  gate_failed INTEGER NOT NULL DEFAULT 0, gate_json TEXT NOT NULL DEFAULT '',
  started_at TEXT NOT NULL, finished_at TEXT,
  PRIMARY KEY(project_id, id)
);

CREATE TABLE IF NOT EXISTS eval_results(
  run_id TEXT NOT NULL, project_id TEXT NOT NULL, case_id TEXT NOT NULL,
  passed INTEGER NOT NULL, score REAL NOT NULL, status TEXT NOT NULL,
  detail TEXT NOT NULL DEFAULT '', origin_run TEXT NOT NULL DEFAULT '',
  PRIMARY KEY(project_id, run_id, case_id)
);

CREATE TABLE IF NOT EXISTS replay_jobs(
  id TEXT NOT NULL, project_id TEXT NOT NULL,
  origin_run TEXT NOT NULL, replay_run TEXT NOT NULL,
  status TEXT NOT NULL, mode TEXT NOT NULL DEFAULT 'mocked',
  step_count INTEGER NOT NULL DEFAULT 0,
  completed_steps INTEGER NOT NULL DEFAULT 0,
  failed_steps INTEGER NOT NULL DEFAULT 0,
  mock_ref TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL, finished_at TEXT,
  PRIMARY KEY(project_id, id)
);

CREATE TABLE IF NOT EXISTS alert_rules(
  id TEXT NOT NULL, project_id TEXT NOT NULL,
  name TEXT NOT NULL, detector TEXT NOT NULL,
  window_min INTEGER NOT NULL DEFAULT 10,
  threshold INTEGER NOT NULL DEFAULT 1,
  agent TEXT NOT NULL DEFAULT '', severity TEXT NOT NULL DEFAULT 'warning',
  webhook_url TEXT NOT NULL DEFAULT '',
  enabled INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL,
  PRIMARY KEY(project_id, id)
);

CREATE TABLE IF NOT EXISTS alert_evaluations(
  project_id TEXT NOT NULL, rule_id TEXT NOT NULL,
  window_start TEXT NOT NULL, metric TEXT NOT NULL,
  value INTEGER NOT NULL, triggered INTEGER NOT NULL,
  delivery_status TEXT NOT NULL DEFAULT 'none',
  delivery_error TEXT NOT NULL DEFAULT '',
  evaluated_at TEXT NOT NULL,
  PRIMARY KEY(project_id, rule_id, window_start)
);

CREATE TABLE IF NOT EXISTS audit_log(
  project_id TEXT NOT NULL, action TEXT NOT NULL,
  actor TEXT NOT NULL, detail TEXT NOT NULL, ts TEXT NOT NULL
);
`

// Migrate initializes the dev-mode schema. The production equivalents live in
// infrastructure/ (001_postgres.sql, 001_clickhouse.sql).
func (s *Store) Migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, schema)
	return err
}
