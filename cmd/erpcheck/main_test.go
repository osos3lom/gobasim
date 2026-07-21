package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOkResult(t *testing.T) {
	if okResult(map[string]interface{}{"ok": true}) != true {
		t.Error("expected ok=true to report true")
	}
	if okResult(map[string]interface{}{"ok": false}) != false {
		t.Error("expected ok=false to report false")
	}
	if okResult(map[string]interface{}{}) != false {
		t.Error("expected missing 'ok' key to report false")
	}
	if okResult(map[string]interface{}{"ok": "not-a-bool"}) != false {
		t.Error("expected non-bool 'ok' value to report false")
	}
}

func TestStr(t *testing.T) {
	if got := str(nil); got != "—" {
		t.Errorf("str(nil) = %q", got)
	}
	if got := str(""); got != "—" {
		t.Errorf("str(\"\") = %q", got)
	}
	if got := str("value"); got != "value" {
		t.Errorf("str(value) = %q", got)
	}
	if got := str(42); got != "42" {
		t.Errorf("str(42) = %q", got)
	}
}

func TestCompact(t *testing.T) {
	got := compact(map[string]interface{}{"a": 1})
	if got != `{"a":1}` {
		t.Errorf("compact = %q", got)
	}
}

func TestLoadDotEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	content := "MSHALIA_API_URL=http://localhost:3000\nAGENT_GATEWAY_SECRET=shh # inline\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("failed to write .env: %v", err)
	}

	t.Setenv("MSHALIA_API_URL", "")
	t.Setenv("AGENT_GATEWAY_SECRET", "")

	loadDotEnv(path)

	if got := os.Getenv("MSHALIA_API_URL"); got != "http://localhost:3000" {
		t.Errorf("MSHALIA_API_URL = %q", got)
	}
	if got := os.Getenv("AGENT_GATEWAY_SECRET"); got != "shh" {
		t.Errorf("AGENT_GATEWAY_SECRET = %q", got)
	}
}

func TestLoadDotEnv_MissingFileIsNoop(t *testing.T) {
	loadDotEnv(filepath.Join(t.TempDir(), "missing.env"))
}
