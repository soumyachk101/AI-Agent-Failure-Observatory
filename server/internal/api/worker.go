package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/ensembyte/observatory/internal/event"
	"github.com/ensembyte/observatory/internal/pipeline"
	"github.com/ensembyte/observatory/internal/queue"
	"github.com/ensembyte/observatory/internal/store"
)

// Worker drains the durable queue and runs the pipeline (normalise ->
// assemble -> detect) on each batch. This mirrors the production NATS
// consumer: events land on the queue, a worker processes them.
//
// In dev mode the ingest path also processes synchronously (for immediacy
// in the UI), so the worker is a safety net for any batch that went to the
// queue only. Both paths are idempotent, so double-processing is safe.
type Worker struct {
	Store store.Engine
	Queue queue.Queue
	Log   *slog.Logger

	pullSize int
	// limiter protects ingestion from SDK floods (spec NFR).
	limiter *rateLimiter
}

// NewWorker constructs a worker with default pull size.
func NewWorker(st store.Engine, q queue.Queue, log *slog.Logger) *Worker {
	if log == nil {
		log = slog.Default()
	}
	return &Worker{Store: st, Queue: q, Log: log, pullSize: 32,
		limiter: newRateLimiter()}
}

// Run drains the queue until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) {
	if err := w.Queue.Restart(); err != nil {
		w.Log.Warn("queue restart", "error", err)
	}
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		msgs := w.Queue.Pull(ctx, w.pullSize)
		if len(msgs) == 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(250 * time.Millisecond):
			}
			continue
		}
		for _, m := range msgs {
			if err := w.process(ctx, m); err != nil {
				// At-least-once: do NOT ack on error; leave it in-flight
				// so a restart re-queues it. Dedup makes the retry safe.
				w.Log.Warn("worker: batch failed (will retry)", "id", m.ID, "error", err)
				continue
			}
			if err := w.Queue.Ack(m); err != nil {
				w.Log.Warn("worker: ack failed", "id", m.ID, "error", err)
			}
		}
	}
}

func (w *Worker) process(ctx context.Context, m queue.Message) error {
	var bp queue.BatchPayload
	if err := json.Unmarshal(m.Payload, &bp); err != nil {
		// Poison message: ack so it doesn't loop forever.
		w.Log.Warn("worker: undecodable batch, skipping", "id", m.ID, "error", err)
		return nil
	}
	project := bp.Project
	if project == "" {
		project = m.ProjectID
	}
	// Decode envelopes.
	envelopes := make([]*event.Envelope, 0, len(bp.Events))
	for _, raw := range bp.Events {
		var env event.Envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			continue
		}
		envelopes = append(envelopes, &env)
	}
	return w.runPipeline(ctx, project, envelopes)
}

// runPipeline decodes, normalizes, assembles, detects.
func (w *Worker) runPipeline(ctx context.Context, project string, envs []*event.Envelope) error {
	pp, err := ProjectPolicyView(ctx, w.Store, project)
	if err != nil {
		// Unknown project: drop the batch rather than crash the worker.
		w.Log.Warn("worker: project policy lookup failed, skipping", "project", project, "error", err)
		return nil
	}
	// Rate limit check (worker path).
	if !w.limiter.allow(project, pp.RatePerMin, pp.RateBurst, time.Now()) {
		w.Log.Warn("worker: rate limited, requeueing batch", "project", project)
		// Re-queue: re-enqueue so it is retried later. The idempotency
		// table ensures we don't double-count already-processed events.
		var raw []json.RawMessage
		for _, e := range envs {
			b, _ := json.Marshal(e)
			raw = append(raw, b)
		}
		payload, _ := json.Marshal(queue.BatchPayload{Project: project, Events: raw})
		return w.Queue.Enqueue(ctx, queue.Message{ProjectID: project, Payload: payload})
	}
	norm := &pipeline.Normalizer{Store: w.Store, RedactKeys: pp.RedactFields}
	res, err := norm.Run(ctx, pp, envs)
	if err != nil {
		return err
	}
	if len(res.Rejected) > 0 {
		w.Log.Info("worker: events rejected", "project", project,
			"rejected", len(res.Rejected), "accepted", len(res.Accepted))
	}
	return nil
}
