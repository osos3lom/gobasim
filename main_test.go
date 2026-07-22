package main

import (
	"testing"

	"sawt-go/internal/gateway"
	"sawt-go/web"
)

// D1 regression: a first-contact auto-create must never start enabled. The
// agent talking to a stranger without operator opt-in is the failure mode
// this guards against.
func TestNewContactParamsDefaultsDisabled(t *testing.T) {
	tests := []struct {
		name     string
		chatJID  string
		pushName string
		wantName string
	}{
		{"with push name", "966500000001@s.whatsapp.net", "Abu Khalid", "Abu Khalid"},
		{"falls back to phone", "966500000001@s.whatsapp.net", "", "+966500000001"},
		// A LID chat_id's digits are an opaque WhatsApp id, not a phone
		// number — falling back to them as the contact's name would leak
		// that format into the dashboard (the operator-facing name field is
		// shown unconditionally, unlike waDisplayPhone's chat_id fallback).
		{"lid falls back to generic name, not lid digits", "90727124070644@lid", "", "+90727124070644"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := gateway.NewContactParams(tt.chatJID, tt.pushName, web.BlueprintDefaults{})
			if p.Enabled {
				t.Fatal("auto-created contacts must default to disabled (explicit operator opt-in)")
			}
			if p.ChatID != tt.chatJID {
				t.Errorf("ChatID = %q, want %q", p.ChatID, tt.chatJID)
			}
			if p.Name != tt.wantName {
				t.Errorf("Name = %q, want %q", p.Name, tt.wantName)
			}
			if p.AgentID != nil || p.PromptOverride != nil {
				t.Error("new contacts must start with no agent assignment or prompt override")
			}
		})
	}
}

// The System Default Blueprint may deliberately override D1: when AutoEnable is
// set, new contacts are created enabled and pre-seeded with the default brain
// and prompt override. This is the explicit operator opt-in escape hatch, so it
// must be exercised alongside the safe default above.
func TestNewContactParamsBlueprintAutoEnable(t *testing.T) {
	bp := web.BlueprintDefaults{
		DefaultAgentID:        "agent_ops",
		DefaultPromptOverride: "Prioritize breeding inquiries.",
		AutoEnable:            true,
	}
	p := gateway.NewContactParams("966500000002@s.whatsapp.net", "Layla", bp)

	if !p.Enabled {
		t.Fatal("AutoEnable blueprint must create the contact enabled")
	}
	if p.AgentID == nil || *p.AgentID != "agent_ops" {
		t.Errorf("AgentID = %v, want pointer to %q", p.AgentID, "agent_ops")
	}
	if p.PromptOverride == nil || *p.PromptOverride != "Prioritize breeding inquiries." {
		t.Errorf("PromptOverride = %v, want the blueprint override", p.PromptOverride)
	}
}

