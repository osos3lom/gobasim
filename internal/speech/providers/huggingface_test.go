package providers

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func loadTestdataWAV(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("failed to read testdata/%s: %v", name, err)
	}
	return b
}

func TestHuggingFaceTranscribe_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("unexpected Authorization header: %q", got)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"text":"مرحبا بالعالم"}`))
	}))
	defer srv.Close()

	p := NewHuggingFaceProvider("test-key", WithHFSTTURL(srv.URL))
	text, err := p.Transcribe(context.Background(), loadTestdataWAV(t, "tiny_16k_mono.wav"), "ar")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text != "مرحبا بالعالم" {
		t.Errorf("unexpected transcript: %q", text)
	}
}

func TestHuggingFaceTranscribe_MissingAPIKey(t *testing.T) {
	p := NewHuggingFaceProvider("")
	_, err := p.Transcribe(context.Background(), loadTestdataWAV(t, "tiny_16k_mono.wav"), "ar")
	if err == nil {
		t.Fatal("expected error for missing api key, got nil")
	}
}

func TestHuggingFaceTranscribe_ModelLoading503(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"Model openai/whisper-large-v3 is currently loading","estimated_time":20.0}`))
	}))
	defer srv.Close()

	p := NewHuggingFaceProvider("test-key", WithHFSTTURL(srv.URL))
	_, err := p.Transcribe(context.Background(), loadTestdataWAV(t, "tiny_16k_mono.wav"), "ar")
	if err == nil {
		t.Fatal("expected error for 503 model-loading response, got nil")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("expected error to surface status 503, got: %v", err)
	}
}

func TestHuggingFaceTranscribe_NetworkTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"text":"too late"}`))
	}))
	defer srv.Close()

	p := NewHuggingFaceProvider("test-key",
		WithHFSTTURL(srv.URL),
		WithHFHTTPClient(&http.Client{Timeout: 20 * time.Millisecond}),
	)

	done := make(chan struct{})
	var err error
	go func() {
		_, err = p.Transcribe(context.Background(), loadTestdataWAV(t, "tiny_16k_mono.wav"), "ar")
		close(done)
	}()

	select {
	case <-done:
		if err == nil {
			t.Fatal("expected a timeout error, got nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Transcribe did not return within 2s of a 20ms client timeout — looks like a hang")
	}
}

func TestHuggingFaceTranscribe_EmptyAudio(t *testing.T) {
	var gotBodyLen int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBodyLen = int(r.ContentLength)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"audio content is empty"}`))
	}))
	defer srv.Close()

	p := NewHuggingFaceProvider("test-key", WithHFSTTURL(srv.URL))
	_, err := p.Transcribe(context.Background(), nil, "ar")
	if err == nil {
		t.Fatal("expected error for empty audio, got nil")
	}
	if gotBodyLen != 0 {
		t.Errorf("expected zero-byte body to reach the server, got %d", gotBodyLen)
	}
}

func TestHuggingFaceTranscribe_MalformedAudioPayload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"could not decode audio: unsupported format"}`))
	}))
	defer srv.Close()

	p := NewHuggingFaceProvider("test-key", WithHFSTTURL(srv.URL))
	_, err := p.Transcribe(context.Background(), loadTestdataWAV(t, "malformed.wav"), "ar")
	if err == nil {
		t.Fatal("expected error for malformed audio payload, got nil")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("expected error to surface status 400, got: %v", err)
	}
}

func TestHuggingFaceTranscribe_RateLimited429(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "2")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"rate limit exceeded"}`))
	}))
	defer srv.Close()

	p := NewHuggingFaceProvider("test-key", WithHFSTTURL(srv.URL))
	_, err := p.Transcribe(context.Background(), loadTestdataWAV(t, "tiny_16k_mono.wav"), "ar")
	if err == nil {
		t.Fatal("expected error for 429, got nil")
	}
	if !strings.Contains(err.Error(), "429") {
		t.Errorf("expected error to surface status 429, got: %v", err)
	}
}

func TestHuggingFaceTranscribe_OversizedPayload413(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		_, _ = w.Write([]byte(`{"error":"payload too large"}`))
	}))
	defer srv.Close()

	p := NewHuggingFaceProvider("test-key", WithHFSTTURL(srv.URL))
	_, err := p.Transcribe(context.Background(), loadTestdataWAV(t, "tiny_16k_mono.wav"), "ar")
	if err == nil {
		t.Fatal("expected error for 413, got nil")
	}
	if !strings.Contains(err.Error(), "413") {
		t.Errorf("expected error to surface status 413, got: %v", err)
	}
}

