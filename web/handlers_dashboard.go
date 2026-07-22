package web

import (
	"context"
	"net/http"
	"time"

	"sawt-go/database"
)

type WAMetrics struct {
	TotalContacts  int
	AiActiveCount  int
	ErpLinkedCount int
	ErpLinkRatio   int
	SlaActiveCount int
	TotalMessages  int
}

func (s *Server) waMetricsData(ctx context.Context) WAMetrics {
	contacts, err := s.queries.ListWaContacts(ctx)
	if err != nil {
		contacts = []database.WaContact{}
	}
	m := WAMetrics{
		TotalContacts: len(contacts),
	}
	for _, c := range contacts {
		if c.Enabled {
			m.AiActiveCount++
		}
		if c.ErpUid != nil && *c.ErpUid != "" {
			m.ErpLinkedCount++
		}
	}
	if m.TotalContacts > 0 {
		m.ErpLinkRatio = (m.ErpLinkedCount * 100) / m.TotalContacts
	}
	chats, err := s.queries.ListWaChatsSummary(ctx)
	if err == nil {
		m.TotalMessages = len(chats)
		now := time.Now()
		for _, ch := range chats {
			if now.Sub(ch.LastMessageAt) < 24*time.Hour {
				m.SlaActiveCount++
			}
		}
	}
	return m
}

func (s *Server) handleGetDashboard(w http.ResponseWriter, r *http.Request) {
	activities, err := s.queries.ListRecentWaActivity(r.Context(), 10)
	if err != nil {
		activities = []database.WaActivity{}
	}

	pending, err := s.queries.ListPendingConfirmations(r.Context())
	if err != nil {
		pending = []database.PendingConfirmation{}
	}

	status, _, _ := s.waMgr.GetStatus()
	uptime, showDisconnectAlert, disconnectedFor := s.waConnectionData()

	s.renderTemplate(w, "dashboard.html", map[string]interface{}{
		"Username":            r.Context().Value(UsernameContextKey),
		"Activities":          activities,
		"PendingApprovals":    pending,
		"Metrics":             s.waMetricsData(r.Context()),
		"Page":                "dashboard",
		"WAStatus":            string(status),
		"Uptime":              uptime,
		"ShowDisconnectAlert": showDisconnectAlert,
		"DisconnectedFor":     disconnectedFor,
		"CSRFToken":           s.ensureCSRFToken(w, r),
	})
}

func (s *Server) handleGetLogsPage(w http.ResponseWriter, r *http.Request) {
	s.renderTemplate(w, "logs.html", map[string]interface{}{
		"Username": r.Context().Value(UsernameContextKey),
		"Page":     "logs",
	})
}
