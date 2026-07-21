package audio

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
)

// OggToWav transcodes OGG/Opus audio bytes (from WhatsApp) to 16kHz mono PCM 16-bit WAV bytes for STT APIs.
func OggToWav(oggBytes []byte) ([]byte, error) {
	if len(oggBytes) == 0 {
		return nil, fmt.Errorf("empty input audio bytes")
	}

	cmd := exec.Command(
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

// WavToOpus transcodes audio bytes (WAV or MP3 from TTS APIs) to an OGG/Opus stream suitable for WhatsApp PTT voice notes.
func WavToOpus(inputAudioBytes []byte) ([]byte, error) {
	if len(inputAudioBytes) == 0 {
		return nil, fmt.Errorf("empty input audio bytes")
	}

	// Create a temporary file for the output to ensure ffmpeg can seek and write correct Ogg/Opus headers (granule positions)
	tmpFile, err := os.CreateTemp("", "sawt-voice-*.ogg")
	if err != nil {
		return nil, fmt.Errorf("failed to create temporary file for audio transcode: %w", err)
	}
	tmpPath := tmpFile.Name()
	_ = tmpFile.Close()                       // Close it so ffmpeg can overwrite it
	defer func() { _ = os.Remove(tmpPath) }() // Clean up

	cmd := exec.Command(
		"ffmpeg",
		"-y",
		"-i", "pipe:0",
		"-c:a", "libopus",
		"-b:a", "32k",
		"-ar", "48000",
		"-ac", "1",
		"-f", "ogg", // Use standard ogg container format
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

// AnyToWav transcodes any input audio bytes (MP3, WAV, etc.) to 16kHz mono PCM 16-bit WAV bytes using ffmpeg.
func AnyToWav(inputBytes []byte) ([]byte, error) {
	if len(inputBytes) == 0 {
		return nil, fmt.Errorf("empty input audio bytes")
	}

	cmd := exec.Command(
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
