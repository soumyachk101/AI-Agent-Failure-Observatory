// Package blobs3 is the S3/MinIO-compatible object store for production
// payload storage, implementing blob.ObjectStore.
//
// Objects are stored with the same key shape and the same redaction
// guarantee as the local dev store: callers put payloads that have already
// been redacted by the pipeline (payload-policy off => the payload ref is
// dropped entirely; capture on => attributes are redacted before the row is
// written), and the ref stored on the event row points at the object. This
// package never re-introduces raw content — S3 only ever sees
// already-redacted bytes, exactly like the local dir store.
package blobs3

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/ensembyte/observatory/internal/blob"
)

// S3 is the MinIO/S3 object store.
type S3 struct {
	client *minio.Client
	bucket string
}

// EndpointConfig describes the target bucket.
type EndpointConfig struct {
	Endpoint  string
	AccessKey string
	SecretKey string
	Region    string
	Bucket    string
	PathStyle bool
}

// New builds the client, creates the bucket if missing, and returns the
// store.
func New(ctx context.Context, cfg EndpointConfig) (*S3, error) {
	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:        credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure:       isHTTPS(cfg.Endpoint),
		Region:       cfg.Region,
		BucketLookup: bucketLookupFor(cfg),
	})
	if err != nil {
		return nil, err
	}
	exists, err := client.BucketExists(ctx, cfg.Bucket)
	if err != nil {
		return nil, fmt.Errorf("blobs3: bucket check: %w", err)
	}
	if !exists {
		if err := client.MakeBucket(ctx, cfg.Bucket, minio.MakeBucketOptions{}); err != nil {
			return nil, fmt.Errorf("blobs3: make bucket: %w", err)
		}
	}
	return &S3{client: client, bucket: cfg.Bucket}, nil
}

// Put stores an already-redacted payload at <project>/<ref> (mirrors the
// local dir store's key shape).
func (s *S3) Put(ctx context.Context, project, ref string, data []byte) error {
	key, err := s.keyFor(project, ref)
	if err != nil {
		return err
	}
	_, err = s.client.PutObject(ctx, s.bucket, key,
		bytes.NewReader(data), int64(len(data)), minio.PutObjectOptions{})
	return err
}

// Get reads a stored payload.
func (s *S3) Get(ctx context.Context, project, ref string) ([]byte, error) {
	key, err := s.keyFor(project, ref)
	if err != nil {
		return nil, err
	}
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	defer obj.Close()
	out, err := io.ReadAll(obj)
	if err == nil {
		return out, nil
	}
	var s3err *minio.ErrorResponse
	if errors.As(err, &s3err) && s3err.Code == "NoSuchKey" {
		return nil, nil
	}
	return nil, err
}

// Has reports whether an object exists.
func (s *S3) Has(ctx context.Context, project, ref string) (bool, error) {
	key, err := s.keyFor(project, ref)
	if err != nil {
		return false, err
	}
	_, err = s.client.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
	if err == nil {
		return true, nil
	}
	var s3err *minio.ErrorResponse
	if errors.As(err, &s3err) && (s3err.Code == "NoSuchKey" || s3err.Code == "NoSuchObject") {
		return false, nil
	}
	return false, err
}

// keyFor builds the object key, enforcing tenant isolation and a
// sanitised ref (same rules as the local dir store).
func (s *S3) keyFor(project, ref string) (string, error) {
	if project == "" {
		// Tenant isolation: a missing project scope is an error, not a
		// fallback to a shared bucket.
		return "", errors.New("blobs3: project scope required")
	}
	p := blob.SanitizeRef(project)
	r := blob.SanitizeRef(ref)
	return fmt.Sprintf("%s/%s", p, r), nil
}

// Compile-time interface conformance: a signature drift fails the build.
var _ blob.ObjectStore = (*S3)(nil)

func isHTTPS(endpoint string) bool {
	return len(endpoint) > 5 && endpoint[:5] == "https"
}

// bucketLookupFor picks the URL lookup strategy: path-style hosts (MinIO
// endpoints) vs virtual-host/S3 DNS.
func bucketLookupFor(cfg EndpointConfig) minio.BucketLookupType {
	if cfg.PathStyle {
		return minio.BucketLookupPath
	}
	return minio.BucketLookupDNS
}
