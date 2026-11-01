// Package store is the persistence abstraction.
//
// Dev mode (default build): embedded SQLite, one file. Real migration code
// for production targets lives in infrastructure/ (ClickHouse DDL +
// PostgreSQL DDL + NATS JetStream). The query methods here map 1:1 onto
// those schemas, so the pipeline code is identical in both modes.
//
// Tenant isolation (spec NFR): every row carries project_id and every
// project-scoped method in this package scopes by it.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Store persists all observatory data.
type Store struct {
	db *sql.DB
}

// EventRow is a persisted event with parsed attributes.
type EventRow struct {
	ID           string
	Project      string
	Run          string
	Span         string
	Parent       string
	Seq          uint64
	Type         string
	TS           time.Time
	AgentName    string
	AgentVersion string
	Model        string
	Provider     string
	Attrs        map[string]any
	PayloadRef   string
	ReceivedAt   time.Time
}

// RunAggregate is a run index row plus provider-reported/estimated usage.
type RunAggregate struct {
	RunID         string
	Project       string
	Status        string
	AgentName     string
	AgentVersion  string
	Model         string
	Provider      string
	Environment   string
	StartedAt     time.Time
	EndedAt       time.Time
	DurationMS    int64
	TokensIn      int64
	TokensOut     int64
	UsageSource   string // "provider" | "estimate"
	CostUSD       float64
	CostEstimated bool
	Retries       int64
	EventCount    int64
	SeqMax        uint64
}

// Signature aggregates a cross-run failure cluster (spec 8.4 / FR-04).
type Signature struct {
	Project       string
	Signature     string
	Kind          string
	ToolName      string
	Count         int64
	RunCount      int64
	FirstSeen     time.Time
	LastSeen      time.Time
	AgentVersions string
	SampleEvent   string
	SampleRun     string
}

// SpanRow is a materialized span with derived duration and usage.
type SpanRow struct {
	ID            string
	Project       string
	RunID         string
	Parent        string
	Name          string
	Kind          string
	Status        string
	AgentName     string
	AgentVersion  string
	ToolName      string
	ToolVersion   string
	ToolArgsHash  string
	ToolSchemaVer string
	Model         string
	Provider      string
	UsageSource   string
	TokensIn      int64
	TokensOut     int64
	CostUSD       float64
	CostEstimated bool
	Retries       int64
	ErrorClass    string
	StartedAt     time.Time
	EndedAt       time.Time
	DurationMS    int64
	SeqStart      uint64
	SeqEnd        uint64
}

func OpenSQLite(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// SQLite is a single-writer store: serialize for the embedded dev store.
	db.SetMaxOpenConns(1)
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// DB exposes the underlying handle for tests and admin tooling.
func (s *Store) DB() *sql.DB { return s.db }

// ---- projects & keys ----

type Project struct {
	ID             string
	Name           string
	Environment    string
	PayloadCapture string // "off" | "on"
	RedactFields   string // comma-separated extra sensitive keys
	RetentionDays  int
	RatePerMin     int
	RateBurst      int
}

// CreateProject registers a project (idempotent on id).
func (s *Store) CreateProject(ctx context.Context, id, name, environment string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO projects(id, name, environment, payload_capture, retention_days,
		                     rate_limit_per_min, rate_limit_burst, created_at)
		VALUES (?, ?, ?, 'off', 30, 3000, 500, ?)
		ON CONFLICT(id) DO UPDATE SET name = excluded.name, environment = excluded.environment`,
		id, name, environment, time.Now().UTC().Format(time.RFC3339))
	return err
}

func (s *Store) GetProject(ctx context.Context, id string) (*Project, error) {
	var p Project
	err := s.db.QueryRowContext(ctx, `
		SELECT id, name, COALESCE(environment,''), payload_capture,
		       COALESCE(redact_fields,''), COALESCE(retention_days,30),
		       COALESCE(rate_limit_per_min,3000), COALESCE(rate_limit_burst,500)
		FROM projects WHERE id = ?`, id).
		Scan(&p.ID, &p.Name, &p.Environment, &p.PayloadCapture, &p.RedactFields,
			&p.RetentionDays, &p.RatePerMin, &p.RateBurst)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *Store) ListProjects(ctx context.Context) ([]Project, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, COALESCE(environment,''), payload_capture,
		       COALESCE(redact_fields,''), COALESCE(retention_days,30),
		       COALESCE(rate_limit_per_min,3000), COALESCE(rate_limit_burst,500)
		FROM projects ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Project
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.Name, &p.Environment, &p.PayloadCapture,
			&p.RedactFields, &p.RetentionDays, &p.RatePerMin, &p.RateBurst); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SetPayloadCapture toggles opt-in payload capture for a project.
func (s *Store) SetPayloadCapture(ctx context.Context, project, mode string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE projects SET payload_capture = ? WHERE id = ?`,
		mode, project)
	return err
}

