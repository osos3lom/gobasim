package providers

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sawt-go/internal/agentcfg"
	"time"
)

const (
	defaultGoogleSTTBaseURL = "https://speech.googleapis.com/v1/speech:recognize"
	defaultGoogleTTSBaseURL = "https://texttospeech.googleapis.com/v1/text:synthesize"
)

type GoogleProvider struct {
	apiKey     string
	httpClient *http.Client
	sttBaseURL string
	ttsBaseURL string
}

// GoogleOption configures a GoogleProvider. Used in tests to inject a fake
// HTTP client and base URLs so no real network call is made.
type GoogleOption func(*GoogleProvider)

func WithGoogleHTTPClient(c *http.Client) GoogleOption {
	return func(p *GoogleProvider) { p.httpClient = c }
}

func WithGoogleSTTBaseURL(url string) GoogleOption {
	return func(p *GoogleProvider) { p.sttBaseURL = url }
}

func WithGoogleTTSBaseURL(url string) GoogleOption {
	return func(p *GoogleProvider) { p.ttsBaseURL = url }
}

func NewGoogleProvider(apiKey string, opts ...GoogleOption) *GoogleProvider {
	p := &GoogleProvider{
		apiKey:     apiKey,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		sttBaseURL: defaultGoogleSTTBaseURL,
		ttsBaseURL: defaultGoogleTTSBaseURL,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

func (p *GoogleProvider) Name() string {
	return "google"
}

// Transcribe calls the Google Cloud Speech-to-Text REST API.
func (p *GoogleProvider) Transcribe(ctx context.Context, wavBytes []byte, language string) (string, error) {
	if p.apiKey == "" {
		return "", fmt.Errorf("google api key is not set")
	}

	if language == "" || language == "ar" {
		language = "ar-SA" // Default/normalize to Arabic (Saudi Arabia)
	}

	url := fmt.Sprintf("%s?key=%s", p.sttBaseURL, p.apiKey)

	// Base64 encode wav bytes
	audioContent := base64.StdEncoding.EncodeToString(wavBytes)

	payload := map[string]interface{}{
		"config": map[string]interface{}{
			"encoding":        "LINEAR16",
			"sampleRateHertz": 16000,
			"languageCode":    language,
		},
		"audio": map[string]interface{}{
			"content": audioContent,
		},
	}

	jsonBytes, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("failed to marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(jsonBytes))
	if err != nil {
		return "", fmt.Errorf("failed to create http request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

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

	var responseStruct struct {
		Results []struct {
			Alternatives []struct {
				Transcript string `json:"transcript"`
			} `json:"alternatives"`
		} `json:"results"`
	}

	if err := json.Unmarshal(respBytes, &responseStruct); err != nil {
		return "", fmt.Errorf("failed to unmarshal response JSON: %w", err)
	}

	if len(responseStruct.Results) == 0 || len(responseStruct.Results[0].Alternatives) == 0 {
		return "", nil // No transcript returned
	}

	return responseStruct.Results[0].Alternatives[0].Transcript, nil
}

// Synthesize calls the Google Cloud Text-to-Speech REST API. The per-agent voice
// (language, voice name, gender, speed) is read from the request context when
// present; otherwise it falls back to the language argument and Arabic defaults.
func (p *GoogleProvider) Synthesize(ctx context.Context, text string, language string) ([]byte, error) {
	if p.apiKey == "" {
		return nil, fmt.Errorf("google api key is not set")
	}

	voice, _ := agentcfg.VoiceFromContext(ctx)
	if voice.LanguageCode != "" {
		language = voice.LanguageCode
	}
	if language == "" || language == "ar" {
		language = "ar-XA" // Standard Arabic
	}

	url := fmt.Sprintf("%s?key=%s", p.ttsBaseURL, p.apiKey)

	// Prefer the agent-configured voice; otherwise use a high-quality Arabic
	// Wavenet default.
	voiceName := voice.VoiceName
	if voiceName == "" {
		voiceName = "ar-XA-Wavenet-A"
		if language == "ar-XA" {
			voiceName = "ar-XA-Wavenet-B"
		}
	}

	voiceParams := map[string]interface{}{
		"languageCode": language,
		"name":         voiceName,
	}
	if voice.Gender != "" {
		voiceParams["ssmlGender"] = voice.Gender
	}
	audioConfig := map[string]interface{}{
		"audioEncoding": "LINEAR16", // Output 16-bit WAV PCM
	}
	if voice.Speed > 0 {
		audioConfig["speakingRate"] = voice.Speed
	}

	payload := map[string]interface{}{
		"input": map[string]interface{}{
			"text": text,
		},
		"voice":       voiceParams,
		"audioConfig": audioConfig,
	}

	jsonBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(jsonBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create http request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

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

	var responseStruct struct {
		AudioContent string `json:"audioContent"`
	}

	if err := json.Unmarshal(respBytes, &responseStruct); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response JSON: %w", err)
	}

	audioBytes, err := base64.StdEncoding.DecodeString(responseStruct.AudioContent)
	if err != nil {
		return nil, fmt.Errorf("failed to decode base64 audioContent: %w", err)
	}

	return audioBytes, nil
}
