// Package storepg is the Postgres adapter for the metadata store: the same
// logical schema and the same query surface as the embedded SQLite dev store
// (store.Store), executed on Postgres via pgx. It is selected at startup
// when OBS_DATABASE_URL is set; otherwise the SQLite fallback is used and
// this package is simply not constructed.
//
// Schema translation notes (one logical schema, Postgres dialect):
//   - TEXT stays TEXT (all keys and RFC3339 timestamp strings, so the
//     dev and prod stores store identical values);
//   - INTEGER -> BIGINT, REAL -> DOUBLE PRECISION;
//   - ON CONFLICT / EXCLUDED / COALESCE(NULLIF(...)) / two-arg MAX/MIN are
//     valid in both dialects, so the SQLite DDL translates with only type
//     changes (see schema.go; mirrors infrastructure/001_postgres.sql).
package storepg

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ensembyte/observatory/internal/store"
)

// PG is the Postgres metadata store.
type PG struct {
	pool *pgxpool.Pool
}

// Compile-time interface conformance: a signature drift fails the build.
var (
	_ store.Engine       = (*PG)(nil)
	_ store.AlertMetrics = (*PG)(nil)
)

// New opens a connection pool and verifies connectivity. Call Migrate
// afterwards (idempotent).
func New(ctx context.Context, dsn string) (*PG, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, err
	}
	return &PG{pool: pool}, nil
}

// Close releases the pool.
func (s *PG) Close() error {
	s.pool.Close()
	return nil
}

// Migrate creates the production schema (Postgres dialect of the SQLite
// reference schema in internal/store/schema.go). Idempotent.
func (s *PG) Migrate(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, pgSchema)
	return err
}

// Time values are stored as RFC3339 strings, exactly like the dev store, so
// both adapters persist identical representations.

func fmtTS(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// parseTS parses a stored timestamp string; empty/invalid => zero time.
func parseTS(v *string) time.Time {
	if v == nil {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339Nano, *v)
	return t
}

// nullStr returns nil for the empty string so COALESCE(NULLIF(...)) upserts
// keep existing values (same helper semantics as the SQLite store).
func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// ---- projects & keys ----

func (s *PG) CreateProject(ctx context.Context, id, name, environment string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO projects(id, name, environment, payload_capture, retention_days,
		                     rate_limit_per_min, rate_limit_burst, created_at)
		VALUES ($1, $2, $3, 'off', 30, 3000, 500, $4)
		ON CONFLICT(id) DO UPDATE SET name = EXCLUDED.name, environment = EXCLUDED.environment`,
		id, name, environment, time.Now().UTC().Format(time.RFC3339))
	return err
}

func (s *PG) GetProject(ctx context.Context, id string) (*store.Project, error) {
	var p store.Project
	err := s.pool.QueryRow(ctx, `
		SELECT id, name, COALESCE(environment,''), payload_capture,
		       COALESCE(redact_fields,''), COALESCE(retention_days,30),
		       COALESCE(rate_limit_per_min,3000), COALESCE(rate_limit_burst,500)
		FROM projects WHERE id = $1`, id).
		Scan(&p.ID, &p.Name, &p.Environment, &p.PayloadCapture, &p.RedactFields,
			&p.RetentionDays, &p.RatePerMin, &p.RateBurst)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *PG) ListProjects(ctx context.Context) ([]store.Project, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, name, COALESCE(environment,''), payload_capture,
		       COALESCE(redact_fields,''), COALESCE(retention_days,30),
		       COALESCE(rate_limit_per_min,3000), COALESCE(rate_limit_burst,500)
		FROM projects ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Project
	for rows.Next() {
		var p store.Project
		if err := rows.Scan(&p.ID, &p.Name, &p.Environment, &p.PayloadCapture,
			&p.RedactFields, &p.RetentionDays, &p.RatePerMin, &p.RateBurst); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *PG) SetPayloadCapture(ctx context.Context, project, mode string) error {
	_, err := s.pool.Exec(ctx, `UPDATE projects SET payload_capture = $1 WHERE id = $2`,
		mode, project)
	return err
}

func (s *PG) SetRateLimits(ctx context.Context, project string, perMin, burst int) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE projects SET rate_limit_per_min = $1, rate_limit_burst = $2 WHERE id = $3`,
		perMin, burst, project)
	return err
}

