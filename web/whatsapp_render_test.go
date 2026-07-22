package web

import (
	"bytes"
	"html/template"
	"strings"
	"testing"
	"time"

	"sawt-go/database"
)

// leaksAsVisibleText reports whether needle appears as rendered element text
// (bounded by ">" and "<") rather than only inside an attribute (hx-get
// routing URLs, DOM ids, form field values) — chat_id legitimately stays in
// those for routing/targeting; it must just never be text a human reads.
func leaksAsVisibleText(html, needle string) bool {
	return strings.Contains(html, ">"+needle+"<")
}

func TestWhatsAppRender_ThreadPilot(t *testing.T) {
	tmpl := template.Must(template.New("layout").Funcs(templateFuncs).ParseFS(templatesFS, "templates/*.html"))

	agents := []database.Agent{
		{ID: "agent_ops", Name: "Operations Brain", Status: "published"},
	}

	erpUID := "uid_123"
	displayName := "Layla Hassan"
	role := "manager"
	chatID := "966501234567@s.whatsapp.net"
	contact := database.WaContact{
		ChatID:         chatID,
		Name:           "Layla",
		Enabled:        true,
		AgentID:        &agents[0].ID,
		PromptOverride: nil,
		ErpUid:         &erpUID,
		ErpDisplayName: &displayName,
		ErpRole:        &role,
	}

	messages := []database.WaMessage{
		{ID: "msg1", ChatID: chatID, Direction: "in", Sender: "customer", MsgType: "text", Content: "Hello", CreatedAt: time.Now()},
	}

	data := map[string]interface{}{
		"Page":                    "whatsapp",
		"CSRFToken":               "token123",
		"PartialView":             "thread_pilot",
		"Partial":                 true,
		"ChatID":                  chatID,
		"ContactErpPhoneOverride": contact.ErpPhoneOverride,
		"WaContact":               contact,
		"Messages":                messages,
		"PublishedAgents":         agents,
		"WindowOpen":              true,
		"WindowClosesIn":          "23h 59m",
		"AgentIDStr":              *contact.AgentID,
	}

	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "whatsapp.html", data); err != nil {
		t.Fatalf("failed to render thread_pilot partial: %v", err)
	}

	out := buf.String()
	wants := []string{
		// waDisplayPhone must convert the WhatsApp JID's international digits
		// (966501234567) into the local format an operator recognizes
		// (0501234567) — this is the core fix: the dashboard shows a real
		// phone number, not internal WhatsApp/ERP identifiers.
		"0501234567",
		"Layla Hassan",
		"manager",
		"Authorized (AI Agent Active)",
		"Operations Brain",
		// The assigned brain must render as the pre-selected option. This guards
		// the AgentIDStr contract: the handlers deref WaContact.AgentID into
		// AgentIDStr so the pilot <select> highlights the current agent.
		`value="agent_ops" selected`,
		"SLA Window: Closes in 23h 59m",
		"Type a message...",
	}

	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("rendered thread_pilot output missing expected text %q", w)
		}
	}
	// The raw WhatsApp JID (chat_id) must never leak into the rendered page as
	// visible text — an operator should only ever read the formatted phone
	// number. It legitimately stays inside hx-* routing URLs/DOM ids, which
	// leaksAsVisibleText (text bounded by ">" and "<") does not flag.
	if leaksAsVisibleText(out, chatID) {
		t.Errorf("rendered output leaked the raw chat_id %q as visible text — waDisplayPhone should have replaced it", chatID)
	}
}

