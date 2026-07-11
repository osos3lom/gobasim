package voicenotes

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

// skipUnlessLive skips the calling test unless RUN_LIVE_AI_TESTS=1 is set,
// so live-network tests never run as part of the default `go test ./...`.
func skipUnlessLive(t *testing.T) {
	t.Helper()
	if os.Getenv("RUN_LIVE_AI_TESTS") != "1" {
		t.Skip("skipping live test: RUN_LIVE_AI_TESTS=1 not set")
	}
}

func requireEnv(t *testing.T, key string) string {
	t.Helper()
	v := os.Getenv(key)
	if v == "" {
		t.Skipf("skipping live test: %s not set", key)
	}
	return v
}

// TestLiveGCSUploadRoundTrip exercises the real GCSUploader against a real
// bucket via Application Default Credentials (the service-account JSON
// pointed to by GOOGLE_APPLICATION_CREDENTIALS). Requires RUN_LIVE_AI_TESTS=1
// and VOICE_STORAGE_BUCKET; skips cleanly otherwise. Uploads a tiny object
// under a qa/ prefix and mints a signed URL — does not delete the object
// afterward (bucket lifecycle rules should handle QA cleanup), so point this
// at a dedicated QA bucket, not production.
func TestLiveGCSUploadRoundTrip(t *testing.T) {
	skipUnlessLive(t)
	requireEnv(t, "GOOGLE_APPLICATION_CREDENTIALS")
	bucket := requireEnv(t, "VOICE_STORAGE_BUCKET")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	uploader, err := NewGCSUploader(ctx, bucket)
	if err != nil {
		t.Fatalf("failed to construct GCSUploader: %v", err)
	}

	objectPath := fmt.Sprintf("qa/aicheck-live-test-%d.txt", time.Now().UnixNano())
	payload := []byte("sawt-go live GCS test object — safe to delete")

	if err := uploader.Upload(ctx, objectPath, "text/plain", map[string]string{"source": "gcs_live_test"}, bytes.NewReader(payload)); err != nil {
		t.Fatalf("live GCS upload failed: %v", err)
	}

	url, err := uploader.SignedURL(objectPath, 5*time.Minute)
	if err != nil {
		t.Fatalf("live GCS signed URL failed: %v", err)
	}
	if url == "" {
		t.Error("expected a non-empty signed URL")
	}
}

// TestLiveGCSUploadPermissionDenied exercises the permission-denied error
// path against a bucket name the configured credentials should not be able
// to write to (an intentionally invalid/nonexistent bucket name). Requires
// RUN_LIVE_AI_TESTS=1 and GOOGLE_APPLICATION_CREDENTIALS.
func TestLiveGCSUploadPermissionDenied(t *testing.T) {
	skipUnlessLive(t)
	requireEnv(t, "GOOGLE_APPLICATION_CREDENTIALS")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	uploader, err := NewGCSUploader(ctx, "sawt-go-nonexistent-bucket-for-qa-negative-test")
	if err != nil {
		t.Fatalf("failed to construct GCSUploader: %v", err)
	}

	err = uploader.Upload(ctx, "qa/should-fail.txt", "text/plain", nil, bytes.NewReader([]byte("x")))
	if err == nil {
		t.Fatal("expected an error uploading to a nonexistent/inaccessible bucket, got nil")
	}
}
