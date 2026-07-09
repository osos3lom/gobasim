package database

import (
	"context"
	"time"
)

// -- GetUserByUsername --
const getUserByUsername = `-- name: GetUserByUsername :one
SELECT id, username, password_hash, created_at FROM users
WHERE username = $1 LIMIT 1
`

func (q *Queries) GetUserByUsername(ctx context.Context, username string) (User, error) {
	row := q.db.QueryRow(ctx, getUserByUsername, username)
	var i User
	err := row.Scan(
		&i.ID,
		&i.Username,
		&i.PasswordHash,
		&i.CreatedAt,
	)
	return i, err
}

// -- CreateUser --
type CreateUserParams struct {
	Username     string `json:"username"`
	PasswordHash string `json:"password_hash"`
}

const createUser = `-- name: CreateUser :one
INSERT INTO users (username, password_hash, created_at)
VALUES ($1, $2, NOW())
RETURNING id, username, password_hash, created_at
`

func (q *Queries) CreateUser(ctx context.Context, arg CreateUserParams) (User, error) {
	row := q.db.QueryRow(ctx, createUser, arg.Username, arg.PasswordHash)
	var i User
	err := row.Scan(
		&i.ID,
		&i.Username,
		&i.PasswordHash,
		&i.CreatedAt,
	)
	return i, err
}

// -- GetSettings --
const getSettings = `-- name: GetSettings :one
SELECT id, tts_model, model_ids, default_speed, bot_config, assistant_agent_id FROM settings
ORDER BY id LIMIT 1
`

func (q *Queries) GetSettings(ctx context.Context) (Setting, error) {
	row := q.db.QueryRow(ctx, getSettings)
	var i Setting
	err := row.Scan(
		&i.ID,
		&i.TtsModel,
		&i.ModelIds,
		&i.DefaultSpeed,
		&i.BotConfig,
		&i.AssistantAgentID,
	)
	return i, err
}

// -- UpdateSettings --
type UpdateSettingsParams struct {
	TtsModel         string  `json:"tts_model"`
	ModelIds         []byte  `json:"model_ids"`
	DefaultSpeed     float32 `json:"default_speed"`
	BotConfig        []byte  `json:"bot_config"`
	AssistantAgentID *string `json:"assistant_agent_id"`
}

const updateSettings = `-- name: UpdateSettings :exec
UPDATE settings
SET tts_model = $1, model_ids = $2, default_speed = $3, bot_config = $4, assistant_agent_id = $5
WHERE id = 1
`

func (q *Queries) UpdateSettings(ctx context.Context, arg UpdateSettingsParams) error {
	_, err := q.db.Exec(ctx, updateSettings,
		arg.TtsModel,
		arg.ModelIds,
		arg.DefaultSpeed,
		arg.BotConfig,
		arg.AssistantAgentID,
	)
	return err
}

// -- CreateSttHistory --
type CreateSttHistoryParams struct {
	ID         string `json:"id"`
	Transcript string `json:"transcript"`
	Filename   string `json:"filename"`
	DurationMs int32  `json:"duration_ms"`
	Language   string `json:"language"`
}

const createSttHistory = `-- name: CreateSttHistory :exec
INSERT INTO stt_history (id, ts, transcript, filename, duration_ms, language)
VALUES ($1, NOW(), $2, $3, $4, $5)
`

func (q *Queries) CreateSttHistory(ctx context.Context, arg CreateSttHistoryParams) error {
	_, err := q.db.Exec(ctx, createSttHistory,
		arg.ID,
		arg.Transcript,
		arg.Filename,
		arg.DurationMs,
		arg.Language,
	)
	return err
}

// -- GetSttHistory --
const getSttHistory = `-- name: GetSttHistory :many
SELECT id, ts, transcript, filename, duration_ms, language FROM stt_history
ORDER BY ts DESC LIMIT $1
`

