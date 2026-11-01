-- infrastructure/001_postgres.sql
-- Production metadata target. Mirrors the SQLite dev schema
-- (server/internal/store/schema.go) so the store's query code ports with
-- minimal change. This holds *transactional* data: projects, keys,
-- dedup/processed, datasets, alert rules, audit. High-volume events, spans
-- and run aggregates live in ClickHouse (001_clickhouse.sql).
--
-- Conventions:
--   * TEXT for ids/refs (the store uses string PKs, not surrogates).
--   * TIMESTAMPTZ, UTC, for every timestamp.
--   * Booleans as BOOLEAN (SQLite used INTEGER 0/1).
--   * The store's `?` placeholders become Postgres `$n`; the store already
--     keeps column names portable for that reason.

CREATE TABLE IF NOT EXISTS projects (
    id                  TEXT PRIMARY KEY,
    name                TEXT NOT NULL,
    environment         TEXT,
    payload_capture     TEXT NOT NULL DEFAULT 'off',
    redact_fields       TEXT NOT NULL DEFAULT '',
    retention_days      INTEGER NOT NULL DEFAULT 30,
    rate_limit_per_min  INTEGER NOT NULL DEFAULT 3000,
    rate_limit_burst    INTEGER NOT NULL DEFAULT 500,
    created_at          TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS api_keys (
    key           TEXT PRIMARY KEY,          -- SHA-256 of the plaintext key
    project_id    TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    label         TEXT NOT NULL DEFAULT '',
    active        BOOLEAN NOT NULL DEFAULT TRUE,
    created_at    TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_api_keys_project ON api_keys(project_id) WHERE active;

-- Idempotency / dedup table (spec NFR: at-least-once + idempotent => once).
-- One row per (project, idempotency_key) processed.
CREATE TABLE IF NOT EXISTS processed (
    project_id  TEXT NOT NULL,
    id          TEXT NOT NULL,
    ts          TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (project_id, id)
);

CREATE TABLE IF NOT EXISTS findings (
    id          TEXT NOT NULL,
    project_id  TEXT NOT NULL,
    run_id      TEXT NOT NULL,
    signature   TEXT,
    kind        TEXT NOT NULL,
    severity    TEXT NOT NULL,
    message     TEXT NOT NULL,
    evidence    TEXT NOT NULL,               -- JSON array of event ids
    created_at  TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (project_id, id)
);
CREATE INDEX IF NOT EXISTS idx_findings_run ON findings(project_id, run_id);
CREATE INDEX IF NOT EXISTS idx_findings_sig ON findings(project_id, signature);

CREATE TABLE IF NOT EXISTS failure_signatures (
    project_id      TEXT NOT NULL,
    signature       TEXT NOT NULL,
    kind            TEXT NOT NULL,
    tool_name       TEXT,
    count           BIGINT NOT NULL DEFAULT 0,
    run_count       BIGINT NOT NULL DEFAULT 0,
    first_seen      TIMESTAMPTZ NOT NULL,
    last_seen       TIMESTAMPTZ NOT NULL,
    agent_versions  TEXT NOT NULL DEFAULT '',
    sample_event    TEXT,
    sample_run      TEXT,
    PRIMARY KEY (project_id, signature)
);

CREATE TABLE IF NOT EXISTS datasets (
    id           TEXT NOT NULL,
    project_id   TEXT NOT NULL,
    name         TEXT NOT NULL,
    version      TEXT NOT NULL,
    description  TEXT,
    case_count   INTEGER NOT NULL DEFAULT 0,
    created_at   TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (project_id, id, version)
);

CREATE TABLE IF NOT EXISTS dataset_cases (
    id                TEXT NOT NULL,
    project_id        TEXT NOT NULL,
    dataset_id        TEXT NOT NULL,
    task              TEXT NOT NULL,
    expected_tool     TEXT,
    expect_completion BOOLEAN NOT NULL DEFAULT FALSE,
    rubric            TEXT NOT NULL DEFAULT '{}',
    PRIMARY KEY (project_id, id)
);
CREATE INDEX IF NOT EXISTS idx_dataset_cases_ds ON dataset_cases(project_id, dataset_id);

CREATE TABLE IF NOT EXISTS evaluators (
    id          TEXT NOT NULL,
    project_id  TEXT NOT NULL,
    name        TEXT NOT NULL,
    version     TEXT NOT NULL,
    kind        TEXT NOT NULL,
    prompt      TEXT NOT NULL DEFAULT '',
    calibrated  BOOLEAN NOT NULL DEFAULT FALSE,
    PRIMARY KEY (project_id, id, version)
);

CREATE TABLE IF NOT EXISTS eval_runs (
    id                TEXT NOT NULL,
    project_id        TEXT NOT NULL,
    dataset_id        TEXT NOT NULL,
    dataset_version   TEXT NOT NULL,
    agent_name        TEXT NOT NULL,
    agent_version     TEXT NOT NULL,
    model             TEXT NOT NULL,
    evaluator_id      TEXT NOT NULL,
    evaluator_version TEXT NOT NULL,
    status            TEXT NOT NULL DEFAULT 'running',
    total_cases       INTEGER NOT NULL DEFAULT 0,
    passed            INTEGER NOT NULL DEFAULT 0,
    avg_score         DOUBLE PRECISION NOT NULL DEFAULT 0,
    score_source      TEXT NOT NULL,
    gate_failed       BOOLEAN NOT NULL DEFAULT FALSE,
    gate_json         TEXT NOT NULL DEFAULT '',
    started_at        TIMESTAMPTZ NOT NULL,
    finished_at       TIMESTAMPTZ,
    PRIMARY KEY (project_id, id)
);
CREATE INDEX IF NOT EXISTS idx_eval_runs_ds ON eval_runs(project_id, dataset_id, started_at DESC);

CREATE TABLE IF NOT EXISTS eval_results (
    run_id       TEXT NOT NULL,
    project_id   TEXT NOT NULL,
    case_id      TEXT NOT NULL,
    passed       BOOLEAN NOT NULL,
    score        DOUBLE PRECISION NOT NULL,
    status       TEXT NOT NULL,
    detail       TEXT NOT NULL DEFAULT '',
    origin_run   TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (project_id, run_id, case_id)
);

CREATE TABLE IF NOT EXISTS replay_jobs (
    id                TEXT NOT NULL,
    project_id        TEXT NOT NULL,
    origin_run        TEXT NOT NULL,
    replay_run        TEXT NOT NULL,
    status            TEXT NOT NULL,
    mode              TEXT NOT NULL DEFAULT 'mocked',
    step_count        INTEGER NOT NULL DEFAULT 0,
    completed_steps   INTEGER NOT NULL DEFAULT 0,
    failed_steps      INTEGER NOT NULL DEFAULT 0,
    mock_ref          TEXT NOT NULL DEFAULT '',
    created_at        TIMESTAMPTZ NOT NULL,
    finished_at       TIMESTAMPTZ,
    PRIMARY KEY (project_id, id)
);

CREATE TABLE IF NOT EXISTS alert_rules (
    id           TEXT NOT NULL,
    project_id   TEXT NOT NULL,
    name         TEXT NOT NULL,
    detector     TEXT NOT NULL,
    window_min   INTEGER NOT NULL DEFAULT 10,
    threshold    INTEGER NOT NULL DEFAULT 1,
    agent        TEXT NOT NULL DEFAULT '',
    severity     TEXT NOT NULL DEFAULT 'warning',
    webhook_url  TEXT NOT NULL DEFAULT '',
    enabled      BOOLEAN NOT NULL DEFAULT TRUE,
    created_at   TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (project_id, id)
);

CREATE TABLE IF NOT EXISTS alert_evaluations (
    project_id      TEXT NOT NULL,
    rule_id         TEXT NOT NULL,
    window_start    TIMESTAMPTZ NOT NULL,
    metric          TEXT NOT NULL,
    value           BIGINT NOT NULL,
    triggered       BOOLEAN NOT NULL,
    delivery_status TEXT NOT NULL DEFAULT 'none',
    delivery_error  TEXT NOT NULL DEFAULT '',
    evaluated_at    TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (project_id, rule_id, window_start)
);

CREATE TABLE IF NOT EXISTS audit_log (
    project_id  TEXT NOT NULL,
    action      TEXT NOT NULL,
    actor       TEXT NOT NULL,
    detail      TEXT NOT NULL,
    ts          TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_audit_log_project ON audit_log(project_id, ts DESC);

-- Note: `events`, `runs` and `spans` are intentionally ABSENT here on purpose
-- (they are the ClickHouse analytics facts). A deployment that wants a single
-- relational database can add them here, but the production target keeps the
-- metadata/analytics split per spec §9.