func (s *PG) CreateAPIKey(ctx context.Context, project, label, key string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO api_keys(key, project_id, label, active, created_at)
		 VALUES ($1,$2,$3,1,$4)
		 ON CONFLICT(key) DO UPDATE SET project_id = EXCLUDED.project_id, label = EXCLUDED.label, active = 1`,
		store.HashKey(key), project, label, time.Now().UTC().Format(time.RFC3339))
	return err
}

func (s *PG) LookupProjectForKey(ctx context.Context, key string) (string, bool, error) {
	var project string
	err := s.pool.QueryRow(ctx,
		`SELECT project_id FROM api_keys WHERE key = $1 AND active = 1`,
		store.HashKey(key)).Scan(&project)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return project, true, nil
}

func (s *PG) ListKeys(ctx context.Context, project string) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT label FROM api_keys WHERE project_id = $1 AND active = 1 ORDER BY label`,
		project)
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

func (s *PG) RevokeKey(ctx context.Context, project, key string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE api_keys SET active = 0 WHERE project_id = $1 AND key = $2`,
		project, store.HashKey(key))
	return err
}

// ---- events / dedup ----

// FilterUnseen returns ids not yet processed for this project and marks the
// new ones (same chunking semantics as the SQLite store).
func (s *PG) FilterUnseen(ctx context.Context, project string, ids []string) []string {
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
		qs := make([]string, len(batch)+1)
		args := []any{project}
		for j, id := range batch {
			qs[j+1] = fmt.Sprintf("$%d", j+2)
			args = append(args, id)
		}
		q := `SELECT id FROM processed WHERE project_id = $1 AND id IN (` +
			strings.Join(qs, ",") + `)`
		rows, err := s.pool.Query(ctx, q, args...)
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
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return unseen
	}
	now := time.Now().UTC().Format(time.RFC3339)
	for _, id := range unseen {
		if _, err := tx.Exec(ctx,
			`INSERT INTO processed(project_id, id, ts) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`,
			project, id, now); err != nil {
			tx.Rollback(ctx)
			return unseen
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return unseen
	}
	return unseen
}

// InsertEvents persists event rows and upserts run aggregates (same query
// surface as the SQLite store; Postgres dialect).
func (s *PG) InsertEvents(ctx context.Context, evs []*store.EventRow) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	for _, e := range evs {
		if e.Project == "" {
			tx.Rollback(ctx)
			return errors.New("storepg: event missing project scope")
		}
		attrs, err := store.JSON(e.Attrs)
		if err != nil {
			attrs = []byte("{}")
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO events(
				project_id, id, run_id, seq, type, ts, span_id, parent_span_id,
				agent_name, agent_version, model, provider, attrs, payload_ref, received_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
			ON CONFLICT(project_id, id) DO UPDATE SET
				run_id = EXCLUDED.run_id,
				seq = EXCLUDED.seq,
				type = EXCLUDED.type,
				attrs = EXCLUDED.attrs,
				payload_ref = COALESCE(EXCLUDED.payload_ref, events.payload_ref)`,
			e.Project, e.ID, e.Run, e.Seq, e.Type, e.TS.UTC().Format(time.RFC3339Nano),
			nullStr(e.Span), nullStr(e.Parent), nullStr(e.AgentName), nullStr(e.AgentVersion),
			nullStr(e.Model), nullStr(e.Provider), string(attrs),
			nullStr(e.PayloadRef), e.ReceivedAt.UTC().Format(time.RFC3339Nano)); err != nil {
			tx.Rollback(ctx)
			return err
		}
		if err := s.upsertRunTx(ctx, tx, e); err != nil {
			tx.Rollback(ctx)
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *PG) upsertRunTx(ctx context.Context, tx pgx.Tx, e *store.EventRow) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	switch e.Type {
	case "run.started":
		_, err := tx.Exec(ctx, `
			INSERT INTO runs(id, project_id, status, agent_name, agent_version, model,
			                provider, started_at, event_count, seq_max, created_at)
			VALUES ($1,$2,'open',$3,$4,$5,$6,$7,1,$8,$9)
			ON CONFLICT(id, project_id) DO UPDATE SET
			  agent_name = COALESCE(NULLIF(EXCLUDED.agent_name,''), runs.agent_name),
			  agent_version = COALESCE(NULLIF(EXCLUDED.agent_version,''), runs.agent_version),
			  model = COALESCE(NULLIF(EXCLUDED.model,''), runs.model),
			  provider = COALESCE(NULLIF(EXCLUDED.provider,''), runs.provider),
			  started_at = COALESCE(runs.started_at, EXCLUDED.started_at),
			  event_count = runs.event_count + 1,
			  seq_max = MAX(runs.seq_max, EXCLUDED.seq_max)`,
			e.Run, e.Project, nullStr(e.AgentName), nullStr(e.AgentVersion),
			nullStr(e.Model), nullStr(e.Provider), e.TS.UTC().Format(time.RFC3339Nano),
			e.Seq, now)
		return err
	case "run.completed", "run.failed", "run.cancelled":
		status := e.Type[len("run."):]
		_, err := tx.Exec(ctx, `
			INSERT INTO runs(id, project_id, status, ended_at, event_count, seq_max, created_at)
			VALUES ($1,$2,$3,$4,1,$5,$6)
			ON CONFLICT(id, project_id) DO UPDATE SET
			  status = EXCLUDED.status,
			  ended_at = COALESCE(runs.ended_at, EXCLUDED.ended_at),
			  event_count = runs.event_count + 1,
			  seq_max = MAX(runs.seq_max, EXCLUDED.seq_max),
			  duration_ms = MAX(runs.duration_ms, EXCLUDED.duration_ms)`,
			e.Run, e.Project, status, e.TS.UTC().Format(time.RFC3339Nano), e.Seq, now)
		if err != nil {
			return err
		}
		// Compute duration in Go (deterministic, not engine-specific) —
		// same policy as the SQLite store.
		var started *string
		_ = tx.QueryRow(ctx,
			`SELECT started_at FROM runs WHERE id = $1 AND project_id = $2`,
			e.Run, e.Project).Scan(&started)
		if started != nil {
			if st, perr := time.Parse(time.RFC3339Nano, *started); perr == nil {
				if ms := int64(e.TS.Sub(st).Milliseconds()); ms > 0 {
					var cur int64
					_ = tx.QueryRow(ctx,
						`SELECT COALESCE(duration_ms,0) FROM runs WHERE id = $1 AND project_id = $2`,
						e.Run, e.Project).Scan(&cur)
					if ms > cur {
						_, _ = tx.Exec(ctx,
							`UPDATE runs SET duration_ms = $1 WHERE id = $2 AND project_id = $3`,
							ms, e.Run, e.Project)
					}
				}
			}
		}
		return nil
	default:
		_, err := tx.Exec(ctx, `
			INSERT INTO runs(id, project_id, status, event_count, seq_max, created_at)
			VALUES ($1,$2,'open',1,$3,$4)
			ON CONFLICT(id, project_id) DO UPDATE SET
			  agent_name = COALESCE(NULLIF(EXCLUDED.agent_name,''), runs.agent_name),
			  agent_version = COALESCE(NULLIF(EXCLUDED.agent_version,''), runs.agent_version),
			  model = COALESCE(NULLIF(EXCLUDED.model,''), runs.model),
			  provider = COALESCE(NULLIF(EXCLUDED.provider,''), runs.provider),
			  event_count = runs.event_count + 1,
			  seq_max = MAX(runs.seq_max, EXCLUDED.seq_max)`,
			e.Run, e.Project, e.Seq, now)
		if err != nil {
			return err
		}
		s.foldUsage(ctx, tx, e)
		return nil
	}
}

