package queue

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// LocalQueue is a file-based durable queue standing in for NATS JetStream in
// dev mode: pending messages live under <dir>/pending, in-flight under
// <dir>/inflight. Restart re-queues in-flight messages (crash recovery),
// giving at-least-once delivery.
type LocalQueue struct {
	dir string
	mu  sync.Mutex
}

var _ Queue = (*LocalQueue)(nil)

func NewLocalQueue(dir string) (*LocalQueue, error) {
	for _, sub := range []string{"pending", "inflight"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return nil, err
		}
	}
	return &LocalQueue{dir: dir}, nil
}

func (q *LocalQueue) subdir(name string) string { return filepath.Join(q.dir, name) }

func (q *LocalQueue) writeAtomically(sub string, m Message) error {
	p := filepath.Join(q.subdir(sub), m.ID+".json")
	if _, err := os.Stat(p); err == nil {
		return nil // duplicate: idempotent no-op
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func (q *LocalQueue) Enqueue(ctx context.Context, m Message) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if m.ID == "" {
		m.ID = MessageID(m.ProjectID, m.Payload)
	}
	if m.EnqueuedAt.IsZero() {
		m.EnqueuedAt = time.Now()
	}
	return q.writeAtomically("pending", m)
}

func (q *LocalQueue) Pull(ctx context.Context, n int) []Message {
	q.mu.Lock()
	defer q.mu.Unlock()
	files, _ := os.ReadDir(q.subdir("pending"))
	sort.Slice(files, func(i, j int) bool { return files[i].Name() < files[j].Name() })
	var out []Message
	for _, f := range files {
		if len(out) >= n {
			break
		}
		p := filepath.Join(q.subdir("pending"), f.Name())
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var m Message
		if err := json.Unmarshal(b, &m); err != nil {
			_ = os.Rename(p, p+".corrupt")
			continue
		}
		if q.writeAtomically("inflight", m) == nil {
			_ = os.Remove(p)
			out = append(out, m)
		}
	}
	return out
}

func (q *LocalQueue) Ack(m Message) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	return os.Remove(filepath.Join(q.subdir("inflight"), m.ID+".json"))
}

func (q *LocalQueue) Restart() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	files, _ := os.ReadDir(q.subdir("inflight"))
	for _, f := range files {
		p := filepath.Join(q.subdir("inflight"), f.Name())
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var m Message
		if err := json.Unmarshal(b, &m); err != nil {
			continue
		}
		if q.writeAtomically("pending", m) == nil {
			_ = os.Remove(p)
		}
	}
	return nil
}

func (q *LocalQueue) Depth() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	files, _ := os.ReadDir(q.subdir("pending"))
	return len(files)
}

func (q *LocalQueue) Close() error { return nil }
