package speech

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeTTS is a hand-rolled TextToSpeech fake for orchestrator tests.
type fakeTTS struct {
	name  string
	bytes []byte
	err   error
	calls *int
}

func (f *fakeTTS) Name() string { return f.name }

func (f *fakeTTS) Synthesize(ctx context.Context, text string, language string) ([]byte, error) {
	if f.calls != nil {
		*f.calls++
	}
	return f.bytes, f.err
}

func TestTTSOrchestrator_EmptyChain(t *testing.T) {
	o := &TTSOrchestrator{}
	_, _, err := o.Synthesize(context.Background(), "hello", "ar")
	if err == nil {
		t.Fatal("expected error for an empty provider chain, got nil")
	}
}

func TestTTSOrchestrator_FirstProviderSucceeds(t *testing.T) {
	var secondCalls int
	o := &TTSOrchestrator{chain: []TextToSpeech{
		&fakeTTS{name: "first", bytes: []byte("audio")},
		&fakeTTS{name: "second", bytes: []byte("unused"), calls: &secondCalls},
	}}

	audio, provider, err := o.Synthesize(context.Background(), "hello", "ar")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(audio) != "audio" || provider != "first" {
		t.Errorf("expected (audio, first), got (%q, %q)", string(audio), provider)
	}
	if secondCalls != 0 {
		t.Errorf("expected the second provider to be short-circuited, but it was called %d time(s)", secondCalls)
	}
}

func TestTTSOrchestrator_FallsBackOnError(t *testing.T) {
	o := &TTSOrchestrator{chain: []TextToSpeech{
		&fakeTTS{name: "first", err: errors.New("first down")},
		&fakeTTS{name: "second", bytes: []byte("recovered")},
	}}

	audio, provider, err := o.Synthesize(context.Background(), "hello", "ar")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(audio) != "recovered" || provider != "second" {
		t.Errorf("expected (recovered, second), got (%q, %q)", string(audio), provider)
	}
}

// TestTTSOrchestrator_EmptyBytesCountsAsFailure pins the rule in tts.go's
// cascade loop: a provider returning (nil-or-empty, nil) must be treated as
// a failure and the chain must continue to the next provider, not return an
// empty success.
func TestTTSOrchestrator_EmptyBytesCountsAsFailure(t *testing.T) {
	o := &TTSOrchestrator{chain: []TextToSpeech{
		&fakeTTS{name: "first", bytes: nil, err: nil}, // "succeeds" with zero bytes
		&fakeTTS{name: "second", bytes: []byte("real audio")},
	}}

	audio, provider, err := o.Synthesize(context.Background(), "hello", "ar")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if provider != "second" {
		t.Errorf("expected the empty-bytes first provider to be treated as failed and fall back to 'second', got provider %q", provider)
	}
	if string(audio) != "real audio" {
		t.Errorf("unexpected audio: %q", string(audio))
	}
}

func TestTTSOrchestrator_AllProvidersFail(t *testing.T) {
	o := &TTSOrchestrator{chain: []TextToSpeech{
		&fakeTTS{name: "first", err: errors.New("first down")},
		&fakeTTS{name: "second", err: errors.New("second down: final")},
	}}

	_, _, err := o.Synthesize(context.Background(), "hello", "ar")
	if err == nil {
		t.Fatal("expected an error when all providers fail, got nil")
	}
	if !strings.Contains(err.Error(), "second down: final") {
		t.Errorf("expected the wrapped error to reference the last provider's failure, got: %v", err)
	}
}

func TestTTSOrchestrator_AllProvidersReturnEmptyBytesIsAFailure(t *testing.T) {
	o := &TTSOrchestrator{chain: []TextToSpeech{
		&fakeTTS{name: "first", bytes: nil, err: nil},
		&fakeTTS{name: "second", bytes: []byte{}, err: nil},
	}}

	_, _, err := o.Synthesize(context.Background(), "hello", "ar")
	if err == nil {
		t.Fatal("expected an error when every provider returns zero bytes, got nil")
	}
}
