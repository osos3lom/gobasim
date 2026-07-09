package voicenotes

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"sawt-go/database"
)

// fakeLedger is an in-memory wa_voice_notes table.
type fakeLedger struct {
	mu   sync.Mutex
	rows map[string]database.WaVoiceNote
}

func newFakeLedger() *fakeLedger {
	return &fakeLedger{rows: map[string]database.WaVoiceNote{}}
}

func (f *fakeLedger) UpsertWaVoiceNote(ctx context.Context, arg database.UpsertWaVoiceNoteParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, exists := f.rows[arg.ID]; exists {
		return nil // ON CONFLICT DO NOTHING
	}
	f.rows[arg.ID] = database.WaVoiceNote{
		ID: arg.ID, ChatID: arg.ChatID, Direction: arg.Direction,
		Sender: arg.Sender, Receiver: arg.Receiver, ObjectPath: arg.ObjectPath,
		MimeType: arg.MimeType, SizeBytes: arg.SizeBytes,
		DurationSeconds: arg.DurationSeconds, Status: "pending",
		NextAttemptAt: time.Now().Add(-time.Second), CreatedAt: time.Now(),
	}
	return nil
}

func (f *fakeLedger) MarkWaVoiceNoteUploaded(ctx context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	row := f.rows[id]
	row.Status = "uploaded"
	f.rows[id] = row
	return nil
}

func (f *fakeLedger) MarkWaVoiceNoteFailed(ctx context.Context, arg database.MarkWaVoiceNoteFailedParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	row := f.rows[arg.ID]
	row.Attempts++
	row.LastError = &arg.LastError
	row.NextAttemptAt = arg.NextAttemptAt
	if row.Attempts >= arg.MaxAttempts {
		row.Status = "failed"
	} else {
		row.Status = "pending"
	}
	f.rows[arg.ID] = row
	return nil
}

func (f *fakeLedger) ListPendingWaVoiceNotes(ctx context.Context, limit int32) ([]database.WaVoiceNote, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []database.WaVoiceNote
	for _, r := range f.rows {
		if r.Status == "pending" && !r.NextAttemptAt.After(time.Now()) {
			out = append(out, r)
		}
		if len(out) >= int(limit) {
			break
		}
	}
	return out, nil
}

// fakeUploader records uploads and can be told to fail.
type fakeUploader struct {
	mu      sync.Mutex
	objects map[string][]byte
	failN   int // fail the first N uploads
}

func newFakeUploader() *fakeUploader {
	return &fakeUploader{objects: map[string][]byte{}}
}

func (f *fakeUploader) Upload(ctx context.Context, objectPath, contentType string, metadata map[string]string, r io.Reader) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failN > 0 {
		f.failN--
		return fmt.Errorf("simulated upload failure")
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	f.objects[objectPath] = data
	return nil
}

func (f *fakeUploader) SignedURL(objectPath string, ttl time.Duration) (string, error) {
	return "https://signed.example/" + objectPath, nil
}

func oggPayload() []byte {
	return append([]byte("OggS"), make([]byte, 200)...)
}

func testStore(t *testing.T, up uploader) (*Store, *fakeLedger) {
	t.Helper()
	db := newFakeLedger()
	s, err := NewStore("voice-notes", t.TempDir(), db, up)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s, db
}

func TestObjectPathDeterministicAndSanitized(t *testing.T) {
	m := Meta{
		MessageID: "3EB0/../evil id", ChatID: "966500000001@s.whatsapp.net",
		Direction: "in", Timestamp: time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC),
	}
	p1 := ObjectPath("voice-notes", m)
	p2 := ObjectPath("voice-notes", m)
	if p1 != p2 {
		t.Fatalf("object path must be deterministic: %q vs %q", p1, p2)
	}
	if strings.Contains(p1, "..") || strings.Contains(p1, " ") {
		t.Fatalf("object path not sanitized: %q", p1)
	}
	if !strings.HasPrefix(p1, "voice-notes/966500000001/2026/07/04/") {
		t.Fatalf("unexpected layout: %q", p1)
	}
	// Same sanitized token, different raw id -> different object (digest).
	m2 := m
	m2.MessageID = "3EB0/./evil id" // sanitizes to the same token
	if ObjectPath("voice-notes", m2) == p1 {
		t.Fatal("distinct raw ids must map to distinct objects")
	}
}