// SetRateLimits updates the per-project ingestion rate limit (spec: buffer
// SDK event floods; protect ingestion).
func (s *Store) SetRateLimits(ctx context.Context, project string, perMin, burst int) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE projects SET rate_limit_per_min = ?, rate_limit_burst = ? WHERE id = ?`,
		perMin, burst, project)
	return err
}

// CreateAPIKey stores a hashed project key. The plaintext is shown to the
// caller exactly once and never persisted (spec: project-scoped keys).
func (s *Store) CreateAPIKey(ctx context.Context, project, label, key string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO api_keys(key, project_id, label, active, created_at)
		 VALUES (?,?,?,?,?)`,
		HashKey(key), project, label, 1, time.Now().UTC().Format(time.RFC3339))
	return err
}

// LookupProjectForKey resolves a plaintext key to its project.
func (s *Store) LookupProjectForKey(ctx context.Context, key string) (string, bool, error) {
	var project string
	err := s.db.QueryRowContext(ctx,
		`SELECT project_id FROM api_keys WHERE key = ? AND active = 1`, HashKey(key)).
		Scan(&project)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return project, true, nil
}

// ListKeys returns labels of active keys for a project.
func (s *Store) ListKeys(ctx context.Context, project string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT label FROM api_keys WHERE project_id = ? AND active = 1 ORDER BY label`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var l string
		if err := rows.Scan(&l); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// RevokeKey deactivates a key (rotation: add the new key first).
func (s *Store) RevokeKey(ctx context.Context, project, key string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE api_keys SET active = 0 WHERE project_id = ? AND key = ?`,
		project, HashKey(key))
	return err
}

// ---- events / dedup ----

