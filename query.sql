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


-- name: GetAgent :one
SELECT * FROM agents
WHERE id = $1 LIMIT 1;

-- name: ListAgents :many
SELECT * FROM agents
ORDER BY name;

-- name: ListPublishedAgents :many
SELECT * FROM agents
WHERE status = 'published'
ORDER BY name;

-- name: CreateAgent :one
INSERT INTO agents (
    id, name, project_id, hosting_region, status, last_edited,
    last_published, template, system_prompt, greeting_message,
    failure_message, model_type, asr, llm, tts, turn_detection,
    start_of_speech, end_of_speech, selective_attention_locking,
    filler_words, max_history, mcp_servers, skills, sub_agents,
    clarification_rules
) VALUES (
    $1, $2, $3, $4, $5, NOW(),
    $6, $7, $8, $9,
    $10, $11, $12, $13, $14, $15,
    $16, $17, $18,
    $19, $20, $21, $22, $23,
    $24
) RETURNING *;

-- name: UpdateAgentWorkflow :one
-- last_published is stamped only on the draft->published transition (D4);
-- the CASE reads the pre-update status, per UPDATE..SET semantics. The panel
-- now edits the full five-block config, so every operator-editable column is set.
UPDATE agents
SET name = $2, system_prompt = $3, greeting_message = $4, failure_message = $5,
    asr = $6, llm = $7, tts = $8, max_history = $9,
    mcp_servers = $11, skills = $12, sub_agents = $13,
    turn_detection = $14, start_of_speech = $15, end_of_speech = $16,
    selective_attention_locking = $17, filler_words = $18,
    clarification_rules = $19,
    last_published = CASE WHEN $10::text = 'published' AND status <> 'published' THEN NOW() ELSE last_published END,
    status = $10, last_edited = NOW()
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

-- name: UpdateWaContactSettings :one
UPDATE wa_contacts
SET enabled = $2, agent_id = $3, updated_at = NOW()
WHERE chat_id = $1
RETURNING *;

-- name: UpdateWaContactErpLink :one
-- Persists the outcome of resolving this contact's phone against the ERP
-- (see internal/erp.Client.ResolveIdentity). erp_unresolved_reason is set
-- when resolution didn't produce an identity, and cleared otherwise.
INSERT INTO wa_contacts (chat_id, erp_uid, erp_display_name, erp_org_id, erp_role, erp_unresolved_reason, erp_resolved_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, NOW(), NOW())
ON CONFLICT (chat_id) DO UPDATE
SET erp_uid = EXCLUDED.erp_uid, erp_display_name = EXCLUDED.erp_display_name,
    erp_org_id = EXCLUDED.erp_org_id, erp_role = EXCLUDED.erp_role,
    erp_unresolved_reason = EXCLUDED.erp_unresolved_reason, erp_resolved_at = NOW()
RETURNING *;

-- name: UpdateWaContactErpOverride :one
-- Sets (or clears, with an empty string) the phone number an operator wants
-- used for ERP identity resolution instead of the one derived from chat_id.
INSERT INTO wa_contacts (chat_id, erp_phone_override, updated_at)
VALUES ($1, $2, NOW())
ON CONFLICT (chat_id) DO UPDATE
SET erp_phone_override = EXCLUDED.erp_phone_override, updated_at = NOW()
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

-- name: CreateWaMessage :exec
INSERT INTO wa_messages (id, chat_id, direction, sender, msg_type, content, status)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: ListWaMessagesByChat :many
SELECT * FROM wa_messages
WHERE chat_id = sqlc.arg(chat_id) AND (sqlc.arg(before_seq)::bigint = 0 OR seq < sqlc.arg(before_seq)::bigint)
ORDER BY seq DESC LIMIT sqlc.arg('limit')::int;

