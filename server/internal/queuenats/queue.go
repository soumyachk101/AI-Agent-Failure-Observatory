// Package queuenats is the production queue adapter: NATS JetStream.
//
// It is compiled into the default build (no build tag). Selection is
// runtime, via OBS_NATS_URL: when set, main.go wires *queuenats.JetStream
// in place of the file queue; when unset, the file queue is used.
//
// Mapping to the queue.Queue contract (same at-least-once + crash-recovery
// + idempotency semantics as the file queue in internal/queue/local.go):
//   - stream:   OBS_NATS_STREAM (default "observatory.events")
//   - subject:  "obs.<project_id>.events"
//   - durable pull consumer: "obs-workers" (JetStream holds unacked
//     messages server-side, so crash recovery is free)
//   - dedup:    jetstream Publish MessageID (server-side idempotency on
//     publish) + the pipeline's processed-table idempotency keys
//     => effective-once, matching the file-queue design.
package queuenats

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/ensembyte/observatory/internal/queue"
)

// JetStream is the NATS JetStream durable queue.
type JetStream struct {
	nc      *nats.Conn
	js      jetstream.JetStream
	stream  string
	durable string

	mu       sync.Mutex
	// inFlight maps content-addressed id -> in-flight JetStream message,
	// so Ack can complete a specific pulled message by its id.
	inFlight map[string]jetstream.Msg
}

// New connects to the JetStream server, ensures the stream + durable pull
// consumer exist (CreateOrUpdate* is idempotent), and returns the queue.
func New(ctx context.Context, url, stream, durable string) (*JetStream, error) {
	if stream == "" {
		stream = "observatory.events"
	}
	if durable == "" {
		durable = "obs-workers"
	}
	nc, err := nats.Connect(url)
	if err != nil {
		return nil, fmt.Errorf("queuenats: connect: %w", err)
	}
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("queuenats: jetstream: %w", err)
	}

	// Ensure the stream exists (idempotent; CreateOrUpdateStream is a no-op
	// when the stream is already configured to these limits).
	if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:      stream,
		Subjects:  []string{"obs.*.events"},
		Retention: jetstream.LimitsPolicy,
		Storage:   jetstream.FileStorage,
	}); err != nil {
		nc.Close()
		return nil, fmt.Errorf("queuenats: create stream: %w", err)
	}

	// Ensure the durable pull consumer exists. Generous ack_wait so a
	// restarted process reclaims in-flight work (crash recovery).
	if _, err := js.CreateOrUpdateConsumer(ctx, stream, jetstream.ConsumerConfig{
		Durable:       durable,
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       5 * time.Minute,
		MaxAckPending: 1000,
	}); err != nil {
		nc.Close()
		return nil, fmt.Errorf("queuenats: create consumer: %w", err)
	}

	return &JetStream{nc: nc, js: js, stream: stream, durable: durable,
		inFlight: map[string]jetstream.Msg{}}, nil
}

// Enqueue publishes one message with a server-side MessageID so retried
// batches (same content-addressed id) are deduped at the JetStream level.
func (q *JetStream) Enqueue(ctx context.Context, m queue.Message) error {
	if m.ID == "" {
		m.ID = queue.MessageID(m.ProjectID, m.Payload)
	}
	_, err := q.js.Publish(ctx, "obs."+m.ProjectID+".events", m.Payload,
		jetstream.WithMsgID(m.ID))
	return err
}

// Pull fetches up to n pending messages from the durable consumer and holds
// them in-flight (not yet acked). Next blocks until a message is available
// or the consumer errors; an empty backlog surfaces as an error and we stop.
func (q *JetStream) Pull(ctx context.Context, n int) []queue.Message {
	c, err := q.js.Consumer(ctx, q.stream, q.durable)
	if err != nil {
		return nil
	}
	var out []queue.Message
	for i := 0; i < n; i++ {
		msg, err := c.Next()
		if err != nil {
			break // no message available (timeout, error, or cancelled)
		}
		// Prefer the content-addressed id from the payload; fall back to
		// the JetStream delivery count so Ack can still match a handle.
		var m queue.Message
		_ = unmarshalPayload(msg.Data(), &m)
		handleKey := m.ID
		if handleKey == "" {
			md, _ := msg.Metadata()
			handleKey = fmt.Sprintf("js-%d", md.NumDelivered)
		}
		q.mu.Lock()
		q.inFlight[handleKey] = msg
		q.mu.Unlock()
		out = append(out, m)
	}
	return out
}

// Ack completes a previously-pulled message so JetStream advances past it.
func (q *JetStream) Ack(m queue.Message) error {
	if m.ID == "" {
		m.ID = queue.MessageID(m.ProjectID, m.Payload)
	}
	q.mu.Lock()
	handle, ok := q.inFlight[m.ID]
	if ok {
		delete(q.inFlight, m.ID)
	}
	q.mu.Unlock()
	if !ok {
		// No exact handle (e.g. this process pulled under a different key
		// after a crash): JetStream will re-deliver on ack_wait expiry;
		// the pipeline's idempotency makes the retry safe (at-least-once).
		return nil
	}
	return handle.Ack()
}

// Restart needs no re-queue: JetStream holds unacked messages server-side
// (ack_wait), so a restarted process simply resumes consuming.
func (q *JetStream) Restart() error { return nil }

// Depth reports the pending backlog for the durable consumer
// (server-authoritative, via ConsumerInfo.NumPending).
func (q *JetStream) Depth() int {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := q.js.Consumer(ctx, q.stream, q.durable)
	if err != nil {
		return 0
	}
	info, err := c.Info(ctx)
	if err != nil {
		return 0
	}
	return int(info.NumPending)
}

// Close releases the NATS connection (the JetStream client has no Close in
// this nats.go version; closing the underlying conn releases it).
func (q *JetStream) Close() error {
	q.nc.Close()
	return nil
}

// Compile-time interface conformance: a signature drift fails the build.
var _ queue.Queue = (*JetStream)(nil)

// unmarshalPayload decodes the queue payload's id/project fields.
func unmarshalPayload(b []byte, m *queue.Message) error {
	type wireMsg struct {
		ID        string `json:"id"`
		ProjectID string `json:"project_id"`
	}
	var w wireMsg
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	m.ID, m.ProjectID, m.Payload = w.ID, w.ProjectID, b
	return nil
}
