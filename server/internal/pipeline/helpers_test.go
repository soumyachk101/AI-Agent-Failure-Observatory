package pipeline

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ensembyte/observatory/internal/store"
)

// ctx0 is the trivial context every pipeline test uses.
func ctx0() context.Context { return context.Background() }

// openTestStore returns a migrated embedded SQLite store for the lifetime of
// the test. Each test gets an isolated data dir, so no cross-test state.
func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := db.Migrate(ctx0()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
