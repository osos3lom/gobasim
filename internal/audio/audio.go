package audio

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"
)

const defaultTranscodeTimeout = 30 * time.Second

// OggToWav transcodes OGG/Opus audio bytes to 16kHz mono PCM 16-bit WAV bytes.
func OggToWav(oggBytes []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTranscodeTimeout)
	defer cancel()
	return OggToWavContext(ctx, oggBytes)
}

// OggToWavContext transcodes OGG/Opus audio bytes using context deadline.
func OggToWavContext(ctx context.Context, oggBytes []byte) ([]byte, error) {
	if len(oggBytes) == 0 {
		return nil, fmt.Errorf("empty input audio bytes")
	}

	cmd := exec.CommandContext(
		ctx,
		"ffmpeg",
		"-y",
		"-i", "pipe:0",
		"-ar", "16000",
		"-ac", "1",
		"-c:a", "pcm_s16le",
		"-f", "wav",
		"pipe:1",
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdin = bytes.NewReader(oggBytes)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg OGG->WAV transcode failed: %w, stderr: %s", err, stderr.String())
	}

	return stdout.Bytes(), nil
}

// WavToOpus transcodes audio bytes (WAV or MP3) to an OGG/Opus stream.
func WavToOpus(inputAudioBytes []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTranscodeTimeout)
	defer cancel()
	return WavToOpusContext(ctx, inputAudioBytes)
}

// WavToOpusContext transcodes audio bytes using context deadline.
func WavToOpusContext(ctx context.Context, inputAudioBytes []byte) ([]byte, error) {
	if len(inputAudioBytes) == 0 {
		return nil, fmt.Errorf("empty input audio bytes")
	}

	tmpFile, err := os.CreateTemp("", "sawt-voice-*.ogg")
	if err != nil {
		return nil, fmt.Errorf("failed to create temporary file for audio transcode: %w", err)
	}
	tmpPath := tmpFile.Name()
	_ = tmpFile.Close()
	defer func() { _ = os.Remove(tmpPath) }()

	cmd := exec.CommandContext(
		ctx,
		"ffmpeg",
		"-y",
		"-i", "pipe:0",
		"-c:a", "libopus",
		"-b:a", "32k",
		"-ar", "48000",
		"-ac", "1",
		"-f", "ogg",
		tmpPath,
	)

	var stderr bytes.Buffer
	cmd.Stdin = bytes.NewReader(inputAudioBytes)
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg WAV->Opus transcode failed: %w, stderr: %s", err, stderr.String())
	}

	outputBytes, err := os.ReadFile(tmpPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read transcoded audio file: %w", err)
	}

	return outputBytes, nil
}

// AnyToWav transcodes any input audio bytes to 16kHz mono PCM 16-bit WAV bytes.
func AnyToWav(inputBytes []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTranscodeTimeout)
	defer cancel()
	return AnyToWavContext(ctx, inputBytes)
}

// AnyToWavContext transcodes any input audio bytes using context deadline.
func AnyToWavContext(ctx context.Context, inputBytes []byte) ([]byte, error) {
	if len(inputBytes) == 0 {
		return nil, fmt.Errorf("empty input audio bytes")
	}

	cmd := exec.CommandContext(
		ctx,
		"ffmpeg",
		"-y",
		"-i", "pipe:0",
		"-ar", "16000",
		"-ac", "1",
		"-c:a", "pcm_s16le",
		"-f", "wav",
		"pipe:1",
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdin = bytes.NewReader(inputBytes)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg transcode to WAV failed: %w, stderr: %s", err, stderr.String())
	}

	return stdout.Bytes(), nil
}

// extractPCMFromWAV attempts to parse raw PCM 16-bit audio samples directly from a WAV byte buffer
// without invoking external ffmpeg processes. Returns (pcmBytes, true) on success.
func extractPCMFromWAV(data []byte) ([]byte, bool) {
	if len(data) < 44 {
		return nil, false
	}
	if !bytes.HasPrefix(data, []byte("RIFF")) || !bytes.Equal(data[8:12], []byte("WAVE")) {
		return nil, false
	}

	idx := 12
	for idx+8 <= len(data) {
		chunkID := string(data[idx : idx+4])
		chunkSize := int(data[idx+4]) | (int(data[idx+5]) << 8) | (int(data[idx+6]) << 16) | (int(data[idx+7]) << 24)
		if chunkID == "data" {
			start := idx + 8
			end := start + chunkSize
			if end > len(data) {
				end = len(data)
			}
			if start < end {
				return data[start:end], true
			}
			return nil, false
		}
		if chunkSize < 0 || idx+8+chunkSize > len(data) {
			break
		}
		idx += 8 + chunkSize
	}

	if len(data) > 44 {
		return data[44:], true
	}
	return nil, false
}

// GenerateWaveform computes a 64-byte normalized PCM waveform representation for WhatsApp voice messages.
// It uses an in-memory fast-path when rawAudio is already a valid WAV file, avoiding external process calls.
func GenerateWaveform(rawAudio []byte) []byte {
	wave := make([]byte, 64)
	for i := 0; i < 64; i++ {
		wave[i] = 2 // default minimum
	}

	var pcmData []byte
	if pcm, ok := extractPCMFromWAV(rawAudio); ok {
		pcmData = pcm
	} else if pcmWav, err := AnyToWav(rawAudio); err == nil && len(pcmWav) > 44 {
		if pcm, ok := extractPCMFromWAV(pcmWav); ok {
			pcmData = pcm
		} else {
			pcmData = pcmWav[44:]
		}
	}

	if len(pcmData) >= 128 { // Need at least 64 16-bit samples
		numSamples := len(pcmData) / 2
		samplesPerBucket := numSamples / 64
		peaks := make([]float64, 64)
		maxPeak := 0.0

		for bucket := 0; bucket < 64; bucket++ {
			startSample := bucket * samplesPerBucket
			endSample := startSample + samplesPerBucket
			if bucket == 63 {
				endSample = numSamples
			}

			sum := 0.0
			count := 0
			for s := startSample; s < endSample; s++ {
				idx := s * 2
				if idx+1 < len(pcmData) {
					sampleVal := int16(pcmData[idx]) | (int16(pcmData[idx+1]) << 8)
					absVal := float64(sampleVal)
					if absVal < 0 {
						absVal = -absVal
					}
					sum += absVal
					count++
				}
			}

			avg := sum
			if count > 0 {
				avg = sum / float64(count)
			}
			peaks[bucket] = avg
			if avg > maxPeak {
				maxPeak = avg
			}
		}

		if maxPeak > 0 {
			for bucket := 0; bucket < 64; bucket++ {
				scaled := (peaks[bucket] / maxPeak) * 95.0
				val := int(scaled)
				if val < 2 {
					val = 2
				}
				if val > 127 {
					val = 127
				}
				wave[bucket] = byte(val)
			}
		}
		return wave
	}

	// Fallback: procedural envelope if decoding fails
	for i := 0; i < 64; i++ {
		envelope := float64(i)
		if i > 32 {
			envelope = float64(64 - i)
		}
		envelope = (envelope / 32.0) * 55.0
		variation := float64((i*7)%15 - 7)
		val := int(envelope + variation)
		if val < 2 {
			val = 2
		}
		if val > 99 {
			val = 99
		}
		wave[i] = byte(val)
	}

	return wave
}
