-- name: GetUserByUsername :one
SELECT * FROM users
WHERE username = $1 LIMIT 1;

-- name: CreateUser :one
INSERT INTO users (username, password_hash, created_at)
VALUES ($1, $2, NOW())
RETURNING *;

-- name: GetSettings :one
SELECT * FROM settings
ORDER BY id LIMIT 1;

-- name: UpdateSettings :exec
UPDATE settings
SET tts_model = $1, model_ids = $2, default_speed = $3, bot_config = $4, assistant_agent_id = $5
WHERE id = 1;

-- name: CreateSttHistory :exec
INSERT INTO stt_history (id, ts, transcript, filename, duration_ms, language)
VALUES ($1, NOW(), $2, $3, $4, $5);

-- name: GetSttHistory :many
SELECT * FROM stt_history
ORDER BY ts DESC LIMIT $1;

-- name: CreateTtsHistory :exec
INSERT INTO tts_history (id, ts, text, model, speed, duration_ms, size_bytes)
VALUES ($1, NOW(), $2, $3, $4, $5, $6);

-- name: GetTtsHistory :many
SELECT * FROM tts_history
ORDER BY ts DESC LIMIT $1;

-- name: CreateWebhookLog :exec
INSERT INTO webhook_logs (id, type, ts, status, input_preview, duration_ms)
VALUES ($1, $2, NOW(), $3, $4, $5);

-- name: GetAgent :one
SELECT * FROM agents
WHERE id = $1 LIMIT 1;

-- name: ListAgents :many
SELECT * FROM agents
ORDER BY name;

-- name: CreateAgent :one
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
) RETURNING *;

-- name: UpdateAgentWorkflow :one
UPDATE agents
SET name = $2, system_prompt = $3, greeting_message = $4, failure_message = $5,
    asr = $6, llm = $7, tts = $8, max_history = $9, status = $10, last_edited = NOW()
WHERE id = $1
RETURNING *;

-- name: GetWaContact :one
SELECT * FROM wa_contacts
WHERE chat_id = $1 LIMIT 1;

-- name: CreateOrUpdateWaContact :one
INSERT INTO wa_contacts (chat_id, name, enabled, agent_id, prompt_override, updated_at)
VALUES ($1, $2, $3, $4, $5, NOW())
ON CONFLICT (chat_id) DO UPDATE
SET name = EXCLUDED.name, enabled = EXCLUDED.enabled, agent_id = EXCLUDED.agent_id, 
    prompt_override = EXCLUDED.prompt_override, updated_at = NOW()
RETURNING *;

-- name: ListWaContacts :many
SELECT * FROM wa_contacts
ORDER BY updated_at DESC;

-- name: CreateWaActivity :exec
INSERT INTO wa_activity (
    id, ts, chat_id, contact_name, direction, msg_type, transcript, 
    reply, language, agent_id, llm_model, tts_model, tool_calls, 
    stt_ms, llm_ms, tts_ms, total_ms, status, error
) VALUES (
    $1, NOW(), $2, $3, $4, $5, $6, 
    $7, $8, $9, $10, $11, $12, 
    $13, $14, $15, $16, $17, $18
);

-- name: ListRecentWaActivity :many
SELECT * FROM wa_activity
ORDER BY ts DESC LIMIT $1;
