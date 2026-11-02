package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// ctx0 is the trivial context every store test uses.
func ctx0() context.Context { return context.Background() }

// mustTime returns a fixed instant for tests that need a non-zero time.
func mustTime(t *testing.T) time.Time {
	t.Helper()
	return time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
}

// openTestStore gives each test an isolated, migrated embedded store.
func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := s.Migrate(ctx0()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
