package audio

import (
	"bytes"
	"fmt"
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

	cmd := exec.Command(
		"ffmpeg",
		"-y",
		"-i", "pipe:0",
		"-c:a", "libopus",
		"-b:a", "32k",
		"-ar", "48000",
		"-ac", "1",
		"-f", "ogg",
		"pipe:1",
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdin = bytes.NewReader(inputAudioBytes)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg WAV->Opus transcode failed: %w, stderr: %s", err, stderr.String())
	}

	return stdout.Bytes(), nil
}
