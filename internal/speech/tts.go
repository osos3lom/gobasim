package speech

import (
	"context"
	"fmt"
	"log"
	"sawt-go/config"
	"sawt-go/internal/agentcfg"
	"sawt-go/internal/speech/providers"
)

type TTSOrchestrator struct {
	chain []TextToSpeech
}

func NewTTSOrchestrator(cfg *config.Config) *TTSOrchestrator {
	var chain []TextToSpeech

	// 1. Primary: Google Cloud TTS (Standard Arabic)
	if cfg.GcpApiKey != "" {
		chain = append(chain, providers.NewGoogleProvider(cfg.GcpApiKey))
		log.Println("TTS Orchestrator: Google Cloud TTS provider registered (Rank 1).")
	} else {
		log.Println("TTS Orchestrator: Google Cloud TTS provider skipped (GCP_API_KEY not set).")
	}

	// 2. Backup A: Hugging Face Spaces (facebook/mms-tts-ara)
	// We can check for either HF API Key or just load it anyway since the Gradio Space is public
	chain = append(chain, providers.NewHuggingFaceProvider(cfg.HfAPIKey))
	log.Println("TTS Orchestrator: Hugging Face Spaces provider registered (Rank 2).")

	// 3. Final Fallback: Local gTTS (Google Translate Web TTS Engine)
	// Always available, zero dependencies, 100% free
	chain = append(chain, providers.NewLocalProvider("", ""))
	log.Println("TTS Orchestrator: Local gTTS provider registered (Rank 3).")

	return &TTSOrchestrator{chain: chain}
}

// Synthesize cascades through the registered providers to synthesize text to audio.
func (o *TTSOrchestrator) Synthesize(ctx context.Context, text string, language string) ([]byte, string, error) {
	if len(o.chain) == 0 {
		return nil, "", fmt.Errorf("no TTS providers are registered in the chain")
	}

	// If the agent named a preferred vendor, try it first while keeping the rest
	// of the cascade as fallback.
	chain := o.chain
	if voice, ok := agentcfg.VoiceFromContext(ctx); ok && voice.Vendor != "" {
		chain = preferVendor(o.chain, voice.Vendor)
	}

	var lastErr error
	for _, provider := range chain {
		log.Printf("TTS Orchestrator: Attempting synthesis with provider '%s'...", provider.Name())
		audioBytes, err := provider.Synthesize(ctx, text, language)
		if err == nil && len(audioBytes) > 0 {
			log.Printf("TTS Orchestrator: Success with provider '%s' (%d bytes).", provider.Name(), len(audioBytes))
			return audioBytes, provider.Name(), nil
		}

		log.Printf("TTS Orchestrator: Provider '%s' failed: %v", provider.Name(), err)
		lastErr = err
	}

	return nil, "", fmt.Errorf("all TTS providers failed in the fallback chain: %w", lastErr)
}

// preferVendor returns a copy of chain with the provider whose Name matches
// vendor moved to the front, so an agent's chosen engine is tried first while
// the remaining providers stay as ordered fallbacks. An unmatched vendor leaves
// the order unchanged.
func preferVendor(chain []TextToSpeech, vendor string) []TextToSpeech {
	out := make([]TextToSpeech, 0, len(chain))
	var preferred TextToSpeech
	for _, p := range chain {
		if p.Name() == vendor {
			preferred = p
			continue
		}
		out = append(out, p)
	}
	if preferred == nil {
		return chain
	}
	return append([]TextToSpeech{preferred}, out...)
}
