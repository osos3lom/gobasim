package database

import (
	"context"
	"time"
)

// -- UpsertPendingConfirmation --
type UpsertPendingConfirmationParams struct {
	ChatID        string    `json:"chat_id"`
	ToolID        string    `json:"tool_id"`
	Args          []byte    `json:"args"`
	OrgID         string    `json:"org_id"`
	ActingUserUid string    `json:"acting_user_uid"`
	Description   string    `json:"description"`
	ExpiresAt     time.Time `json:"expires_at"`
}

const upsertPendingConfirmation = `-- name: UpsertPendingConfirmation :exec
INSERT INTO pending_confirmations (chat_id, tool_id, args, org_id, acting_user_uid, description, created_at, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, NOW(), $7)
ON CONFLICT (chat_id) DO UPDATE
SET tool_id = EXCLUDED.tool_id, args = EXCLUDED.args, org_id = EXCLUDED.org_id,
    acting_user_uid = EXCLUDED.acting_user_uid, description = EXCLUDED.description,
    created_at = NOW(), expires_at = EXCLUDED.expires_at
`

func (q *Queries) UpsertPendingConfirmation(ctx context.Context, arg UpsertPendingConfirmationParams) error {
	_, err := q.db.Exec(ctx, upsertPendingConfirmation,
		arg.ChatID, arg.ToolID, arg.Args, arg.OrgID,
		arg.ActingUserUid, arg.Description, arg.ExpiresAt,
	)
	return err
}

// -- GetPendingConfirmation --
const getPendingConfirmation = `-- name: GetPendingConfirmation :one
SELECT chat_id, tool_id, args, org_id, acting_user_uid, description, created_at, expires_at
FROM pending_confirmations
WHERE chat_id = $1
`

func (q *Queries) GetPendingConfirmation(ctx context.Context, chatID string) (PendingConfirmation, error) {
	row := q.db.QueryRow(ctx, getPendingConfirmation, chatID)
	var i PendingConfirmation
	err := row.Scan(
		&i.ChatID,
		&i.ToolID,
		&i.Args,
		&i.OrgID,
		&i.ActingUserUid,
		&i.Description,
		&i.CreatedAt,
		&i.ExpiresAt,
	)
	return i, err
}

// -- DeletePendingConfirmation --
const deletePendingConfirmation = `-- name: DeletePendingConfirmation :exec
DELETE FROM pending_confirmations WHERE chat_id = $1
`

func (q *Queries) DeletePendingConfirmation(ctx context.Context, chatID string) error {
	_, err := q.db.Exec(ctx, deletePendingConfirmation, chatID)
	return err
}