func TestWhatsAppRender_SLAClosed(t *testing.T) {
	tmpl := template.Must(template.New("layout").Funcs(templateFuncs).ParseFS(templatesFS, "templates/*.html"))

	chatID := "966501234567@s.whatsapp.net"
	contact := database.WaContact{
		ChatID:  chatID,
		Name:    "Layla",
		Enabled: false,
	}

	data := map[string]interface{}{
		"Page":                    "whatsapp",
		"CSRFToken":               "token123",
		"PartialView":             "thread_pilot",
		"Partial":                 true,
		"ChatID":                  chatID,
		"ContactErpPhoneOverride": contact.ErpPhoneOverride,
		"WaContact":               contact,
		"Messages":                []database.WaMessage{},
		"PublishedAgents":         []database.Agent{},
		"WindowOpen":              false,
	}

	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "whatsapp.html", data); err != nil {
		t.Fatalf("failed to render thread_pilot partial: %v", err)
	}

	out := buf.String()
	wants := []string{
		"0501234567",
		"SLA Window: Closed (24h+ inactive)",
		"Arabic Check-in Template",
		"English Check-in Template",
		"Regular chat input locked...",
	}

	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("rendered thread_pilot output (SLA closed) missing expected text %q", w)
		}
	}
	if leaksAsVisibleText(out, chatID) {
		t.Errorf("rendered output leaked the raw chat_id %q as visible text", chatID)
	}
}

// TestWhatsAppRender_LIDContactShowsNoRawLID is the direct regression test
// for the root-cause bug: a WhatsApp LID chat_id (WhatsApp's opaque
// per-contact id, e.g. "90727124070644@lid" — see internal/erp/link.go's
// LIDResolver) must never render as visible text; an unresolved LID
// contact shows a generic "Unlinked" label instead, and a resolved one
// (erp_phone_override populated — see ResolveAndPersistContactIdentity)
// shows the real phone in local format.
func TestWhatsAppRender_LIDContactShowsNoRawLID(t *testing.T) {
	tmpl := template.Must(template.New("layout").Funcs(templateFuncs).ParseFS(templatesFS, "templates/*.html"))
	const lidChatID = "90727124070644@lid"

	t.Run("unresolved LID shows a generic label, not the LID digits", func(t *testing.T) {
		contact := database.WaContact{ChatID: lidChatID, Name: "Osama", Enabled: false}
		data := map[string]interface{}{
			"Page":                    "whatsapp",
			"CSRFToken":               "token123",
			"PartialView":             "thread_pilot",
			"Partial":                 true,
			"ChatID":                  lidChatID,
			"ContactErpPhoneOverride": contact.ErpPhoneOverride,
			"WaContact":               contact,
			"Messages":                []database.WaMessage{},
			"PublishedAgents":         []database.Agent{},
			"WindowOpen":              false,
		}
		var buf bytes.Buffer
		if err := tmpl.ExecuteTemplate(&buf, "whatsapp.html", data); err != nil {
			t.Fatalf("failed to render: %v", err)
		}
		out := buf.String()
		if leaksAsVisibleText(out, "90727124070644") || leaksAsVisibleText(out, lidChatID) {
			t.Errorf("rendered output leaked the raw LID digits, got:\n%s", out)
		}
		if !strings.Contains(out, "Unlinked") {
			t.Errorf("expected an 'Unlinked' fallback label for a still-unresolved LID contact")
		}
	})

	t.Run("resolved LID shows the local-format phone, not the LID digits", func(t *testing.T) {
		phone := "966546906905"
		contact := database.WaContact{ChatID: lidChatID, Name: "Osama", Enabled: true, ErpPhoneOverride: &phone}
		data := map[string]interface{}{
			"Page":                    "whatsapp",
			"CSRFToken":               "token123",
			"PartialView":             "thread_pilot",
			"Partial":                 true,
			"ChatID":                  lidChatID,
			"ContactErpPhoneOverride": contact.ErpPhoneOverride,
			"WaContact":               contact,
			"Messages":                []database.WaMessage{},
			"PublishedAgents":         []database.Agent{},
			"WindowOpen":              false,
		}
		var buf bytes.Buffer
		if err := tmpl.ExecuteTemplate(&buf, "whatsapp.html", data); err != nil {
			t.Fatalf("failed to render: %v", err)
		}
		out := buf.String()
		if leaksAsVisibleText(out, "90727124070644") || leaksAsVisibleText(out, lidChatID) {
			t.Errorf("rendered output leaked the raw LID digits, got:\n%s", out)
		}
		if !strings.Contains(out, "0546906905") {
			t.Errorf("expected the resolved phone in local format (0546906905), got:\n%s", out)
		}
	})
}
