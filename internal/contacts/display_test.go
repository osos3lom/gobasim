package contacts

import "testing"

func TestDisplayPhone(t *testing.T) {
	override := "966546906905"
	blank := "   "

	tests := []struct {
		name     string
		chatID   string
		override *string
		want     string
	}{
		{"saudi jid becomes local format", "966501234567@s.whatsapp.net", nil, "0501234567"},
		{"override wins over the jid", "966501234567@s.whatsapp.net", &override, "0546906905"},
		{"blank override falls back to the jid", "966501234567@s.whatsapp.net", &blank, "0501234567"},
		// The core LID rule: no phone is known, so callers get "" and must
		// render their own "unlinked" affordance rather than the raw id.
		{"unresolved lid yields empty", "90727124070644@lid", nil, ""},
		{"resolved lid uses the override", "90727124070644@lid", &override, "0546906905"},
		{"non-saudi keeps international form", "971501234567@s.whatsapp.net", nil, "+971501234567"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DisplayPhone(tt.chatID, tt.override); got != tt.want {
				t.Errorf("DisplayPhone(%q) = %q, want %q", tt.chatID, got, tt.want)
			}
		})
	}
}

func TestFormatPhoneDisplay(t *testing.T) {
	tests := []struct{ in, want string }{
		{"966546906905", "0546906905"},
		{"+966546906905", "0546906905"},
		{"0546906905", "0546906905"},
		{"971501234567", "+971501234567"},
		{"", ""},
		// Over-long digit strings are LID-shaped, never a real phone number —
		// refuse rather than emit a plausible-looking "+90727124070644".
		{"90727124070644", ""},
	}

	for _, tt := range tests {
		if got := FormatPhoneDisplay(tt.in); got != tt.want {
			t.Errorf("FormatPhoneDisplay(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
