package store

import (
	"testing"
)

// HashKey is deterministic and irreversible (plaintext keys are never
// persisted; the lookup path hashes and compares).
func TestHashKeyStableAndOpaque(t *testing.T) {
	a, b := HashKey("obs_abc"), HashKey("obs_abc")
	if a != b {
		t.Fatalf("same key produced different hashes: %q vs %q", a, b)
	}
	if HashKey("obs_abc") == HashKey("obs_xyz") {
		t.Fatal("different keys produced the same hash")
	}
	// 64 hex chars = sha256; the plaintext must not appear.
	if len(a) != 64 {
		t.Fatalf("hash length = %d, want 64", len(a))
	}
	if contains(a, "obs_abc") {
		t.Fatalf("plaintext leaked into the hash: %q", a)
	}
}

// Tenant isolation (spec NFR): a project key can only resolve its own
// project; a key from one project must not read another's data.
func TestTenantIsolation(t *testing.T) {
	s := openTestStore(t)
	if err := s.CreateProject(ctx0(), "acme", "Acme", "dev"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateProject(ctx0(), "globex", "Globex", "prod"); err != nil {
		t.Fatal(err)
	}
	// Distinct keys per tenant.
	_ = s.CreateAPIKey(ctx0(), "acme", "k1", "acme-key")
	_ = s.CreateAPIKey(ctx0(), "globex", "k1", "globex-key")

	proj, ok, err := s.LookupProjectForKey(ctx0(), "acme-key")
	if err != nil || !ok || proj != "acme" {
		t.Fatalf("acme key resolved to %q (ok=%v err=%v), want acme", proj, ok, err)
	}
	// A foreign key does not resolve to another tenant.
	if p, ok, _ := s.LookupProjectForKey(ctx0(), "globex-key"); ok && p != "globex" {
		t.Fatalf("globex key resolved to %q", p)
	}
	// An unknown key resolves to nothing.
	if _, ok, _ := s.LookupProjectForKey(ctx0(), "nope"); ok {
		t.Fatal("unknown key resolved to a project")
	}
}

// Run timelines are scoped by project: a run written under acme is invisible
// when queried under globex.
func TestRunTimelineScopedByProject(t *testing.T) {
	s := openTestStore(t)
	_ = s.CreateProject(ctx0(), "acme", "Acme", "dev")
	_ = s.CreateProject(ctx0(), "globex", "Globex", "prod")

	acme := []*EventRow{{
		ID: "e1", Project: "acme", Run: "run-1", Seq: 1,
		Type: "run.started", TS: mustTime(t), Attrs: map[string]any{},
	}}
	if err := s.InsertEvents(ctx0(), acme); err != nil {
		t.Fatal(err)
	}
	// Querying the same run id under a different project must be empty.
	got, err := s.RunTimeline(ctx0(), "globex", "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Events) != 0 {
		t.Fatalf("cross-tenant leak: globex saw %d acme events", len(got.Events))
	}
	// ...and the owning project sees it.
	got, _ = s.RunTimeline(ctx0(), "acme", "run-1")
	if len(got.Events) != 1 {
		t.Fatalf("acme saw %d events, want 1", len(got.Events))
	}
}

// FilterUnseen dedups: an id already processed is not returned again.
func TestFilterUnseenDedups(t *testing.T) {
	s := openTestStore(t)
	first := s.FilterUnseen(ctx0(), "acme", []string{"k1", "k2"})
	if len(first) != 2 {
		t.Fatalf("first pass should report 2 unseen, got %d", len(first))
	}
	second := s.FilterUnseen(ctx0(), "acme", []string{"k1", "k2", "k3"})
	if len(second) != 1 || second[0] != "k3" {
		t.Fatalf("second pass should report only k3, got %v", second)
	}
}

func TestPayloadCaptureOffDropsRef(t *testing.T) {
	// mapEnvelope is in the pipeline; this test documents the store-side
	// invariant that an empty payload_ref is stored as NULL, not "off".
	s := openTestStore(t)
	_ = s.CreateProject(ctx0(), "acme", "Acme", "dev")
	rows := []*EventRow{{
		ID: "e1", Project: "acme", Run: "r", Seq: 1,
		Type: "run.started", TS: mustTime(t),
		Attrs: map[string]any{}, PayloadRef: "",
	}}
	if err := s.InsertEvents(ctx0(), rows); err != nil {
		t.Fatal(err)
	}
	tl, err := s.RunTimeline(ctx0(), "acme", "r")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range tl.Events {
		if e.PayloadRef != "" {
			t.Fatalf("capture-off payload_ref not nulled: %q", e.PayloadRef)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
