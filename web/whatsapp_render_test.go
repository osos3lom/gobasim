package web

import (
	"bytes"
	"html/template"
	"strings"
	"testing"
	"time"

	"sawt-go/database"
)

func TestWhatsAppRender_ThreadPilot(t *testing.T) {
	tmpl := template.Must(template.New("layout").ParseFS(templatesFS, "templates/*.html"))

	agents := []database.Agent{
		{ID: "agent_ops", Name: "Operations Brain", Status: "published"},
	}

	contact := database.WaContact{
		ChatID:         "1234@s.whatsapp.net",
		Name:           "Layla",
		Enabled:        true,
		AgentID:        &agents[0].ID,
		PromptOverride: nil,
	}

	messages := []database.WaMessage{
		{ID: "msg1", ChatID: "1234@s.whatsapp.net", Direction: "in", Sender: "customer", MsgType: "text", Content: "Hello", CreatedAt: time.Now()},
	}

	data := map[string]interface{}{
		"Page":            "whatsapp",
		"CSRFToken":       "token123",
		"PartialView":     "thread_pilot",
		"Partial":         true,
		"WaContact":       contact,
		"Messages":        messages,
		"PublishedAgents": agents,
		"Role":            "manager",
		"WindowOpen":      true,
		"WindowClosesIn":  "23h 59m",
		"AgentIDStr":      *contact.AgentID,
	}

	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "whatsapp.html", data); err != nil {
		t.Fatalf("failed to render thread_pilot partial: %v", err)
	}

	out := buf.String()
	wants := []string{
		"1234@s.whatsapp.net",
		"Resolved Role: manager",
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
}

func TestWhatsAppRender_SLAClosed(t *testing.T) {
	tmpl := template.Must(template.New("layout").ParseFS(templatesFS, "templates/*.html"))

	contact := database.WaContact{
		ChatID:  "1234@s.whatsapp.net",
		Name:    "Layla",
		Enabled: false,
	}

	data := map[string]interface{}{
		"Page":            "whatsapp",
		"CSRFToken":       "token123",
		"PartialView":     "thread_pilot",
		"Partial":         true,
		"WaContact":       contact,
		"Messages":        []database.WaMessage{},
		"PublishedAgents": []database.Agent{},
		"Role":            "client",
		"WindowOpen":      false,
	}

	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "whatsapp.html", data); err != nil {
		t.Fatalf("failed to render thread_pilot partial: %v", err)
	}

	out := buf.String()
	wants := []string{
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
}
