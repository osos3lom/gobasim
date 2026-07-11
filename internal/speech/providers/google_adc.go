package providers

import (
	"context"
	"fmt"

	speechapi "cloud.google.com/go/speech/apiv1"
	speechpb "cloud.google.com/go/speech/apiv1/speechpb"
	texttospeechapi "cloud.google.com/go/texttospeech/apiv1"
	texttospeechpb "cloud.google.com/go/texttospeech/apiv1/texttospeechpb"
	"github.com/googleapis/gax-go/v2"
)

// speechRecognizeAPI is the narrow slice of *speechapi.Client this provider
// needs. The real client satisfies it implicitly; tests inject a fake.
type speechRecognizeAPI interface {
	Recognize(ctx context.Context, req *speechpb.RecognizeRequest, opts ...gax.CallOption) (*speechpb.RecognizeResponse, error)
}

// ttsSynthesizeAPI is the narrow slice of *texttospeechapi.Client this
// provider needs.
type ttsSynthesizeAPI interface {
	SynthesizeSpeech(ctx context.Context, req *texttospeechpb.SynthesizeSpeechRequest, opts ...gax.CallOption) (*texttospeechpb.SynthesizeSpeechResponse, error)
}

// GoogleADCProvider is an alternative Google STT/TTS provider that
// authenticates via Application Default Credentials (a service-account JSON
// referenced by GOOGLE_APPLICATION_CREDENTIALS, or GCE/GKE metadata) using
// the official gRPC client libraries, instead of the GCP_API_KEY REST calls
// in google.go. It is additive — GoogleProvider (REST) is untouched.
type GoogleADCProvider struct {
	stt speechRecognizeAPI
	tts ttsSynthesizeAPI

	// Real clients, kept only to Close() them; nil when constructed via the
	// test-only injector.
	sttClient *speechapi.Client
	ttsClient *texttospeechapi.Client
}

// NewGoogleADCProvider resolves Application Default Credentials and dials
// both the Speech and Text-to-Speech gRPC APIs.
func NewGoogleADCProvider(ctx context.Context) (*GoogleADCProvider, error) {
	sttClient, err := speechapi.NewClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create Google Speech ADC client: %w", err)
	}

	ttsClient, err := texttospeechapi.NewClient(ctx)
	if err != nil {
		_ = sttClient.Close()
		return nil, fmt.Errorf("failed to create Google Text-to-Speech ADC client: %w", err)
	}

	return &GoogleADCProvider{
		stt:       sttClient,
		tts:       ttsClient,
		sttClient: sttClient,
		ttsClient: ttsClient,
	}, nil
}

// newGoogleADCProviderFromAPIs is the test-only constructor that injects
// fakes instead of dialing real gRPC endpoints.
func newGoogleADCProviderFromAPIs(stt speechRecognizeAPI, tts ttsSynthesizeAPI) *GoogleADCProvider {
	return &GoogleADCProvider{stt: stt, tts: tts}
}

func (p *GoogleADCProvider) Name() string {
	return "google-adc"
}

// Transcribe mirrors GoogleProvider's defaulting (LINEAR16, 16kHz, ar-SA) but
// calls Recognize over gRPC via Application Default Credentials.
func (p *GoogleADCProvider) Transcribe(ctx context.Context, wavBytes []byte, language string) (string, error) {
	if language == "" || language == "ar" {
		language = "ar-SA"
	}

	req := &speechpb.RecognizeRequest{
		Config: &speechpb.RecognitionConfig{
			Encoding:        speechpb.RecognitionConfig_LINEAR16,
			SampleRateHertz: 16000,
			LanguageCode:    language,
		},
		Audio: &speechpb.RecognitionAudio{
			AudioSource: &speechpb.RecognitionAudio_Content{Content: wavBytes},
		},
	}

	resp, err := p.stt.Recognize(ctx, req)
	if err != nil {
		return "", fmt.Errorf("google adc speech recognize failed: %w", err)
	}

	if len(resp.Results) == 0 || len(resp.Results[0].Alternatives) == 0 {
		return "", nil
	}

	return resp.Results[0].Alternatives[0].Transcript, nil
}

// Synthesize mirrors GoogleProvider's defaulting (ar-XA voices, LINEAR16
// out) but calls SynthesizeSpeech over gRPC via Application Default
// Credentials.
func (p *GoogleADCProvider) Synthesize(ctx context.Context, text string, language string) ([]byte, error) {
	if language == "" || language == "ar" {
		language = "ar-XA"
	}

	voiceName := "ar-XA-Wavenet-A"
	if language == "ar-XA" {
		voiceName = "ar-XA-Wavenet-B"
	}

	req := &texttospeechpb.SynthesizeSpeechRequest{
		Input: &texttospeechpb.SynthesisInput{
			InputSource: &texttospeechpb.SynthesisInput_Text{Text: text},
		},
		Voice: &texttospeechpb.VoiceSelectionParams{
			LanguageCode: language,
			Name:         voiceName,
		},
		AudioConfig: &texttospeechpb.AudioConfig{
			AudioEncoding: texttospeechpb.AudioEncoding_LINEAR16,
		},
	}

	resp, err := p.tts.SynthesizeSpeech(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("google adc text-to-speech synthesize failed: %w", err)
	}

	return resp.AudioContent, nil
}

// Close releases the underlying gRPC connections. No-op on a
// test-constructed provider (sttClient/ttsClient are nil).
func (p *GoogleADCProvider) Close() error {
	var errs []error
	if p.sttClient != nil {
		if err := p.sttClient.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if p.ttsClient != nil {
		if err := p.ttsClient.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("failed to close google adc clients: %v", errs)
	}
	return nil
}
