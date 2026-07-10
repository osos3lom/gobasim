package database

import (
	"context"
	"time"
)

// This file is hand-written to match query.sql (sqlc is not installed in the dev
// environment; re-running `sqlc generate` later is compatible). It backs the
// agentic-gateway audit fixes: inbound dedup (C1) and the durable tool-step log (C2).

// -- MarkMessageProcessed --
// Records an inbound WhatsApp message id, returning it only when newly inserted.
// A duplicate delivery hits ON CONFLICT DO NOTHING and the RETURNING yields
// pgx.ErrNoRows, which the caller treats as "already processed — skip" (C1).
const markMessageProcessed = `-- name: MarkMessageProcessed :one
INSERT INTO processed_messages (id) VALUES ($1)
ON CONFLICT (id) DO NOTHING
RETURNING id
`

func (q *Queries) MarkMessageProcessed(ctx context.Context, id string) (string, error) {
	row := q.db.QueryRow(ctx, markMessageProcessed, id)
	var out string
	err := row.Scan(&out)
	return out, err
}

// -- PurgeProcessedMessagesBefore --
const purgeProcessedMessagesBefore = `-- name: PurgeProcessedMessagesBefore :exec
DELETE FROM processed_messages WHERE processed_at < $1
`

func (q *Queries) PurgeProcessedMessagesBefore(ctx context.Context, before time.Time) error {
	_, err := q.db.Exec(ctx, purgeProcessedMessagesBefore, before)
	return err
}

// -- CreateToolExecution --
type CreateToolExecutionParams struct {
	ChatID string `json:"chat_id"`
	ToolID string `json:"tool_id"`
	Args   []byte `json:"args"`
	Result []byte `json:"result"`
	Status string `json:"status"`
}

const createToolExecution = `-- name: CreateToolExecution :exec
INSERT INTO tool_executions (chat_id, tool_id, args, result, status)
VALUES ($1, $2, $3, $4, $5)
`

func (q *Queries) CreateToolExecution(ctx context.Context, arg CreateToolExecutionParams) error {
	_, err := q.db.Exec(ctx, createToolExecution, arg.ChatID, arg.ToolID, arg.Args, arg.Result, arg.Status)
	return err
}

// -- PurgeToolExecutionsBefore --
const purgeToolExecutionsBefore = `-- name: PurgeToolExecutionsBefore :exec
DELETE FROM tool_executions WHERE ts < $1
`

func (q *Queries) PurgeToolExecutionsBefore(ctx context.Context, before time.Time) error {
	_, err := q.db.Exec(ctx, purgeToolExecutionsBefore, before)
	return err
}
