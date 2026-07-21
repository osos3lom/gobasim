package providers

import (
	"context"
	"errors"
	"sawt-go/internal/agentcfg"
	"sync"
	"testing"

	speechpb "cloud.google.com/go/speech/apiv1/speechpb"
	texttospeechpb "cloud.google.com/go/texttospeech/apiv1/texttospeechpb"
	"github.com/googleapis/gax-go/v2"
)

type fakeSpeechAPI struct {
	resp *speechpb.RecognizeResponse
	err  error
	// mu guards lastReq: the concurrency tests call Recognize from many
	// goroutines at once, so the recording write must be synchronized. The
	// single-threaded tests read lastReq sequentially after the call, so a
	// plain field read there is race-free.
	mu      sync.Mutex
	lastReq *speechpb.RecognizeRequest
}

func (f *fakeSpeechAPI) Recognize(ctx context.Context, req *speechpb.RecognizeRequest, opts ...gax.CallOption) (*speechpb.RecognizeResponse, error) {
	f.mu.Lock()
	f.lastReq = req
	f.mu.Unlock()
	return f.resp, f.err
}

type fakeTTSAPI struct {
	resp *texttospeechpb.SynthesizeSpeechResponse
	err  error
	// mu guards lastReq; see fakeSpeechAPI for the rationale.
	mu      sync.Mutex
	lastReq *texttospeechpb.SynthesizeSpeechRequest
}

func (f *fakeTTSAPI) SynthesizeSpeech(ctx context.Context, req *texttospeechpb.SynthesizeSpeechRequest, opts ...gax.CallOption) (*texttospeechpb.SynthesizeSpeechResponse, error) {
	f.mu.Lock()
	f.lastReq = req
	f.mu.Unlock()
	return f.resp, f.err
}

func TestGoogleADCTranscribe_Success(t *testing.T) {
	fake := &fakeSpeechAPI{
		resp: &speechpb.RecognizeResponse{
			Results: []*speechpb.SpeechRecognitionResult{
				{Alternatives: []*speechpb.SpeechRecognitionAlternative{{Transcript: "مرحبا"}}},
			},
		},
	}
	p := newGoogleADCProviderFromAPIs(fake, nil)

	text, err := p.Transcribe(context.Background(), loadTestdataWAV(t, "tiny_16k_mono.wav"), "ar")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text != "مرحبا" {
		t.Errorf("unexpected transcript: %q", text)
	}
	if fake.lastReq.Config.Encoding != speechpb.RecognitionConfig_LINEAR16 {
		t.Errorf("expected LINEAR16 encoding, got %v", fake.lastReq.Config.Encoding)
	}
	if fake.lastReq.Config.SampleRateHertz != 16000 {
		t.Errorf("expected 16000 Hz, got %d", fake.lastReq.Config.SampleRateHertz)
	}
	if fake.lastReq.Config.LanguageCode != "ar-SA" {
		t.Errorf("expected ar-SA default language, got %q", fake.lastReq.Config.LanguageCode)
	}
}

func TestGoogleADCTranscribe_NoResultsIsNotAnError(t *testing.T) {
	fake := &fakeSpeechAPI{resp: &speechpb.RecognizeResponse{Results: nil}}
	p := newGoogleADCProviderFromAPIs(fake, nil)

	text, err := p.Transcribe(context.Background(), loadTestdataWAV(t, "tiny_16k_mono.wav"), "ar")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text != "" {
		t.Errorf("expected empty transcript for no results, got %q", text)
	}
}

func TestGoogleADCTranscribe_APIError(t *testing.T) {
	fake := &fakeSpeechAPI{err: errors.New("rpc error: code = PermissionDenied")}
	p := newGoogleADCProviderFromAPIs(fake, nil)

	_, err := p.Transcribe(context.Background(), loadTestdataWAV(t, "tiny_16k_mono.wav"), "ar")
	if err == nil {
		t.Fatal("expected error to propagate from the API, got nil")
	}
}

func TestGoogleADCTranscribe_EmptyAudio(t *testing.T) {
	fake := &fakeSpeechAPI{err: errors.New("rpc error: code = InvalidArgument desc = empty audio")}
	p := newGoogleADCProviderFromAPIs(fake, nil)

	_, err := p.Transcribe(context.Background(), nil, "ar")
	if err == nil {
		t.Fatal("expected error for empty audio, got nil")
	}
	if content := fake.lastReq.Audio.AudioSource.(*speechpb.RecognitionAudio_Content).Content; content != nil {
		t.Errorf("expected nil content to reach the request, got %v", content)
	}
}

func TestGoogleADCTranscribe_ConcurrentRequestsAreRaceFree(t *testing.T) {
	fake := &fakeSpeechAPI{
		resp: &speechpb.RecognizeResponse{
			Results: []*speechpb.SpeechRecognitionResult{
				{Alternatives: []*speechpb.SpeechRecognitionAlternative{{Transcript: "ok"}}},
			},
		},
	}
	p := newGoogleADCProviderFromAPIs(fake, nil)
	wav := loadTestdataWAV(t, "tiny_16k_mono.wav")

	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := p.Transcribe(context.Background(), wav, "ar")
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("unexpected error from concurrent Transcribe: %v", err)
		}
	}
}

