package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDotEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	content := "PORT=9091\n# comment\n\nSECURE_COOKIE=false # inline\nMALFORMED\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("failed to write .env: %v", err)
	}

	t.Setenv("PORT", "")
	t.Setenv("SECURE_COOKIE", "")

	if err := loadDotEnv(path); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := os.Getenv("PORT"); got != "9091" {
		t.Errorf("PORT = %q", got)
	}
	if got := os.Getenv("SECURE_COOKIE"); got != "false" {
		t.Errorf("SECURE_COOKIE = %q", got)
	}
}

func TestLoadDotEnv_MissingFileReturnsError(t *testing.T) {
	err := loadDotEnv(filepath.Join(t.TempDir(), "missing.env"))
	if err == nil {
		t.Fatal("expected an error for a missing .env file")
	}
}

func TestReadSchemaFile_NotFound(t *testing.T) {
	// From the test's working directory none of the candidate paths exist,
	// so this should surface the "not found" error rather than panic.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}
	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("failed to chdir: %v", err)
	}
	defer func() { _ = os.Chdir(wd) }()

	if _, err := readSchemaFile(); err == nil {
		t.Error("expected an error when schema.sql is nowhere to be found")
	}
}

func TestReadSchemaFile_Found(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "schema.sql"), []byte("CREATE TABLE x();"), 0600); err != nil {
		t.Fatalf("failed to write schema.sql: %v", err)
	}
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("failed to chdir: %v", err)
	}
	defer func() { _ = os.Chdir(wd) }()

	got, err := readSchemaFile()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "CREATE TABLE x();" {
		t.Errorf("readSchemaFile() = %q", got)
	}
}