// FilterUnseen returns ids not yet processed for this project and marks the
// new ones. Batching keeps the IN-clause bounded.
func (s *Store) FilterUnseen(ctx context.Context, project string, ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	const chunk = 500
	var unseen []string
	for i := 0; i < len(ids); i += chunk {
		end := i + chunk
		if end > len(ids) {
			end = len(ids)
		}
		batch := ids[i:end]
		placeholders := make([]string, len(batch))
		args := []any{project}
		for j, id := range batch {
			placeholders[j] = "?"
			args = append(args, id)
		}
		q := `SELECT id FROM processed WHERE project_id = ? AND id IN (` +
			strings.Join(placeholders, ",") + `)`
		rows, err := s.db.QueryContext(ctx, q, args...)
		if err != nil {
			return nil
		}
		seen := map[string]bool{}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err == nil {
				seen[id] = true
			}
		}
		rows.Close()
		for _, id := range batch {
			if !seen[id] {
				unseen = append(unseen, id)
			}
		}
	}
	if len(unseen) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return unseen
	}
	for _, id := range unseen {
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO processed(project_id, id, ts) VALUES (?,?,?)`,
			project, id, time.Now().UTC().Format(time.RFC3339)); err != nil {
			tx.Rollback()
			return unseen
		}
	}
	if err := tx.Commit(); err != nil {
		return unseen
	}
	return unseen
}

// InsertEvents persists event rows and upserts run/span aggregates. Callers
// must have deduped via FilterUnseen.
func (s *Store) InsertEvents(ctx context.Context, evs []*EventRow) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	for _, e := range evs {
		if e.Project == "" {
			tx.Rollback()
			return errors.New("store: event missing project scope")
		}
		attrs, err := json.Marshal(e.Attrs)
		if err != nil {
			attrs = []byte("{}")
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT OR REPLACE INTO events(
				project_id, id, run_id, seq, type, ts, span_id, parent_span_id,
				agent_name, agent_version, model, provider, attrs, payload_ref, received_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			e.Project, e.ID, e.Run, e.Seq, e.Type, e.TS.UTC().Format(time.RFC3339Nano),
			nullStr(e.Span), nullStr(e.Parent), nullStr(e.AgentName), nullStr(e.AgentVersion),
			nullStr(e.Model), nullStr(e.Provider), string(attrs),
			nullStr(e.PayloadRef), e.ReceivedAt.UTC().Format(time.RFC3339Nano)); err != nil {
			tx.Rollback()
			return err
		}
		if err := s.upsertRunTx(ctx, tx, e); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) upsertRunTx(ctx context.Context, tx *sql.Tx, e *EventRow) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	switch e.Type {
	case "run.started":
		_, err := tx.ExecContext(ctx, `
			INSERT INTO runs(id, project_id, status, agent_name, agent_version, model,
			                provider, started_at, event_count, seq_max, created_at)
			VALUES (?,?,'open',?,?,?,?,?,1,?,?)
			ON CONFLICT(id, project_id) DO UPDATE SET
			  agent_name = COALESCE(NULLIF(excluded.agent_name,''), runs.agent_name),
			  agent_version = COALESCE(NULLIF(excluded.agent_version,''), runs.agent_version),
			  model = COALESCE(NULLIF(excluded.model,''), runs.model),
			  provider = COALESCE(NULLIF(excluded.provider,''), runs.provider),
			  started_at = COALESCE(runs.started_at, excluded.started_at),
			  event_count = runs.event_count + 1,
			  seq_max = MAX(runs.seq_max, excluded.seq_max)`,
			e.Run, e.Project, e.AgentName, e.AgentVersion, e.Model, e.Provider,
			e.TS.UTC().Format(time.RFC3339Nano), e.Seq, now)
		return err
	case "run.completed", "run.failed", "run.cancelled":
		status := e.Type[len("run."):]
		_, err := tx.ExecContext(ctx, `
			INSERT INTO runs(id, project_id, status, ended_at, event_count, seq_max, created_at)
			VALUES (?,?,?,1,?,?,?)
			ON CONFLICT(id, project_id) DO UPDATE SET
			  status = excluded.status,
			  ended_at = COALESCE(runs.ended_at, excluded.ended_at),
			  event_count = runs.event_count + 1,
			  seq_max = MAX(runs.seq_max, excluded.seq_max),
			  duration_ms = MAX(runs.duration_ms, excluded.duration_ms)`,
			e.Run, e.Project, status,
			e.TS.UTC().Format(time.RFC3339Nano), e.Seq, now)
		if err != nil {
			return err
		}
		// Compute duration in Go (deterministic, not engine-specific).
		var started sql.NullString
		_ = tx.QueryRowContext(ctx,
			`SELECT started_at FROM runs WHERE id = ? AND project_id = ?`,
			e.Run, e.Project).Scan(&started)
		if started.Valid {
			if st, perr := time.Parse(time.RFC3339Nano, started.String); perr == nil {
				if ms := int64(e.TS.Sub(st).Milliseconds()); ms > 0 {
					var cur int64
					_ = tx.QueryRowContext(ctx,
						`SELECT COALESCE(duration_ms,0) FROM runs WHERE id = ? AND project_id = ?`,
						e.Run, e.Project).Scan(&cur)
					if ms > cur {
						_, _ = tx.ExecContext(ctx,
							`UPDATE runs SET duration_ms = ? WHERE id = ? AND project_id = ?`,
							ms, e.Run, e.Project)
					}
				}
			}
		}
		return nil
	default:
		_, err := tx.ExecContext(ctx, `
			INSERT INTO runs(id, project_id, status, event_count, seq_max, created_at)
			VALUES (?,?,'open',1,?,?)
			ON CONFLICT(id, project_id) DO UPDATE SET
			  agent_name = COALESCE(NULLIF(excluded.agent_name,''), runs.agent_name),
			  agent_version = COALESCE(NULLIF(excluded.agent_version,''), runs.agent_version),
			  model = COALESCE(NULLIF(excluded.model,''), runs.model),
			  provider = COALESCE(NULLIF(excluded.provider,''), runs.provider),
			  event_count = runs.event_count + 1,
			  seq_max = MAX(runs.seq_max, excluded.seq_max)`,
			e.Run, e.Project, e.Seq, now)
		if err != nil {
			return err
		}
		s.foldUsage(ctx, tx, e)
		return nil
	}
}

// foldUsage folds provider-reported or estimated usage into the run
// aggregate, marking estimates explicitly (MVP DoD: labelled source).
func (s *Store) foldUsage(ctx context.Context, tx *sql.Tx, e *EventRow) {
	a := e.Attrs
	if v, ok := a["model.tokens_in"]; ok {
		_, _ = tx.ExecContext(ctx, `UPDATE runs SET tokens_in = tokens_in + ? WHERE id = ?`,
			int64(asFloat(v)), e.Run)
	}
	if v, ok := a["model.tokens_out"]; ok {
		_, _ = tx.ExecContext(ctx, `UPDATE runs SET tokens_out = tokens_out + ? WHERE id = ?`,
			int64(asFloat(v)), e.Run)
	}
	if v, ok := a["model.retries"]; ok {
		_, _ = tx.ExecContext(ctx, `UPDATE runs SET retries = retries + ? WHERE id = ?`,
			int64(asFloat(v)), e.Run)
	}
	if v, ok := a["cost.usd"]; ok {
		_, _ = tx.ExecContext(ctx, `UPDATE runs SET cost_usd = cost_usd + ? WHERE id = ?`,
			asFloat(v), e.Run)
		if est, ok := a["cost.estimated"]; ok && asBool(est) {
			_, _ = tx.ExecContext(ctx,
				`UPDATE runs SET cost_estimated = 1 WHERE id = ?`, e.Run)
		}
	}
	if src, ok := a["usage.source"]; ok {
		if s2, ok := src.(string); ok && s2 != "" {
			_, _ = tx.ExecContext(ctx,
				`UPDATE runs SET usage_source = ? WHERE id = ?`, s2, e.Run)
		}
	}
}

func asFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case json.Number:
		f, _ := n.Float64()
		return f
	}
	return 0
}

func asBool(v any) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	return false
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// ---- runs / spans / timeline ----

// RunTimeline returns ordered events, spans and the run aggregate.
type Timeline struct {
	Events []*EventRow
	Spans  []*SpanRow
	Run    *RunAggregate
}

func (s *Store) RunTimeline(ctx context.Context, project, run string) (*Timeline, error) {
	tl := &Timeline{Run: &RunAggregate{RunID: run, Project: project, Status: "open"}}
	if r, err := s.GetRun(ctx, project, run); err == nil && r != nil {
		tl.Run = r
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, span_id, parent_span_id, seq, type, ts,
		       COALESCE(agent_name,''), COALESCE(agent_version,''),
		       COALESCE(model,''), COALESCE(provider,''),
		       COALESCE(attrs,'{}'), COALESCE(payload_ref,''), received_at
		FROM events WHERE project_id = ? AND run_id = ?
		ORDER BY seq ASC, received_at ASC`, project, run)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		e := &EventRow{Project: project, Run: run, Attrs: map[string]any{}}
		var ts, recv, attrJSON string
		if err := rows.Scan(&e.ID, &e.Span, &e.Parent, &e.Seq, &e.Type, &ts,
			&e.AgentName, &e.AgentVersion, &e.Model, &e.Provider,
			&attrJSON, &e.PayloadRef, &recv); err != nil {
			return nil, err
		}
		e.TS, _ = time.Parse(time.RFC3339Nano, ts)
		e.ReceivedAt, _ = time.Parse(time.RFC3339Nano, recv)
		_ = json.Unmarshal([]byte(attrJSON), &e.Attrs)
		tl.Events = append(tl.Events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	srows, err := s.db.QueryContext(ctx, `
		SELECT id, run_id, COALESCE(parent_span_id,''), COALESCE(name,''), COALESCE(kind,''),
		       COALESCE(status,''), COALESCE(agent_name,''), COALESCE(agent_version,''),
		       COALESCE(tool_name,''), COALESCE(tool_version,''),
		       COALESCE(tool_args_hash,''), COALESCE(tool_schema_version,''),
		       COALESCE(model,''), COALESCE(provider,''), COALESCE(usage_source,''),
		       COALESCE(tokens_in,0), COALESCE(tokens_out,0),
		       COALESCE(cost_usd,0), COALESCE(cost_estimated,0), COALESCE(retries,0),
		       COALESCE(error_class,''), COALESCE(started_at,''), COALESCE(ended_at,''),
		       COALESCE(duration_ms,0), COALESCE(seq_start,0), COALESCE(seq_end,0)
		FROM spans WHERE project_id = ? AND run_id = ?
		ORDER BY seq_start ASC, id ASC`, project, run)
	if err != nil {
		return nil, err
	}
	defer srows.Close()
	for srows.Next() {
		sp := &SpanRow{Project: project, RunID: run}
		var started, ended string
		var costEst int64
		if err := srows.Scan(&sp.ID, &sp.RunID, &sp.Parent, &sp.Name, &sp.Kind, &sp.Status,
			&sp.AgentName, &sp.AgentVersion, &sp.ToolName, &sp.ToolVersion,
			&sp.ToolArgsHash, &sp.ToolSchemaVer, &sp.Model, &sp.Provider,
			&sp.UsageSource, &sp.TokensIn, &sp.TokensOut, &sp.CostUSD,
			&costEst, &sp.Retries, &sp.ErrorClass, &started, &ended,
			&sp.DurationMS, &sp.SeqStart, &sp.SeqEnd); err != nil {
			return nil, err
		}
		sp.CostEstimated = costEst == 1
		if v, _ := time.Parse(time.RFC3339Nano, started); !v.IsZero() {
			sp.StartedAt = v
		}
		if v, _ := time.Parse(time.RFC3339Nano, ended); !v.IsZero() {
			sp.EndedAt = v
		}
		tl.Spans = append(tl.Spans, sp)
	}
	return tl, srows.Err()
}