func TestValidateOgg(t *testing.T) {
	tests := []struct {
		name    string
		data    []byte
		wantErr bool
	}{
		{"valid ogg", oggPayload(), false},
		{"too small", []byte("OggS"), true},
		{"wrong magic", append([]byte("RIFF"), make([]byte, 200)...), true},
		{"too large", append([]byte("OggS"), make([]byte, maxNoteBytes)...), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateOgg(tt.data); (err != nil) != tt.wantErr {
				t.Errorf("validateOgg() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestSaveThenWorkerUploadsAndCleansSpool(t *testing.T) {
	up := newFakeUploader()
	s, db := testStore(t, up)

	m := Meta{MessageID: "MSG1", ChatID: "9665@s.whatsapp.net", Direction: "in", Sender: "9665", Receiver: "bot"}
	s.Save(context.Background(), m, oggPayload())

	// Row recorded as pending, spool file present.
	row, ok := db.rows["MSG1-in"]
	if !ok || row.Status != "pending" {
		t.Fatalf("expected pending ledger row, got %+v", row)
	}
	spool := filepath.Join(s.spoolDir, filepath.Base(row.ObjectPath))
	if _, err := os.Stat(spool); err != nil {
		t.Fatalf("expected spool file: %v", err)
	}

	s.processPending(context.Background())

	if db.rows["MSG1-in"].Status != "uploaded" {
		t.Fatalf("expected uploaded, got %q", db.rows["MSG1-in"].Status)
	}
	if _, ok := up.objects[row.ObjectPath]; !ok {
		t.Fatal("expected object in fake bucket")
	}
	if _, err := os.Stat(spool); !os.IsNotExist(err) {
		t.Fatal("expected spool file to be cleaned up after upload")
	}
}

func TestSaveIsIdempotent(t *testing.T) {
	up := newFakeUploader()
	s, db := testStore(t, up)
	m := Meta{MessageID: "MSG1", ChatID: "9665@s.whatsapp.net", Direction: "in"}

	s.Save(context.Background(), m, oggPayload())
	s.Save(context.Background(), m, oggPayload()) // duplicate delivery

	if len(db.rows) != 1 {
		t.Fatalf("expected exactly 1 ledger row, got %d", len(db.rows))
	}
}

func TestUploadFailureRetriesWithBackoffThenSucceeds(t *testing.T) {
	up := newFakeUploader()
	up.failN = 1
	s, db := testStore(t, up)
	m := Meta{MessageID: "MSG2", ChatID: "9665@s.whatsapp.net", Direction: "out"}
	s.Save(context.Background(), m, oggPayload())

	s.processPending(context.Background())
	row := db.rows["MSG2-out"]
	if row.Status != "pending" || row.Attempts != 1 || row.LastError == nil {
		t.Fatalf("expected 1 failed attempt still pending, got %+v", row)
	}
	if !row.NextAttemptAt.After(time.Now()) {
		t.Fatal("expected a future next_attempt_at (backoff)")
	}

	// Simulate the backoff elapsing, then the sweep retries and succeeds.
	row.NextAttemptAt = time.Now().Add(-time.Second)
	db.mu.Lock()
	db.rows["MSG2-out"] = row
	db.mu.Unlock()

	s.processPending(context.Background())
	if db.rows["MSG2-out"].Status != "uploaded" {
		t.Fatalf("expected uploaded after retry, got %q", db.rows["MSG2-out"].Status)
	}
}

func TestMissingSpoolFileParksRowTerminally(t *testing.T) {
	up := newFakeUploader()
	s, db := testStore(t, up)
	m := Meta{MessageID: "MSG3", ChatID: "9665@s.whatsapp.net", Direction: "in"}
	s.Save(context.Background(), m, oggPayload())

	// Lose the spool file before the worker runs.
	row := db.rows["MSG3-in"]
	os.Remove(filepath.Join(s.spoolDir, filepath.Base(row.ObjectPath)))

	s.processPending(context.Background())
	if db.rows["MSG3-in"].Status != "failed" {
		t.Fatalf("expected terminal 'failed' when spool is lost, got %q", db.rows["MSG3-in"].Status)
	}
}

func TestSaveRejectsInvalidPayloads(t *testing.T) {
	up := newFakeUploader()
	s, db := testStore(t, up)

	s.Save(context.Background(), Meta{MessageID: "M", ChatID: "c@x", Direction: "in"}, []byte("not audio at all, but long enough to pass the size check........."))
	s.Save(context.Background(), Meta{MessageID: "M2", ChatID: "c@x", Direction: "sideways"}, oggPayload())

	if len(db.rows) != 0 {
		t.Fatalf("invalid payloads must not create ledger rows, got %d", len(db.rows))
	}
}

func TestNilStoreIsInert(t *testing.T) {
	var s *Store
	s.Save(context.Background(), Meta{MessageID: "x", Direction: "in"}, oggPayload())
	s.StartWorker(context.Background())
	if up, failed := s.Stats(); up != 0 || failed != 0 {
		t.Fatal("nil store must report zero stats")
	}
	if _, err := s.SignedURL("x", time.Minute); err == nil {
		t.Fatal("nil store must error on SignedURL")
	}
}

func TestBackoffIsExponentialAndCapped(t *testing.T) {
	if backoff(1) != baseBackoff {
		t.Errorf("attempt 1 = %v, want %v", backoff(1), baseBackoff)
	}
	if backoff(2) != 2*baseBackoff {
		t.Errorf("attempt 2 = %v, want %v", backoff(2), 2*baseBackoff)
	}
	if backoff(30) != maxBackoff {
		t.Errorf("attempt 30 = %v, want cap %v", backoff(30), maxBackoff)
	}
}
