package config

import "testing"

func clearConfigEnv(t *testing.T) {
	t.Helper()
	keys := []string{
		"PORT", "MSHALIA_API_URL", "AGENT_GATEWAY_SECRET", "NIM_API_KEY", "NIM_BASE_URL",
		"NIM_MODEL", "STT_PROVIDER", "STT_MODEL", "OPENAI_API_KEY", "OPENAI_API_BASE",
		"HF_API_KEY", "TTS_PROVIDER", "TTS_MODEL", "PAIR_PHONE_NUMBER", "SESSION_SECRET",
		"GROQ_API_KEY", "GCP_API_KEY", "SECURE_COOKIE", "ADMIN_USERNAME", "ADMIN_PASSWORD",
		"LLM_FALLBACK_MODEL", "ERROR_WEBHOOK_URL", "RETENTION_DAYS", "MAX_INFLIGHT",
		"VOICE_STORAGE_BUCKET", "VOICE_STORAGE_PREFIX", "VOICE_SPOOL_DIR", "DEFAULT_ORG_ID",
		"DATABASE_URL",
	}
	for _, k := range keys {
		t.Setenv(k, "")
	}
}

func TestLoadConfig_Defaults(t *testing.T) {
	clearConfigEnv(t)
	cfg := LoadConfig()

	if cfg.Port != "8080" {
		t.Errorf("expected default Port 8080, got %q", cfg.Port)
	}
	if cfg.MshaliaAPIURL != "http://localhost:3001" {
		t.Errorf("expected default MshaliaAPIURL, got %q", cfg.MshaliaAPIURL)
	}
	if cfg.NimBaseURL != "https://integrate.api.nvidia.com/v1" {
		t.Errorf("expected default NimBaseURL, got %q", cfg.NimBaseURL)
	}
	if cfg.NimModel != "meta/llama-3.1-70b-instruct" {
		t.Errorf("expected default NimModel, got %q", cfg.NimModel)
	}
	if cfg.OpenaiAPIBase != "https://api.openai.com/v1" {
		t.Errorf("expected default OpenaiAPIBase, got %q", cfg.OpenaiAPIBase)
	}
	if cfg.SttProvider != "groq" {
		t.Errorf("expected default SttProvider groq, got %q", cfg.SttProvider)
	}
	if cfg.SttModel != "whisper-large-v3" {
		t.Errorf("expected default SttModel, got %q", cfg.SttModel)
	}
	if cfg.TtsProvider != "google" {
		t.Errorf("expected default TtsProvider google, got %q", cfg.TtsProvider)
	}
	if cfg.LlmFallbackModel != "gpt-4o-mini" {
		t.Errorf("expected default LlmFallbackModel, got %q", cfg.LlmFallbackModel)
	}
	if cfg.SecureCookie {
		t.Error("expected SecureCookie to default false")
	}
	if cfg.SessionSecret == "" {
		t.Error("expected a generated SessionSecret when unset and SecureCookie=false")
	}
	if cfg.RetentionDays != 90 {
		t.Errorf("expected default RetentionDays 90, got %d", cfg.RetentionDays)
	}
	if cfg.MaxInflight != 32 {
		t.Errorf("expected default MaxInflight 32, got %d", cfg.MaxInflight)
	}
	if cfg.VoiceStoragePrefix != "voice-notes" {
		t.Errorf("expected default VoiceStoragePrefix, got %q", cfg.VoiceStoragePrefix)
	}
	if cfg.VoiceSpoolDir != "voice-spool" {
		t.Errorf("expected default VoiceSpoolDir, got %q", cfg.VoiceSpoolDir)
	}
}

