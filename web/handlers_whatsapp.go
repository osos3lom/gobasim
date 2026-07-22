package web

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"rsc.io/qr"

	"sawt-go/database"
	"sawt-go/internal/audio"
	"sawt-go/internal/erp"
	"sawt-go/internal/monitor"
	"sawt-go/internal/voicenotes"
	waClient "sawt-go/internal/whatsmeow"
)

const (
	disconnectAlertThreshold = 15 * time.Second
	waMessagesThreadLimit     = 50
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

func (s *Server) fetchPublishedAgents(ctx context.Context) []database.Agent {
	agents, err := s.queries.ListPublishedAgents(ctx)
	if err != nil {
		return []database.Agent{}
	}
	return agents
}

func (s *Server) fetchWaContactRows(ctx context.Context, query, csrfToken string) []waContactRow {
	contacts, err := s.queries.ListWaContacts(ctx)
	if err != nil {
		return []waContactRow{}
	}
	publishedAgents := s.fetchPublishedAgents(ctx)

	query = strings.ToLower(query)
	rows := make([]waContactRow, 0, len(contacts))
	for _, c := range contacts {
		if strings.HasSuffix(strings.ToLower(c.ChatID), "@lid") && (c.ErpPhoneOverride == nil || *c.ErpPhoneOverride == "") && s.waMgr != nil {
			lidUser := strings.Split(c.ChatID, "@")[0]
			if phone, ok := s.waMgr.ResolvePhoneForLID(ctx, lidUser); ok && phone != "" {
				c.ErpPhoneOverride = &phone
				_, _ = s.queries.UpdateWaContactErpOverride(ctx, database.UpdateWaContactErpOverrideParams{
					ChatID:           c.ChatID,
					ErpPhoneOverride: &phone,
				})
				if s.erpClient != nil {
					if _, err := erp.ResolveAndPersistContactIdentity(ctx, s.erpClient, s.queries, c.ChatID, &phone, s.waMgr); err == nil {
						if refreshed, err := s.queries.GetWaContact(ctx, c.ChatID); err == nil {
							c = refreshed
						}
					}
				}
			}
		}

		if query != "" && !strings.Contains(strings.ToLower(c.Name), query) && !strings.Contains(strings.ToLower(c.ChatID), query) {
			continue
		}
		rows = append(rows, waContactRow{
			WaContact:          c,
			CSRFToken:          csrfToken,
			PublishedAgents:    publishedAgents,
			ErpUnresolvedLabel: erpUnresolvedLabel(c.ErpUnresolvedReason),
		})
	}
	return rows
}

func agentIDValue(id *string) string {
	if id == nil {
		return ""
	}
	return *id
}

func waDisplayPhone(chatID string, phoneOverride *string) string {
	if phoneOverride != nil && strings.TrimSpace(*phoneOverride) != "" {
		return formatPhoneDisplay(strings.TrimSpace(*phoneOverride))
	}
	if strings.HasSuffix(strings.ToLower(chatID), "@lid") {
		return ""
	}
	parts := strings.Split(chatID, "@")
	if len(parts) > 0 {
		return formatPhoneDisplay(parts[0])
	}
	return ""
}

func formatPhoneDisplay(phone string) string {
	digits := strings.TrimPrefix(strings.TrimSpace(phone), "+")
	if digits == "" {
		return ""
	}
	// Saudi international format: 966546906905 -> 0546906905
	if strings.HasPrefix(digits, "966") && len(digits) == 12 {
		return "0" + digits[3:]
	}
	// Saudi local format: 0546906905
	if strings.HasPrefix(digits, "0") {
		return digits
	}
	// Raw LID digits (typically 14-15 digits like 90727124070644) are NOT phone numbers
	if len(digits) > 13 && !strings.HasPrefix(digits, "966") {
		return ""
	}
	return "+" + digits
}

func cleanContactName(name string, chatID string, phoneOverride *string) string {
	trimmed := strings.TrimSpace(name)
	isRawLID := trimmed == "" ||
		strings.HasSuffix(strings.ToLower(trimmed), "@lid") ||
		(strings.HasPrefix(trimmed, "+") && len(trimmed) > 13 && !strings.HasPrefix(trimmed, "+966")) ||
		(len(trimmed) > 13 && strings.Contains(chatID, "@lid"))

	if isRawLID {
		phone := waDisplayPhone(chatID, phoneOverride)
		if phone != "" {
			return phone
		}
		return "WhatsApp Contact"
	}
	return trimmed
}


func erpUnresolvedLabel(reason *string) string {
	if reason == nil {
		return "not yet resolved"
	}
	switch *reason {
	case erp.UnresolvedPhoneUnverified:
		return "phone unverified"
	case erp.UnresolvedLidUnlinked:
		return "LID — Set Phone Override"
	case erp.UnresolvedNoMatch:
		return "no ERP match"
	default:
		return *reason
	}
}

func (s *Server) handleGetWhatsAppPage(w http.ResponseWriter, r *http.Request) {
	token := s.ensureCSRFToken(w, r)
	chats, err := s.queries.ListWaChatsSummary(r.Context())
	if err != nil {
		chats = []database.ListWaChatsSummaryRow{}
	}

	var bp BlueprintDefaults
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

func (s *Server) handleGetWaContactsFragment(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	s.renderTemplate(w, "whatsapp.html", map[string]interface{}{
		"Contacts":    s.fetchWaContactRows(r.Context(), query, s.ensureCSRFToken(w, r)),
		"PartialView": "contacts",
		"Partial":     true,
	})
}

func (s *Server) handlePostToggleWaContact(w http.ResponseWriter, r *http.Request) {
	chatID := chi.URLParam(r, "chatID")

	contact, err := s.queries.GetWaContact(r.Context(), chatID)
	if err != nil {
		http.Error(w, "Contact not found", http.StatusNotFound)
		return
	}

	updated, err := s.queries.CreateOrUpdateWaContact(r.Context(), database.CreateOrUpdateWaContactParams{
		ChatID:         contact.ChatID,
		Name:           contact.Name,
		Enabled:        !contact.Enabled,
		AgentID:        contact.AgentID,
		PromptOverride: contact.PromptOverride,
	})
	if err != nil {
		http.Error(w, "Failed to update contact", http.StatusInternalServerError)
		return
	}

	view := r.FormValue("view")
	if view == "pilot" {
		s.renderTemplate(w, "whatsapp.html", map[string]interface{}{
			"WaContact":          updated,
			"CSRFToken":          s.ensureCSRFToken(w, r),
			"PublishedAgents":    s.fetchPublishedAgents(r.Context()),
			"AgentIDStr":         agentIDValue(updated.AgentID),
			"ErpUnresolvedLabel": erpUnresolvedLabel(updated.ErpUnresolvedReason),
			"PartialView":        "pilot_panel",
			"Partial":            true,
		})
	} else {
		s.renderTemplate(w, "whatsapp.html", map[string]interface{}{
			"Row": waContactRow{
				WaContact:          updated,
				CSRFToken:          s.ensureCSRFToken(w, r),
				PublishedAgents:    s.fetchPublishedAgents(r.Context()),
				ErpUnresolvedLabel: erpUnresolvedLabel(updated.ErpUnresolvedReason),
			},
			"PartialView": "contact_row",
			"Partial":     true,
		})
	}
}

func (s *Server) handlePostAssignWaContactAgent(w http.ResponseWriter, r *http.Request) {
	chatID := chi.URLParam(r, "chatID")
	agentID := strings.TrimSpace(r.FormValue("agent_id"))
	promptOverride := r.FormValue("prompt_override")

	contact, err := s.queries.GetWaContact(r.Context(), chatID)
	if err != nil {
		http.Error(w, "Contact not found", http.StatusNotFound)
		return
	}

	var agentIDPtr *string
	if agentID != "" {
		agent, err := s.queries.GetAgent(r.Context(), agentID)
		if err != nil {
			http.Error(w, "Agent not found", http.StatusBadRequest)
			return
		}
		if agent.Status != "published" {
			http.Error(w, "Only published agents can be assigned", http.StatusBadRequest)
			return
		}
		agentIDPtr = &agentID
	}

	var promptOverridePtr *string
	if promptOverride != "" {
		promptOverridePtr = &promptOverride
	}

	updated, err := s.queries.CreateOrUpdateWaContact(r.Context(), database.CreateOrUpdateWaContactParams{
		ChatID:         contact.ChatID,
		Name:           contact.Name,
		Enabled:        contact.Enabled,
		AgentID:        agentIDPtr,
		PromptOverride: promptOverridePtr,
	})
	if err != nil {
		http.Error(w, "Failed to update contact", http.StatusInternalServerError)
		return
	}

	view := r.FormValue("view")
	if view == "pilot" {
		s.renderTemplate(w, "whatsapp.html", map[string]interface{}{
			"WaContact":          updated,
			"CSRFToken":          s.ensureCSRFToken(w, r),
			"PublishedAgents":    s.fetchPublishedAgents(r.Context()),
			"AgentIDStr":         agentIDValue(updated.AgentID),
			"ErpUnresolvedLabel": erpUnresolvedLabel(updated.ErpUnresolvedReason),
			"PartialView":        "pilot_panel",
			"Partial":            true,
		})
	} else {
		s.renderTemplate(w, "whatsapp.html", map[string]interface{}{
			"Row": waContactRow{
				WaContact:          updated,
				CSRFToken:          s.ensureCSRFToken(w, r),
				PublishedAgents:    s.fetchPublishedAgents(r.Context()),
				ErpUnresolvedLabel: erpUnresolvedLabel(updated.ErpUnresolvedReason),
			},
			"PartialView": "contact_row",
			"Partial":     true,
		})
	}
}

func (s *Server) renderWaContactAfterErpAction(w http.ResponseWriter, r *http.Request, contact database.WaContact) {
	if r.FormValue("view") == "pilot" {
		s.renderTemplate(w, "whatsapp.html", map[string]interface{}{
			"WaContact":          contact,
			"CSRFToken":          s.ensureCSRFToken(w, r),
			"PublishedAgents":    s.fetchPublishedAgents(r.Context()),
			"AgentIDStr":         agentIDValue(contact.AgentID),
			"ErpUnresolvedLabel": erpUnresolvedLabel(contact.ErpUnresolvedReason),
			"PartialView":        "pilot_panel",
			"Partial":            true,
		})
		return
	}
	s.renderTemplate(w, "whatsapp.html", map[string]interface{}{
		"Row": waContactRow{
			WaContact:          contact,
			CSRFToken:          s.ensureCSRFToken(w, r),
			PublishedAgents:    s.fetchPublishedAgents(r.Context()),
			ErpUnresolvedLabel: erpUnresolvedLabel(contact.ErpUnresolvedReason),
		},
		"PartialView": "contact_row",
		"Partial":     true,
	})
}

func (s *Server) handlePostSetWaContactErpOverride(w http.ResponseWriter, r *http.Request) {
	chatID := chi.URLParam(r, "chatID")
	override := strings.TrimSpace(r.FormValue("erp_phone_override"))

	var overridePtr *string
	if override != "" {
		overridePtr = &override
	}

	contact, err := s.queries.UpdateWaContactErpOverride(r.Context(), database.UpdateWaContactErpOverrideParams{
		ChatID:           chatID,
		ErpPhoneOverride: overridePtr,
	})
	if err != nil {
		http.Error(w, "Failed to update contact", http.StatusInternalServerError)
		return
	}

	if s.erpClient != nil {
		if _, err := erp.ResolveAndPersistContactIdentity(r.Context(), s.erpClient, s.queries, chatID, contact.ErpPhoneOverride, s.waMgr); err != nil {
			monitor.ReportError(r.Context(), "identity", err)
		} else if refreshed, err := s.queries.GetWaContact(r.Context(), chatID); err == nil {
			contact = refreshed
		}
	}

	s.renderWaContactAfterErpAction(w, r, contact)
}

func (s *Server) handlePostResolveWaContactIdentity(w http.ResponseWriter, r *http.Request) {
	chatID := chi.URLParam(r, "chatID")

	contact, err := s.queries.GetWaContact(r.Context(), chatID)
	if err != nil {
		http.Error(w, "Contact not found", http.StatusNotFound)
		return
	}

	if s.erpClient == nil {
		http.Error(w, "ERP integration not configured", http.StatusServiceUnavailable)
		return
	}

	if _, err := erp.ResolveAndPersistContactIdentity(r.Context(), s.erpClient, s.queries, chatID, contact.ErpPhoneOverride, s.waMgr); err != nil {
		monitor.ReportError(r.Context(), "identity", err)
		http.Error(w, "ERP resolution failed: "+err.Error(), http.StatusBadGateway)
		return
	}

	refreshed, err := s.queries.GetWaContact(r.Context(), chatID)
	if err != nil {
		http.Error(w, "Contact not found", http.StatusNotFound)
		return
	}

	s.renderWaContactAfterErpAction(w, r, refreshed)
}

func (s *Server) handlePostSendWaLinkInvite(w http.ResponseWriter, r *http.Request) {
	chatID := chi.URLParam(r, "chatID")
	contact, err := s.queries.GetWaContact(r.Context(), chatID)
	if err != nil {
		http.Error(w, "Contact not found", http.StatusNotFound)
		return
	}

	var identity *erp.Identity
	if s.erpClient != nil {
		if linkResult, lErr := erp.ResolveAndPersistContactIdentity(r.Context(), s.erpClient, s.queries, chatID, contact.ErpPhoneOverride, s.waMgr); lErr == nil && linkResult != nil {
			identity = linkResult.Identity
		}
	}

	var msg string
	if identity != nil {
		msg = fmt.Sprintf("مرحباً %s! عثرنا على حسابك في النظام بصفتك (%s). هل ترغب في ربط هذا الرقم بحسابك والتواصل مع مساعد صوت؟ أجب بـ 'نعم' للتأكيد.", identity.DisplayName, identity.Role)
	} else {
		msg = "أهلاً بك! رقمك غير مسجل في نظام مشالية بعد. يرجى تزويد المسؤول برقم جوالك أو تحديث بياناتك في النظام لربط الحساب."
	}

	if err := s.waMgr.SendTextMessage(r.Context(), chatID, msg); err != nil {
		http.Error(w, "Failed to send link invite: "+err.Error(), http.StatusInternalServerError)
		return
	}

	s.renderWaContactAfterErpAction(w, r, contact)
}

func (s *Server) handleGetWaContactTools(w http.ResponseWriter, r *http.Request) {
	chatID := chi.URLParam(r, "chatID")
	tools, err := s.queries.ListToolExecutionsByChat(r.Context(), database.ListToolExecutionsByChatParams{
		ChatID: chatID,
		Limit:  20,
	})
	if err != nil {
		tools = []database.ToolExecution{}
	}

	s.renderTemplate(w, "whatsapp.html", map[string]interface{}{
		"Tools":       tools,
		"ChatID":      chatID,
		"PartialView": "contact_tools",
		"Partial":     true,
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

func (s *Server) handleGetWaChatsFragment(w http.ResponseWriter, r *http.Request) {
	chats, err := s.queries.ListWaChatsSummary(r.Context())
	if err != nil {
		chats = []database.ListWaChatsSummaryRow{}
	}

	s.renderTemplate(w, "whatsapp.html", map[string]interface{}{
		"Chats":       chats,
		"PartialView": "chats",
		"Partial":     true,
	})
}

func (s *Server) handleGetWaMessagesFragment(w http.ResponseWriter, r *http.Request) {
	chatID := chi.URLParam(r, "chatID")

	var beforeSeq int64
	if v := r.URL.Query().Get("before_seq"); v != "" {
		beforeSeq, _ = strconv.ParseInt(v, 10, 64)
	}

	messages, err := s.queries.ListWaMessagesByChat(r.Context(), database.ListWaMessagesByChatParams{
		ChatID:    chatID,
		BeforeSeq: beforeSeq,
		Limit:     waMessagesThreadLimit,
	})
	if err != nil {
		messages = []database.WaMessage{}
	}
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}

	if beforeSeq > 0 {
		s.renderTemplate(w, "whatsapp.html", map[string]interface{}{
			"ChatID":                  chatID,
			"ContactErpPhoneOverride": (*string)(nil),
			"Messages":                messages,
			"CSRFToken":               s.ensureCSRFToken(w, r),
			"PartialView":             "messages",
			"Partial":                 true,
		})
		return
	}

	contact, err := s.queries.GetWaContact(r.Context(), chatID)
	if err != nil {
		contact = database.WaContact{
			ChatID: chatID,
		}
	} else if strings.HasSuffix(strings.ToLower(chatID), "@lid") && (contact.ErpPhoneOverride == nil || *contact.ErpPhoneOverride == "") && s.waMgr != nil {
		lidUser := strings.Split(chatID, "@")[0]
		if phone, ok := s.waMgr.ResolvePhoneForLID(r.Context(), lidUser); ok && phone != "" {
			contact.ErpPhoneOverride = &phone
			_, _ = s.queries.UpdateWaContactErpOverride(r.Context(), database.UpdateWaContactErpOverrideParams{
				ChatID:           chatID,
				ErpPhoneOverride: &phone,
			})
			if s.erpClient != nil {
				if _, err := erp.ResolveAndPersistContactIdentity(r.Context(), s.erpClient, s.queries, chatID, &phone, s.waMgr); err == nil {
					if refreshed, err := s.queries.GetWaContact(r.Context(), chatID); err == nil {
						contact = refreshed
					}
				}
			}
		}
	}

	var lastInbound *database.WaMessage
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Direction == "in" {
			lastInbound = &messages[i]
			break
		}
	}

	var windowOpen bool
	var windowClosesIn string
	if lastInbound != nil {
		elapsed := time.Since(lastInbound.CreatedAt)
		if elapsed < 24*time.Hour {
			windowOpen = true
			closesAt := lastInbound.CreatedAt.Add(24 * time.Hour)
			remaining := time.Until(closesAt)
			if remaining > 0 {
				hours := int(remaining.Hours())
				mins := int(remaining.Minutes()) % 60
				windowClosesIn = fmt.Sprintf("%dh %dm", hours, mins)
			}
		}
	}

	s.renderTemplate(w, "whatsapp.html", map[string]interface{}{
		"ChatID":                  chatID,
		"ContactErpPhoneOverride": contact.ErpPhoneOverride,
		"Messages":                messages,
		"CSRFToken":               s.ensureCSRFToken(w, r),
		"WindowOpen":              windowOpen,
		"WindowClosesIn":          windowClosesIn,
		"WaContact":               contact,
		"PublishedAgents":         s.fetchPublishedAgents(r.Context()),
		"AgentIDStr":              agentIDValue(contact.AgentID),
		"ErpUnresolvedLabel":      erpUnresolvedLabel(contact.ErpUnresolvedReason),
		"PartialView":             "thread_pilot",
		"Partial":                 true,
	})
}

func (s *Server) handlePostSendWaText(w http.ResponseWriter, r *http.Request) {
	chatID := chi.URLParam(r, "chatID")
	text := strings.TrimSpace(r.FormValue("text"))
	if text == "" {
		http.Error(w, "Message text is required", http.StatusBadRequest)
		return
	}

	status := "sent"
	if err := s.waMgr.SendTextMessage(r.Context(), chatID, text); err != nil {
		log.Printf("web: failed to send WhatsApp text message: %v", err)
		status = "failed"
	}

	idBytes := make([]byte, 8)
	_, _ = rand.Read(idBytes)
	msg := database.WaMessage{
		ID:        "wamsg_" + hex.EncodeToString(idBytes),
		ChatID:    chatID,
		Direction: "out",
		Sender:    "operator",
		MsgType:   "text",
		Content:   text,
		Status:    status,
		CreatedAt: time.Now(),
	}
	_ = s.queries.CreateWaMessage(r.Context(), database.CreateWaMessageParams{
		ID:        msg.ID,
		ChatID:    msg.ChatID,
		Direction: msg.Direction,
		Sender:    msg.Sender,
		MsgType:   msg.MsgType,
		Content:   msg.Content,
		Status:    msg.Status,
	})

	s.renderTemplate(w, "whatsapp.html", map[string]interface{}{
		"Message":     msg,
		"PartialView": "message_bubble",
		"Partial":     true,
	})
}

func (s *Server) handlePostSendWaVoice(w http.ResponseWriter, r *http.Request) {
	chatID := chi.URLParam(r, "chatID")
	text := strings.TrimSpace(r.FormValue("text"))
	if text == "" {
		http.Error(w, "Message text is required", http.StatusBadRequest)
		return
	}

	status := "sent"
	rawWav, _, err := s.ttsOrch.Synthesize(r.Context(), text, "ar")
	if err != nil {
		log.Printf("web: TTS synthesis failed for manual voice send: %v", err)
		status = "failed"
	} else {
		opusBytes, err := audio.WavToOpus(rawWav)
		if err != nil {
			log.Printf("web: audio transcoding failed for manual voice send: %v", err)
			status = "failed"
		} else if err := s.waMgr.SendVoiceMessage(r.Context(), chatID, opusBytes); err != nil {
			log.Printf("web: failed to send WhatsApp voice message: %v", err)
			status = "failed"
		} else {
			s.voiceStore.Save(r.Context(), voicenotes.Meta{
				MessageID: "opvoice_" + hex.EncodeToString(func() []byte { b := make([]byte, 8); _, _ = rand.Read(b); return b }()),
				ChatID:    chatID,
				Direction: "out",
				Sender:    "operator",
				Receiver:  strings.Split(chatID, "@")[0],
				Timestamp: time.Now(),
			}, opusBytes)
		}
	}

	idBytes := make([]byte, 8)
	_, _ = rand.Read(idBytes)
	msg := database.WaMessage{
		ID:        "wamsg_" + hex.EncodeToString(idBytes),
		ChatID:    chatID,
		Direction: "out",
		Sender:    "operator",
		MsgType:   "voice",
		Content:   text,
		Status:    status,
		CreatedAt: time.Now(),
	}
	_ = s.queries.CreateWaMessage(r.Context(), database.CreateWaMessageParams{
		ID:        msg.ID,
		ChatID:    msg.ChatID,
		Direction: msg.Direction,
		Sender:    msg.Sender,
		MsgType:   msg.MsgType,
		Content:   msg.Content,
		Status:    msg.Status,
	})

	s.renderTemplate(w, "whatsapp.html", map[string]interface{}{
		"Message":     msg,
		"PartialView": "message_bubble",
		"Partial":     true,
	})
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

	bp := BlueprintDefaults{
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