// GetRun fetches a run aggregate.
func (s *Store) GetRun(ctx context.Context, project, run string) (*RunAggregate, error) {
	var r RunAggregate
	var started, ended sql.NullString
	var costEst int64
	err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(status,'open'), COALESCE(agent_name,''), COALESCE(agent_version,''),
		       COALESCE(model,''), COALESCE(provider,''), COALESCE(environment,''),
		       COALESCE(started_at,''), COALESCE(ended_at,''), COALESCE(duration_ms,0),
		       COALESCE(tokens_in,0), COALESCE(tokens_out,0), COALESCE(usage_source,''),
		       COALESCE(cost_usd,0), COALESCE(cost_estimated,0), COALESCE(retries,0),
		       COALESCE(event_count,0), COALESCE(seq_max,0)
		FROM runs WHERE project_id = ? AND id = ?`, project, run).
		Scan(&r.Status, &r.AgentName, &r.AgentVersion, &r.Model, &r.Provider,
			&r.Environment, &started, &ended, &r.DurationMS, &r.TokensIn, &r.TokensOut,
			&r.UsageSource, &r.CostUSD, &costEst, &r.Retries, &r.EventCount, &r.SeqMax)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r.RunID, r.Project = run, project
	r.CostEstimated = costEst == 1
	if v, _ := time.Parse(time.RFC3339Nano, started.String); !v.IsZero() {
		r.StartedAt = v
	}
	if v, _ := time.Parse(time.RFC3339Nano, ended.String); !v.IsZero() {
		r.EndedAt = v
	}
	return &r, nil
}
