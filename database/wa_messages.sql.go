package database

import (
	"context"
	"time"
)

// -- CreateWaMessage --
type CreateWaMessageParams struct {
	ID        string `json:"id"`
	ChatID    string `json:"chat_id"`
	Direction string `json:"direction"`
	Sender    string `json:"sender"`
	MsgType   string `json:"msg_type"`
	Content   string `json:"content"`
	Status    string `json:"status"`
}

const createWaMessage = `-- name: CreateWaMessage :exec
INSERT INTO wa_messages (id, chat_id, direction, sender, msg_type, content, status)
VALUES ($1, $2, $3, $4, $5, $6, $7)
`

func (q *Queries) CreateWaMessage(ctx context.Context, arg CreateWaMessageParams) error {
	_, err := q.db.Exec(ctx, createWaMessage,
		arg.ID, arg.ChatID, arg.Direction, arg.Sender, arg.MsgType, arg.Content, arg.Status,
	)
	return err
}

// -- ListWaMessagesByChat --
// Cursor-paginated (fetch-older-than-cursor, matching how a chat UI loads
// more history scrolling up): BeforeSeq=0 means "start from the most recent
// page". seq (not the WhatsApp-message-id-derived TEXT id) is the cursor
// because message ids aren't chronologically sortable.
type ListWaMessagesByChatParams struct {
	ChatID    string `json:"chat_id"`
	BeforeSeq int64  `json:"before_seq"`
	Limit     int32  `json:"limit"`
}

const listWaMessagesByChat = `-- name: ListWaMessagesByChat :many
SELECT id, seq, chat_id, direction, sender, msg_type, content, status, created_at FROM wa_messages
WHERE chat_id = $1 AND ($2::bigint = 0 OR seq < $2)
ORDER BY seq DESC LIMIT $3
`

func (q *Queries) ListWaMessagesByChat(ctx context.Context, arg ListWaMessagesByChatParams) ([]WaMessage, error) {
	rows, err := q.db.Query(ctx, listWaMessagesByChat, arg.ChatID, arg.BeforeSeq, arg.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []WaMessage
	for rows.Next() {
		var i WaMessage
		if err := rows.Scan(
			&i.ID,
			&i.Seq,
			&i.ChatID,
			&i.Direction,
			&i.Sender,
			&i.MsgType,
			&i.Content,
			&i.Status,
			&i.CreatedAt,
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

// -- ListWaChatsSummary --
// One row per chat_id (most-recently-active first), joined against
// wa_contacts for the enabled/agent badges the Messages tab's chat list shows.
const listWaChatsSummary = `-- name: ListWaChatsSummary :many
SELECT * FROM (
    SELECT DISTINCT ON (m.chat_id)
        m.chat_id, m.content AS last_message, m.direction AS last_direction,
        m.sender AS last_sender, m.created_at AS last_message_at,
        c.name AS contact_name, c.enabled AS contact_enabled, c.agent_id AS contact_agent_id
    FROM wa_messages m
    LEFT JOIN wa_contacts c ON c.chat_id = m.chat_id
    ORDER BY m.chat_id, m.created_at DESC, m.id DESC
) t
ORDER BY last_message_at DESC
`

func (q *Queries) ListWaChatsSummary(ctx context.Context) ([]WaChatSummary, error) {
	rows, err := q.db.Query(ctx, listWaChatsSummary)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []WaChatSummary
	for rows.Next() {
		var i WaChatSummary
		if err := rows.Scan(
			&i.ChatID,
			&i.LastMessage,
			&i.LastDirection,
			&i.LastSender,
			&i.LastMessageAt,
			&i.ContactName,
			&i.ContactEnabled,
			&i.ContactAgentID,
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

// -- RedactWaMessagesBefore --
// Redacts (not hard-deletes) content in place, unlike wa_activity's approach
// mirrored here: wa_messages is the user-facing chat browser, so hard
// deletion would leave visible gaps in a conversation thread — redaction
// preserves the row/turn-taking structure while blanking PII.
const redactWaMessagesBefore = `-- name: RedactWaMessagesBefore :exec
UPDATE wa_messages
SET content = '[redacted]'
WHERE created_at < $1 AND content <> '[redacted]'
`

func (q *Queries) RedactWaMessagesBefore(ctx context.Context, cutoff time.Time) error {
	_, err := q.db.Exec(ctx, redactWaMessagesBefore, cutoff)
	return err
}
