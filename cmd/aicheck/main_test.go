package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseOnly(t *testing.T) {
	all := parseOnly("")
	if !all("anything") {
		t.Error("expected empty filter to allow everything")
	}

	subset := parseOnly("hf, google-adc")
	if !subset("hf") {
		t.Error("expected 'hf' to be allowed")
	}
	if !subset("google-adc") {
		t.Error("expected 'google-adc' to be allowed (whitespace trimmed)")
	}
	if subset("gcs") {
		t.Error("expected 'gcs' to be excluded")
	}
}

func TestPresence(t *testing.T) {
	if got := presence(""); got != "NOT SET" {
		t.Errorf("presence(\"\") = %q", got)
	}
	if got := presence("secret"); got != "set" {
		t.Errorf("presence(non-empty) = %q", got)
	}
}

func TestPrintResult(t *testing.T) {
	// Just verify it doesn't panic across the pass/fail/skip branches.
	printResult(checkResult{Provider: "p", Check: "c", Pass: true, Elapsed: time.Millisecond})
	printResult(checkResult{Provider: "p", Check: "c", Pass: false, Elapsed: time.Millisecond})
	printResult(checkResult{Provider: "p", Check: "c", Skip: true, Elapsed: time.Millisecond})
}

func TestTimeIt(t *testing.T) {
	elapsed, err := timeIt(func() error { return nil })
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if elapsed < 0 {
		t.Errorf("expected non-negative elapsed, got %v", elapsed)
	}

	wantErr := errors.New("boom")
	_, err = timeIt(func() error { return wantErr })
	if !errors.Is(err, wantErr) {
		t.Errorf("expected error to propagate, got %v", err)
	}
}

func TestErrString(t *testing.T) {
	if got := errString(nil); got != "<nil>" {
		t.Errorf("errString(nil) = %q", got)
	}
	if got := errString(errors.New("oops")); got != "oops" {
		t.Errorf("errString(err) = %q", got)
	}
}

func TestTruncate(t *testing.T) {
	short := "hello"
	if got := truncate(short); got != short {
		t.Errorf("truncate(short) = %q", got)
	}
	long := strings.Repeat("a", 250)
	got := truncate(long)
	if len(got) != 203 { // 200 bytes + the 3-byte "…" rune
		t.Errorf("expected truncated length 203, got %d", len(got))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("expected truncated string to end with an ellipsis, got %q", got)
	}
}

func TestLoadDotEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	content := "FOO=bar\n# a comment\n\nBAZ=qux # inline comment\nMALFORMED_LINE\nEMPTY=\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("failed to write test .env: %v", err)
	}

	t.Setenv("FOO", "")
	t.Setenv("BAZ", "")
	t.Setenv("EMPTY", "")

	loadDotEnv(path)

	if got := os.Getenv("FOO"); got != "bar" {
		t.Errorf("FOO = %q, want bar", got)
	}
	if got := os.Getenv("BAZ"); got != "qux" {
		t.Errorf("BAZ = %q, want qux (inline comment stripped)", got)
	}
}

func TestLoadDotEnv_MissingFileIsNoop(t *testing.T) {
	// Should not panic when the file doesn't exist.
	loadDotEnv(filepath.Join(t.TempDir(), "does-not-exist.env"))
}
