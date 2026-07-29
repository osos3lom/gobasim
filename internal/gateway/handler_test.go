package gateway

import (
	"testing"

	"sawt-go/internal/contacts"
)

func TestNewContactParams(t *testing.T) {
	bp := contacts.BlueprintDefaults{
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

// D1 regression: a first-contact auto-create must never start enabled unless
// the operator's blueprint explicitly says so.
func TestNewContactParams_DefaultsDisabled(t *testing.T) {
	params := NewContactParams("966501234567@s.whatsapp.net", "Layla", contacts.BlueprintDefaults{})

	if params.Enabled {
		t.Error("auto-created contacts must default to disabled (explicit operator opt-in)")
	}
	if params.AgentID != nil || params.PromptOverride != nil {
		t.Error("new contacts must start with no agent assignment or prompt override")
	}
}

func TestNewContactParams_EmptyPushNameUsesDisplayPhone(t *testing.T) {
	params := NewContactParams("966501234567@s.whatsapp.net", "", contacts.BlueprintDefaults{})

	// Saudi numbers are shown to operators in local format, not raw
	// international digits — see contacts.FormatPhoneDisplay.
	if params.Name != "0501234567" {
		t.Errorf("Name = %q, want 0501234567", params.Name)
	}
}

// A "@lid" chat id's digits are an opaque WhatsApp identifier, not a phone
// number. Naming a contact after them puts that identifier straight into the
// dashboard (the name is rendered unconditionally), so an unnamed LID contact
// must fall back to the generic placeholder instead.
func TestNewContactParams_LIDNeverBecomesTheName(t *testing.T) {
	params := NewContactParams("90727124070644@lid", "", contacts.BlueprintDefaults{})

	if params.Name != contacts.GenericContactName {
		t.Errorf("Name = %q, want %q", params.Name, contacts.GenericContactName)
	}
	if params.Name == "+90727124070644" || params.Name == "90727124070644" {
		t.Error("LID digits leaked into the contact name")
	}
}
