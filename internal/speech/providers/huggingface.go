package providers

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	defaultHFSTTURL = "https://api-inference.huggingface.co/models/openai/whisper-large-v3"
	defaultHFTTSURL = "https://facebook-multimodal-mms.hf.space/api/predict"
)

type HuggingFaceProvider struct {
	apiKey     string
	httpClient *http.Client
	sttURL     string
	ttsURL     string
}

// HFOption configures a HuggingFaceProvider. Used in tests to inject a fake
// HTTP client and URLs so no real network call is made.
type HFOption func(*HuggingFaceProvider)

func WithHFHTTPClient(c *http.Client) HFOption {
	return func(p *HuggingFaceProvider) { p.httpClient = c }
}

func WithHFSTTURL(url string) HFOption {
	return func(p *HuggingFaceProvider) { p.sttURL = url }
}

func WithHFTTSURL(url string) HFOption {
	return func(p *HuggingFaceProvider) { p.ttsURL = url }
}

func NewHuggingFaceProvider(apiKey string, opts ...HFOption) *HuggingFaceProvider {
	p := &HuggingFaceProvider{
		apiKey:     apiKey,
		httpClient: &http.Client{Timeout: 45 * time.Second},
		sttURL:     defaultHFSTTURL,
		ttsURL:     defaultHFTTSURL,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

func (p *HuggingFaceProvider) Name() string {
	return "huggingface"
}

// Transcribe uses Hugging Face Serverless Inference API to run Whisper.
func (p *HuggingFaceProvider) Transcribe(ctx context.Context, wavBytes []byte, language string) (string, error) {
	if p.apiKey == "" {
		return "", fmt.Errorf("huggingface api key is not set")
	}

	req, err := http.NewRequestWithContext(ctx, "POST", p.sttURL, bytes.NewReader(wavBytes))
	if err != nil {
		return "", fmt.Errorf("failed to create http request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	req.Header.Set("Content-Type", "audio/wav")

	resp, err := p.httpClient.Do(req)
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

// Synthesize uses a Gradio Hugging Face Space running facebook/mms-tts-ara.
func (p *HuggingFaceProvider) Synthesize(ctx context.Context, text string, language string) ([]byte, error) {
	// Gradio input format for MMS: [text, model/language]
	payload := map[string]interface{}{
		"data": []interface{}{
			text,
			"ara", // Arabic target language code
		},
	}

	jsonBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", p.ttsURL, bytes.NewReader(jsonBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create http request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request failed: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("api error status %d: %s", resp.StatusCode, string(respBytes))
	}

	// Gradio response matches: {"data": [{"name": "audio.wav", "data": "data:audio/wav;base64,..."}], "duration": ...}
	var gradioResponse struct {
		Data []struct {
			Data string `json:"data"` // Base64 encoded audio
			Name string `json:"name"`
		} `json:"data"`
	}

	if err := json.Unmarshal(respBytes, &gradioResponse); err != nil {
		return nil, fmt.Errorf("failed to unmarshal gradio response: %w", err)
	}

	if len(gradioResponse.Data) == 0 || gradioResponse.Data[0].Data == "" {
		return nil, fmt.Errorf("gradio space returned no audio data")
	}

	// Extract base64 part
	base64Data := gradioResponse.Data[0].Data
	if idx := strings.Index(base64Data, "base64,"); idx != -1 {
		base64Data = base64Data[idx+7:]
	}

	audioBytes, err := base64.StdEncoding.DecodeString(base64Data)
	if err != nil {
		return nil, fmt.Errorf("failed to decode base64 audio data: %w", err)
	}

	return audioBytes, nil
}
