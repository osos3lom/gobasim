package config

import (
	"crypto/rand"
	"encoding/hex"
	"log"
	"os"
	"strconv"
)

type Config struct {
	DatabaseURL          string
	Port                 string
	AgentGatewaySecret   string
	MshaliaAPIURL        string
	NimAPIKey            string
	NimBaseURL           string
	NimModel             string
	SttProvider          string
	SttModel             string
	OpenaiAPIKey         string
	OpenaiAPIBase        string
	HfAPIKey             string
	TtsProvider          string
	TtsModel             string
	PairPhoneNumber      string
	SessionSecret        string
	GroqAPIKey           string
	GcpApiKey            string
	SecureCookie         bool
	AdminUsername        string
	AdminPassword        string
	LlmFallbackModel     string
	ErrorWebhookURL      string
	RetentionDays        int
}

func LoadConfig() *Config {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	mshaliaURL := os.Getenv("MSHALIA_API_URL")
	if mshaliaURL == "" {
		mshaliaURL = "http://localhost:3001"
	}

	nimBaseURL := os.Getenv("NIM_BASE_URL")
	if nimBaseURL == "" {
		nimBaseURL = "https://integrate.api.nvidia.com/v1"
	}

	nimModel := os.Getenv("NIM_MODEL")
	if nimModel == "" {
		nimModel = "meta/llama-3.3-70b-instruct"
	}

	openaiAPIBase := os.Getenv("OPENAI_API_BASE")
	if openaiAPIBase == "" {
		openaiAPIBase = "https://api.openai.com/v1"
	}

	sttProvider := os.Getenv("STT_PROVIDER")
	if sttProvider == "" {
		sttProvider = "groq"
	}

	sttModel := os.Getenv("STT_MODEL")
	if sttModel == "" {
		sttModel = "whisper-large-v3"
	}

	ttsProvider := os.Getenv("TTS_PROVIDER")
	if ttsProvider == "" {
		ttsProvider = "google"
	}

	sessionSecret := os.Getenv("SESSION_SECRET")
	if sessionSecret == "" {
		log.Println("WARNING: SESSION_SECRET env var is not set. Generating a random key for this run. Users will be logged out on server restart.")
		bytes := make([]byte, 32)
		if _, err := rand.Read(bytes); err != nil {
			sessionSecret = "sawt-session-secret-change-me"
		} else {
			sessionSecret = hex.EncodeToString(bytes)
		}
	}

	secureCookie, _ := strconv.ParseBool(os.Getenv("SECURE_COOKIE"))

	llmFallbackModel := os.Getenv("LLM_FALLBACK_MODEL")
	if llmFallbackModel == "" {
		llmFallbackModel = "gpt-4o-mini"
	}

	return &Config{
		DatabaseURL:         os.Getenv("DATABASE_URL"),
		Port:                port,
		AgentGatewaySecret:  os.Getenv("AGENT_GATEWAY_SECRET"),
		MshaliaAPIURL:       mshaliaURL,
		NimAPIKey:           os.Getenv("NIM_API_KEY"),
		NimBaseURL:          nimBaseURL,
		NimModel:            nimModel,
		SttProvider:         sttProvider,
		SttModel:            sttModel,
		OpenaiAPIKey:        os.Getenv("OPENAI_API_KEY"),
		OpenaiAPIBase:       openaiAPIBase,
		HfAPIKey:            os.Getenv("HF_API_KEY"),
		TtsProvider:         ttsProvider,
		TtsModel:            os.Getenv("TTS_MODEL"),
		PairPhoneNumber:     os.Getenv("PAIR_PHONE_NUMBER"),
		SessionSecret:       sessionSecret,
		GroqAPIKey:          os.Getenv("GROQ_API_KEY"),
		GcpApiKey:           os.Getenv("GCP_API_KEY"),
		SecureCookie:        secureCookie,
		AdminUsername:       os.Getenv("ADMIN_USERNAME"),
		AdminPassword:       os.Getenv("ADMIN_PASSWORD"),
		LlmFallbackModel:    llmFallbackModel,
		ErrorWebhookURL:     os.Getenv("ERROR_WEBHOOK_URL"),
		RetentionDays:       GetEnvInt("RETENTION_DAYS", 90),
	}
}

func GetEnvInt(key string, defaultVal int) int {
	valStr := os.Getenv(key)
	if valStr == "" {
		return defaultVal
	}
	val, err := strconv.Atoi(valStr)
	if err != nil {
		return defaultVal
	}
	return val
}
