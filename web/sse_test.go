package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// sseRecorder is a minimal http.ResponseWriter + http.Flusher whose Write
// hands each chunk over a channel, so the test can synchronize on delivery
// instead of racing on a shared buffer with the handler's goroutine.
type sseRecorder struct {
	header http.Header
	lines  chan string
}

func newSSERecorder() *sseRecorder {
	return &sseRecorder{header: make(http.Header), lines: make(chan string, 10)}
}

func (s *sseRecorder) Header() http.Header        { return s.header }
func (s *sseRecorder) WriteHeader(statusCode int) {}
func (s *sseRecorder) Flush()                     {}
func (s *sseRecorder) Write(p []byte) (int, error) {
	s.lines <- string(p)
	return len(p), nil
}

func TestHandleSSELogs(t *testing.T) {
	db := &mockDBTX{}
	server, _ := setupTestServer(t, db)

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/api/logs", nil).WithContext(ctx)
	rec := newSSERecorder()

	done := make(chan struct{})
	go func() {
		server.handleSSELogs(rec, req)
		close(done)
	}()

	var received string
	timeout := time.After(3 * time.Second)
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()

waitForLine:
	for {
		select {
		case <-timeout:
			break waitForLine
		case line := <-rec.lines:
			received = line
			if strings.Contains(line, "sse-test-log-line") {
				break waitForLine
			}
		case <-ticker.C:
			_, _ = server.logBroker.Write([]byte("sse-test-log-line"))
		}
	}

	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handleSSELogs did not return after context cancellation")
	}

	if !strings.Contains(received, "sse-test-log-line") {
		t.Fatalf("expected the published log line in the SSE output, got %q", received)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}
}
