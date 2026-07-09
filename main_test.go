package main

import "testing"

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
		{"falls back to phone", "966500000001@s.whatsapp.net", "", "966500000001"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newContactParams(tt.chatJID, tt.pushName)
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
