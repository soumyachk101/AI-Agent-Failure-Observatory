// Package analyticsch is the ClickHouse analytics write path for production
// mode (selected when OBS_CLICKHOUSE_DSN is set).
//
// It inserts the same logical data the SQLite store persists — raw events,
// materialized spans and run aggregates — into the ClickHouse tables
// documented in deploy/CLICKHOUSE-POSTGRES-NATS.md (infrastructure/ DDL is
// written by the docs head; these table/column names mirror
// internal/store/schema.go so the two stay consistent).
//
// Runs and spans are modeled as ReplacingMergeTree (the UpsertSpan MAX/MIN
// semantics map onto version-based dedup, not in-place updates); events are
// plain MergeTree (append-only, idempotent via the dedup table).
//
// Fail-safe: inserts are batched and buffered in bounded memory. If
// ClickHouse is briefly unreachable, rows buffer up to CHBufferMaxRows
// (OBS_CH_BUFFER_MAX_ROWS, default 100000) and are re-flushed; overflow logs
// and drops the oldest rows (buffering with a hard memory ceiling — chosen
// over silent loss on every call, documented here and in the architecture
// note). Dropping never blocks the ingestion path.
package analyticsch

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"

	"github.com/ensembyte/observatory/internal/store"
)

// Sink is the analytics write surface. The production implementation
// (*ClickHouse) batches inserts; the dev build wires a no-op so the call
// sites are identical and a real service is never required locally.
type Sink interface {
	// WriteEvents persists event rows + run aggregates (mirrors the
	// SQLite InsertEvents run-upserts).
	WriteEvents(ctx context.Context, evs []*store.EventRow) error
	// WriteSpan persists a materialized span (mirrors UpsertSpan).
	WriteSpan(ctx context.Context, sp *store.SpanRow) error
	// Flush forces any buffered rows to ClickHouse. Returns the number of
	// rows dropped (if the buffer overflowed and failed to drain).
	Flush(ctx context.Context) error
	// Close drains the buffer and releases the connection.
	Close() error
}

// Compile-time conformance.
var _ Sink = (*ClickHouse)(nil)

// row is one buffered ClickHouse row.
type row struct {
	kind string // "event" | "span"
	ev   *store.EventRow
	sp   *store.SpanRow
}

// ClickHouse is the batched, bounded-memory analytics sink.
type ClickHouse struct {
	conn  clickhouse.Conn
	lim   int    // buffer row limit (OBS_CH_BUFFER_MAX_ROWS)
	rows  []row  // buffered pending inserts
	log   *slog.Logger
	drops int64

	closeOnce sync.Once
	closed    chan struct{}

	mu sync.Mutex
}

// New connects to ClickHouse (clickhouse:// DSN, native or HTTP protocol
// per the driver's own parser) and ensures the target tables exist.
func New(ctx context.Context, dsn string, log *slog.Logger, bufferMaxRows int) (*ClickHouse, error) {
	if log == nil {
		log = slog.Default()
	}
	opts, err := clickhouse.ParseDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("analyticsch: parse dsn: %w", err)
	}
	conn, err := clickhouse.Open(opts)
	if err != nil {
		return nil, err
	}
	c := &ClickHouse{conn: conn, log: log, closed: make(chan struct{}),
		lim: bufferMaxRows}
	if c.lim <= 0 {
		c.lim = 100000
	}
	if err := c.ensureSchema(ctx); err != nil {
		conn.Close()
		return nil, err
	}
	return c, nil
}

