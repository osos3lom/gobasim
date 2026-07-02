package database

import (
	"context"
	"time"
)

// -- PurgeSttHistoryBefore --
const purgeSttHistoryBefore = `-- name: PurgeSttHistoryBefore :exec
DELETE FROM stt_history WHERE ts < $1
`

func (q *Queries) PurgeSttHistoryBefore(ctx context.Context, cutoff time.Time) error {
	_, err := q.db.Exec(ctx, purgeSttHistoryBefore, cutoff)
	return err
}

// -- PurgeTtsHistoryBefore --
const purgeTtsHistoryBefore = `-- name: PurgeTtsHistoryBefore :exec
DELETE FROM tts_history WHERE ts < $1
`

func (q *Queries) PurgeTtsHistoryBefore(ctx context.Context, cutoff time.Time) error {
	_, err := q.db.Exec(ctx, purgeTtsHistoryBefore, cutoff)
	return err
}

// -- PurgeConversationTurnsBefore --
const purgeConversationTurnsBefore = `-- name: PurgeConversationTurnsBefore :exec
DELETE FROM conversation_turns WHERE ts < $1
`

func (q *Queries) PurgeConversationTurnsBefore(ctx context.Context, cutoff time.Time) error {
	_, err := q.db.Exec(ctx, purgeConversationTurnsBefore, cutoff)
	return err
}

// -- RedactWaActivityBefore --
// Keeps the audit row (timings, status, tool ids) but strips the PII payload.
const redactWaActivityBefore = `-- name: RedactWaActivityBefore :exec
UPDATE wa_activity
SET transcript = '[redacted]', reply = '[redacted]'
WHERE ts < $1 AND transcript <> '[redacted]'
`

func (q *Queries) RedactWaActivityBefore(ctx context.Context, cutoff time.Time) error {
	_, err := q.db.Exec(ctx, redactWaActivityBefore, cutoff)
	return err
}