func TestHuggingFaceTranscribe_ConcurrentRequestsAreRaceFree(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"text":"ok"}`))
	}))
	defer srv.Close()

	p := NewHuggingFaceProvider("test-key", WithHFSTTURL(srv.URL))
	wav := loadTestdataWAV(t, "tiny_16k_mono.wav")

	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := p.Transcribe(context.Background(), wav, "ar")
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("unexpected error from concurrent Transcribe: %v", err)
		}
	}
}

func TestHuggingFaceSynthesize_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[{"name":"audio.wav","data":"data:audio/wav;base64,aGVsbG8="}],"duration":0.5}`))
	}))
	defer srv.Close()

	p := NewHuggingFaceProvider("test-key", WithHFTTSURL(srv.URL))
	audio, err := p.Synthesize(context.Background(), "hello", "ar")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(audio) != "hello" {
		t.Errorf("unexpected decoded audio: %q", string(audio))
	}
}

func TestHuggingFaceSynthesize_NoAPIKeyStillWorks(t *testing.T) {
	// The Gradio Space is public — Synthesize must not require an API key.
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[{"name":"audio.wav","data":"data:audio/wav;base64,aGVsbG8="}]}`))
	}))
	defer srv.Close()

	p := NewHuggingFaceProvider("", WithHFTTSURL(srv.URL))
	if _, err := p.Synthesize(context.Background(), "hello", "ar"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotAuth != "" {
		t.Errorf("expected no Authorization header without an api key, got %q", gotAuth)
	}
}

func TestHuggingFaceSynthesize_MalformedBase64(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[{"name":"audio.wav","data":"data:audio/wav;base64,not-valid-base64!!"}]}`))
	}))
	defer srv.Close()

	p := NewHuggingFaceProvider("test-key", WithHFTTSURL(srv.URL))
	_, err := p.Synthesize(context.Background(), "hello", "ar")
	if err == nil {
		t.Fatal("expected error for malformed base64, got nil")
	}
}

func TestHuggingFaceSynthesize_EmptyGradioData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	p := NewHuggingFaceProvider("test-key", WithHFTTSURL(srv.URL))
	_, err := p.Synthesize(context.Background(), "hello", "ar")
	if err == nil {
		t.Fatal("expected error for empty gradio data, got nil")
	}
}

func TestHuggingFaceSynthesize_RateLimited429(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"rate limited"}`))
	}))
	defer srv.Close()

	p := NewHuggingFaceProvider("test-key", WithHFTTSURL(srv.URL))
	_, err := p.Synthesize(context.Background(), "hello", "ar")
	if err == nil {
		t.Fatal("expected error for 429, got nil")
	}
}

func TestHuggingFaceSynthesize_EmptyText(t *testing.T) {
	var gotPayload string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		gotPayload = string(buf)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"text is empty"}`))
	}))
	defer srv.Close()

	p := NewHuggingFaceProvider("test-key", WithHFTTSURL(srv.URL))
	_, err := p.Synthesize(context.Background(), "", "ar")
	if err == nil {
		t.Fatal("expected error for empty text, got nil")
	}
	if !strings.Contains(gotPayload, `"data":["","ara"]`) {
		t.Errorf("expected empty text to be passed through to the remote API, got payload: %q", gotPayload)
	}
}

func TestHuggingFaceSynthesize_NonUTF8Text(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[{"name":"audio.wav","data":"data:audio/wav;base64,aGVsbG8="}]}`))
	}))
	defer srv.Close()

	p := NewHuggingFaceProvider("test-key", WithHFTTSURL(srv.URL))
	// A raw invalid-UTF8 byte string; json.Marshal will replace invalid
	// sequences with the Unicode replacement character rather than erroring.
	if _, err := p.Synthesize(context.Background(), string([]byte{0xff, 0xfe}), "ar"); err != nil {
		t.Fatalf("unexpected error for non-UTF8 text: %v", err)
	}
}

func TestHuggingFaceSynthesize_ConcurrentRequestsAreRaceFree(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[{"name":"audio.wav","data":"data:audio/wav;base64,aGVsbG8="}]}`))
	}))
	defer srv.Close()

	p := NewHuggingFaceProvider("test-key", WithHFTTSURL(srv.URL))

	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := p.Synthesize(context.Background(), "hello", "ar")
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("unexpected error from concurrent Synthesize: %v", err)
		}
	}
}