// ChSchema is the DDL for the three analytics tables. It is the ClickHouse
// dialect of the SQLite reference schema (deploy/CLICKHOUSE-POSTGRES-NATS.md
// documents the intent; infrastructure/ owns the final DDL).
const ChSchema = `
CREATE TABLE IF NOT EXISTS events(
  project_id String,
  id String,
  run_id String,
  seq UInt64,
  type String,
  ts DateTime64(9),
  span_id String,
  parent_span_id String,
  agent_name String,
  agent_version String,
  model String,
  provider String,
  attrs String,
  payload_ref String,
  received_at DateTime64(9),
  event_month UInt16,
  ingested_month UInt16
) ENGINE = MergeTree
PARTITION BY event_month
ORDER BY (project_id, run_id, seq, ingested_month)
TTL toDateTime(ts) + INTERVAL 90 DAY;

CREATE TABLE IF NOT EXISTS spans(
  id String,
  project_id String,
  run_id String,
  parent_span_id String,
  name String,
  kind String,
  status String,
  agent_name String,
  agent_version String,
  tool_name String,
  tool_version String,
  tool_args_hash String,
  tool_schema_version String,
  model String,
  provider String,
  usage_source String,
  tokens_in Int64,
  tokens_out Int64,
  cost_usd Float64,
  cost_estimated UInt8,
  retries Int64,
  error_class String,
  started_at DateTime64(9),
  ended_at DateTime64(9),
  duration_ms Int64,
  seq_start UInt64,
  seq_end UInt64,
  version UInt32,
  span_month UInt16
) ENGINE = ReplacingMergeTree(version)
PARTITION BY (project_id, span_month)
ORDER BY (project_id, run_id, id);

CREATE TABLE IF NOT EXISTS runs(
  id String,
  project_id String,
  status String,
  agent_name String,
  agent_version String,
  model String,
  provider String,
  environment String,
  started_at DateTime64(9),
  ended_at DateTime64(9),
  duration_ms Int64,
  tokens_in Int64,
  tokens_out Int64,
  usage_source String,
  cost_usd Float64,
  cost_estimated UInt8,
  retries Int64,
  event_count Int64,
  seq_max UInt64,
  created_at DateTime64(9),
  run_month UInt16
) ENGINE = ReplacingMergeTree(seq_max)
PARTITION BY (project_id, run_month)
ORDER BY (project_id, id);
`

