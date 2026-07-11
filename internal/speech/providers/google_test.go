package providers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestGoogleTranscribe_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("key"); got != "test-key" {
			t.Errorf("unexpected key query param: %q", got)
		}
		var body map[string]interface{}
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &body)
		cfg := body["config"].(map[string]interface{})
		if cfg["encoding"] != "LINEAR16" || cfg["sampleRateHertz"] != float64(16000) {
			t.Errorf("unexpected config: %+v", cfg)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"results":[{"alternatives":[{"transcript":"مرحبا"}]}]}`))
	}))
	defer srv.Close()

	p := NewGoogleProvider("test-key", WithGoogleSTTBaseURL(srv.URL))
	text, err := p.Transcribe(context.Background(), loadTestdataWAV(t, "tiny_16k_mono.wav"), "ar")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text != "مرحبا" {
		t.Errorf("unexpected transcript: %q", text)
	}
}

func TestGoogleTranscribe_NoResultsIsNotAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"results":[]}`))
	}))
	defer srv.Close()

	p := NewGoogleProvider("test-key", WithGoogleSTTBaseURL(srv.URL))
	text, err := p.Transcribe(context.Background(), loadTestdataWAV(t, "tiny_16k_mono.wav"), "ar")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text != "" {
		t.Errorf("expected empty transcript for no results, got %q", text)
	}
}

func TestGoogleTranscribe_MissingAPIKey(t *testing.T) {
	p := NewGoogleProvider("")
	_, err := p.Transcribe(context.Background(), loadTestdataWAV(t, "tiny_16k_mono.wav"), "ar")
	if err == nil {
		t.Fatal("expected error for missing api key, got nil")
	}
}

func TestGoogleTranscribe_ServerError5xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"error":{"code":503,"message":"backend unavailable"}}`))
	}))
	defer srv.Close()

	p := NewGoogleProvider("test-key", WithGoogleSTTBaseURL(srv.URL))
	_, err := p.Transcribe(context.Background(), loadTestdataWAV(t, "tiny_16k_mono.wav"), "ar")
	if err == nil {
		t.Fatal("expected error for 503, got nil")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("expected error to surface status 503, got: %v", err)
	}
}

func TestGoogleTranscribe_NetworkTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"results":[]}`))
	}))
	defer srv.Close()

	p := NewGoogleProvider("test-key",
		WithGoogleSTTBaseURL(srv.URL),
		WithGoogleHTTPClient(&http.Client{Timeout: 20 * time.Millisecond}),
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

func TestGoogleTranscribe_EmptyAudio(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"code":400,"message":"audio content is empty"}}`))
	}))
	defer srv.Close()

	p := NewGoogleProvider("test-key", WithGoogleSTTBaseURL(srv.URL))
	_, err := p.Transcribe(context.Background(), nil, "ar")
	if err == nil {
		t.Fatal("expected error for empty audio, got nil")
	}
}

func TestGoogleTranscribe_MalformedAudioPayload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"code":400,"message":"invalid encoding"}}`))
	}))
	defer srv.Close()

	p := NewGoogleProvider("test-key", WithGoogleSTTBaseURL(srv.URL))
	_, err := p.Transcribe(context.Background(), loadTestdataWAV(t, "malformed.wav"), "ar")
	if err == nil {
		t.Fatal("expected error for malformed audio payload, got nil")
	}
}

func TestGoogleTranscribe_RateLimited429(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"code":429,"message":"quota exceeded"}}`))
	}))
	defer srv.Close()

	p := NewGoogleProvider("test-key", WithGoogleSTTBaseURL(srv.URL))
	_, err := p.Transcribe(context.Background(), loadTestdataWAV(t, "tiny_16k_mono.wav"), "ar")
	if err == nil {
		t.Fatal("expected error for 429, got nil")
	}
}

func TestGoogleTranscribe_OversizedPayload413(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		w.Write([]byte(`{"error":{"code":413,"message":"request too large"}}`))
	}))
	defer srv.Close()

	p := NewGoogleProvider("test-key", WithGoogleSTTBaseURL(srv.URL))
	_, err := p.Transcribe(context.Background(), loadTestdataWAV(t, "tiny_16k_mono.wav"), "ar")
	if err == nil {
		t.Fatal("expected error for 413, got nil")
	}
}

func TestGoogleTranscribe_ConcurrentRequestsAreRaceFree(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"results":[{"alternatives":[{"transcript":"ok"}]}]}`))
	}))
	defer srv.Close()

	p := NewGoogleProvider("test-key", WithGoogleSTTBaseURL(srv.URL))
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

func TestGoogleSynthesize_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"audioContent":"aGVsbG8="}`))
	}))
	defer srv.Close()

	p := NewGoogleProvider("test-key", WithGoogleTTSBaseURL(srv.URL))
	audio, err := p.Synthesize(context.Background(), "hello", "ar")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(audio) != "hello" {
		t.Errorf("unexpected decoded audio: %q", string(audio))
	}
}

func TestGoogleSynthesize_MissingAPIKey(t *testing.T) {
	p := NewGoogleProvider("")
	_, err := p.Synthesize(context.Background(), "hello", "ar")
	if err == nil {
		t.Fatal("expected error for missing api key, got nil")
	}
}

func TestGoogleSynthesize_MalformedBase64(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"audioContent":"not-valid-base64!!"}`))
	}))
	defer srv.Close()

	p := NewGoogleProvider("test-key", WithGoogleTTSBaseURL(srv.URL))
	_, err := p.Synthesize(context.Background(), "hello", "ar")
	if err == nil {
		t.Fatal("expected error for malformed base64, got nil")
	}
}

func TestGoogleSynthesize_RateLimited429(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"code":429,"message":"quota exceeded"}}`))
	}))
	defer srv.Close()

	p := NewGoogleProvider("test-key", WithGoogleTTSBaseURL(srv.URL))
	_, err := p.Synthesize(context.Background(), "hello", "ar")
	if err == nil {
		t.Fatal("expected error for 429, got nil")
	}
}

func TestGoogleSynthesize_EmptyText(t *testing.T) {
	var gotPayload map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &gotPayload)
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"code":400,"message":"text is empty"}}`))
	}))
	defer srv.Close()

	p := NewGoogleProvider("test-key", WithGoogleTTSBaseURL(srv.URL))
	_, err := p.Synthesize(context.Background(), "", "ar")
	if err == nil {
		t.Fatal("expected error for empty text, got nil")
	}
	input := gotPayload["input"].(map[string]interface{})
	if input["text"] != "" {
		t.Errorf("expected empty text to be passed through to the remote API, got: %+v", input)
	}
}

func TestGoogleSynthesize_NonUTF8Text(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"audioContent":"aGVsbG8="}`))
	}))
	defer srv.Close()

	p := NewGoogleProvider("test-key", WithGoogleTTSBaseURL(srv.URL))
	if _, err := p.Synthesize(context.Background(), string([]byte{0xff, 0xfe}), "ar"); err != nil {
		t.Fatalf("unexpected error for non-UTF8 text: %v", err)
	}
}

func TestGoogleSynthesize_ConcurrentRequestsAreRaceFree(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"audioContent":"aGVsbG8="}`))
	}))
	defer srv.Close()

	p := NewGoogleProvider("test-key", WithGoogleTTSBaseURL(srv.URL))

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
