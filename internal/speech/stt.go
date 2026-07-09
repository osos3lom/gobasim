package speech

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"sawt-go/config"
	"sawt-go/internal/speech/providers"
)

type STTOrchestrator struct {
	chain []SpeechToText
}

func NewSTTOrchestrator(cfg *config.Config) *STTOrchestrator {
	var chain []SpeechToText

	// 1. Primary: Groq Cloud (Whisper Large V3)
	if cfg.GroqAPIKey != "" {
		chain = append(chain, providers.NewGroqProvider(cfg.GroqAPIKey, cfg.SttModel))
		log.Println("STT Orchestrator: Groq provider registered (Rank 1).")
	} else {
		log.Println("STT Orchestrator: Groq provider skipped (GROQ_API_KEY not set).")
	}

	// 2. Backup A: Hugging Face Serverless Inference API
	if cfg.HfAPIKey != "" {
		chain = append(chain, providers.NewHuggingFaceProvider(cfg.HfAPIKey))
		log.Println("STT Orchestrator: Hugging Face provider registered (Rank 2).")
	} else {
		log.Println("STT Orchestrator: Hugging Face provider skipped (HF_API_KEY not set).")
	}

	// 3. Backup B: Google Cloud STT
	if cfg.GcpApiKey != "" {
		chain = append(chain, providers.NewGoogleProvider(cfg.GcpApiKey))
		log.Println("STT Orchestrator: Google Cloud STT provider registered (Rank 3).")
	} else {
		log.Println("STT Orchestrator: Google Cloud STT provider skipped (GCP_API_KEY not set).")
	}

	// 4. Final Fallback: Local Whisper (whisper.cpp wrapper)
	whisperCLI, _ := exec.LookPath("whisper-cli")
	whisperModel := "models/ggml-tiny.bin"
	if _, err := os.Stat(whisperModel); err == nil || whisperCLI != "" {
		chain = append(chain, providers.NewLocalProvider(whisperCLI, whisperModel))
		log.Println("STT Orchestrator: Local Whisper provider registered (Rank 4).")
	} else {
		// Register it anyway since it is our hard fallback, but warn that files might be missing
		chain = append(chain, providers.NewLocalProvider("", ""))
		log.Printf("STT Orchestrator: Local Whisper registered but local model file '%s' was not found.", whisperModel)
	}

	return &STTOrchestrator{chain: chain}
}

// Transcribe cascades through the registered providers to transcribe the audio.
func (o *STTOrchestrator) Transcribe(ctx context.Context, wavBytes []byte, language string) (string, string, error) {
	if len(o.chain) == 0 {
		return "", "", fmt.Errorf("no STT providers are registered in the chain")
	}

	var lastErr error
	for _, provider := range o.chain {
		log.Printf("STT Orchestrator: Attempting transcription with provider '%s'...", provider.Name())
		text, err := provider.Transcribe(ctx, wavBytes, language)
		if err == nil {
			log.Printf("STT Orchestrator: Success with provider '%s'.", provider.Name())
			return text, provider.Name(), nil
		}

		log.Printf("STT Orchestrator: Provider '%s' failed: %v", provider.Name(), err)
		lastErr = err
	}

	return "", "", fmt.Errorf("all STT providers failed in the fallback chain: %w", lastErr)
}
