package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNz(t *testing.T) {
	if got := nz(""); got != "(none / general chat)" {
		t.Errorf("nz(\"\") = %q", got)
	}
	if got := nz("chat about horses"); got != "chat about horses" {
		t.Errorf("nz(non-empty) = %q", got)
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
