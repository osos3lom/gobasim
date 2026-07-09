package voicenotes

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"cloud.google.com/go/storage"
)

// GCSUploader implements uploader against a Firebase Cloud Storage bucket
// (Firebase Storage buckets ARE GCS buckets; the Firebase SDK is not needed
// server-side). Credentials come from Application Default Credentials: the
// GCE instance service account in production, or GOOGLE_APPLICATION_CREDENTIALS
// pointing at a service-account key file in development.
type GCSUploader struct {
	client *storage.Client
	bucket string
}

func NewGCSUploader(ctx context.Context, bucket string) (*GCSUploader, error) {
	client, err := storage.NewClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCS client: %w", err)
	}
	return &GCSUploader{client: client, bucket: bucket}, nil
}

// Upload streams r into the object. ChunkSize is dropped from the client
// default (16 MB buffer!) to 256 KB so the worker's resident memory stays
// bounded regardless of file size — the whole point on a 1 GB host.
func (g *GCSUploader) Upload(ctx context.Context, objectPath, contentType string, metadata map[string]string, r io.Reader) error {
	w := g.client.Bucket(g.bucket).Object(objectPath).NewWriter(ctx)
	w.ChunkSize = 256 * 1024
	w.ContentType = contentType
	w.Metadata = metadata
	w.CacheControl = "private, max-age=0"

	if _, err := io.Copy(w, r); err != nil {
		_ = w.Close()
		return fmt.Errorf("failed to stream object %s: %w", objectPath, err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("failed to finalize object %s: %w", objectPath, err)
	}
	return nil
}

// SignedURL mints a V4 GET URL. On GCE this signs via the IAM Credentials
// API (the instance service account needs roles/iam.serviceAccountTokenCreator
// on itself); with a key file it signs locally.
func (g *GCSUploader) SignedURL(objectPath string, ttl time.Duration) (string, error) {
	return g.client.Bucket(g.bucket).SignedURL(objectPath, &storage.SignedURLOptions{
		Scheme:  storage.SigningSchemeV4,
		Method:  http.MethodGet,
		Expires: time.Now().Add(ttl),
	})
}