// foldUsage folds provider-reported or estimated usage into the run
// aggregate, marking estimates explicitly (same as the SQLite store).
func (s *PG) foldUsage(ctx context.Context, tx pgx.Tx, e *store.EventRow) {
	a := e.Attrs
	if v, ok := a["model.tokens_in"]; ok {
		_, _ = tx.Exec(ctx, `UPDATE runs SET tokens_in = tokens_in + $1 WHERE id = $2`,
			int64(store.AsFloat(v)), e.Run)
	}
	if v, ok := a["model.tokens_out"]; ok {
		_, _ = tx.Exec(ctx, `UPDATE runs SET tokens_out = tokens_out + $1 WHERE id = $2`,
			int64(store.AsFloat(v)), e.Run)
	}
	if v, ok := a["model.retries"]; ok {
		_, _ = tx.Exec(ctx, `UPDATE runs SET retries = retries + $1 WHERE id = $2`,
			int64(store.AsFloat(v)), e.Run)
	}
	if v, ok := a["cost.usd"]; ok {
		_, _ = tx.Exec(ctx, `UPDATE runs SET cost_usd = cost_usd + $1 WHERE id = $2`,
			store.AsFloat(v), e.Run)
		if est, ok := a["cost.estimated"]; ok && store.AsBool(est) {
			_, _ = tx.Exec(ctx, `UPDATE runs SET cost_estimated = 1 WHERE id = $1`, e.Run)
		}
	}
	if src, ok := a["usage.source"]; ok {
		if s2, ok := src.(string); ok && s2 != "" {
			_, _ = tx.Exec(ctx, `UPDATE runs SET usage_source = $1 WHERE id = $2`, s2, e.Run)
		}
	}
}