func TestGoogleADCSynthesize_Success(t *testing.T) {
	fake := &fakeTTSAPI{resp: &texttospeechpb.SynthesizeSpeechResponse{AudioContent: []byte("hello")}}
	p := newGoogleADCProviderFromAPIs(nil, fake)

	audio, err := p.Synthesize(context.Background(), "hello", "ar")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(audio) != "hello" {
		t.Errorf("unexpected audio: %q", string(audio))
	}
	if fake.lastReq.Voice.LanguageCode != "ar-XA" {
		t.Errorf("expected ar-XA default language, got %q", fake.lastReq.Voice.LanguageCode)
	}
	if fake.lastReq.AudioConfig.AudioEncoding != texttospeechpb.AudioEncoding_LINEAR16 {
		t.Errorf("expected LINEAR16 encoding, got %v", fake.lastReq.AudioConfig.AudioEncoding)
	}
}

func TestGoogleADCSynthesize_APIError(t *testing.T) {
	fake := &fakeTTSAPI{err: errors.New("rpc error: code = ResourceExhausted")}
	p := newGoogleADCProviderFromAPIs(nil, fake)

	_, err := p.Synthesize(context.Background(), "hello", "ar")
	if err == nil {
		t.Fatal("expected error to propagate from the API, got nil")
	}
}

func TestGoogleADCSynthesize_EmptyText(t *testing.T) {
	fake := &fakeTTSAPI{err: errors.New("rpc error: code = InvalidArgument desc = text is empty")}
	p := newGoogleADCProviderFromAPIs(nil, fake)

	_, err := p.Synthesize(context.Background(), "", "ar")
	if err == nil {
		t.Fatal("expected error for empty text, got nil")
	}
	gotText := fake.lastReq.Input.InputSource.(*texttospeechpb.SynthesisInput_Text).Text
	if gotText != "" {
		t.Errorf("expected empty text to be passed through to the request, got %q", gotText)
	}
}

func TestGoogleADCSynthesize_NonUTF8Text(t *testing.T) {
	fake := &fakeTTSAPI{resp: &texttospeechpb.SynthesizeSpeechResponse{AudioContent: []byte("ok")}}
	p := newGoogleADCProviderFromAPIs(nil, fake)

	if _, err := p.Synthesize(context.Background(), string([]byte{0xff, 0xfe}), "ar"); err != nil {
		t.Fatalf("unexpected error for non-UTF8 text: %v", err)
	}
}

func TestGoogleADCSynthesize_ConcurrentRequestsAreRaceFree(t *testing.T) {
	fake := &fakeTTSAPI{resp: &texttospeechpb.SynthesizeSpeechResponse{AudioContent: []byte("ok")}}
	p := newGoogleADCProviderFromAPIs(nil, fake)

	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := p.Synthesize(context.Background(), "hello", "ar")
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("unexpected error from concurrent Synthesize: %v", err)
		}
	}
}

func TestGoogleADCProvider_Name(t *testing.T) {
	p := newGoogleADCProviderFromAPIs(nil, nil)
	if p.Name() != "google-adc" {
		t.Errorf("unexpected provider name: %q", p.Name())
	}
}

func TestGoogleADCProvider_CloseIsNoOpForTestConstructedProvider(t *testing.T) {
	p := newGoogleADCProviderFromAPIs(nil, nil)
	if err := p.Close(); err != nil {
		t.Errorf("expected Close on a test-constructed provider to be a no-op, got: %v", err)
	}
}

func TestNewGoogleADCProvider_InvalidCredentialsPathFailsCleanly(t *testing.T) {
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "testdata/does-not-exist.json")

	_, err := NewGoogleADCProvider(context.Background())
	if err == nil {
		t.Fatal("expected error for a nonexistent GOOGLE_APPLICATION_CREDENTIALS path, got nil")
	}
}

func TestGoogleADCSynthesize_PerAgentVoice(t *testing.T) {
	fake := &fakeTTSAPI{resp: &texttospeechpb.SynthesizeSpeechResponse{AudioContent: []byte("hello")}}
	p := newGoogleADCProviderFromAPIs(nil, fake)

	voice := agentcfg.TTS{
		LanguageCode: "en-US",
		VoiceName:    "en-US-Wavenet-F",
		Gender:       "FEMALE",
		Speed:        1.2,
	}
	ctx := agentcfg.WithVoice(context.Background(), voice)

	audio, err := p.Synthesize(ctx, "hello", "ar")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(audio) != "hello" {
		t.Errorf("unexpected audio: %q", string(audio))
	}
	if fake.lastReq.Voice.LanguageCode != "en-US" {
		t.Errorf("expected en-US, got %q", fake.lastReq.Voice.LanguageCode)
	}
	if fake.lastReq.Voice.Name != "en-US-Wavenet-F" {
		t.Errorf("expected en-US-Wavenet-F, got %q", fake.lastReq.Voice.Name)
	}
	if fake.lastReq.Voice.SsmlGender != texttospeechpb.SsmlVoiceGender_FEMALE {
		t.Errorf("expected FEMALE gender, got %v", fake.lastReq.Voice.SsmlGender)
	}
	if float32(fake.lastReq.AudioConfig.SpeakingRate) != 1.2 {
		t.Errorf("expected speaking rate 1.2, got %v", fake.lastReq.AudioConfig.SpeakingRate)
	}
}

