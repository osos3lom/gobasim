package promptops

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// Module is one versioned prompt fragment in a compiled prompt stack.
type Module struct {
	ID        string
	Name      string
	Type      string
	VersionID string
	Version   int32
	Body      string
	Hash      string
	Order     int32
}

// CompileInput is the context required to assemble a runtime system prompt.
// OverridePrompt preserves the current Sawt precedence: contact prompt override
// wins over agent prompt stacks and legacy agent/system fallback text.
type CompileInput struct {
	OverridePrompt string
	LegacyPrompt   string
	Summary        string
	Modules        []Module
}

type CompiledPrompt struct {
	Content      string
	Hash         string
	ModuleHashes []string
	Source       string
}

func Compile(in CompileInput) CompiledPrompt {
	content := strings.TrimSpace(in.OverridePrompt)
	source := "override"
	var moduleHashes []string

	if content == "" && len(in.Modules) > 0 {
		parts := make([]string, 0, len(in.Modules))
		for _, m := range in.Modules {
			body := strings.TrimSpace(m.Body)
			if body == "" {
				continue
			}
			parts = append(parts, body)
			if m.Hash != "" {
				moduleHashes = append(moduleHashes, m.Hash)
			}
		}
		content = strings.Join(parts, "\n\n")
		source = "prompt_stack"
	}

	if content == "" {
		content = strings.TrimSpace(in.LegacyPrompt)
		source = "legacy"
	}

	if summary := strings.TrimSpace(in.Summary); summary != "" {
		content += "\n\nSummary of the conversation so far:\n" + summary
	}

	return CompiledPrompt{
		Content:      content,
		Hash:         Hash(content),
		ModuleHashes: moduleHashes,
		Source:       source,
	}
}

func Hash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(sum[:])
}
