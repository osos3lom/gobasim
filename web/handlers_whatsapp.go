package web

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strings"
	"time"

	"rsc.io/qr"

	"sawt-go/database"
	"sawt-go/internal/contacts"
	waClient "sawt-go/internal/whatsmeow"
)

const (
	disconnectAlertThreshold = 15 * time.Second
	waMessagesThreadLimit    = 50
	// waSLAWindow is WhatsApp's 24-hour customer-service reply window.
	waSLAWindow = 24 * time.Hour
)

func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	h := d / time.Hour
	d -= h * time.Hour
	m := d / time.Minute
	d -= m * time.Minute
	s := d / time.Second

	if h > 0 {
		return fmt.Sprintf("%dh %dm", h, m)
	}
	if m > 0 {
		return fmt.Sprintf("%dm %ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}

func (s *Server) waConnectionData() (uptime string, showDisconnectAlert bool, disconnectedFor string) {
	state, connectedAt, disconnectedSince := s.waMgr.GetConnectionInfo()

	if state == waClient.StateConnected && !connectedAt.IsZero() {
		uptime = formatDuration(time.Since(connectedAt))
	}
	if disconnectedSince >= disconnectAlertThreshold {
		showDisconnectAlert = true
		disconnectedFor = formatDuration(disconnectedSince)
	}
	return
}

func qrDataURL(qrString string) template.URL {
	if qrString == "" {
		return ""
	}
	qBytes, err := qr.Encode(qrString, qr.L)
	if err != nil {
		log.Printf("web: failed to encode QR code: %v", err)
		return ""
	}
	return template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(qBytes.PNG()))
}

func (s *Server) renderWhatsAppCard(w http.ResponseWriter, r *http.Request, overrides map[string]interface{}) {
	status, qrString, pairCode := s.waMgr.GetStatus()
	uptime, showDisconnectAlert, disconnectedFor := s.waConnectionData()

	data := map[string]interface{}{
		"WAStatus":            string(status),
		"WAQR":                qrDataURL(qrString),
		"WAPair":              pairCode,
		"Uptime":              uptime,
		"ShowDisconnectAlert": showDisconnectAlert,
		"DisconnectedFor":     disconnectedFor,
		"Metrics":             s.waMetricsData(r.Context()),
		"Partial":             true,
		"CSRFToken":           s.ensureCSRFToken(w, r),
	}
	for k, v := range overrides {
		data[k] = v
	}
	s.renderTemplate(w, "whatsapp.html", data)
}

type waContactRow struct {
	database.WaContact
	CSRFToken          string
	PublishedAgents    []database.Agent
	ErpUnresolvedLabel string
}

func (s *Server) handleGetWhatsAppPage(w http.ResponseWriter, r *http.Request) {
	token := s.ensureCSRFToken(w, r)
	chats, err := s.queries.ListWaChatsSummary(r.Context())
	if err != nil {
		chats = []database.ListWaChatsSummaryRow{}
	}

	var bp contacts.BlueprintDefaults
	settings, err := s.queries.GetSettings(r.Context())
	if err == nil && len(settings.BotConfig) > 0 {
		_ = json.Unmarshal(settings.BotConfig, &bp)
	}

	s.renderWhatsAppCard(w, r, map[string]interface{}{
		"Username":        r.Context().Value(UsernameContextKey),
		"Page":            "whatsapp",
		"Partial":         false,
		"Contacts":        s.fetchWaContactRows(r.Context(), "", token),
		"Chats":           chats,
		"CSRFToken":       token,
		"Blueprint":       bp,
		"PublishedAgents": s.fetchPublishedAgents(r.Context()),
	})
}

func (s *Server) handleGetWhatsAppStatus(w http.ResponseWriter, r *http.Request) {
	s.renderWhatsAppCard(w, r, nil)
}

func (s *Server) handlePostWhatsAppPairCode(w http.ResponseWriter, r *http.Request) {
	phone := r.FormValue("phone")
	if phone == "" {
		http.Error(w, "Phone number is required", http.StatusBadRequest)
		return
	}

	var sb strings.Builder
	for _, char := range phone {
		if char >= '0' && char <= '9' {
			sb.WriteRune(char)
		}
	}
	phone = sb.String()

	if len(phone) < 8 || len(phone) > 15 {
		http.Error(w, "Invalid phone number length (must be 8-15 digits)", http.StatusBadRequest)
		return
	}

	prettyCode, err := s.waMgr.RequestPairingCode(phone)
	if err != nil {
		log.Printf("web: pairing code request failed: %v", err)
		http.Error(w, "Could not generate a pairing code — check the WhatsApp connection and try again.", http.StatusInternalServerError)
		return
	}

	s.renderWhatsAppCard(w, r, map[string]interface{}{
		"WAStatus": "pairing_ready",
		"WAQR":     "",
		"WAPair":   prettyCode,
	})
}

func (s *Server) handlePostWhatsAppLogout(w http.ResponseWriter, r *http.Request) {
	if err := s.waMgr.Logout(r.Context()); err != nil {
		_, _ = fmt.Fprintf(w, "<div class='bg-red-900 border border-red-500 text-red-200 px-4 py-3 rounded'>Logout failed: %v</div>", err)
		return
	}
	s.renderWhatsAppCard(w, r, nil)
}

func (s *Server) handlePostWhatsAppRepair(w http.ResponseWriter, r *http.Request) {
	qrChan, err := s.waMgr.RearmQR(r.Context())
	if err != nil {
		_, _ = fmt.Fprintf(w, "<div class='bg-red-900 border border-red-500 text-red-200 px-4 py-3 rounded'>Could not start a new pairing session: %v</div>", err)
		return
	}
	if err := s.waMgr.Connect(r.Context()); err != nil {
		_, _ = fmt.Fprintf(w, "<div class='bg-red-900 border border-red-500 text-red-200 px-4 py-3 rounded'>Failed to reconnect: %v</div>", err)
		return
	}
	go s.waMgr.StreamQRToState(context.Background(), qrChan, nil)

	s.renderWhatsAppCard(w, r, nil)
}

func (s *Server) handlePostWhatsAppSettings(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		feedbackErr(w, "Malformed form submission.")
		return
	}

	agentID := strings.TrimSpace(r.FormValue("agent_id"))
	promptOverride := r.FormValue("prompt_override")
	autoEnable := r.FormValue("auto_enable") == "on"

	settings, err := s.queries.GetSettings(r.Context())
	if err != nil {
		feedbackErr(w, "Failed to load current settings.")
		return
	}

	if agentID != "" {
		agent, err := s.queries.GetAgent(r.Context(), agentID)
		if err != nil {
			feedbackErr(w, "Default agent not found.")
			return
		}
		if agent.Status != "published" {
			feedbackErr(w, "Only published agents can be set as system defaults.")
			return
		}
	}

	bp := contacts.BlueprintDefaults{
		DefaultAgentID:        agentID,
		DefaultPromptOverride: promptOverride,
		AutoEnable:            autoEnable,
	}
	bpBytes, err := json.Marshal(bp)
	if err != nil {
		feedbackErr(w, "Failed to marshal settings.")
		return
	}

	err = s.queries.UpdateSettings(r.Context(), database.UpdateSettingsParams{
		TtsModel:         settings.TtsModel,
		ModelIds:         settings.ModelIds,
		DefaultSpeed:     settings.DefaultSpeed,
		BotConfig:        bpBytes,
		AssistantAgentID: settings.AssistantAgentID,
	})
	if err != nil {
		log.Printf("web: failed to update system defaults: %v", err)
		feedbackErr(w, "Failed to save settings.")
		return
	}

	_, _ = w.Write([]byte("<div class='bg-emerald-900 border border-emerald-500 text-emerald-200 px-4 py-3 rounded'>System defaults saved successfully!</div>"))
}