func (q *Queries) GetSttHistory(ctx context.Context, limit int32) ([]SttHistory, error) {
	rows, err := q.db.Query(ctx, getSttHistory, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []SttHistory
	for rows.Next() {
		var i SttHistory
		if err := rows.Scan(
			&i.ID,
			&i.Ts,
			&i.Transcript,
			&i.Filename,
			&i.DurationMs,
			&i.Language,
		); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

// -- CreateTtsHistory --
type CreateTtsHistoryParams struct {
	ID         string  `json:"id"`
	Text       string  `json:"text"`
	Model      string  `json:"model"`
	Speed      float32 `json:"speed"`
	DurationMs int32   `json:"duration_ms"`
	SizeBytes  int32   `json:"size_bytes"`
}

const createTtsHistory = `-- name: CreateTtsHistory :exec
INSERT INTO tts_history (id, ts, text, model, speed, duration_ms, size_bytes)
VALUES ($1, NOW(), $2, $3, $4, $5, $6)
`

func (q *Queries) CreateTtsHistory(ctx context.Context, arg CreateTtsHistoryParams) error {
	_, err := q.db.Exec(ctx, createTtsHistory,
		arg.ID,
		arg.Text,
		arg.Model,
		arg.Speed,
		arg.DurationMs,
		arg.SizeBytes,
	)
	return err
}

// -- GetTtsHistory --
const getTtsHistory = `-- name: GetTtsHistory :many
SELECT id, ts, text, model, speed, duration_ms, size_bytes FROM tts_history
ORDER BY ts DESC LIMIT $1
`

func (q *Queries) GetTtsHistory(ctx context.Context, limit int32) ([]TtsHistory, error) {
	rows, err := q.db.Query(ctx, getTtsHistory, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []TtsHistory
	for rows.Next() {
		var i TtsHistory
		if err := rows.Scan(
			&i.ID,
			&i.Ts,
			&i.Text,
			&i.Model,
			&i.Speed,
			&i.DurationMs,
			&i.SizeBytes,
		); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

// -- CreateWebhookLog --
type CreateWebhookLogParams struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	Status     int32  `json:"status"`
	Input      string `json:"input"`
	DurationMs int32  `json:"duration_ms"`
}

const createWebhookLog = `-- name: CreateWebhookLog :exec
INSERT INTO webhook_logs (id, type, ts, status, input_preview, duration_ms)
VALUES ($1, $2, NOW(), $3, $4, $5)
`

func (q *Queries) CreateWebhookLog(ctx context.Context, arg CreateWebhookLogParams) error {
	_, err := q.db.Exec(ctx, createWebhookLog,
		arg.ID,
		arg.Type,
		arg.Status,
		arg.Input,
		arg.DurationMs,
	)
	return err
}

// -- GetAgent --
const getAgent = `-- name: GetAgent :one
SELECT id, name, project_id, hosting_region, status, last_edited, last_published, template, system_prompt, greeting_message, failure_message, model_type, asr, llm, tts, turn_detection, start_of_speech, end_of_speech, selective_attention_locking, filler_words, max_history, mcp_servers, skills FROM agents
WHERE id = $1 LIMIT 1
`

func (q *Queries) GetAgent(ctx context.Context, id string) (Agent, error) {
	row := q.db.QueryRow(ctx, getAgent, id)
	var i Agent
	err := row.Scan(
		&i.ID,
		&i.Name,
		&i.ProjectID,
		&i.HostingRegion,
		&i.Status,
		&i.LastEdited,
		&i.LastPublished,
		&i.Template,
		&i.SystemPrompt,
		&i.GreetingMessage,
		&i.FailureMessage,
		&i.ModelType,
		&i.Asr,
		&i.Llm,
		&i.Tts,
		&i.TurnDetection,
		&i.StartOfSpeech,
		&i.EndOfSpeech,
		&i.SelectiveAttentionLocking,
		&i.FillerWords,
		&i.MaxHistory,
		&i.McpServers,
		&i.Skills,
	)
	return i, err
}

// -- ListAgents --
const listAgents = `-- name: ListAgents :many
SELECT id, name, project_id, hosting_region, status, last_edited, last_published, template, system_prompt, greeting_message, failure_message, model_type, asr, llm, tts, turn_detection, start_of_speech, end_of_speech, selective_attention_locking, filler_words, max_history, mcp_servers, skills FROM agents
ORDER BY name
`

func (q *Queries) ListAgents(ctx context.Context) ([]Agent, error) {
	rows, err := q.db.Query(ctx, listAgents)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []Agent
	for rows.Next() {
		var i Agent
		if err := rows.Scan(
			&i.ID,
			&i.Name,
			&i.ProjectID,
			&i.HostingRegion,
			&i.Status,
			&i.LastEdited,
			&i.LastPublished,
			&i.Template,
			&i.SystemPrompt,
			&i.GreetingMessage,
			&i.FailureMessage,
			&i.ModelType,
			&i.Asr,
			&i.Llm,
			&i.Tts,
			&i.TurnDetection,
			&i.StartOfSpeech,
			&i.EndOfSpeech,
			&i.SelectiveAttentionLocking,
			&i.FillerWords,
			&i.MaxHistory,
			&i.McpServers,
			&i.Skills,
		); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

// -- CreateAgent --
type CreateAgentParams struct {
	ID                        string     `json:"id"`
	Name                      string     `json:"name"`
	ProjectID                 string     `json:"project_id"`
	HostingRegion             string     `json:"hosting_region"`
	Status                    string     `json:"status"`
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

const createAgent = `-- name: CreateAgent :one
INSERT INTO agents (
    id, name, project_id, hosting_region, status, last_edited, 
    last_published, template, system_prompt, greeting_message, 
    failure_message, model_type, asr, llm, tts, turn_detection, 
    start_of_speech, end_of_speech, selective_attention_locking, 
    filler_words, max_history, mcp_servers, skills
) VALUES (
    $1, $2, $3, $4, $5, NOW(), 
    $6, $7, $8, $9, 
    $10, $11, $12, $13, $14, $15, 
    $16, $17, $18, 
    $19, $20, $21, $22
) RETURNING id, name, project_id, hosting_region, status, last_edited, last_published, template, system_prompt, greeting_message, failure_message, model_type, asr, llm, tts, turn_detection, start_of_speech, end_of_speech, selective_attention_locking, filler_words, max_history, mcp_servers, skills
`

func (q *Queries) CreateAgent(ctx context.Context, arg CreateAgentParams) (Agent, error) {
	row := q.db.QueryRow(ctx, createAgent,
		arg.ID, arg.Name, arg.ProjectID, arg.HostingRegion, arg.Status,
		arg.LastPublished, arg.Template, arg.SystemPrompt, arg.GreetingMessage,
		arg.FailureMessage, arg.ModelType, arg.Asr, arg.Llm, arg.Tts, arg.TurnDetection,
		arg.StartOfSpeech, arg.EndOfSpeech, arg.SelectiveAttentionLocking,
		arg.FillerWords, arg.MaxHistory, arg.McpServers, arg.Skills,
	)
	var i Agent
	err := row.Scan(
		&i.ID, &i.Name, &i.ProjectID, &i.HostingRegion, &i.Status, &i.LastEdited,
		&i.LastPublished, &i.Template, &i.SystemPrompt, &i.GreetingMessage,
		&i.FailureMessage, &i.ModelType, &i.Asr, &i.Llm, &i.Tts, &i.TurnDetection,
		&i.StartOfSpeech, &i.EndOfSpeech, &i.SelectiveAttentionLocking,
		&i.FillerWords, &i.MaxHistory, &i.McpServers, &i.Skills,
	)
	return i, err
}

// -- UpdateAgentWorkflow --
type UpdateAgentWorkflowParams struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	SystemPrompt    string `json:"system_prompt"`
	GreetingMessage string `json:"greeting_message"`
	FailureMessage  string `json:"failure_message"`
	Asr             []byte `json:"asr"`
	Llm             []byte `json:"llm"`
	Tts             []byte `json:"tts"`
	MaxHistory      int32  `json:"max_history"`
	Status          string `json:"status"`
}

const updateAgentWorkflow = `-- name: UpdateAgentWorkflow :one
UPDATE agents
SET name = $2, system_prompt = $3, greeting_message = $4, failure_message = $5,
    asr = $6, llm = $7, tts = $8, max_history = $9, status = $10, last_edited = NOW()
WHERE id = $1
RETURNING id, name, project_id, hosting_region, status, last_edited, last_published, template, system_prompt, greeting_message, failure_message, model_type, asr, llm, tts, turn_detection, start_of_speech, end_of_speech, selective_attention_locking, filler_words, max_history, mcp_servers, skills
`

func (q *Queries) UpdateAgentWorkflow(ctx context.Context, arg UpdateAgentWorkflowParams) (Agent, error) {
	row := q.db.QueryRow(ctx, updateAgentWorkflow,
		arg.ID, arg.Name, arg.SystemPrompt, arg.GreetingMessage, arg.FailureMessage,
		arg.Asr, arg.Llm, arg.Tts, arg.MaxHistory, arg.Status,
	)
	var i Agent
	err := row.Scan(
		&i.ID, &i.Name, &i.ProjectID, &i.HostingRegion, &i.Status, &i.LastEdited,
		&i.LastPublished, &i.Template, &i.SystemPrompt, &i.GreetingMessage,
		&i.FailureMessage, &i.ModelType, &i.Asr, &i.Llm, &i.Tts, &i.TurnDetection,
		&i.StartOfSpeech, &i.EndOfSpeech, &i.SelectiveAttentionLocking,
		&i.FillerWords, &i.MaxHistory, &i.McpServers, &i.Skills,
	)
	return i, err
}

// -- GetWaContact --
const getWaContact = `-- name: GetWaContact :one
SELECT chat_id, name, enabled, agent_id, prompt_override, contact_type, updated_at FROM wa_contacts
WHERE chat_id = $1 LIMIT 1
`

func (q *Queries) GetWaContact(ctx context.Context, chatID string) (WaContact, error) {
	row := q.db.QueryRow(ctx, getWaContact, chatID)
	var i WaContact
	err := row.Scan(
		&i.ChatID,
		&i.Name,
		&i.Enabled,
		&i.AgentID,
		&i.PromptOverride,
		&i.ContactType,
		&i.UpdatedAt,
	)
	return i, err
}

// -- CreateOrUpdateWaContact --
type CreateOrUpdateWaContactParams struct {
	ChatID         string  `json:"chat_id"`
	Name           string  `json:"name"`
	Enabled        bool    `json:"enabled"`
	AgentID        *string `json:"agent_id"`
	PromptOverride *string `json:"prompt_override"`
	ContactType    string  `json:"contact_type"`
}

const createOrUpdateWaContact = `-- name: CreateOrUpdateWaContact :one
INSERT INTO wa_contacts (chat_id, name, enabled, agent_id, prompt_override, contact_type, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, NOW())
ON CONFLICT (chat_id) DO UPDATE
SET name = EXCLUDED.name, enabled = EXCLUDED.enabled, agent_id = EXCLUDED.agent_id, 
    prompt_override = EXCLUDED.prompt_override, contact_type = EXCLUDED.contact_type, updated_at = NOW()
RETURNING chat_id, name, enabled, agent_id, prompt_override, contact_type, updated_at
`

func (q *Queries) CreateOrUpdateWaContact(ctx context.Context, arg CreateOrUpdateWaContactParams) (WaContact, error) {
	row := q.db.QueryRow(ctx, createOrUpdateWaContact,
		arg.ChatID,
		arg.Name,
		arg.Enabled,
		arg.AgentID,
		arg.PromptOverride,
		arg.ContactType,
	)
	var i WaContact
	err := row.Scan(
		&i.ChatID,
		&i.Name,
		&i.Enabled,
		&i.AgentID,
		&i.PromptOverride,
		&i.ContactType,
		&i.UpdatedAt,
	)
	return i, err
}

// -- UpdateWaContactSettings --
type UpdateWaContactSettingsParams struct {
	ChatID      string  `json:"chat_id"`
	Enabled     bool    `json:"enabled"`
	AgentID     *string `json:"agent_id"`
	ContactType string  `json:"contact_type"`
}

const updateWaContactSettings = `-- name: UpdateWaContactSettings :one
UPDATE wa_contacts
SET enabled = $2, agent_id = $3, contact_type = $4, updated_at = NOW()
WHERE chat_id = $1
RETURNING chat_id, name, enabled, agent_id, prompt_override, contact_type, updated_at
`

func (q *Queries) UpdateWaContactSettings(ctx context.Context, arg UpdateWaContactSettingsParams) (WaContact, error) {
	row := q.db.QueryRow(ctx, updateWaContactSettings,
		arg.ChatID,
		arg.Enabled,
		arg.AgentID,
		arg.ContactType,
	)
	var i WaContact
	err := row.Scan(
		&i.ChatID,
		&i.Name,
		&i.Enabled,
		&i.AgentID,
		&i.PromptOverride,
		&i.ContactType,
		&i.UpdatedAt,
	)
	return i, err
}

// -- ListWaContacts --
const listWaContacts = `-- name: ListWaContacts :many
SELECT chat_id, name, enabled, agent_id, prompt_override, contact_type, updated_at FROM wa_contacts
ORDER BY updated_at DESC
`

func (q *Queries) ListWaContacts(ctx context.Context) ([]WaContact, error) {
	rows, err := q.db.Query(ctx, listWaContacts)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []WaContact
	for rows.Next() {
		var i WaContact
		if err := rows.Scan(
			&i.ChatID,
			&i.Name,
			&i.Enabled,
			&i.AgentID,
			&i.PromptOverride,
			&i.ContactType,
			&i.UpdatedAt,
		); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

// -- CreateWaActivity --
type CreateWaActivityParams struct {
	ID          string  `json:"id"`
	ChatID      string  `json:"chat_id"`
	ContactName string  `json:"contact_name"`
	Direction   string  `json:"direction"`
	MsgType     string  `json:"msg_type"`
	Transcript  string  `json:"transcript"`
	Reply       string  `json:"reply"`
	Language    string  `json:"language"`
	AgentID     *string `json:"agent_id"`
	LlmModel    *string `json:"llm_model"`
	TtsModel    *string `json:"tts_model"`
	ToolCalls   []byte  `json:"tool_calls"`
	SttMs       int32   `json:"stt_ms"`
	LlmMs       int32   `json:"llm_ms"`
	TtsMs       int32   `json:"tts_ms"`
	TotalMs     int32   `json:"total_ms"`
	Status      string  `json:"status"`
	Error       *string `json:"error"`
}

const createWaActivity = `-- name: CreateWaActivity :exec
INSERT INTO wa_activity (
    id, ts, chat_id, contact_name, direction, msg_type, transcript, 
    reply, language, agent_id, llm_model, tts_model, tool_calls, 
    stt_ms, llm_ms, tts_ms, total_ms, status, error
) VALUES (
    $1, NOW(), $2, $3, $4, $5, $6, 
    $7, $8, $9, $10, $11, $12, 
    $13, $14, $15, $16, $17, $18
)
`

func (q *Queries) CreateWaActivity(ctx context.Context, arg CreateWaActivityParams) error {
	_, err := q.db.Exec(ctx, createWaActivity,
		arg.ID, arg.ChatID, arg.ContactName, arg.Direction, arg.MsgType,
		arg.Transcript, arg.Reply, arg.Language, arg.AgentID, arg.LlmModel,
		arg.TtsModel, arg.ToolCalls, arg.SttMs, arg.LlmMs, arg.TtsMs,
		arg.TotalMs, arg.Status, arg.Error,
	)
	return err
}

// -- ListRecentWaActivity --
const listRecentWaActivity = `-- name: ListRecentWaActivity :many
SELECT id, ts, chat_id, contact_name, direction, msg_type, transcript, reply, language, agent_id, llm_model, tts_model, tool_calls, stt_ms, llm_ms, tts_ms, total_ms, status, error FROM wa_activity
ORDER BY ts DESC LIMIT $1
`

func (q *Queries) ListRecentWaActivity(ctx context.Context, limit int32) ([]WaActivity, error) {
	rows, err := q.db.Query(ctx, listRecentWaActivity, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []WaActivity
	for rows.Next() {
		var i WaActivity
		if err := rows.Scan(
			&i.ID, &i.Ts, &i.ChatID, &i.ContactName, &i.Direction, &i.MsgType,
			&i.Transcript, &i.Reply, &i.Language, &i.AgentID, &i.LlmModel,
			&i.TtsModel, &i.ToolCalls, &i.SttMs, &i.LlmMs, &i.TtsMs,
			&i.TotalMs, &i.Status, &i.Error,
		); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}
