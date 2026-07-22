package config

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"log"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	DatabaseURL        string
	Port               string
	AgentGatewaySecret string
	MshaliaAPIURL      string
	NimAPIKey          string
	NimBaseURL         string
	NimModel           string
	SttProvider        string
	SttModel           string
	OpenaiAPIKey       string
	OpenaiAPIBase      string
	HfAPIKey           string
	TtsProvider        string
	TtsModel           string
	PairPhoneNumber    string
	SessionSecret      string
	GroqAPIKey         string
	GcpApiKey          string
	SecureCookie       bool
	AdminUsername      string
	AdminPassword      string
	LlmFallbackModel   string
	ErrorWebhookURL    string
	RetentionDays      int

	// MaxInflight bounds how many inbound WhatsApp messages may be in the
	// STT/LLM/ERP pipeline concurrently. It is the global backpressure cap
	// that complements the per-chat inbound rate limiter.
	MaxInflight int

	// Voice-note archival to Firebase Cloud Storage (a GCS bucket).
	// Empty VoiceStorageBucket disables the feature entirely.
	VoiceStorageBucket string
	VoiceStoragePrefix string
	VoiceSpoolDir      string

	// DefaultOrgID is the org assigned to a resolved-but-orgless privileged
	// actor (super_admin/admin/owner) so they can operate over WhatsApp when
	// their ERP record carries no orgIds. Empty disables the fallback. See
	// internal/erp.ApplyDefaultOrg — this closes the M9 gap where super-admin
	// phones resolved with no org and the tool loop bailed as "unlinked".
	DefaultOrgID string
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
		nimModel = "meta/llama-3.1-70b-instruct"
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

	secureCookie, _ := strconv.ParseBool(os.Getenv("SECURE_COOKIE"))

	sessionSecret := os.Getenv("SESSION_SECRET")
	if sessionSecret == "" {
		// SECURE_COOKIE=true is the production signal (HTTPS deployment).
		// Running production with an ephemeral per-boot secret would silently
		// log every operator out on each restart and is almost certainly a
		// deployment mistake — fail fast instead of limping along.
		if secureCookie {
			log.Fatal("FATAL: SESSION_SECRET must be set when SECURE_COOKIE=true (production). Generate one with: openssl rand -hex 32")
		}
		log.Println("WARNING: SESSION_SECRET env var is not set. Generating a random key for this run. Users will be logged out on server restart.")
		bytes := make([]byte, 32)
		if _, err := rand.Read(bytes); err != nil {
			sessionSecret = "sawt-session-secret-change-me"
		} else {
			sessionSecret = hex.EncodeToString(bytes)
		}
	}

	llmFallbackModel := os.Getenv("LLM_FALLBACK_MODEL")
	if llmFallbackModel == "" {
		llmFallbackModel = "gpt-4o-mini"
	}

	return &Config{
		DatabaseURL:        os.Getenv("DATABASE_URL"),
		Port:               port,
		AgentGatewaySecret: os.Getenv("AGENT_GATEWAY_SECRET"),
		MshaliaAPIURL:      mshaliaURL,
		NimAPIKey:          os.Getenv("NIM_API_KEY"),
		NimBaseURL:         nimBaseURL,
		NimModel:           nimModel,
		SttProvider:        sttProvider,
		SttModel:           sttModel,
		OpenaiAPIKey:       os.Getenv("OPENAI_API_KEY"),
		OpenaiAPIBase:      openaiAPIBase,
		HfAPIKey:           os.Getenv("HF_API_KEY"),
		TtsProvider:        ttsProvider,
		TtsModel:           os.Getenv("TTS_MODEL"),
		PairPhoneNumber:    os.Getenv("PAIR_PHONE_NUMBER"),
		SessionSecret:      sessionSecret,
		GroqAPIKey:         os.Getenv("GROQ_API_KEY"),
		GcpApiKey:          os.Getenv("GCP_API_KEY"),
		SecureCookie:       secureCookie,
		AdminUsername:      os.Getenv("ADMIN_USERNAME"),
		AdminPassword:      os.Getenv("ADMIN_PASSWORD"),
		LlmFallbackModel:   llmFallbackModel,
		ErrorWebhookURL:    os.Getenv("ERROR_WEBHOOK_URL"),
		RetentionDays:      GetEnvInt("RETENTION_DAYS", 90),
		MaxInflight:        GetEnvInt("MAX_INFLIGHT", 32),
		VoiceStorageBucket: os.Getenv("VOICE_STORAGE_BUCKET"),
		VoiceStoragePrefix: getEnvDefault("VOICE_STORAGE_PREFIX", "voice-notes"),
		VoiceSpoolDir:      getEnvDefault("VOICE_SPOOL_DIR", "voice-spool"),
		DefaultOrgID:       os.Getenv("DEFAULT_ORG_ID"),
	}
}

func getEnvDefault(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
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

// CanonicalErpURL returns MshaliaAPIURL trimmed of trailing slashes,
// falling back to http://mshalia.vercel.app if empty.
func (c *Config) CanonicalErpURL() string {
	if c == nil || c.MshaliaAPIURL == "" {
		return "http://mshalia.vercel.app"
	}
	return strings.TrimRight(c.MshaliaAPIURL, "/")
}

// LoadDotEnv parses a simple key=value .env file and sets non-empty keys into environment variables.
func LoadDotEnv(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.Index(line, "=")
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		if i := strings.Index(val, " #"); i >= 0 {
			val = strings.TrimSpace(val[:i])
		}
		if key != "" {
			_ = os.Setenv(key, val)
		}
	}
	return sc.Err()
}
