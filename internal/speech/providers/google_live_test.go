package providers

import (
	"context"
	"testing"
	"time"
)

// TestLiveGoogleTranscribe exercises the real GCP_API_KEY REST Speech-to-Text
// endpoint. Requires RUN_LIVE_AI_TESTS=1 and GCP_API_KEY; skips cleanly
// otherwise.
func TestLiveGoogleTranscribe(t *testing.T) {
	skipUnlessLive(t)
	apiKey := requireEnv(t, "GCP_API_KEY")

	p := NewGoogleProvider(apiKey)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	text, err := p.Transcribe(ctx, loadTestdataWAV(t, "tiny_16k_mono.wav"), "ar")
	if err != nil {
		t.Fatalf("live Google REST transcribe failed: %v", err)
	}
	t.Logf("Google transcript (tone, not speech — no meaningful text expected): %q", text)
}

// TestLiveGoogleSynthesize exercises the real GCP_API_KEY REST
// Text-to-Speech endpoint.
func TestLiveGoogleSynthesize(t *testing.T) {
	skipUnlessLive(t)
	apiKey := requireEnv(t, "GCP_API_KEY")

	p := NewGoogleProvider(apiKey)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	audio, err := p.Synthesize(ctx, "مرحبا", "ar")
	if err != nil {
		t.Fatalf("live Google REST synthesize failed: %v", err)
	}
	if len(audio) == 0 {
		t.Error("expected non-empty audio from live Google REST synthesize")
	}
}
