package analyticsch

import (
	"context"
	"time"

	"github.com/ensembyte/observatory/internal/store"
)

// Composite wraps a primary metadata store with the ClickHouse analytics
// sink: events and spans are written to the primary (Postgres or the dev
// SQLite) AND mirrored to ClickHouse. All other Engine methods pass straight
// through to the primary, so the pipeline layer never learns the difference
// (zero pipeline changes — the sink is bolted on by embedding store.Engine).
//
// If ClickHouse is unreachable the sink buffers (bounded) and the primary
// write already succeeded, so ingestion is not blocked by an analytics
// outage. WriteEvents/WriteSpan only surface an error on overflow.
type Composite struct {
	store.Engine
	sink *ClickHouse
}

// WithAnalytics returns an Engine that mirrors events/spans to the sink.
func WithAnalytics(primary store.Engine, sink *ClickHouse) store.Engine {
	return &Composite{Engine: primary, sink: sink}
}

// InsertEvents persists to the primary first, then mirrors to ClickHouse.
func (c *Composite) InsertEvents(ctx context.Context, evs []*store.EventRow) error {
	if err := c.Engine.InsertEvents(ctx, evs); err != nil {
		return err
	}
	return c.sink.WriteEvents(ctx, evs)
}

// UpsertSpan persists to the primary first, then mirrors to ClickHouse.
func (c *Composite) UpsertSpan(ctx context.Context, sp *store.SpanRow) error {
	if err := c.Engine.UpsertSpan(ctx, sp); err != nil {
		return err
	}
	return c.sink.WriteSpan(ctx, sp)
}

// AlertMetrics forwarding so alertsvc's built-in detectors keep working on
// the primary store when analytics mirroring is enabled.
func (c *Composite) AlertFindingKind(ctx context.Context, project, kind string, since time.Time) int64 {
	am, _ := c.Engine.(store.AlertMetrics)
	if am == nil {
		return 0
	}
	return am.AlertFindingKind(ctx, project, kind, since)
}

func (c *Composite) AlertRunStatusCounts(ctx context.Context, project string, since time.Time) (total, failed int64) {
	am, _ := c.Engine.(store.AlertMetrics)
	if am == nil {
		return 0, 0
	}
	return am.AlertRunStatusCounts(ctx, project, since)
}

func (c *Composite) AlertLatencyRegressions(ctx context.Context, project string, since time.Time) int64 {
	am, _ := c.Engine.(store.AlertMetrics)
	if am == nil {
		return 0
	}
	return am.AlertLatencyRegressions(ctx, project, since)
}

var _ store.Engine = (*Composite)(nil)
var _ store.AlertMetrics = (*Composite)(nil)
