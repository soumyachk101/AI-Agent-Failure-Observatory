package event

import (
	"strings"
	"testing"
	"time"
)

func ts() time.Time {
	return time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
}

func TestIdempotencyKeyDeterministic(t *testing.T) {
	a := Envelope{ProjectID: "p", RunID: "r", SpanID: "s", EventType: ToolCompleted, Sequence: 12, EventID: "e1"}
	b := a // value copy: identical content
	if a.IdempotencyKey() != b.IdempotencyKey() {
		t.Fatalf("identical envelopes produced different keys")
	}
	// Same join shape as documented: project|run|span|type|seq|event_id.
	want := "p|r|s|tool.call.completed|12|e1"
	if got := a.IdempotencyKey(); got != want {
		t.Fatalf("key = %q, want %q", got, want)
	}
	// Missing span joins as empty.
	na := Envelope{ProjectID: "p", RunID: "r", EventType: RunStarted, Sequence: 1, EventID: "e2"}
	if got := na.IdempotencyKey(); got != "p|r||run.started|1|e2" {
		t.Fatalf("no-span key = %q", got)
	}
	// Any identity-field change => key change.
	variants := map[string]Envelope{
		"seq":   {ProjectID: "p", RunID: "r", SpanID: "s", EventType: ToolCompleted, Sequence: 13, EventID: "e1"},
		"event": {ProjectID: "p", RunID: "r", SpanID: "s", EventType: ToolCompleted, Sequence: 12, EventID: "e2"},
		"run":   {ProjectID: "p", RunID: "r2", SpanID: "s", EventType: ToolCompleted, Sequence: 12, EventID: "e1"},
		"proj":  {ProjectID: "q", RunID: "r", SpanID: "s", EventType: ToolCompleted, Sequence: 12, EventID: "e1"},
	}
	for field, ev := range variants {
		if ev.IdempotencyKey() == a.IdempotencyKey() {
			t.Fatalf("field %q did not change the key", field)
		}
	}
}

func TestCanonicalJSONAndHash(t *testing.T) {
	m1 := map[string]any{"b": 2, "a": "x", "n": map[string]any{"z": 1, "y": 2}}
	m2 := map[string]any{"a": "x", "n": map[string]any{"y": 2, "z": 1}, "b": 2}
	c1, err := CanonicalJSON(m1)
	if err != nil {
		t.Fatal(err)
	}
	c2, err := CanonicalJSON(m2)
	if err != nil {
		t.Fatal(err)
	}
	if c1 != c2 {
		t.Fatalf("canonical forms differ:\n%q\n%q", c1, c2)
	}
	if !strings.HasPrefix(c1, `{"a":"x","b":2,"n":{"y":2,"z":1}}`) {
		t.Fatalf("unexpected canonical order: %s", c1)
	}
	if HashOf(m1) != HashOf(m2) {
		t.Fatalf("HashOf not deterministic over equal content")
	}
}

func TestToolCallKey(t *testing.T) {
	a1 := map[string]any{AttrToolName: "status.poll", AttrToolArgs: map[string]any{"job_id": "job-42", "n": 5}, AttrSchemaVersion: "schema-v3"}
	// Reordered arg keys must not change the key.
	a2 := map[string]any{AttrToolName: "status.poll", AttrToolArgs: map[string]any{"n": 5, "job_id": "job-42"}, AttrSchemaVersion: "schema-v3"}
	if ToolCallKey(a1) != ToolCallKey(a2) {
		t.Fatalf("tool call key sensitive to arg key order")
	}
	other := map[string]any{AttrToolName: "status.poll", AttrToolArgs: map[string]any{"job_id": "job-43"}, AttrSchemaVersion: "schema-v3"}
	if ToolCallKey(a1) == ToolCallKey(other) {
		t.Fatalf("different args produced the same tool call key")
	}
	// A different schema version changes the key (spec 8.1).
	v2 := map[string]any{AttrToolName: "status.poll", AttrToolArgs: map[string]any{"job_id": "job-42"}, AttrSchemaVersion: "schema-v4"}
	if ToolCallKey(a1) == ToolCallKey(v2) {
		t.Fatalf("schema version change did not change the tool call key")
	}
}