// ---- runs / spans / timeline ----

// UpsertSpan writes or updates a span aggregate (same semantics as the
// SQLite store; two-arg MAX/MIN are valid Postgres scalars too).
func (s *PG) UpsertSpan(ctx context.Context, sp *store.SpanRow) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO spans(
		  id, project_id, run_id, parent_span_id, name, kind, status,
		  agent_name, agent_version, tool_name, tool_version, tool_args_hash,
		  tool_schema_version, model, provider, usage_source,
		  tokens_in, tokens_out, cost_usd, cost_estimated, retries, error_class,
		  started_at, ended_at, duration_ms, seq_start, seq_end)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27)
		ON CONFLICT(id, project_id) DO UPDATE SET
		  run_id = EXCLUDED.run_id,
		  parent_span_id = COALESCE(NULLIF(EXCLUDED.parent_span_id,''), spans.parent_span_id),
		  name = COALESCE(NULLIF(EXCLUDED.name,''), spans.name),
		  kind = COALESCE(NULLIF(EXCLUDED.kind,''), spans.kind),
		  status = COALESCE(NULLIF(EXCLUDED.status,''), spans.status),
		  agent_name = COALESCE(NULLIF(EXCLUDED.agent_name,''), spans.agent_name),
		  agent_version = COALESCE(NULLIF(EXCLUDED.agent_version,''), spans.agent_version),
		  tool_name = COALESCE(NULLIF(EXCLUDED.tool_name,''), spans.tool_name),
		  tool_version = COALESCE(NULLIF(EXCLUDED.tool_version,''), spans.tool_version),
		  tool_args_hash = COALESCE(NULLIF(EXCLUDED.tool_args_hash,''), spans.tool_args_hash),
		  tool_schema_version = COALESCE(NULLIF(EXCLUDED.tool_schema_version,''), spans.tool_schema_version),
		  model = COALESCE(NULLIF(EXCLUDED.model,''), spans.model),
		  provider = COALESCE(NULLIF(EXCLUDED.provider,''), spans.provider),
		  usage_source = COALESCE(NULLIF(EXCLUDED.usage_source,''), spans.usage_source),
		  tokens_in = MAX(spans.tokens_in, EXCLUDED.tokens_in),
		  tokens_out = MAX(spans.tokens_out, EXCLUDED.tokens_out),
		  cost_usd = MAX(spans.cost_usd, EXCLUDED.cost_usd),
		  cost_estimated = MAX(spans.cost_estimated, EXCLUDED.cost_estimated),
		  retries = MAX(spans.retries, EXCLUDED.retries),
		  error_class = COALESCE(NULLIF(EXCLUDED.error_class,''), spans.error_class),
		  started_at = COALESCE(NULLIF(EXCLUDED.started_at,''), spans.started_at),
		  ended_at = COALESCE(NULLIF(EXCLUDED.ended_at,''), spans.ended_at),
		  duration_ms = MAX(spans.duration_ms, EXCLUDED.duration_ms),
		  seq_start = CASE WHEN spans.seq_start = 0 THEN EXCLUDED.seq_start
		                  ELSE MIN(spans.seq_start, EXCLUDED.seq_start) END,
		  seq_end = MAX(spans.seq_end, EXCLUDED.seq_end)`,
		sp.ID, sp.Project, sp.RunID, nullStr(sp.Parent), nullStr(sp.Name), nullStr(sp.Kind),
		nullStr(sp.Status), nullStr(sp.AgentName), nullStr(sp.AgentVersion),
		nullStr(sp.ToolName), nullStr(sp.ToolVersion), nullStr(sp.ToolArgsHash),
		nullStr(sp.ToolSchemaVer), nullStr(sp.Model), nullStr(sp.Provider),
		nullStr(sp.UsageSource), sp.TokensIn, sp.TokensOut, sp.CostUSD,
		boolInt(sp.CostEstimated), sp.Retries, nullStr(sp.ErrorClass),
		fmtTS(sp.StartedAt), fmtTS(sp.EndedAt), sp.DurationMS, sp.SeqStart, sp.SeqEnd)
	return err
}

// ListRuns pages the run index for a project, most recent first.
func (s *PG) ListRuns(ctx context.Context, project, status, agentName, agentVersion, model, environment string, limit, offset int) ([]*store.RunAggregate, error) {
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT id, status FROM runs WHERE project_id = $1`
	args := []any{project}
	n := 2
	add := func(col, v string) {
		if v == "" {
			return
		}
		q += fmt.Sprintf(" AND %s = $%d", col, n)
		args = append(args, v)
		n++
	}
	add("status", status)
	add("agent_name", agentName)
	add("agent_version", agentVersion)
	add("model", model)
	add("environment", environment)
	q += fmt.Sprintf(" ORDER BY started_at DESC NULLS LAST LIMIT $%d OFFSET $%d", n, n+1)
	args = append(args, limit, offset)

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*store.RunAggregate
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

