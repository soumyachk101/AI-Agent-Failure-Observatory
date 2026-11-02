// Package pipeline implements the normaliser: event validation ->
// dedup -> payload policy -> row mapping -> store insert. The run
// assembler (assembler.go) and rule engine (rules.go) run after insert so
// detectors see durable evidence.
package pipeline

import (
	"context"
	"strings"
	"time"

	"github.com/ensembyte/observatory/internal/event"
	"github.com/ensembyte/observatory/internal/store"
)

// ProjectPolicy is the per-project view the normaliser needs.
type ProjectPolicy struct {
	ID             string
	PayloadCapture string // "off" | "on"
	RedactFields   []string
	RatePerMin     int
	RateBurst      int
}

// NormalizeResult reports what happened to a batch so the API can reply
// per-event (accepted / dedup / rejected).
type NormalizeResult struct {
	Accepted  []string // event ids written
	Rejected  []string // event ids that failed validation
	RejectedReasons map[string]string
}

// Normalizer turns envelopes into durable rows.
type Normalizer struct {
	Store      store.Engine
	RedactKeys []string
}

// Run normalizes and persists a batch. idsSeen deduplicates against the
// processed table (at-least-once + idempotent => effective-once).
func (n *Normalizer) Run(ctx context.Context, project ProjectPolicy, envelopes []*event.Envelope) (*NormalizeResult, error) {
	res := &NormalizeResult{RejectedReasons: map[string]string{}}
	if len(envelopes) == 0 {
		return res, nil
	}

	// 1. Validate + normalize.
	valid := make([]*event.Envelope, 0, len(envelopes))
	for _, e := range envelopes {
		ce := *e
		ce.Normalize(project.ID)
		if err := ce.Validate(); err != nil {
			res.Rejected = append(res.Rejected, ce.EventID)
			res.RejectedReasons[ce.EventID] = err.Error()
			continue
		}
		valid = append(valid, &ce)
	}
	if len(valid) == 0 {
		return res, nil
	}

	// 2. Idempotency: drop anything already processed.
	ids := make([]string, 0, len(valid))
	for _, e := range valid {
		ids = append(ids, e.IdempotencyKey())
	}
	unseen := n.Store.FilterUnseen(ctx, project.ID, ids)
	if unseen == nil {
		return res, nil // whole batch already processed
	}
	unseenSet := make(map[string]bool, len(unseen))
	for _, id := range unseen {
		unseenSet[id] = true
	}

	// 3. Payload policy: redact when capture is on (off => drop payload
	//    refs entirely so raw evidence is never persisted).
	rows := make([]*store.EventRow, 0, len(valid))
	for _, e := range valid {
		if !unseenSet[e.IdempotencyKey()] {
			continue
		}
		row := mapEnvelope(e, project)
		rows = append(rows, row)
		res.Accepted = append(res.Accepted, e.EventID)
	}
	if len(rows) == 0 {
		return res, nil
	}

	// 4. Persist.
	if err := n.Store.InsertEvents(ctx, rows); err != nil {
		return res, err
	}

	// 5. Assembler + rule engine operate on the persisted rows.
	if err := n.Assemble(ctx, project, rows); err != nil {
		return res, err
	}
	if err := n.Detect(ctx, project, rows); err != nil {
		return res, err
	}
	return res, nil
}

// mapEnvelope converts an envelope to a store row, applying payload policy.
func mapEnvelope(e *event.Envelope, p ProjectPolicy) *store.EventRow {
	attrs := e.Attributes
	payloadRef := ""
	if e.PayloadRef != nil {
		payloadRef = *e.PayloadRef
	}
	if p.PayloadCapture == "on" && payloadRef != "" {
		attrs = event.Redact(attrs, p.RedactFields)
	} else {
		// Capture off by default: drop the payload reference so raw content
		// is never retained (spec: payload capture opt-in).
		payloadRef = ""
	}
	row := &store.EventRow{
		ID:           e.EventID,
		Project:      p.ID,
		Run:          e.RunID,
		Span:         e.SpanID,
		Parent:       e.ParentSpanID,
		Seq:          e.Sequence,
		Type:         e.EventType,
		TS:           e.Timestamp,
		Attrs:        attrs,
		PayloadRef:   payloadRef,
		ReceivedAt:   time.Now().UTC(),
	}
	if e.Agent != nil {
		row.AgentName, row.AgentVersion = e.Agent.Name, e.Agent.Version
	}
	if e.Provider != nil {
		row.Model, row.Provider = e.Provider.Model, e.Provider.Name
	}
	return row
}

// splitProjectPolicy exposes a project's redact fields as a list.
func SplitFields(csv string) []string {
	if csv == "" {
		return nil
	}
	var out []string
	for _, f := range strings.Split(csv, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}
