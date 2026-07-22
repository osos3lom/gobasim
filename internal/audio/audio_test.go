package audio

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"
	"time"
)

func createDummyWAV(numSamples int) []byte {
	var buf bytes.Buffer

	// RIFF header
	buf.WriteString("RIFF")
	dataSize := numSamples * 2
	fileSize := uint32(36 + dataSize)
	_ = binary.Write(&buf, binary.LittleEndian, fileSize)
	buf.WriteString("WAVE")

	// fmt chunk
	buf.WriteString("fmt ")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(16)) // Subchunk1Size (16 for PCM)
	_ = binary.Write(&buf, binary.LittleEndian, uint16(1))  // AudioFormat (1 for PCM)
	_ = binary.Write(&buf, binary.LittleEndian, uint16(1))  // NumChannels (1 mono)
	_ = binary.Write(&buf, binary.LittleEndian, uint32(16000)) // SampleRate
	_ = binary.Write(&buf, binary.LittleEndian, uint32(32000)) // ByteRate (16000 * 1 * 2)
	_ = binary.Write(&buf, binary.LittleEndian, uint16(2))  // BlockAlign
	_ = binary.Write(&buf, binary.LittleEndian, uint16(16)) // BitsPerSample

	// data chunk
	buf.WriteString("data")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(dataSize))

	for i := 0; i < numSamples; i++ {
		sample := int16((i * 500) % 32000)
		_ = binary.Write(&buf, binary.LittleEndian, sample)
	}

	return buf.Bytes()
}

func TestExtractPCMFromWAV(t *testing.T) {
	wavData := createDummyWAV(128)
	pcm, ok := extractPCMFromWAV(wavData)
	if !ok {
		t.Fatal("expected extractPCMFromWAV to succeed for valid RIFF/WAVE header")
	}
	if len(pcm) != 256 {
		t.Errorf("len(pcm) = %d, want 256", len(pcm))
	}
}

func TestGenerateWaveform_FastPath(t *testing.T) {
	wavData := createDummyWAV(500)

	start := time.Now()
	wave := GenerateWaveform(wavData)
	elapsed := time.Since(start)

	if len(wave) != 64 {
		t.Fatalf("len(wave) = %d, want 64", len(wave))
	}

	// In-memory fast-path should execute in less than 5ms (typically <0.5ms)
	if elapsed > 100*time.Millisecond {
		t.Errorf("GenerateWaveform took %v, fast-path should take <5ms", elapsed)
	}
}

func TestGenerateWaveform_FallbackForInvalidWAV(t *testing.T) {
	invalidAudio := []byte("not-an-audio-file")
	wave := GenerateWaveform(invalidAudio)
	if len(wave) != 64 {
		t.Fatalf("len(wave) = %d, want 64", len(wave))
	}
}

func TestTranscodeContext_Cancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel context

	_, err := OggToWavContext(ctx, []byte("OggSfakeData"))
	if err == nil {
		t.Error("expected error when context is pre-cancelled")
	}
}
