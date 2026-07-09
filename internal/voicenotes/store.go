// Package voicenotes archives WhatsApp voice notes (both directions) to
// Firebase Cloud Storage (a GCS bucket) without ever holding more than one
// audio file in flight:
//
//	Save():  validate -> write spool file -> insert 'pending' ledger row -> nudge worker
//	worker:  single goroutine; streams spool file -> GCS, marks the row, deletes the spool
//	sweep:   periodic re-scan of 'pending' rows (crash/outage recovery + backoff pacing)
//
// The Postgres row is the source of truth for retry state (attempts,
// next_attempt_at, last_error), so a process restart resumes exactly where it
// left off from the spool directory. Uploads are idempotent: the object name
// is derived deterministically from the message id, and re-uploading the same
// object simply overwrites identical bytes.
package voicenotes

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"crypto/sha256"
	"encoding/hex"

	"sawt-go/database"
)

const (
	// maxNoteBytes caps a single voice note (WhatsApp's own media limit is
	// 16 MB; a typical 1-minute Opus note is ~250 KB).
	maxNoteBytes = 16 << 20
	// minNoteBytes rejects obviously-truncated payloads.
	minNoteBytes = 64
	// maxAttempts is how many upload tries a note gets before its ledger row
	// flips to the terminal 'failed' state.
	maxAttempts = 5
	// batchSize bounds how many pending rows one worker pass claims.
	batchSize = 8
	// sweepInterval paces retry scans; first attempts run immediately via the
	// notify channel, so this only governs recovery/backoff latency.
	sweepInterval = time.Minute
	// uploadTimeout bounds a single object upload.
	uploadTimeout = 2 * time.Minute
	baseBackoff   = 30 * time.Second
	maxBackoff    = time.Hour
)

// Meta describes one voice note to archive.
type Meta struct {
	MessageID       string
	ChatID          string
	Direction       string // "in" | "out"
	Sender          string
	Receiver        string
	DurationSeconds int32
	Timestamp       time.Time
}

// ledger is the narrow slice of the database layer this package needs.
type ledger interface {
	UpsertWaVoiceNote(ctx context.Context, arg database.UpsertWaVoiceNoteParams) error
	MarkWaVoiceNoteUploaded(ctx context.Context, id string) error
	MarkWaVoiceNoteFailed(ctx context.Context, arg database.MarkWaVoiceNoteFailedParams) error
	ListPendingWaVoiceNotes(ctx context.Context, limit int32) ([]database.WaVoiceNote, error)
}

// uploader abstracts the object store so the worker is testable offline.
type uploader interface {
	Upload(ctx context.Context, objectPath, contentType string, metadata map[string]string, r io.Reader) error
	SignedURL(objectPath string, ttl time.Duration) (string, error)
}

// Store coordinates spooling, the ledger, and the upload worker. A nil *Store
// is valid and inert: every method no-ops, so call sites need no feature flag.
type Store struct {
	prefix   string
	spoolDir string
	db       ledger
	up       uploader
	notify   chan struct{}

	uploaded atomic.Int64
	failed   atomic.Int64
}

// NewStore prepares the spool directory and wires the ledger + uploader.
func NewStore(prefix, spoolDir string, db ledger, up uploader) (*Store, error) {
	if err := os.MkdirAll(spoolDir, 0o700); err != nil {
		return nil, fmt.Errorf("failed to create voice spool dir %s: %w", spoolDir, err)
	}
	return &Store{
		prefix:   strings.Trim(prefix, "/"),
		spoolDir: spoolDir,
		db:       db,
		up:       up,
		notify:   make(chan struct{}, 1),
	}, nil
}

