// Package blob is the object-store abstraction. Dev mode is a local
// directory; production is S3-compatible (see infrastructure/DEPLOYMENT.md).
package blob

import (
	"context"
	"errors"
	"os"
	"path"
	"strings"
	"sync"
)

// ObjectStore persists opt-in payloads separately from analytics rows
// (spec NFR: separate metadata from large payload storage).
type ObjectStore interface {
	Put(ctx context.Context, project, ref string, data []byte) error
	Get(ctx context.Context, project, ref string) ([]byte, error)
	Has(ctx context.Context, project, ref string) (bool, error)
}

// LocalObjectStore is the dev-mode object store: a directory tree
// <root>/<project>/<ref> with atomic writes.
type LocalObjectStore struct {
	Root string
	mu   sync.Mutex
}

var _ ObjectStore = (*LocalObjectStore)(nil)

func NewLocalObjectStore(root string) (*LocalObjectStore, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	return &LocalObjectStore{Root: root}, nil
}

func (s *LocalObjectStore) pathFor(project, ref string) string {
	if project == "" {
		// Tenant isolation: a missing project scope is an error, not a
		// fallback to a shared bucket.
		return ""
	}
	return path.Join(s.Root, SanitizeRef(project), SanitizeRef(ref))
}

func (s *LocalObjectStore) Put(ctx context.Context, project, ref string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.pathFor(project, ref)
	if p == "" {
		return errors.New("blob: project scope required")
	}
	if err := os.MkdirAll(path.Dir(p), 0o755); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func (s *LocalObjectStore) Get(ctx context.Context, project, ref string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.pathFor(project, ref)
	if p == "" {
		return nil, errors.New("blob: project scope required")
	}
	b, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return nil, nil
	}
	return b, err
}

func (s *LocalObjectStore) Has(ctx context.Context, project, ref string) (bool, error) {
	p := s.pathFor(project, ref)
	if p == "" {
		return false, nil
	}
	_, err := os.Stat(p)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

// SanitizeRef forbids traversal and keeps refs addressable. Exported so the
// S3 adapter (blobs3) reuses the exact same key-sanitising rules.
func SanitizeRef(ref string) string {
	ref = strings.TrimSpace(ref)
	ref = strings.ReplaceAll(ref, "..", "")
	ref = strings.Trim(ref, "/")
	var b strings.Builder
	for _, r := range ref {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == '.' || r == '_' || r == '-' || r == '/' {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	if b.Len() == 0 {
		return "unnamed"
	}
	return b.String()
}
