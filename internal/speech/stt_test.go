package speech

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeSTT is a hand-rolled SpeechToText fake for orchestrator tests.
type fakeSTT struct {
	name  string
	text  string
	err   error
	calls *int
}

func (f *fakeSTT) Name() string { return f.name }

func (f *fakeSTT) Transcribe(ctx context.Context, wavBytes []byte, language string) (string, error) {
	if f.calls != nil {
		*f.calls++
	}
	return f.text, f.err
}

func TestSTTOrchestrator_EmptyChain(t *testing.T) {
	o := &STTOrchestrator{}
	_, _, err := o.Transcribe(context.Background(), []byte("wav"), "ar")
	if err == nil {
		t.Fatal("expected error for an empty provider chain, got nil")
	}
}

func TestSTTOrchestrator_FirstProviderSucceeds(t *testing.T) {
	var secondCalls int
	o := &STTOrchestrator{chain: []SpeechToText{
		&fakeSTT{name: "first", text: "hello"},
		&fakeSTT{name: "second", text: "unused", calls: &secondCalls},
	}}

	text, provider, err := o.Transcribe(context.Background(), []byte("wav"), "ar")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text != "hello" || provider != "first" {
		t.Errorf("expected (hello, first), got (%q, %q)", text, provider)
	}
	if secondCalls != 0 {
		t.Errorf("expected the second provider to be short-circuited, but it was called %d time(s)", secondCalls)
	}
}

func TestSTTOrchestrator_FallsBackOnError(t *testing.T) {
	var thirdCalls int
	o := &STTOrchestrator{chain: []SpeechToText{
		&fakeSTT{name: "first", err: errors.New("first down")},
		&fakeSTT{name: "second", text: "recovered"},
		&fakeSTT{name: "third", text: "unused", calls: &thirdCalls},
	}}

	text, provider, err := o.Transcribe(context.Background(), []byte("wav"), "ar")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text != "recovered" || provider != "second" {
		t.Errorf("expected (recovered, second), got (%q, %q)", text, provider)
	}
	if thirdCalls != 0 {
		t.Errorf("expected the third provider to be short-circuited once the second succeeds, but it was called %d time(s)", thirdCalls)
	}
}

func TestSTTOrchestrator_AllProvidersFail(t *testing.T) {
	o := &STTOrchestrator{chain: []SpeechToText{
		&fakeSTT{name: "first", err: errors.New("first down")},
		&fakeSTT{name: "second", err: errors.New("second down: final")},
	}}

	_, _, err := o.Transcribe(context.Background(), []byte("wav"), "ar")
	if err == nil {
		t.Fatal("expected an error when all providers fail, got nil")
	}
	if !strings.Contains(err.Error(), "second down: final") {
		t.Errorf("expected the wrapped error to reference the last provider's failure, got: %v", err)
	}
}
