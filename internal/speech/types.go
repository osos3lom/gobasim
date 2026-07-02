package speech

import (
	"context"
)

// SpeechToText is the interface that any STT provider must implement.
type SpeechToText interface {
	Name() string
	Transcribe(ctx context.Context, wavBytes []byte, language string) (string, error)
}

// TextToSpeech is the interface that any TTS provider must implement.
type TextToSpeech interface {
	Name() string
	Synthesize(ctx context.Context, text string, language string) ([]byte, error)
}