func (c *ClickHouse) ensureSchema(ctx context.Context) error {
	for _, stmt := range splitStatements(ChSchema) {
		if err := c.conn.Exec(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

// WriteEvents buffers event rows and flushes when the batch or the
// interval/row thresholds are reached.
func (c *ClickHouse) WriteEvents(ctx context.Context, evs []*store.EventRow) error {
	c.mu.Lock()
	for _, e := range evs {
		c.rows = append(c.rows, row{kind: "event", ev: e})
	}
	overflow := int64(0)
	if len(c.rows) > c.lim {
		// Bounded memory: drop the oldest rows, keep the newest window.
		keep := c.lim
		overflow = int64(len(c.rows) - keep)
		c.rows = c.rows[len(c.rows)-keep:]
		c.drops += overflow
	}
	full := len(c.rows) >= c.flushThreshold(c.lim)
	c.mu.Unlock()
	if overflow > 0 {
		c.log.Warn("clickhouse buffer overflow, dropping oldest rows",
			"dropped", overflow, "total_dropped", c.drops)
	}
	// Flush on size so bursts don't sit in memory; also rely on the
	// periodic flusher started by main for time-based draining.
	if full {
		return c.Flush(ctx)
	}
	return nil
}

// WriteSpan buffers a span row.
func (c *ClickHouse) WriteSpan(ctx context.Context, sp *store.SpanRow) error {
	c.mu.Lock()
	c.rows = append(c.rows, row{kind: "span", sp: sp})
	overflow := int64(0)
	if len(c.rows) > c.lim {
		keep := c.lim
		overflow = int64(len(c.rows) - keep)
		c.rows = c.rows[len(c.rows)-keep:]
		c.drops += overflow
	}
	full := len(c.rows) >= c.flushThreshold(c.lim)
	c.mu.Unlock()
	if overflow > 0 {
		c.log.Warn("clickhouse buffer overflow, dropping oldest rows",
			"dropped", overflow, "total_dropped", c.drops)
	}
	if full {
		return c.Flush(ctx)
	}
	return nil
}

func (c *ClickHouse) flushThreshold(lim int) int {
	// Flush when the buffer is at least a quarter full.
	if t := lim / 4; t > 0 {
		return t
	}
	return lim
}

// Flush drains the buffered rows into ClickHouse, batched. On failure the
// rows remain buffered (bounded by c.lim) so they are retried; a persistently
// unreachable ClickHouse therefore degrades to "bounded buffer + eventual
// drop on overflow" rather than blocking ingestion.
func (c *ClickHouse) Flush(ctx context.Context) error {
	c.mu.Lock()
	if len(c.rows) == 0 {
		c.mu.Unlock()
		return nil
	}
	batch := c.rows
	c.rows = nil
	c.mu.Unlock()

	const batchCap = 5000
	for i := 0; i < len(batch); i += batchCap {
		end := i + batchCap
		if end > len(batch) {
			end = len(batch)
		}
		chunk := batch[i:end]
		if err := c.insertChunk(ctx, chunk); err != nil {
			// Put the chunk back (bounded re-buffer); count drops on
			// overflow so nothing grows unbounded.
			c.mu.Lock()
			c.rows = append(chunk, c.rows...)
			overflow := int64(0)
			if len(c.rows) > c.lim {
				overflow = int64(len(c.rows) - c.lim)
				c.rows = c.rows[len(c.rows)-c.lim:]
				c.drops += overflow
			}
			c.mu.Unlock()
			if overflow > 0 {
				c.log.Warn("clickhouse flush failed, buffer overflow",
					"dropped", overflow, "total_dropped", c.drops)
			} else {
				c.log.Error("clickhouse flush failed, retrying later",
					"rows", len(chunk), "error", err)
			}
			return err
		}
	}
	return nil
}

func (c *ClickHouse) insertChunk(ctx context.Context, chunk []row) error {
	var evs []*store.EventRow
	var spans []*store.SpanRow
	for _, r := range chunk {
		switch r.kind {
		case "event":
			evs = append(evs, r.ev)
		case "span":
			spans = append(spans, r.sp)
		}
	}
	if len(evs) > 0 {
		if err := c.insertEvents(ctx, evs); err != nil {
			return err
		}
		// Run aggregates fold from the same event batch (ReplacingMergeTree
		// dedups; the latest seq_max version wins).
		runs := foldRuns(evs)
		if len(runs) > 0 {
			runList := make([]*store.RunAggregate, 0, len(runs))
			for _, r := range runs {
				runList = append(runList, r)
			}
			if err := c.insertRuns(ctx, runList); err != nil {
				return err
			}
		}
	}
	if len(spans) > 0 {
		if err := c.insertSpans(ctx, spans); err != nil {
			return err
		}
	}
	return nil
}

// ym is a toYYYYMM-style partition value (month as int).
func ym(t time.Time) uint16 {
	if t.IsZero() {
		return 0
	}
	return uint16(t.UTC().Year())*100 + uint16(t.UTC().Month())
}

func (c *ClickHouse) insertEvents(ctx context.Context, evs []*store.EventRow) error {
	batch, err := c.conn.PrepareBatch(ctx, eventInsertSQL())
	if err != nil {
		return err
	}
	for _, e := range evs {
		if e.Project == "" {
			continue
		}
		attrs := "{}"
		if e.Attrs != nil {
			if b, jerr := store.JSON(e.Attrs); jerr == nil {
				attrs = string(b)
			}
		}
		args := []any{
			e.Project, e.ID, e.Run, e.Seq, e.Type,
			nonZeroTime(e.TS), nullStr(e.Span), nullStr(e.Parent),
			nullStr(e.AgentName), nullStr(e.AgentVersion),
			nullStr(e.Model), nullStr(e.Provider),
			attrs, nullStr(e.PayloadRef), nonZeroTime(e.ReceivedAt),
			ym(nonZeroTime(e.TS)), ym(nonZeroTime(e.ReceivedAt)),
		}
		if err := batch.Append(args...); err != nil {
			return err
		}
	}
	return batch.Send()
}

func (c *ClickHouse) insertSpans(ctx context.Context, spans []*store.SpanRow) error {
	batch, err := c.conn.PrepareBatch(ctx, spanInsertSQL())
	if err != nil {
		return err
	}
	for _, sp := range spans {
		ended := nonZeroTime(sp.EndedAt)
		args := []any{
			sp.ID, sp.Project, sp.RunID, nullStr(sp.Parent), nullStr(sp.Name),
			nullStr(sp.Kind), nullStr(sp.Status),
			nullStr(sp.AgentName), nullStr(sp.AgentVersion),
			nullStr(sp.ToolName), nullStr(sp.ToolVersion),
			nullStr(sp.ToolArgsHash), nullStr(sp.ToolSchemaVer),
			nullStr(sp.Model), nullStr(sp.Provider), nullStr(sp.UsageSource),
			sp.TokensIn, sp.TokensOut, sp.CostUSD,
			boolInt(sp.CostEstimated), sp.Retries, nullStr(sp.ErrorClass),
			nonZeroTime(sp.StartedAt), ended,
			sp.DurationMS, sp.SeqStart, sp.SeqEnd,
			uint32(ended.Unix()), ym(ended),
		}
		if err := batch.Append(args...); err != nil {
			return err
		}
	}
	return batch.Send()
}

// RunFlushLoop periodically drains the buffer (time-based complement to the
// size-based flush in WriteEvents/WriteSpan). Runs until ctx is done.
func (c *ClickHouse) RunFlushLoop(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.closed:
			return
		case <-t.C:
			fctx, fcancel := context.WithTimeout(context.Background(), 30*time.Second)
			if err := c.Flush(fctx); err != nil {
				c.log.Warn("clickhouse periodic flush", "error", err)
			}
			fcancel()
		}
	}
}

// Close drains the buffer and releases the connection.
func (c *ClickHouse) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	// Best-effort final drain; a persistent outage means the last buffered
	// rows are lost (bounded: at most c.lim rows, documented drop policy).
	if err := c.Flush(context.Background()); err != nil {
		c.log.Warn("clickhouse final drain", "rows_dropped", c.Dropped(), "error", err)
	}
	return c.conn.Close()
}

