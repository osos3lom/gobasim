package gateway

import (
	"testing"

	"sawt-go/web"
)

func TestWaDisplayPhone(t *testing.T) {
	tests := []struct {
		chatID   string
		expected string
	}{
		{"966501234567@s.whatsapp.net", "+966501234567"},
		{"12345@lid", "+12345"},
		{"invalid", "+invalid"},
	}

	for _, tt := range tests {
		got := waDisplayPhone(tt.chatID)
		if got != tt.expected {
			t.Errorf("waDisplayPhone(%q) = %q, want %q", tt.chatID, got, tt.expected)
		}
	}
}

func TestNewContactParams(t *testing.T) {
	bp := web.BlueprintDefaults{
		DefaultAgentID:        "agent_123",
		DefaultPromptOverride: "Custom system prompt",
		AutoEnable:            true,
	}

	params := NewContactParams("966501234567@s.whatsapp.net", "John Doe", bp)

	if params.ChatID != "966501234567@s.whatsapp.net" {
		t.Errorf("ChatID = %q, want 966501234567@s.whatsapp.net", params.ChatID)
	}
	if params.Name != "John Doe" {
		t.Errorf("Name = %q, want John Doe", params.Name)
	}
	if !params.Enabled {
		t.Error("Enabled = false, want true")
	}
	if params.AgentID == nil || *params.AgentID != "agent_123" {
		t.Errorf("AgentID = %v, want agent_123", params.AgentID)
	}
	if params.PromptOverride == nil || *params.PromptOverride != "Custom system prompt" {
		t.Errorf("PromptOverride = %v, want Custom system prompt", params.PromptOverride)
	}
}

func TestNewContactParams_EmptyPushNameUsesDisplayPhone(t *testing.T) {
	bp := web.BlueprintDefaults{}
	params := NewContactParams("966501234567@s.whatsapp.net", "", bp)

	if params.Name != "+966501234567" {
		t.Errorf("Name = %q, want +966501234567", params.Name)
	}
	if params.Enabled {
		t.Error("Enabled = true, want false when AutoEnable is false")
	}
}
