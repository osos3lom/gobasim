package config

import (
	"os"
	"strconv"
)

type Config struct {
	DatabaseURL          string
	GatewaySharedSecret  string
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
		sessionSecret = "sawt-session-secret-change-me"
	}

	return &Config{
		DatabaseURL:         os.Getenv("DATABASE_URL"),
		GatewaySharedSecret: os.Getenv("GATEWAY_SHARED_SECRET"),
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
