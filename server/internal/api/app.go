// Package api is the HTTP control plane: ingestion, auth, run queries,
// failure intelligence, evaluation, replay and alert endpoints
// (spec section 10).
package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"time"

	"github.com/ensembyte/observatory/internal/blob"
	"github.com/ensembyte/observatory/internal/pipeline"
	"github.com/ensembyte/observatory/internal/queue"
	"github.com/ensembyte/observatory/internal/store"
)

// App wires the control plane together.
type App struct {
	Store     store.Engine
	Queue     queue.Queue
	Normalizer *pipeline.Normalizer
	Blob      blob.ObjectStore
	APIKeys   KeyStore
	// Worker is the pipeline runner; StartWorker launches it.
	Worker *Worker
	// Log is the redacting structured logger.
	Log *slog.Logger
	// SigningSecret HMAC-signs webhook deliveries (spec 12). Empty in dev.
	SigningSecret string
}

// KeyStore abstracts API key lookup so the API can be tested in-memory.
type KeyStore interface {
	LookupProjectForKey(ctx context.Context, key string) (string, bool, error)
	CreateAPIKey(ctx context.Context, project, label, key string) error
	ListKeys(ctx context.Context, project string) ([]string, error)
	RevokeKey(ctx context.Context, project, key string) error
}

// New builds the app. The worker is constructed but not started.
func New(st store.Engine, q queue.Queue, b blob.ObjectStore, log *slog.Logger) *App {
	_ = st
	_ = q
	_ = b
	return &App{
		Store: st, Queue: q, Blob: b, Log: log,
		Normalizer: &pipeline.Normalizer{Store: st},
		APIKeys:    st,
		Worker:     NewWorker(st, q, log),
	}
}

// ProjectPolicyView adapts store.Project to pipeline.ProjectPolicy.
func ProjectPolicyView(ctx context.Context, st store.Engine, project string) (pipeline.ProjectPolicy, error) {
	p, err := st.GetProject(ctx, project)
	if err != nil {
		return pipeline.ProjectPolicy{}, err
	}
	if p == nil {
		return pipeline.ProjectPolicy{}, fmt.Errorf("project %s not found", project)
	}
	pp := pipeline.ProjectPolicy{
		ID: p.ID, PayloadCapture: p.PayloadCapture,
		RedactFields: pipeline.SplitFields(p.RedactFields),
		RatePerMin: p.RatePerMin, RateBurst: p.RateBurst,
	}
	return pp, nil
}

// NewKey generates a plaintext project key. Format: obs_<rand32hex>. The
// plaintext is returned once and stored hashed; never logged.
func NewKey() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "obs_" + hex.EncodeToString(b), nil
}

// rateLimiter is a per-project token bucket that protects ingestion from SDK
// buffer floods (spec NFR: buffer SDK events during transient failures, but
// cap sustained ingestion).
type rateLimiter struct {
	bucket map[string]*bucket
}

type bucket struct {
	tokens   float64
	maxBurst float64
	refill   float64 // tokens/sec
	last     time.Time
}

func newRateLimiter() *rateLimiter {
	return &rateLimiter{bucket: map[string]*bucket{}}
}

// allow returns false when the project has exceeded its budget.
func (r *rateLimiter) allow(project string, perMin, burst int, now time.Time) bool {
	if perMin <= 0 || burst <= 0 {
		return true
	}
	b, ok := r.bucket[project]
	if !ok {
		b = &bucket{tokens: float64(burst), maxBurst: float64(burst),
			refill: float64(perMin) / 60.0, last: now}
		r.bucket[project] = b
	}
	elapsed := now.Sub(b.last).Seconds()
	b.tokens += elapsed * b.refill
	if b.tokens > b.maxBurst {
		b.tokens = b.maxBurst
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