// RunTimeline returns ordered events, spans and the run aggregate.
func (s *PG) RunTimeline(ctx context.Context, project, run string) (*store.Timeline, error) {
	tl := &store.Timeline{Run: &store.RunAggregate{RunID: run, Project: project, Status: "open"}}
	if r, err := s.GetRun(ctx, project, run); err == nil && r != nil {
		tl.Run = r
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, COALESCE(span_id,''), COALESCE(parent_span_id,''), seq, type, ts,
		       COALESCE(agent_name,''), COALESCE(agent_version,''),
		       COALESCE(model,''), COALESCE(provider,''),
		       COALESCE(attrs,'{}'), COALESCE(payload_ref,''), received_at
		FROM events WHERE project_id = $1 AND run_id = $2
		ORDER BY seq ASC, received_at ASC`, project, run)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		e := &store.EventRow{Project: project, Run: run, Attrs: map[string]any{}}
		var tsStr, recv, attrJSON string
		if err := rows.Scan(&e.ID, &e.Span, &e.Parent, &e.Seq, &e.Type, &tsStr,
			&e.AgentName, &e.AgentVersion, &e.Model, &e.Provider,
			&attrJSON, &e.PayloadRef, &recv); err != nil {
			return nil, err
		}
		e.TS, _ = time.Parse(time.RFC3339Nano, tsStr)
		e.ReceivedAt, _ = time.Parse(time.RFC3339Nano, recv)
		_ = store.JSONDecode(attrJSON, &e.Attrs)
		tl.Events = append(tl.Events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	srows, err := s.pool.Query(ctx, `
		SELECT id, run_id, COALESCE(parent_span_id,''), COALESCE(name,''), COALESCE(kind,''),
		       COALESCE(status,''), COALESCE(agent_name,''), COALESCE(agent_version,''),
		       COALESCE(tool_name,''), COALESCE(tool_version,''),
		       COALESCE(tool_args_hash,''), COALESCE(tool_schema_version,''),
		       COALESCE(model,''), COALESCE(provider,''), COALESCE(usage_source,''),
		       COALESCE(tokens_in,0), COALESCE(tokens_out,0),
		       COALESCE(cost_usd,0), COALESCE(cost_estimated,0), COALESCE(retries,0),
		       COALESCE(error_class,''), started_at, ended_at,
		       COALESCE(duration_ms,0), COALESCE(seq_start,0), COALESCE(seq_end,0)
		FROM spans WHERE project_id = $1 AND run_id = $2
		ORDER BY seq_start ASC, id ASC`, project, run)
	if err != nil {
		return nil, err
	}
	defer srows.Close()
	for srows.Next() {
		sp := &store.SpanRow{Project: project, RunID: run}
		var started, ended *string
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
		sp.StartedAt, sp.EndedAt = parseTS(started), parseTS(ended)
		tl.Spans = append(tl.Spans, sp)
	}
	return tl, srows.Err()
}

// GetRun fetches a run aggregate.
func (s *PG) GetRun(ctx context.Context, project, run string) (*store.RunAggregate, error) {
	var r store.RunAggregate
	var started, ended *string
	var costEst int64
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(status,'open'), COALESCE(agent_name,''), COALESCE(agent_version,''),
		       COALESCE(model,''), COALESCE(provider,''), COALESCE(environment,''),
		       started_at, ended_at, COALESCE(duration_ms,0),
		       COALESCE(tokens_in,0), COALESCE(tokens_out,0), COALESCE(usage_source,''),
		       COALESCE(cost_usd,0), COALESCE(cost_estimated,0), COALESCE(retries,0),
		       COALESCE(event_count,0), COALESCE(seq_max,0)
		FROM runs WHERE project_id = $1 AND id = $2`, project, run).
		Scan(&r.Status, &r.AgentName, &r.AgentVersion, &r.Model, &r.Provider,
			&r.Environment, &started, &ended, &r.DurationMS, &r.TokensIn, &r.TokensOut,
			&r.UsageSource, &r.CostUSD, &costEst, &r.Retries, &r.EventCount, &r.SeqMax)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r.RunID, r.Project = run, project
	r.CostEstimated = costEst == 1
	r.StartedAt, r.EndedAt = parseTS(started), parseTS(ended)
	return &r, nil
}
