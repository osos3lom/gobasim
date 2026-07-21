package providers

import (
	"context"
	"testing"
	"time"
)

// TestLiveGoogleADCTranscribe exercises the real Google Cloud Speech-to-Text
// gRPC API via Application Default Credentials. Requires RUN_LIVE_AI_TESTS=1
// and GOOGLE_APPLICATION_CREDENTIALS pointing at a service-account JSON with
// the Speech API enabled; skips cleanly otherwise.
func TestLiveGoogleADCTranscribe(t *testing.T) {
	skipUnlessLive(t)
	requireEnv(t, "GOOGLE_APPLICATION_CREDENTIALS")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	p, err := NewGoogleADCProvider(ctx)
	if err != nil {
		t.Fatalf("failed to construct GoogleADCProvider: %v", err)
	}
	defer func() { _ = p.Close() }()

	text, err := p.Transcribe(ctx, loadTestdataWAV(t, "tiny_16k_mono.wav"), "ar")
	if err != nil {
		t.Fatalf("live Google ADC transcribe failed: %v", err)
	}
	t.Logf("Google ADC transcript (tone, not speech — no meaningful text expected): %q", text)
}

// TestLiveGoogleADCSynthesize exercises the real Google Cloud Text-to-Speech
// gRPC API via Application Default Credentials.
func TestLiveGoogleADCSynthesize(t *testing.T) {
	skipUnlessLive(t)
	requireEnv(t, "GOOGLE_APPLICATION_CREDENTIALS")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	p, err := NewGoogleADCProvider(ctx)
	if err != nil {
		t.Fatalf("failed to construct GoogleADCProvider: %v", err)
	}
	defer func() { _ = p.Close() }()

	audio, err := p.Synthesize(ctx, "مرحبا", "ar")
	if err != nil {
		t.Fatalf("live Google ADC synthesize failed: %v", err)
	}
	if len(audio) == 0 {
		t.Error("expected non-empty audio from live Google ADC synthesize")
	}
}