// Save validates the OGG payload, spools it to disk, and records a pending
// ledger row. It never blocks on the network: the worker uploads later.
// Failures are logged and swallowed — archival must never break the reply path.
func (s *Store) Save(ctx context.Context, m Meta, data []byte) {
	if s == nil {
		return
	}
	if err := validateOgg(data); err != nil {
		log.Printf("[voicenotes] rejecting %s note %s: %v", m.Direction, m.MessageID, err)
		return
	}
	if m.Direction != "in" && m.Direction != "out" {
		log.Printf("[voicenotes] rejecting note %s: invalid direction %q", m.MessageID, m.Direction)
		return
	}
	if m.Timestamp.IsZero() {
		m.Timestamp = time.Now()
	}

	objectPath := ObjectPath(s.prefix, m)
	if err := os.WriteFile(s.spoolPath(objectPath), data, 0o600); err != nil {
		log.Printf("[voicenotes] failed to spool note %s: %v", m.MessageID, err)
		return
	}

	rowID := m.MessageID + "-" + m.Direction
	err := s.db.UpsertWaVoiceNote(ctx, database.UpsertWaVoiceNoteParams{
		ID:              rowID,
		ChatID:          m.ChatID,
		Direction:       m.Direction,
		Sender:          m.Sender,
		Receiver:        m.Receiver,
		ObjectPath:      objectPath,
		MimeType:        "audio/ogg",
		SizeBytes:       int64(len(data)),
		DurationSeconds: m.DurationSeconds,
	})
	if err != nil {
		log.Printf("[voicenotes] failed to record ledger row for %s: %v", rowID, err)
		return
	}

	// Non-blocking nudge: if the worker is busy, the sweep catches the row.
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

// StartWorker launches the single upload goroutine. One worker is deliberate:
// on a 1 GB host, serialized uploads bound both memory (one streamed file at
// a time) and outbound bandwidth contention with the WhatsApp socket.
func (s *Store) StartWorker(ctx context.Context) {
	if s == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(sweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-s.notify:
			case <-ticker.C:
			}
			s.processPending(ctx)
		}
	}()
}

// SignedURL returns a time-limited GET URL for an archived note.
func (s *Store) SignedURL(objectPath string, ttl time.Duration) (string, error) {
	if s == nil {
		return "", fmt.Errorf("voice-note storage is not configured")
	}
	return s.up.SignedURL(objectPath, ttl)
}

// Stats reports lifetime worker counters (for logs/health surfaces).
func (s *Store) Stats() (uploaded, failed int64) {
	if s == nil {
		return 0, 0
	}
	return s.uploaded.Load(), s.failed.Load()
}

func (s *Store) processPending(ctx context.Context) {
	rows, err := s.db.ListPendingWaVoiceNotes(ctx, batchSize)
	if err != nil {
		log.Printf("[voicenotes] failed to list pending uploads: %v", err)
		return
	}
	for _, row := range rows {
		if ctx.Err() != nil {
			return
		}
		s.processOne(ctx, row)
	}
}

func (s *Store) processOne(ctx context.Context, row database.WaVoiceNote) {
	spool := s.spoolPath(row.ObjectPath)
	f, err := os.Open(spool)
	if err != nil {
		// Spool file gone (disk wipe, manual cleanup): unrecoverable, park it
		// terminally rather than retrying forever. MaxAttempts=0 forces the
		// CASE in MarkWaVoiceNoteFailed to the terminal 'failed' state.
		s.markFailed(ctx, row, "spool file missing: "+err.Error(), 0)
		return
	}

	upCtx, cancel := context.WithTimeout(ctx, uploadTimeout)
	err = s.up.Upload(upCtx, row.ObjectPath, row.MimeType, map[string]string{
		"chat-id":    row.ChatID,
		"message-id": row.ID,
		"direction":  row.Direction,
		"sender":     row.Sender,
		"receiver":   row.Receiver,
		"created-at": row.CreatedAt.UTC().Format(time.RFC3339),
	}, f)
	cancel()
	// Close before any Remove: Windows refuses to delete a file with an open
	// handle, and we no longer need the reader either way.
	f.Close()

	if err != nil {
		s.markFailed(ctx, row, err.Error(), maxAttempts)
		return
	}

	if err := s.db.MarkWaVoiceNoteUploaded(ctx, row.ID); err != nil {
		// The object exists but the ledger update failed; leave the spool in
		// place — the sweep re-uploads idempotently and re-marks.
		log.Printf("[voicenotes] uploaded %s but failed to mark ledger: %v", row.ID, err)
		return
	}
	if err := os.Remove(spool); err != nil && !os.IsNotExist(err) {
		log.Printf("[voicenotes] uploaded %s but failed to remove spool file: %v", row.ID, err)
	}
	s.uploaded.Add(1)
	log.Printf("[voicenotes] archived %s (%d bytes) to %s", row.ID, row.SizeBytes, row.ObjectPath)
}

