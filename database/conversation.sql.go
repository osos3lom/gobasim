package database

import (
	"context"
)

// -- CreateConversationTurn --
type CreateConversationTurnParams struct {
	ChatID  string `json:"chat_id"`
	Role    string `json:"role"`
	Content string `json:"content"`
}

const createConversationTurn = `-- name: CreateConversationTurn :one
INSERT INTO conversation_turns (chat_id, role, content)
VALUES ($1, $2, $3)
RETURNING id, chat_id, role, content, ts
`

func (q *Queries) CreateConversationTurn(ctx context.Context, arg CreateConversationTurnParams) (ConversationTurn, error) {
	row := q.db.QueryRow(ctx, createConversationTurn, arg.ChatID, arg.Role, arg.Content)
	var i ConversationTurn
	err := row.Scan(
		&i.ID,
		&i.ChatID,
		&i.Role,
		&i.Content,
		&i.Ts,
	)
	return i, err
}

// -- ListConversationTurnsAfter --
type ListConversationTurnsAfterParams struct {
	ChatID  string `json:"chat_id"`
	AfterID int64  `json:"after_id"`
	Limit   int32  `json:"limit"`
}

const listConversationTurnsAfter = `-- name: ListConversationTurnsAfter :many
SELECT id, chat_id, role, content, ts FROM conversation_turns
WHERE chat_id = $1 AND id > $2
ORDER BY id ASC
LIMIT $3
`

func (q *Queries) ListConversationTurnsAfter(ctx context.Context, arg ListConversationTurnsAfterParams) ([]ConversationTurn, error) {
	rows, err := q.db.Query(ctx, listConversationTurnsAfter, arg.ChatID, arg.AfterID, arg.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []ConversationTurn
	for rows.Next() {
		var i ConversationTurn
		if err := rows.Scan(
			&i.ID,
			&i.ChatID,
			&i.Role,
			&i.Content,
			&i.Ts,
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

// -- GetConversationState --
const getConversationState = `-- name: GetConversationState :one
SELECT chat_id, summary, summarized_through, updated_at FROM conversation_state
WHERE chat_id = $1
`

func (q *Queries) GetConversationState(ctx context.Context, chatID string) (ConversationState, error) {
	row := q.db.QueryRow(ctx, getConversationState, chatID)
	var i ConversationState
	err := row.Scan(
		&i.ChatID,
		&i.Summary,
		&i.SummarizedThrough,
		&i.UpdatedAt,
	)
	return i, err
}

// -- UpsertConversationState --
type UpsertConversationStateParams struct {
	ChatID            string `json:"chat_id"`
	Summary           string `json:"summary"`
	SummarizedThrough int64  `json:"summarized_through"`
}

const upsertConversationState = `-- name: UpsertConversationState :exec
INSERT INTO conversation_state (chat_id, summary, summarized_through, updated_at)
VALUES ($1, $2, $3, NOW())
ON CONFLICT (chat_id) DO UPDATE
SET summary = EXCLUDED.summary, summarized_through = EXCLUDED.summarized_through, updated_at = NOW()
`

func (q *Queries) UpsertConversationState(ctx context.Context, arg UpsertConversationStateParams) error {
	_, err := q.db.Exec(ctx, upsertConversationState, arg.ChatID, arg.Summary, arg.SummarizedThrough)
	return err
}
