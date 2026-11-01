//go:build nats

// Package queue — production target: NATS JetStream.
//
// This file is a build-tagged reference implementation for deployments where
// NATS JetStream is available. It is NOT compiled in the default build and
// adds no dependency to the module; wire it up in internal/worker when
// deploying against a real JetStream cluster.
//
// Mapping to the Queue contract:
//   - stream:  "observatory.events"
//   - subject: "obs.<project_id>.events"
//   - durable consumer: "obs-workers", ack_wait generous, max_ack_pending bounded.
//   - at-least-once + pipeline idempotency keys => effective-once processing.
//
// import (
//
//	"github.com/nats-io/nats.go/jetstream"
// )
//
// func NewJetStreamQueue(url, stream string) (*JetStreamQueue, error) {
//	nc, err := nats.Connect(url)
//	if err != nil {
//		return nil, err
//	}
//	js, err := jetstream.New(nc)
//	if err != nil {
//		return nil, err
//	}
//	_, err = js.CreateStream(nats.JSStreamConfig{
//		Name:      stream,
//		Subjects:  []string{"obs.*.events"},
//		Retention: nats.WorkFileRetention,
//		Storage:   nats.FileStorageType,
//	})
//	if err != nil {
//		return nil, err
//	}
//	return &JetStreamQueue{js: js, stream: stream}, nil
// }
//
// type JetStreamQueue struct {
//	js     jetstream.JetStreamContext
//	stream string
// }
//
// func (q *JetStreamQueue) Enqueue(ctx context.Context, m Message) error {
//	_, err := q.js.Publish("obs."+m.ProjectID+".events", m.Payload,
//		jetstream.MessageID(m.ID)) // MessageID gives server-side dedup
//	return err
// }
//
// func (q *JetStreamQueue) Pull(ctx context.Context, n int) []Message {
//	// jetstream.PullAsync with consumer "obs-workers"; map to Message.
//	panic("implement against jetstream.PullAsync; see local.go for semantics")
// }
//
// func (q *JetStreamQueue) Ack(m Message) error { /* consumer.NextMsg ack */
//	panic("implement")
// }
//
// func (q *JetStreamQueue) Restart() error { return nil } // JetStream retains
// func (q *JetStreamQueue) Depth() int       { return 0 } // nats.StreamInfo
// func (q *JetStreamQueue) Close() error    { return nil }
