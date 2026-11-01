// Package queue abstracts durable event processing. Dev mode is a file-based
// durable queue; production is NATS JetStream (internal/queue/nats.go,
// build tag "nats"). Both guarantee at-least-once delivery; the pipeline
// makes processing idempotent, so delivery guarantees + idempotency yield
// effective-once (spec NFR).
package queue

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

// Message is one durable unit of work (a batch of events).
type Message struct {
	ID         string    `json:"id"`
	ProjectID  string    `json:"project_id"`
	Payload    []byte    `json:"payload"`
	EnqueuedAt time.Time `json:"enqueued_at"`
}

// MessageID is content-addressed so retried batches are deduped.
func MessageID(project string, payload []byte) string {
	sum := sha256.Sum256(payload)
	return project + "." + hex.EncodeToString(sum[:8])
}

// Queue is the durable queue contract.
type Queue interface {
	// Enqueue persists the message; duplicate IDs are no-ops.
	Enqueue(ctx context.Context, m Message) error
	// Pull moves up to n pending messages to in-flight and returns them.
	Pull(ctx context.Context, n int) []Message
	// Ack completes a message. At-least-once: an unacked message is re-queued
	// by Restart when the process restarts.
	Ack(m Message) error
	// Restart re-queues in-flight messages (crash recovery).
	Restart() error
	// Depth is the pending backlog size (observability).
	Depth() int
	Close() error
}

// BatchPayload is the canonical queue payload: a batch of envelopes.
type BatchPayload struct {
	Project string       `json:"project"`
	Events  []json.RawMessage `json:"events"`
}
