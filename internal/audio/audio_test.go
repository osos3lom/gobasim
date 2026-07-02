package audio

import "testing"

func TestOggToWavRejectsEmptyInput(t *testing.T) {
	if _, err := OggToWav(nil); err == nil {
		t.Fatal("expected error for empty input")
	}
	if _, err := OggToWav([]byte{}); err == nil {
		t.Fatal("expected error for zero-length input")
	}
}

func TestWavToOpusRejectsEmptyInput(t *testing.T) {
	if _, err := WavToOpus(nil); err == nil {
		t.Fatal("expected error for empty input")
	}
	if _, err := WavToOpus([]byte{}); err == nil {
		t.Fatal("expected error for zero-length input")
	}
}