// Dropped returns the total number of rows lost to buffer overflow.
func (c *ClickHouse) Dropped() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.drops
}

// ---- SQL builders & helpers ----

// Plain INSERT targets (explicit column lists, plain VALUES batches — the
// standard clickhouse-go v2 batch pattern).
func eventInsertSQL() string {
	return `INSERT INTO events(project_id, id, run_id, seq, type, ts, span_id,
		parent_span_id, agent_name, agent_version, model, provider, attrs,
		payload_ref, received_at, event_month, ingested_month)`
}

func spanInsertSQL() string {
	return `INSERT INTO spans(id, project_id, run_id, parent_span_id, name, kind,
		status, agent_name, agent_version, tool_name, tool_version,
		tool_args_hash, tool_schema_version, model, provider, usage_source,
		tokens_in, tokens_out, cost_usd, cost_estimated, retries, error_class,
		started_at, ended_at, duration_ms, seq_start, seq_end, version, span_month)`
}

func runInsertSQL() string {
	return `INSERT INTO runs(id, project_id, status, agent_name, agent_version,
		model, provider, environment, started_at, ended_at, duration_ms,
		tokens_in, tokens_out, usage_source, cost_usd, cost_estimated, retries,
		event_count, seq_max, created_at, run_month)`
}

// foldRuns derives the per-run aggregate from a batch of events — the same
// folding logic as the SQLite store's upsertRunTx, written once per run per
// flush so ReplacingMergeTree dedups by the highest seq_max version.
func foldRuns(evs []*store.EventRow) map[string]*store.RunAggregate {
	out := map[string]*store.RunAggregate{}
	for _, e := range evs {
		if e.Project == "" {
			continue
		}
		r := out[e.Run]
		if r == nil {
			r = &store.RunAggregate{RunID: e.Run, Project: e.Project,
				Status: "open", Environment: "prod",
				AgentName: e.AgentName, AgentVersion: e.AgentVersion,
				Model: e.Model, Provider: e.Provider,
				StartedAt: e.TS, UsageSource: "provider"}
			out[e.Run] = r
		} else {
			// Keep first-seen identity fields when new ones are empty.
			if r.AgentName == "" {
				r.AgentName = e.AgentName
			}
			if r.AgentVersion == "" {
				r.AgentVersion = e.AgentVersion
			}
			if r.Model == "" {
				r.Model = e.Model
			}
			if r.Provider == "" {
				r.Provider = e.Provider
			}
		}
		if r.StartedAt.IsZero() {
			r.StartedAt = e.TS
		}
		r.EventCount++
		if e.Seq > r.SeqMax {
			r.SeqMax = e.Seq
		}
		switch e.Type {
		case "run.completed", "run.failed", "run.cancelled":
			r.Status = e.Type[len("run."):]
			r.EndedAt = e.TS
			if !r.StartedAt.IsZero() {
				if ms := int64(e.TS.Sub(r.StartedAt).Milliseconds()); ms > r.DurationMS {
					r.DurationMS = ms
				}
			}
		}
		// Fold usage attributes (same keys the SQLite store folds).
		if v, ok := e.Attrs["model.tokens_in"]; ok {
			r.TokensIn += int64(store.AsFloat(v))
		}
		if v, ok := e.Attrs["model.tokens_out"]; ok {
			r.TokensOut += int64(store.AsFloat(v))
		}
		if v, ok := e.Attrs["model.retries"]; ok {
			r.Retries += int64(store.AsFloat(v))
		}
		if v, ok := e.Attrs["cost.usd"]; ok {
			r.CostUSD += store.AsFloat(v)
			if est, ok := e.Attrs["cost.estimated"]; ok && store.AsBool(est) {
				r.CostEstimated = true
			}
		}
		if src, ok := e.Attrs["usage.source"]; ok {
			if s2, ok := src.(string); ok && s2 != "" {
				r.UsageSource = s2
			}
		}
	}
	return out
}

