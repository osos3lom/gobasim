package providers

import (
	"context"
	"testing"
	"time"
)

// TestLiveHuggingFaceTranscribe exercises the real HF Serverless Inference
// API (Whisper). Requires RUN_LIVE_AI_TESTS=1 and HF_API_KEY; skips cleanly
// otherwise. May legitimately fail with a 503 on a cold model — that's
// expected live-service behavior, not a test bug.
func TestLiveHuggingFaceTranscribe(t *testing.T) {
	skipUnlessLive(t)
	apiKey := requireEnv(t, "HF_API_KEY")

	p := NewHuggingFaceProvider(apiKey)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	text, err := p.Transcribe(ctx, loadTestdataWAV(t, "tiny_16k_mono.wav"), "ar")
	if err != nil {
		t.Fatalf("live HF transcribe failed: %v", err)
	}
	t.Logf("HF transcript (tone, not speech — no meaningful text expected): %q", text)
}

// TestLiveHuggingFaceSynthesize exercises the real public Gradio Space
// (facebook/mms-tts-ara). Requires RUN_LIVE_AI_TESTS=1; no HF_API_KEY needed
// since the Space is public, but still gated behind the live flag so it
// never runs in CI.
func TestLiveHuggingFaceSynthesize(t *testing.T) {
	skipUnlessLive(t)

	p := NewHuggingFaceProvider("")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	audio, err := p.Synthesize(ctx, "مرحبا", "ar")
	if err != nil {
		t.Fatalf("live HF synthesize failed: %v", err)
	}
	if len(audio) == 0 {
		t.Error("expected non-empty audio from live HF synthesize")
	}
}