-- name: ListWaChatsSummary :many
SELECT * FROM (
    SELECT DISTINCT ON (m.chat_id)
        m.chat_id, m.content AS last_message, m.direction AS last_direction,
        m.sender AS last_sender, m.created_at AS last_message_at,
        c.name AS contact_name, c.enabled AS contact_enabled, c.agent_id AS contact_agent_id,
        c.erp_display_name AS contact_erp_display_name, c.erp_org_id AS contact_erp_org_id,
        c.erp_role AS contact_erp_role, c.erp_unresolved_reason AS contact_erp_unresolved_reason,
        c.erp_phone_override AS contact_erp_phone_override
    FROM wa_messages m
    LEFT JOIN wa_contacts c ON c.chat_id = m.chat_id
    ORDER BY m.chat_id, m.created_at DESC, m.id DESC
) t
ORDER BY last_message_at DESC;

-- name: RedactWaMessagesBefore :exec
UPDATE wa_messages
SET content = '[redacted]'
WHERE created_at < $1 AND content <> '[redacted]';

-- name: CreateConversationTurn :one
INSERT INTO conversation_turns (chat_id, role, content)
VALUES ($1, $2, $3)
RETURNING id, chat_id, role, content, ts;

-- name: ListConversationTurnsAfter :many
SELECT id, chat_id, role, content, ts FROM conversation_turns
WHERE chat_id = sqlc.arg(chat_id) AND id > sqlc.arg(after_id)::bigint
ORDER BY id ASC
LIMIT sqlc.arg('limit')::int;

-- name: GetConversationState :one
SELECT chat_id, summary, summarized_through, updated_at FROM conversation_state
WHERE chat_id = $1;

-- name: UpsertConversationState :exec
INSERT INTO conversation_state (chat_id, summary, summarized_through, updated_at)
VALUES ($1, $2, $3, NOW())
ON CONFLICT (chat_id) DO UPDATE
SET summary = EXCLUDED.summary, summarized_through = EXCLUDED.summarized_through, updated_at = NOW();

-- name: UpsertPendingConfirmation :exec
-- Also resets missing_fields/collect_rounds/intent to their defaults on
-- conflict, so a completed 'collecting' row that graduates into an ordinary
-- confirmation (F-1 fix) doesn't carry stale slot-filling state forward.
INSERT INTO pending_confirmations (chat_id, tool_id, args, org_id, acting_user_uid, description, status, claimed_at, created_at, expires_at, missing_fields, collect_rounds, intent)
VALUES ($1, $2, $3, $4, $5, $6, 'pending', NULL, NOW(), $7, '[]'::jsonb, 0, '')
ON CONFLICT (chat_id) DO UPDATE
SET tool_id = EXCLUDED.tool_id, args = EXCLUDED.args, org_id = EXCLUDED.org_id,
    acting_user_uid = EXCLUDED.acting_user_uid, description = EXCLUDED.description,
    status = 'pending', claimed_at = NULL,
    created_at = NOW(), expires_at = EXCLUDED.expires_at,
    missing_fields = '[]'::jsonb, collect_rounds = 0, intent = '';

-- name: GetPendingConfirmation :one
SELECT * FROM pending_confirmations
WHERE chat_id = $1;

-- ClaimPendingConfirmation atomically transitions a chat's pending row to
-- 'executing' and returns it. Because the UPDATE matches only status='pending',
-- exactly one of two concurrent resolvers wins the row; the loser gets no row.
-- This is the guard against double-executing a confirmed (financial) tool.
-- name: ClaimPendingConfirmation :one
UPDATE pending_confirmations
SET status = 'executing', claimed_at = NOW()
WHERE chat_id = $1 AND status = 'pending'
RETURNING *;

-- name: DeletePendingConfirmation :exec
DELETE FROM pending_confirmations WHERE chat_id = $1;

-- name: PurgeExpiredConfirmations :exec
DELETE FROM pending_confirmations WHERE expires_at < $1;

