package providers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"sawt-go/internal/agentcfg"
)

// TestGoogleSynthesize_HonorsAgentVoice proves the per-agent voice threaded via
// context reaches the Google TTS payload: language code, voice name, SSML gender,
// and speaking rate all come from the agent config, overriding the ar-XA defaults.
func TestGoogleSynthesize_HonorsAgentVoice(t *testing.T) {
	var got map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"audioContent":"aGVsbG8="}`))
	}))
	defer srv.Close()

	p := NewGoogleProvider("test-key", WithGoogleTTSBaseURL(srv.URL))
	ctx := agentcfg.WithVoice(context.Background(), agentcfg.TTS{
		Vendor: "google", LanguageCode: "en-US", VoiceName: "en-US-Neural2-C", Gender: "MALE", Speed: 1.25,
	})
	if _, err := p.Synthesize(ctx, "hello", "ar"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	voice, _ := got["voice"].(map[string]interface{})
	if voice["languageCode"] != "en-US" || voice["name"] != "en-US-Neural2-C" || voice["ssmlGender"] != "MALE" {
		t.Errorf("voice params not threaded from agent config: %+v", voice)
	}
	audioCfg, _ := got["audioConfig"].(map[string]interface{})
	if rate, _ := audioCfg["speakingRate"].(float64); rate < 1.24 || rate > 1.26 {
		t.Errorf("speakingRate = %v, want ~1.25", audioCfg["speakingRate"])
	}
}
