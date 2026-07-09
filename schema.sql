-- Sawt Platform Postgres Database Schema DDL

CREATE TABLE IF NOT EXISTS settings (
    id SERIAL PRIMARY KEY,
    tts_model TEXT NOT NULL DEFAULT 'habibi',
    model_ids JSONB NOT NULL DEFAULT '{"habibi":"habibi-tts","silma":"silma-tts","whisper":"openai/whisper-large-v3"}'::jsonb,
    default_speed REAL NOT NULL DEFAULT 1.0,
    bot_config JSONB NOT NULL DEFAULT '{}'::jsonb,
    assistant_agent_id TEXT
);

CREATE TABLE IF NOT EXISTS tts_history (
    id TEXT PRIMARY KEY,
    ts TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    text TEXT NOT NULL,
    model TEXT NOT NULL,
    speed REAL NOT NULL,
    duration_ms INTEGER NOT NULL,
    size_bytes INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS stt_history (
    id TEXT PRIMARY KEY,
    ts TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    transcript TEXT NOT NULL,
    filename TEXT NOT NULL,
    duration_ms INTEGER NOT NULL,
    language TEXT NOT NULL DEFAULT 'ar'
);

CREATE TABLE IF NOT EXISTS webhook_logs (
    id TEXT PRIMARY KEY,
    type TEXT NOT NULL,
    ts TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    status INTEGER NOT NULL,
    input_preview TEXT NOT NULL DEFAULT '',
    duration_ms INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS agents (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    project_id TEXT NOT NULL DEFAULT 'Default Project (****75e1)',
    hosting_region TEXT NOT NULL DEFAULT 'Europe',
    status TEXT NOT NULL DEFAULT 'draft',
    last_edited TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_published TIMESTAMPTZ,
    template TEXT NOT NULL DEFAULT 'Blank Template',
    system_prompt TEXT NOT NULL DEFAULT '',
    greeting_message TEXT NOT NULL DEFAULT '',
    failure_message TEXT NOT NULL DEFAULT '',
    model_type TEXT NOT NULL DEFAULT 'asr-llm-tts',
    asr JSONB NOT NULL DEFAULT '{"vendor":"deepgram","model":"nova-3","language":"en"}'::jsonb,
    llm JSONB NOT NULL DEFAULT '{"vendor":"openai","url":"https://api.openai.com/v1/chat/completions","model":"gpt-4o-mini"}'::jsonb,
    tts JSONB NOT NULL DEFAULT '{"vendor":"minimax","model":"speech-2.8-turbo","voice":"Radiant Girl"}'::jsonb,
    turn_detection BOOLEAN NOT NULL DEFAULT TRUE,
    start_of_speech BOOLEAN NOT NULL DEFAULT TRUE,
    end_of_speech BOOLEAN NOT NULL DEFAULT TRUE,
    selective_attention_locking BOOLEAN NOT NULL DEFAULT FALSE,
    filler_words BOOLEAN NOT NULL DEFAULT FALSE,
    max_history INTEGER NOT NULL DEFAULT 10,
    mcp_servers JSONB NOT NULL DEFAULT '[]'::jsonb,
    skills JSONB NOT NULL DEFAULT '[]'::jsonb
);

CREATE TABLE IF NOT EXISTS users (
    id SERIAL PRIMARY KEY,
    username VARCHAR(100) UNIQUE NOT NULL,
    password_hash TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS health_check (
    id SERIAL PRIMARY KEY,
    note TEXT NOT NULL DEFAULT 'ok',
    checked_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS wa_contacts (
    chat_id TEXT PRIMARY KEY,
    name TEXT NOT NULL DEFAULT '',
    enabled BOOLEAN NOT NULL DEFAULT FALSE,
    agent_id TEXT,
    prompt_override TEXT,
    contact_type TEXT NOT NULL DEFAULT 'viewer',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS wa_activity (
    id TEXT PRIMARY KEY,
    ts TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    chat_id TEXT NOT NULL,
    contact_name TEXT NOT NULL DEFAULT '',
    direction TEXT NOT NULL DEFAULT 'in',
    msg_type TEXT NOT NULL DEFAULT 'ptt',
    transcript TEXT NOT NULL DEFAULT '',
    reply TEXT NOT NULL DEFAULT '',
    language TEXT NOT NULL DEFAULT 'ar',
    agent_id TEXT,
    llm_model TEXT,
    tts_model TEXT,
    tool_calls JSONB NOT NULL DEFAULT '[]'::jsonb,
    stt_ms INTEGER NOT NULL DEFAULT 0,
    llm_ms INTEGER NOT NULL DEFAULT 0,
    tts_ms INTEGER NOT NULL DEFAULT 0,
    total_ms INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'ok',
    error TEXT
);

CREATE TABLE IF NOT EXISTS conversation_turns (
    id BIGSERIAL PRIMARY KEY,
    chat_id TEXT NOT NULL,
    role TEXT NOT NULL,
    content TEXT NOT NULL,
    ts TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS conversation_state (
    chat_id TEXT PRIMARY KEY,
    summary TEXT NOT NULL DEFAULT '',
    summarized_through BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS pending_confirmations (
    chat_id TEXT PRIMARY KEY,
    tool_id TEXT NOT NULL,
    args JSONB NOT NULL DEFAULT '{}'::jsonb,
    org_id TEXT NOT NULL,
    acting_user_uid TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_conversation_turns_chat_id ON conversation_turns (chat_id, id DESC);
CREATE INDEX IF NOT EXISTS idx_tts_history_ts ON tts_history (ts DESC);
CREATE INDEX IF NOT EXISTS idx_stt_history_ts ON stt_history (ts DESC);
CREATE INDEX IF NOT EXISTS idx_wa_contacts_updated ON wa_contacts (updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_wa_activity_ts ON wa_activity (ts DESC);

-- Idempotent column addition for existing tables
ALTER TABLE wa_contacts ADD COLUMN IF NOT EXISTS contact_type TEXT NOT NULL DEFAULT 'viewer';