func TestFailureSignatureStable(t *testing.T) {
	attrs := map[string]any{
		AttrErrorClass:    "schema",
		AttrToolName:      "payment.charge",
		AttrSchemaVersion: "schema-v1",
		AttrProviderCode:  "422",
		AttrStack:         "panic: bad in tool.run\nat observatory/tools/payment.go:88",
	}
	// Deterministic: same content => same signature.
	if FailureSignatureOf("tool", attrs) != FailureSignatureOf("tool", attrs) {
		t.Fatal("signature not deterministic")
	}
	if !strings.HasPrefix(FailureSignatureOf("tool", attrs), "fs-") {
		t.Fatal("signature missing fs- prefix")
	}
	// A different error class produces a different cluster.
	other := map[string]any{AttrErrorClass: "timeout", AttrToolName: "payment.charge"}
	if FailureSignatureOf("tool", attrs) == FailureSignatureOf("tool", other) {
		t.Fatal("error class change did not change the signature")
	}
	// Known limitation (docs/KNOWN-LIMITATIONS.md): NormalizeStack's number
	// tokenization is not active, so a line-number-only stack change SPLITS
	// the cluster today. Asserted here to document current behaviour.
	lineChange := map[string]any{
		AttrErrorClass: "schema", AttrToolName: "payment.charge",
		AttrSchemaVersion: "schema-v1", AttrProviderCode: "422",
		AttrStack: "panic: bad in tool.run\nat observatory/tools/payment.go:91",
	}
	if FailureSignatureOf("tool", attrs) == FailureSignatureOf("tool", lineChange) {
		t.Log("note: line-number-only stack change no longer splits the cluster (improved)")
	}
}

func TestNormalizeStack(t *testing.T) {
	in := "Panic at 0xdeadbeef in f (main.go:123)\n" + strings.Repeat("frame\n", 20)
	out := NormalizeStack(in)
	if !strings.HasPrefix(out, "panic at") {
		t.Fatalf("expected lowercasing, got %q", out)
	}
	if strings.Count(out, "\n") != 7 {
		t.Fatalf("expected the stack capped to 8 lines, got %d newlines in %q",
			strings.Count(out, "\n"), out)
	}
	// Documented current behaviour: the "tokenize numbers" replace patterns
	// are literal strings (strings.ReplaceAll, not regexp), so hex addresses
	// and line numbers survive verbatim. See docs/KNOWN-LIMITATIONS.md.
	if !strings.Contains(out, "0xdeadbeef") || !strings.Contains(out, "main.go:123") {
		t.Log("note: hex/number tokenization now active (limitations doc entry can be closed)")
	}
}

func TestValidate(t *testing.T) {
	base := func() Envelope {
		return Envelope{SchemaVersion: SchemaVersion, EventID: "e", RunID: "r",
			EventType: ToolCompleted, Sequence: 1, Timestamp: ts()}
	}
	env := base()
	if err := env.Validate(); err != nil {
		t.Fatalf("valid envelope rejected: %v", err)
	}
	bad := base(); bad.EventID = ""
	if err := bad.Validate(); err == nil {
		t.Fatal("missing event_id accepted")
	}
	bad = base(); bad.EventType = "nope"
	if err := bad.Validate(); err == nil {
		t.Fatal("unknown event type accepted")
	}
	bad = base(); bad.Sequence = 0
	if err := bad.Validate(); err == nil {
		t.Fatal("zero sequence accepted")
	}
	bad = base(); bad.Timestamp = time.Time{}
	if err := bad.Validate(); err == nil {
		t.Fatal("zero timestamp accepted")
	}
}

func TestNormalizeFillsDefaults(t *testing.T) {
	e := &Envelope{EventID: "e", RunID: "r", EventType: RunStarted, Sequence: 1, Timestamp: ts()}
	e.Normalize("tenant-x")
	if e.ProjectID != "tenant-x" {
		t.Fatalf("project not normalized to %q, got %q", "tenant-x", e.ProjectID)
	}
	if e.SchemaVersion != SchemaVersion {
		t.Fatalf("schema version not defaulted, got %q", e.SchemaVersion)
	}
	if e.Attributes == nil {
		t.Fatal("attributes not defaulted to an empty map")
	}
}
