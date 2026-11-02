// Alert metrics for the dev SQLite store (implements store.AlertMetrics).
// alertsvc consumes these instead of raw SQL so the same calls work on any
// Engine (Postgres keeps the semantics; see storepg).
package store

import (
	"context"
	"time"
)

// AlertFindingKind counts findings of a kind since a window start.
func (s *Store) AlertFindingKind(ctx context.Context, project, kind string, since time.Time) int64 {
	var n int64
	err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(COUNT(*),0) FROM findings
		WHERE project_id = ? AND kind = ? AND created_at >= ?`,
		project, kind, since.UTC().Format(time.RFC3339)).Scan(&n)
	if err != nil {
		return 0
	}
	return n
}

// AlertRunStatusCounts reports open and failed runs since a window start,
// the two numbers the failure-rate detector needs.
func (s *Store) AlertRunStatusCounts(ctx context.Context, project string, since time.Time) (total, failed int64) {
	err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(CASE WHEN status='open' THEN 1 ELSE 0 END),0),
		       COALESCE(SUM(CASE WHEN status='failed' THEN 1 ELSE 0 END),0)
		FROM runs WHERE project_id = ? AND started_at >= ?`,
		project, since.UTC().Format(time.RFC3339)).Scan(&total, &failed)
	if err != nil {
		return 0, 0
	}
	return total, failed
}

// AlertLatencyRegressions counts runs whose duration is more than 2x the
// average duration over the same window (one pass, window aggregate).
func (s *Store) AlertLatencyRegressions(ctx context.Context, project string, since time.Time) int64 {
	var n int64
	err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(CASE WHEN duration_ms > 2 * avg_ms THEN 1 ELSE 0 END),0)
		FROM (
			SELECT duration_ms, AVG(duration_ms) OVER () AS avg_ms
			FROM runs WHERE project_id = ? AND started_at >= ? AND duration_ms > 0
		)`, project, since.UTC().Format(time.RFC3339)).Scan(&n)
	if err != nil {
		return 0
	}
	return n
}
