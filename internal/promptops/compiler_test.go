package promptops

import (
	"strings"
	"testing"
)

func TestCompile_OverrideWins(t *testing.T) {
	out := Compile(CompileInput{
		OverridePrompt: "  custom override  ",
		LegacyPrompt:   "legacy text",
		Modules:        []Module{{Body: "module body"}},
	})
	if out.Source != "override" {
		t.Errorf("expected source 'override', got %q", out.Source)
	}
	if out.Content != "custom override" {
		t.Errorf("expected trimmed override content, got %q", out.Content)
	}
	if out.Hash != Hash("custom override") {
		t.Errorf("hash mismatch")
	}
	if len(out.ModuleHashes) != 0 {
		t.Errorf("expected no module hashes when override wins, got %v", out.ModuleHashes)
	}
}

func TestCompile_ModuleStackWhenNoOverride(t *testing.T) {
	out := Compile(CompileInput{
		LegacyPrompt: "legacy text",
		Modules: []Module{
			{Body: "  first module  ", Hash: "h1"},
			{Body: "", Hash: "h-empty"},
			{Body: "second module", Hash: "h2"},
		},
	})
	if out.Source != "prompt_stack" {
		t.Errorf("expected source 'prompt_stack', got %q", out.Source)
	}
	wantContent := "first module\n\nsecond module"
	if out.Content != wantContent {
		t.Errorf("Content = %q, want %q", out.Content, wantContent)
	}
	if len(out.ModuleHashes) != 2 || out.ModuleHashes[0] != "h1" || out.ModuleHashes[1] != "h2" {
		t.Errorf("expected module hashes [h1 h2], got %v", out.ModuleHashes)
	}
}

func TestCompile_LegacyFallback(t *testing.T) {
	out := Compile(CompileInput{
		LegacyPrompt: "  legacy fallback  ",
	})
	if out.Source != "legacy" {
		t.Errorf("expected source 'legacy', got %q", out.Source)
	}
	if out.Content != "legacy fallback" {
		t.Errorf("Content = %q", out.Content)
	}
}

func TestCompile_ModulesAllBlankFallsThroughToLegacy(t *testing.T) {
	out := Compile(CompileInput{
		LegacyPrompt: "legacy text",
		Modules:      []Module{{Body: "   "}, {Body: ""}},
	})
	if out.Source != "legacy" {
		t.Errorf("expected fallback to legacy when all modules blank, got %q", out.Source)
	}
	if out.Content != "legacy text" {
		t.Errorf("Content = %q", out.Content)
	}
}

func TestCompile_AppendsSummary(t *testing.T) {
	out := Compile(CompileInput{
		OverridePrompt: "base prompt",
		Summary:        "  conversation summary  ",
	})
	if !strings.Contains(out.Content, "Summary of the conversation so far:\nconversation summary") {
		t.Errorf("expected summary appended, got %q", out.Content)
	}
	if !strings.HasPrefix(out.Content, "base prompt") {
		t.Errorf("expected base prompt to precede summary, got %q", out.Content)
	}
}

func TestCompile_EmptySummaryNotAppended(t *testing.T) {
	out := Compile(CompileInput{
		OverridePrompt: "base prompt",
		Summary:        "   ",
	})
	if out.Content != "base prompt" {
		t.Errorf("expected no summary appended, got %q", out.Content)
	}
}

func TestCompile_AllEmpty(t *testing.T) {
	out := Compile(CompileInput{})
	if out.Content != "" {
		t.Errorf("expected empty content, got %q", out.Content)
	}
	if out.Source != "legacy" {
		t.Errorf("expected source 'legacy' as final fallback, got %q", out.Source)
	}
	if out.Hash != Hash("") {
		t.Errorf("expected hash of empty string")
	}
}

func TestHash_Deterministic(t *testing.T) {
	h1 := Hash("some content")
	h2 := Hash("some content")
	if h1 != h2 {
		t.Errorf("expected deterministic hash, got %q vs %q", h1, h2)
	}
	if !strings.HasPrefix(h1, "sha256:") {
		t.Errorf("expected sha256: prefix, got %q", h1)
	}
	if Hash("a") == Hash("b") {
		t.Error("expected different content to hash differently")
	}
}