-- UpsertCollecting parks a tool call that is still missing required args
-- (F-1 fix). Mirrors UpsertPendingConfirmation's upsert shape exactly, but
-- with status='collecting' and the extra slot-filling columns populated.
-- name: UpsertCollecting :exec
INSERT INTO pending_confirmations (chat_id, tool_id, args, org_id, acting_user_uid, description, status, claimed_at, created_at, expires_at, missing_fields, collect_rounds, intent)
VALUES ($1, $2, $3, $4, $5, $6, 'collecting', NULL, NOW(), $7, $8, $9, $10)
ON CONFLICT (chat_id) DO UPDATE
SET tool_id = EXCLUDED.tool_id, args = EXCLUDED.args, org_id = EXCLUDED.org_id,
    acting_user_uid = EXCLUDED.acting_user_uid, description = EXCLUDED.description,
    status = 'collecting', claimed_at = NULL,
    created_at = NOW(), expires_at = EXCLUDED.expires_at,
    missing_fields = EXCLUDED.missing_fields, collect_rounds = EXCLUDED.collect_rounds,
    intent = EXCLUDED.intent;

-- ClaimCollecting mirrors ClaimPendingConfirmation exactly, atomically
-- transitioning 'collecting' -> 'collecting_claimed' so two concurrent
-- inbound messages for the same chat can't both resume the same round.
-- name: ClaimCollecting :one
UPDATE pending_confirmations
SET status = 'collecting_claimed', claimed_at = NOW()
WHERE chat_id = $1 AND status = 'collecting'
RETURNING *;

-- name: PurgeSttHistoryBefore :exec
DELETE FROM stt_history WHERE ts < $1;

-- name: PurgeTtsHistoryBefore :exec
DELETE FROM tts_history WHERE ts < $1;

-- name: PurgeConversationTurnsBefore :exec
DELETE FROM conversation_turns WHERE ts < $1;

-- name: RedactWaActivityBefore :exec
UPDATE wa_activity
SET transcript = '[redacted]', reply = '[redacted]'
WHERE ts < $1 AND transcript <> '[redacted]';

-- Voice-note archive (Firebase Cloud Storage ledger) ------------------------

-- name: UpsertWaVoiceNote :exec
-- ON CONFLICT DO NOTHING makes enqueueing idempotent: re-processing the same
-- WhatsApp message never creates a second upload job.
INSERT INTO wa_voice_notes (id, chat_id, direction, sender, receiver, object_path, mime_type, size_bytes, duration_seconds, status)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'pending')
ON CONFLICT (id) DO NOTHING;

-- name: MarkWaVoiceNoteUploaded :exec
UPDATE wa_voice_notes
SET status = 'uploaded', uploaded_at = NOW(), last_error = NULL
WHERE id = $1;

-- name: MarkWaVoiceNoteFailed :exec
-- Failure keeps the row 'pending' (retryable) until attempts reaches the cap,
-- then flips it to the terminal 'failed'. next_attempt_at carries the
-- caller-computed exponential backoff.
UPDATE wa_voice_notes
SET attempts = attempts + 1,
    last_error = $2,
    next_attempt_at = $3,
    status = CASE WHEN attempts + 1 >= sqlc.arg(max_attempts) THEN 'failed' ELSE 'pending' END
WHERE id = $1;

-- name: ListPendingWaVoiceNotes :many
SELECT * FROM wa_voice_notes
WHERE status = 'pending' AND next_attempt_at <= NOW()
ORDER BY next_attempt_at ASC
LIMIT $1;

-- name: PurgeWaVoiceNotesBefore :exec
DELETE FROM wa_voice_notes WHERE created_at < $1;

-- name: MarkMessageProcessed :one
INSERT INTO processed_messages (id) VALUES ($1)
ON CONFLICT (id) DO NOTHING
RETURNING id;

-- name: PurgeProcessedMessagesBefore :exec
DELETE FROM processed_messages WHERE processed_at < $1;

-- name: CreateToolExecution :exec
INSERT INTO tool_executions (chat_id, tool_id, args, result, status)
VALUES ($1, $2, $3, $4, $5);

-- name: ListToolExecutionsByChat :many
SELECT * FROM tool_executions
WHERE chat_id = $1
ORDER BY ts DESC
LIMIT $2;

-- name: PurgeToolExecutionsBefore :exec
DELETE FROM tool_executions WHERE ts < $1;

-- name: ListPendingConfirmations :many
SELECT * FROM pending_confirmations
WHERE status = 'pending'
ORDER BY created_at DESC;
