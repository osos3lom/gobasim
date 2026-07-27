package web

import (
	"context"
	"fmt"
	"net/http"
	"sawt-go/database"
	"sawt-go/internal/contacts"
	"sawt-go/internal/erp"
	"sawt-go/internal/monitor"
	"strings"

	"github.com/go-chi/chi/v5"
)

// WhatsApp contact management: the contacts list/fragment, per-contact agent
// and prompt assignment, and the ERP identity actions (phone override,
// re-resolve, link invite) plus the per-contact tool audit panel.
// Connection/pairing and chat/message handlers live in their sibling files.

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

func cleanContactName(name string, chatID string, phoneOverride *string) string {
	trimmed := strings.TrimSpace(name)
	isRawLID := trimmed == "" ||
		strings.HasSuffix(strings.ToLower(trimmed), "@lid") ||
		(strings.HasPrefix(trimmed, "+") && len(trimmed) > 13 && !strings.HasPrefix(trimmed, "+966")) ||
		(len(trimmed) > 13 && strings.Contains(chatID, "@lid"))

	if isRawLID {
		phone := contacts.DisplayPhone(chatID, phoneOverride)
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
