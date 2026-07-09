package database

import (
	"context"
	"time"
)

// WaVoiceNote mirrors the wa_voice_notes table: the metadata + upload-state
// ledger for voice notes archived to Firebase Cloud Storage. The audio bytes
// themselves are never stored in Postgres.
type WaVoiceNote struct {
	ID              string     `json:"id"`
	ChatID          string     `json:"chat_id"`
	Direction       string     `json:"direction"`
	Sender          string     `json:"sender"`
	Receiver        string     `json:"receiver"`
	ObjectPath      string     `json:"object_path"`
	MimeType        string     `json:"mime_type"`
	SizeBytes       int64      `json:"size_bytes"`
	DurationSeconds int32      `json:"duration_seconds"`
	Status          string     `json:"status"`
	Attempts        int32      `json:"attempts"`
	NextAttemptAt   time.Time  `json:"next_attempt_at"`
	LastError       *string    `json:"last_error"`
	CreatedAt       time.Time  `json:"created_at"`
	UploadedAt      *time.Time `json:"uploaded_at"`
}

// -- UpsertWaVoiceNote --
type UpsertWaVoiceNoteParams struct {
	ID              string `json:"id"`
	ChatID          string `json:"chat_id"`
	Direction       string `json:"direction"`
	Sender          string `json:"sender"`
	Receiver        string `json:"receiver"`
	ObjectPath      string `json:"object_path"`
	MimeType        string `json:"mime_type"`
	SizeBytes       int64  `json:"size_bytes"`
	DurationSeconds int32  `json:"duration_seconds"`
}

const upsertWaVoiceNote = `-- name: UpsertWaVoiceNote :exec
INSERT INTO wa_voice_notes (id, chat_id, direction, sender, receiver, object_path, mime_type, size_bytes, duration_seconds, status)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'pending')
ON CONFLICT (id) DO NOTHING
`

func (q *Queries) UpsertWaVoiceNote(ctx context.Context, arg UpsertWaVoiceNoteParams) error {
	_, err := q.db.Exec(ctx, upsertWaVoiceNote,
		arg.ID, arg.ChatID, arg.Direction, arg.Sender, arg.Receiver,
		arg.ObjectPath, arg.MimeType, arg.SizeBytes, arg.DurationSeconds,
	)
	return err
}

// -- MarkWaVoiceNoteUploaded --
const markWaVoiceNoteUploaded = `-- name: MarkWaVoiceNoteUploaded :exec
UPDATE wa_voice_notes
SET status = 'uploaded', uploaded_at = NOW(), last_error = NULL
WHERE id = $1
`

func (q *Queries) MarkWaVoiceNoteUploaded(ctx context.Context, id string) error {
	_, err := q.db.Exec(ctx, markWaVoiceNoteUploaded, id)
	return err
}

// -- MarkWaVoiceNoteFailed --
type MarkWaVoiceNoteFailedParams struct {
	ID            string    `json:"id"`
	LastError     string    `json:"last_error"`
	NextAttemptAt time.Time `json:"next_attempt_at"`
	MaxAttempts   int32     `json:"max_attempts"`
}

const markWaVoiceNoteFailed = `-- name: MarkWaVoiceNoteFailed :exec
UPDATE wa_voice_notes
SET attempts = attempts + 1,
    last_error = $2,
    next_attempt_at = $3,
    status = CASE WHEN attempts + 1 >= $4 THEN 'failed' ELSE 'pending' END
WHERE id = $1
`

func (q *Queries) MarkWaVoiceNoteFailed(ctx context.Context, arg MarkWaVoiceNoteFailedParams) error {
	_, err := q.db.Exec(ctx, markWaVoiceNoteFailed,
		arg.ID, arg.LastError, arg.NextAttemptAt, arg.MaxAttempts,
	)
	return err
}

// -- ListPendingWaVoiceNotes --
const listPendingWaVoiceNotes = `-- name: ListPendingWaVoiceNotes :many
SELECT id, chat_id, direction, sender, receiver, object_path, mime_type, size_bytes, duration_seconds, status, attempts, next_attempt_at, last_error, created_at, uploaded_at
FROM wa_voice_notes
WHERE status = 'pending' AND next_attempt_at <= NOW()
ORDER BY next_attempt_at ASC
LIMIT $1
`

func (q *Queries) ListPendingWaVoiceNotes(ctx context.Context, limit int32) ([]WaVoiceNote, error) {
	rows, err := q.db.Query(ctx, listPendingWaVoiceNotes, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []WaVoiceNote
	for rows.Next() {
		var i WaVoiceNote
		if err := rows.Scan(
			&i.ID, &i.ChatID, &i.Direction, &i.Sender, &i.Receiver,
			&i.ObjectPath, &i.MimeType, &i.SizeBytes, &i.DurationSeconds,
			&i.Status, &i.Attempts, &i.NextAttemptAt, &i.LastError,
			&i.CreatedAt, &i.UploadedAt,
		); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	return items, rows.Err()
}

// -- PurgeWaVoiceNotesBefore --
const purgeWaVoiceNotesBefore = `-- name: PurgeWaVoiceNotesBefore :exec
DELETE FROM wa_voice_notes WHERE created_at < $1
`

func (q *Queries) PurgeWaVoiceNotesBefore(ctx context.Context, cutoff time.Time) error {
	_, err := q.db.Exec(ctx, purgeWaVoiceNotesBefore, cutoff)
	return err
}