func TestLoadConfig_Overrides(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("PORT", "9090")
	t.Setenv("MSHALIA_API_URL", "https://mshalia.example.com")
	t.Setenv("AGENT_GATEWAY_SECRET", "secret123")
	t.Setenv("NIM_API_KEY", "nim-key")
	t.Setenv("NIM_BASE_URL", "https://nim.example.com")
	t.Setenv("NIM_MODEL", "custom-model")
	t.Setenv("STT_PROVIDER", "google")
	t.Setenv("STT_MODEL", "custom-stt")
	t.Setenv("OPENAI_API_KEY", "oai-key")
	t.Setenv("OPENAI_API_BASE", "https://oai.example.com")
	t.Setenv("HF_API_KEY", "hf-key")
	t.Setenv("TTS_PROVIDER", "local")
	t.Setenv("TTS_MODEL", "custom-tts")
	t.Setenv("PAIR_PHONE_NUMBER", "9665XXXXXXXX")
	t.Setenv("SESSION_SECRET", "fixed-secret")
	t.Setenv("GROQ_API_KEY", "groq-key")
	t.Setenv("GCP_API_KEY", "gcp-key")
	t.Setenv("SECURE_COOKIE", "true")
	t.Setenv("ADMIN_USERNAME", "admin")
	t.Setenv("ADMIN_PASSWORD", "hunter2")
	t.Setenv("LLM_FALLBACK_MODEL", "gpt-5")
	t.Setenv("ERROR_WEBHOOK_URL", "https://hooks.example.com/x")
	t.Setenv("RETENTION_DAYS", "30")
	t.Setenv("MAX_INFLIGHT", "8")
	t.Setenv("VOICE_STORAGE_BUCKET", "my-bucket")
	t.Setenv("VOICE_STORAGE_PREFIX", "custom-prefix")
	t.Setenv("VOICE_SPOOL_DIR", "custom-spool")
	t.Setenv("DEFAULT_ORG_ID", "org-1")
	t.Setenv("DATABASE_URL", "postgres://example")

	cfg := LoadConfig()

	if cfg.Port != "9090" {
		t.Errorf("Port = %q", cfg.Port)
	}
	if cfg.MshaliaAPIURL != "https://mshalia.example.com" {
		t.Errorf("MshaliaAPIURL = %q", cfg.MshaliaAPIURL)
	}
	if cfg.AgentGatewaySecret != "secret123" {
		t.Errorf("AgentGatewaySecret = %q", cfg.AgentGatewaySecret)
	}
	if cfg.NimAPIKey != "nim-key" {
		t.Errorf("NimAPIKey = %q", cfg.NimAPIKey)
	}
	if cfg.NimBaseURL != "https://nim.example.com" {
		t.Errorf("NimBaseURL = %q", cfg.NimBaseURL)
	}
	if cfg.NimModel != "custom-model" {
		t.Errorf("NimModel = %q", cfg.NimModel)
	}
	if cfg.SttProvider != "google" {
		t.Errorf("SttProvider = %q", cfg.SttProvider)
	}
	if cfg.SttModel != "custom-stt" {
		t.Errorf("SttModel = %q", cfg.SttModel)
	}
	if cfg.OpenaiAPIKey != "oai-key" {
		t.Errorf("OpenaiAPIKey = %q", cfg.OpenaiAPIKey)
	}
	if cfg.OpenaiAPIBase != "https://oai.example.com" {
		t.Errorf("OpenaiAPIBase = %q", cfg.OpenaiAPIBase)
	}
	if cfg.HfAPIKey != "hf-key" {
		t.Errorf("HfAPIKey = %q", cfg.HfAPIKey)
	}
	if cfg.TtsProvider != "local" {
		t.Errorf("TtsProvider = %q", cfg.TtsProvider)
	}
	if cfg.TtsModel != "custom-tts" {
		t.Errorf("TtsModel = %q", cfg.TtsModel)
	}
	if cfg.PairPhoneNumber != "9665XXXXXXXX" {
		t.Errorf("PairPhoneNumber = %q", cfg.PairPhoneNumber)
	}
	if cfg.SessionSecret != "fixed-secret" {
		t.Errorf("SessionSecret = %q", cfg.SessionSecret)
	}
	if cfg.GroqAPIKey != "groq-key" {
		t.Errorf("GroqAPIKey = %q", cfg.GroqAPIKey)
	}
	if cfg.GcpApiKey != "gcp-key" {
		t.Errorf("GcpApiKey = %q", cfg.GcpApiKey)
	}
	if !cfg.SecureCookie {
		t.Error("expected SecureCookie true")
	}
	if cfg.AdminUsername != "admin" {
		t.Errorf("AdminUsername = %q", cfg.AdminUsername)
	}
	if cfg.AdminPassword != "hunter2" {
		t.Errorf("AdminPassword = %q", cfg.AdminPassword)
	}
	if cfg.LlmFallbackModel != "gpt-5" {
		t.Errorf("LlmFallbackModel = %q", cfg.LlmFallbackModel)
	}
	if cfg.ErrorWebhookURL != "https://hooks.example.com/x" {
		t.Errorf("ErrorWebhookURL = %q", cfg.ErrorWebhookURL)
	}
	if cfg.RetentionDays != 30 {
		t.Errorf("RetentionDays = %d", cfg.RetentionDays)
	}
	if cfg.MaxInflight != 8 {
		t.Errorf("MaxInflight = %d", cfg.MaxInflight)
	}
	if cfg.VoiceStorageBucket != "my-bucket" {
		t.Errorf("VoiceStorageBucket = %q", cfg.VoiceStorageBucket)
	}
	if cfg.VoiceStoragePrefix != "custom-prefix" {
		t.Errorf("VoiceStoragePrefix = %q", cfg.VoiceStoragePrefix)
	}
	if cfg.VoiceSpoolDir != "custom-spool" {
		t.Errorf("VoiceSpoolDir = %q", cfg.VoiceSpoolDir)
	}
	if cfg.DefaultOrgID != "org-1" {
		t.Errorf("DefaultOrgID = %q", cfg.DefaultOrgID)
	}
	if cfg.DatabaseURL != "postgres://example" {
		t.Errorf("DatabaseURL = %q", cfg.DatabaseURL)
	}
}

func TestGetEnvInt(t *testing.T) {
	t.Run("missing uses default", func(t *testing.T) {
		t.Setenv("TEST_ENV_INT_MISSING", "")
		if got := GetEnvInt("TEST_ENV_INT_MISSING", 42); got != 42 {
			t.Errorf("got %d, want 42", got)
		}
	})
	t.Run("valid int overrides default", func(t *testing.T) {
		t.Setenv("TEST_ENV_INT_VALID", "17")
		if got := GetEnvInt("TEST_ENV_INT_VALID", 42); got != 17 {
			t.Errorf("got %d, want 17", got)
		}
	})
	t.Run("invalid int uses default", func(t *testing.T) {
		t.Setenv("TEST_ENV_INT_INVALID", "not-a-number")
		if got := GetEnvInt("TEST_ENV_INT_INVALID", 42); got != 42 {
			t.Errorf("got %d, want 42", got)
		}
	})
}