func (c *ClickHouse) insertRuns(ctx context.Context, runs []*store.RunAggregate) error {
	batch, err := c.conn.PrepareBatch(ctx, runInsertSQL())
	if err != nil {
		return err
	}
	for _, r := range runs {
		ended := nonZeroTime(r.EndedAt)
		args := []any{
			r.RunID, r.Project, r.Status, nullStr(r.AgentName),
			nullStr(r.AgentVersion), nullStr(r.Model), nullStr(r.Provider),
			nullStr(r.Environment),
			nonZeroTime(r.StartedAt), ended, r.DurationMS,
			r.TokensIn, r.TokensOut, nullStr(r.UsageSource), r.CostUSD,
			boolInt(r.CostEstimated), r.Retries, r.EventCount, r.SeqMax,
			ended, ym(ended),
		}
		if err := batch.Append(args...); err != nil {
			return err
		}
	}
	return batch.Send()
}

func nonZeroTime(t time.Time) time.Time {
	if t.IsZero() {
		return time.Unix(0, 0).UTC()
	}
	return t.UTC()
}

func nullStr(s string) any {
	if s == "" {
		return ""
	}
	return s
}

func boolInt(b bool) uint8 {
	if b {
		return 1
	}
	return 0
}

func splitStatements(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	// The schema is fixed DDL with no ';' inside literals; a plain split is safe.
	return strings.Split(s, ";")
}