func (s *Store) markFailed(ctx context.Context, row database.WaVoiceNote, reason string, maxTries int32) {
	s.failed.Add(1)
	if len(reason) > 500 {
		reason = reason[:500]
	}
	err := s.db.MarkWaVoiceNoteFailed(ctx, database.MarkWaVoiceNoteFailedParams{
		ID:            row.ID,
		LastError:     reason,
		NextAttemptAt: time.Now().Add(backoff(row.Attempts + 1)),
		MaxAttempts:   maxTries,
	})
	if err != nil {
		log.Printf("[voicenotes] failed to record upload failure for %s: %v", row.ID, err)
	}
	log.Printf("[voicenotes] upload attempt %d for %s failed: %s", row.Attempts+1, row.ID, reason)
}

func (s *Store) spoolPath(objectPath string) string {
	return filepath.Join(s.spoolDir, path.Base(objectPath))
}

// backoff returns the exponential retry delay for the given attempt number
// (1-based): 30s, 1m, 2m, 4m, ... capped at an hour.
func backoff(attempt int32) time.Duration {
	d := baseBackoff
	for i := int32(1); i < attempt; i++ {
		d *= 2
		if d >= maxBackoff {
			return maxBackoff
		}
	}
	return d
}

// validateOgg enforces the size window and the OGG container magic; both
// WhatsApp voice notes and our TTS output are OGG/Opus, so anything else is
// a wiring bug or a hostile payload.
func validateOgg(data []byte) error {
	if len(data) < minNoteBytes {
		return fmt.Errorf("payload too small (%d bytes)", len(data))
	}
	if len(data) > maxNoteBytes {
		return fmt.Errorf("payload too large (%d bytes, max %d)", len(data), maxNoteBytes)
	}
	if !bytes.HasPrefix(data, []byte("OggS")) {
		return fmt.Errorf("payload is not an OGG container")
	}
	return nil
}

// ObjectPath builds the deterministic, collision-resistant object name:
//
//	{prefix}/{chat}/{YYYY}/{MM}/{DD}/{messageID}-{direction}-{digest8}.ogg
//
// The digest covers the raw (pre-sanitization) id, so two ids that sanitize
// to the same token still map to distinct objects. Determinism is what makes
// retried uploads idempotent.
func ObjectPath(prefix string, m Meta) string {
	t := m.Timestamp
	if t.IsZero() {
		t = time.Now()
	}
	chat := sanitizeToken(strings.SplitN(m.ChatID, "@", 2)[0])
	sum := sha256.Sum256([]byte(m.MessageID + "|" + m.Direction + "|" + m.ChatID))
	base := fmt.Sprintf("%s-%s-%s.ogg", sanitizeToken(m.MessageID), m.Direction, hex.EncodeToString(sum[:4]))
	return path.Join(prefix, chat, t.UTC().Format("2006/01/02"), base)
}

// sanitizeToken keeps [A-Za-z0-9_-] and caps length so ids can't smuggle
// path separators or unbounded names into object paths or spool filenames.
func sanitizeToken(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
		if b.Len() >= 64 {
			break
		}
	}
	if b.Len() == 0 {
		return "unknown"
	}
	return b.String()
}
