package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"time"
)

type GroqProvider struct {
	apiKey string
	model  string
}

func NewGroqProvider(apiKey, model string) *GroqProvider {
	if model == "" {
		model = "whisper-large-v3"
	}
	return &GroqProvider{
		apiKey: apiKey,
		model:  model,
	}
}

func (p *GroqProvider) Name() string {
	return "groq"
}

func (p *GroqProvider) Transcribe(ctx context.Context, wavBytes []byte, language string) (string, error) {
	if p.apiKey == "" {
		return "", fmt.Errorf("groq api key is not set")
	}

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	// Add file field
	part, err := writer.CreateFormFile("file", "voice.wav")
	if err != nil {
		return "", fmt.Errorf("failed to create multipart form file: %w", err)
	}
	if _, err := io.Copy(part, bytes.NewReader(wavBytes)); err != nil {
		return "", fmt.Errorf("failed to write multipart audio bytes: %w", err)
	}

	// Add model field
	if err := writer.WriteField("model", p.model); err != nil {
		return "", fmt.Errorf("failed to write multipart model field: %w", err)
	}

	// Add language field
	if language != "" {
		if err := writer.WriteField("language", language); err != nil {
			return "", fmt.Errorf("failed to write multipart language field: %w", err)
		}
	}

	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("failed to close multipart writer: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.groq.com/openai/v1/audio/transcriptions", body)
	if err != nil {
		return "", fmt.Errorf("failed to create http request: %w", err)
	}

	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+p.apiKey)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("http request failed: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("api error status %d: %s", resp.StatusCode, string(respBytes))
	}

	var result struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(respBytes, &result); err != nil {
		return "", fmt.Errorf("failed to unmarshal JSON response: %w, payload: %s", err, string(respBytes))
	}

	return result.Text, nil
}
