package web

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"sawt-go/database"
	"sawt-go/internal/audio"
	"sawt-go/internal/erp"
	"sawt-go/internal/voicenotes"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// WhatsApp chat and message handlers: the chat list, the message thread
// fragment (with its 24h SLA window computation), and operator-initiated
// text/voice sends. Contact management lives in handlers_wa_contacts.go.

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
	} else {
		contact = s.backfillLIDPhone(r, contact)
	}

	windowOpen, windowClosesIn := slaWindow(messages)

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

// slaWindow reports whether WhatsApp's 24-hour customer-service window is
// still open for a thread, and how long is left, derived from the newest
// inbound message in the loaded page.
//
// Note the page bound (waMessagesThreadLimit): if more than that many
// bot/operator messages have been sent since the contact last wrote in, no
// inbound appears in the page and the window reads as closed even though it
// technically is not. Acceptable at current volume; a dedicated "latest
// inbound timestamp" query would make it exact. messages must be oldest-first.
func slaWindow(messages []database.WaMessage) (open bool, closesIn string) {
	var lastInbound *database.WaMessage
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Direction == "in" {
			lastInbound = &messages[i]
			break
		}
	}
	if lastInbound == nil || time.Since(lastInbound.CreatedAt) >= waSLAWindow {
		return false, ""
	}
	remaining := time.Until(lastInbound.CreatedAt.Add(waSLAWindow))
	if remaining <= 0 {
		return true, ""
	}
	return true, fmt.Sprintf("%dh %dm", int(remaining.Hours()), int(remaining.Minutes())%60)
}

// backfillLIDPhone opportunistically fills in a LID contact's phone number the
// first time an operator opens the thread. WhatsApp gives many contacts an
// opaque "@lid" chat id; whatsmeow learns the real number separately, so if we
// have it and the contact has no phone recorded yet, persist it and re-resolve
// the ERP identity. Best-effort: any failure leaves the contact untouched.
func (s *Server) backfillLIDPhone(r *http.Request, contact database.WaContact) database.WaContact {
	chatID := contact.ChatID
	if !strings.HasSuffix(strings.ToLower(chatID), "@lid") || s.waMgr == nil {
		return contact
	}
	if contact.ErpPhoneOverride != nil && *contact.ErpPhoneOverride != "" {
		return contact
	}

	phone, ok := s.waMgr.ResolvePhoneForLID(r.Context(), strings.Split(chatID, "@")[0])
	if !ok || phone == "" {
		return contact
	}

	contact.ErpPhoneOverride = &phone
	_, _ = s.queries.UpdateWaContactErpOverride(r.Context(), database.UpdateWaContactErpOverrideParams{
		ChatID:           chatID,
		ErpPhoneOverride: &phone,
	})
	if s.erpClient == nil {
		return contact
	}
	if _, err := erp.ResolveAndPersistContactIdentity(r.Context(), s.erpClient, s.queries, chatID, &phone, s.waMgr); err != nil {
		return contact
	}
	if refreshed, err := s.queries.GetWaContact(r.Context(), chatID); err == nil {
		return refreshed
	}
	return contact
}
