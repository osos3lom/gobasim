package providers

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type LocalProvider struct {
	whisperCLIPath   string
	whisperModelPath string
}

func NewLocalProvider(whisperCLIPath, whisperModelPath string) *LocalProvider {
	if whisperCLIPath == "" {
		whisperCLIPath = "whisper-cli" // Assumed in system PATH
	}
	if whisperModelPath == "" {
		whisperModelPath = "models/ggml-tiny.bin" // Default tiny model
	}
	return &LocalProvider{
		whisperCLIPath:   whisperCLIPath,
		whisperModelPath: whisperModelPath,
	}
}

func (p *LocalProvider) Name() string {
	return "local"
}

// Transcribe executes whisper.cpp as a subprocess.
func (p *LocalProvider) Transcribe(ctx context.Context, wavBytes []byte, language string) (string, error) {
	// 1. Create a temporary WAV file for the CLI to read
	tmpDir := os.TempDir()
	tmpWav := filepath.Join(tmpDir, fmt.Sprintf("sawt_stt_%d.wav", time.Now().UnixNano()))

	if err := os.WriteFile(tmpWav, wavBytes, 0600); err != nil {
		return "", fmt.Errorf("failed to create temporary audio file: %w", err)
	}
	defer os.Remove(tmpWav) // Ensure clean up

	// 2. Build the command. whisper.cpp CLI arguments:
	// -m <model> -f <input-file> -l <lang> --no-timestamps --threads 2
	if language == "" {
		language = "ar"
	}

	cmd := exec.CommandContext(ctx,
		p.whisperCLIPath,
		"-m", p.whisperModelPath,
		"-f", tmpWav,
		"-l", language,
		"--no-timestamps",
		"-t", "2", // Safe for e2-micro (2 vCPUs)
	)

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("local whisper.cpp execution failed: %w, stderr: %s", err, stderr.String())
	}

	// 3. Clean up the output text
	transcription := strings.TrimSpace(stdout.String())
	return transcription, nil
}

// Synthesize requests the Google Translate TTS endpoint.
func (p *LocalProvider) Synthesize(ctx context.Context, text string, language string) ([]byte, error) {
	if language == "" {
		language = "ar"
	}

	// URL format for Google Translate TTS
	baseURL := "http://translate.google.com/translate_tts"
	params := url.Values{}
	params.Set("ie", "UTF-8")
	params.Set("tl", language)
	params.Set("client", "tw-ob") // Bypasses immediate captcha/API blocks
	params.Set("q", text)

	requestURL := fmt.Sprintf("%s?%s", baseURL, params.Encode())

	req, err := http.NewRequestWithContext(ctx, "GET", requestURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create local gTTS HTTP request: %w", err)
	}

	// Must set a real User-Agent to avoid HTTP 403 Forbidden
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/100.0.0.0 Safari/537.36")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("local gTTS HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("local gTTS returned status %d: %s", resp.StatusCode, string(respBytes))
	}

	audioBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read local gTTS response audio stream: %w", err)
	}

	return audioBytes, nil
}
