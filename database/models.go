package database

import (
	"time"
)

type Setting struct {
	ID               int32   `json:"id"`
	TtsModel         string  `json:"tts_model"`
	ModelIds         []byte  `json:"model_ids"`
	DefaultSpeed     float32 `json:"default_speed"`
	BotConfig        []byte  `json:"bot_config"`
	AssistantAgentID *string `json:"assistant_agent_id"`
}

type TtsHistory struct {
	ID         string    `json:"id"`
	Ts         time.Time `json:"ts"`
	Text       string    `json:"text"`
	Model      string    `json:"model"`
	Speed      float32   `json:"speed"`
	DurationMs int32     `json:"duration_ms"`
	SizeBytes  int32     `json:"size_bytes"`
}

type SttHistory struct {
	ID         string    `json:"id"`
	Ts         time.Time `json:"ts"`
	Transcript string    `json:"transcript"`
	Filename   string    `json:"filename"`
	DurationMs int32     `json:"duration_ms"`
	Language   string    `json:"language"`
}

type WebhookLog struct {
	ID           string    `json:"id"`
	Type         string    `json:"type"`
	Ts           time.Time `json:"ts"`
	Status       int32     `json:"status"`
	InputPreview string    `json:"input_preview"`
	DurationMs   int32     `json:"duration_ms"`
}

type Agent struct {
	ID                        string     `json:"id"`
	Name                      string     `json:"name"`
	ProjectID                 string     `json:"project_id"`
	HostingRegion             string     `json:"hosting_region"`
	Status                    string     `json:"status"`
	LastEdited                time.Time  `json:"last_edited"`
	LastPublished             *time.Time `json:"last_published"`
	Template                  string     `json:"template"`
	SystemPrompt              string     `json:"system_prompt"`
	GreetingMessage           string     `json:"greeting_message"`
	FailureMessage            string     `json:"failure_message"`
	ModelType                 string     `json:"model_type"`
	Asr                       []byte     `json:"asr"`
	Llm                       []byte     `json:"llm"`
	Tts                       []byte     `json:"tts"`
	TurnDetection             bool       `json:"turn_detection"`
	StartOfSpeech             bool       `json:"start_of_speech"`
	EndOfSpeech               bool       `json:"end_of_speech"`
	SelectiveAttentionLocking bool       `json:"selective_attention_locking"`
	FillerWords               bool       `json:"filler_words"`
	MaxHistory                int32      `json:"max_history"`
	McpServers                []byte     `json:"mcp_servers"`
	Skills                    []byte     `json:"skills"`
}

type User struct {
	ID           int32     `json:"id"`
	Username     string    `json:"username"`
	PasswordHash string    `json:"password_hash"`
	CreatedAt    time.Time `json:"created_at"`
}

type HealthCheck struct {
	ID        int32     `json:"id"`
	Note      string    `json:"note"`
	CheckedAt time.Time `json:"checked_at"`
}

type WaContact struct {
	ChatID         string    `json:"chat_id"`
	Name           string    `json:"name"`
	Enabled        bool      `json:"enabled"`
	AgentID        *string   `json:"agent_id"`
	PromptOverride *string   `json:"prompt_override"`
	ContactType    string    `json:"contact_type"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type ConversationTurn struct {
	ID      int64     `json:"id"`
	ChatID  string    `json:"chat_id"`
	Role    string    `json:"role"`
	Content string    `json:"content"`
	Ts      time.Time `json:"ts"`
}

type ConversationState struct {
	ChatID            string    `json:"chat_id"`
	Summary           string    `json:"summary"`
	SummarizedThrough int64     `json:"summarized_through"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type PendingConfirmation struct {
	ChatID        string    `json:"chat_id"`
	ToolID        string    `json:"tool_id"`
	Args          []byte    `json:"args"`
	OrgID         string    `json:"org_id"`
	ActingUserUid string    `json:"acting_user_uid"`
	Description   string    `json:"description"`
	CreatedAt     time.Time `json:"created_at"`
	ExpiresAt     time.Time `json:"expires_at"`
}

type WaActivity struct {
	ID          string    `json:"id"`
	Ts          time.Time `json:"ts"`
	ChatID      string    `json:"chat_id"`
	ContactName string    `json:"contact_name"`
	Direction   string    `json:"direction"`
	MsgType     string    `json:"msg_type"`
	Transcript  string    `json:"transcript"`
	Reply       string    `json:"reply"`
	Language    string    `json:"language"`
	AgentID     *string   `json:"agent_id"`
	LlmModel    *string   `json:"llm_model"`
	TtsModel    *string   `json:"tts_model"`
	ToolCalls   []byte    `json:"tool_calls"`
	SttMs       int32     `json:"stt_ms"`
	LlmMs       int32     `json:"llm_ms"`
	TtsMs       int32     `json:"tts_ms"`
	TotalMs     int32     `json:"total_ms"`
	Status      string    `json:"status"`
	Error       *string   `json:"error"`
}
