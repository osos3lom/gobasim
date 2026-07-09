package web

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"sawt-go/config"
	"sawt-go/database"
	"sawt-go/internal/audio"
	"sawt-go/internal/monitor"
	"sawt-go/internal/ratelimit"
	"sawt-go/internal/speech"
	"sawt-go/internal/voicenotes"
	waClient "sawt-go/internal/whatsmeow"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"rsc.io/qr"
)

// TemplatesFS holds embedded HTML template files.
//
//go:embed templates/*
var templatesFS embed.FS

// staticFS holds embedded compiled assets (Tailwind CSS build output).
// Regenerate web/static/app.css via `npm run build:css` after
// editing web/static/src/input.css or template class names.
//
//go:embed static/app.css
var staticFS embed.FS

type Server struct {
	cfg          *config.Config
	queries      *database.Queries
	auth         *AuthManager
	tmpl         *template.Template
	static       fs.FS
	logBroker    *LogBroker
	waMgr        *waClient.WhatsAppManager
	ttsOrch      *speech.TTSOrchestrator
	loginLimiter *ratelimit.Limiter
	voiceStore   *voicenotes.Store // optional; nil disables archival
}

// SetVoiceStore attaches the (optional) voice-note archival store so
// operator-sent voice notes are archived like automated replies. A nil store
// is valid — the constructor signature stays stable for existing call sites
// and tests.
func (s *Server) SetVoiceStore(store *voicenotes.Store) {
	s.voiceStore = store
}

// NewServer wires up the dashboard's dependencies. ttsOrch is the same
// instance main.go already constructs for the auto-reply pipeline — reused
// (not re-constructed) here so "send as voice note" shares one TTS provider
// fallback chain instead of double-initializing a second one.
func NewServer(cfg *config.Config, queries *database.Queries, waMgr *waClient.WhatsAppManager, ttsOrch *speech.TTSOrchestrator) *Server {
	// Parse embedded templates
	tmpl := template.Must(template.New("layout").ParseFS(templatesFS, "templates/*.html"))

	static, err := fs.Sub(staticFS, "static")
	if err != nil {
		log.Fatalf("web: failed to load embedded static assets: %v", err)
	}

	broker := NewLogBroker()
	go broker.Start()

	// Redirect log outputs to both stdout and our SSE broker
	multiWriter := io.MultiWriter(os.Stdout, broker)
	log.SetOutput(multiWriter)

	return &Server{
		cfg:          cfg,
		queries:      queries,
		auth:         NewAuthManager(cfg, queries),
		tmpl:         tmpl,
		static:       static,
		logBroker:    broker,
		waMgr:        waMgr,
		ttsOrch:      ttsOrch,
		loginLimiter: ratelimit.New(10, 5*time.Minute),
	}
}

// renderTemplate executes templates and checks/logs execution errors
func (s *Server) renderTemplate(w http.ResponseWriter, name string, data interface{}) {
	err := s.tmpl.ExecuteTemplate(w, name, data)
	if err != nil {
		log.Printf("Template execution error: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

// renderError renders the styled full-page error template. Only used for
// top-level navigation failures (404, 405, rate limiting) — HTMX fragment
// endpoints keep returning small inline HTML snippets since a full HTML
// document can't be swapped into a partial target.
func (s *Server) renderError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := s.tmpl.ExecuteTemplate(w, "error.html", map[string]interface{}{
		"Status":  status,
		"Message": message,
	}); err != nil {
		log.Printf("Template execution error: %v", err)
	}
}

func (s *Server) GetRouter() chi.Router {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(reportPanics)
	r.Use(middleware.Recoverer)
	r.Use(securityHeaders)

	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		s.renderError(w, http.StatusNotFound, "The page you're looking for doesn't exist.")
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		s.renderError(w, http.StatusMethodNotAllowed, "That method isn't allowed on this route.")
	})

	// Compiled Tailwind CSS + small JS helpers, embedded at build time.
	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.FS(s.static))))

	// Auth page routes
	r.Get("/login", s.handleGetLogin)
	r.With(s.requireCSRF).Post("/login", s.handlePostLogin)
	// Logout is state-changing, so it is POST + CSRF (D3). It stays outside
	// RequireAuth: clearing the cookie on an already-expired session must
	// still work rather than bounce through the login redirect.
	r.With(s.requireCSRF).Post("/logout", s.handleLogout)

	// Protected routes
	r.Group(func(r chi.Router) {
		r.Use(s.auth.RequireAuth)

		r.Get("/", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
		})
		r.Get("/dashboard", s.handleGetDashboard)
		r.Get("/dashboard/logs", s.handleGetLogsPage)
		r.Get("/dashboard/workflows", s.handleGetWorkflowsPage)
		r.With(s.requireCSRF).Post("/dashboard/workflows/update", s.handlePostUpdateWorkflow)
		r.With(s.requireCSRF).Post("/dashboard/workflows/create", s.handlePostCreateWorkflow)
		r.Get("/dashboard/whatsapp", s.handleGetWhatsAppPage)
		r.Get("/dashboard/whatsapp/status", s.handleGetWhatsAppStatus)
		r.With(s.requireCSRF).Post("/dashboard/whatsapp/pair-code", s.handlePostWhatsAppPairCode)
		r.With(s.requireCSRF).Post("/dashboard/whatsapp/logout", s.handlePostWhatsAppLogout)
		r.With(s.requireCSRF).Post("/dashboard/whatsapp/repair", s.handlePostWhatsAppRepair)
		r.Get("/dashboard/whatsapp/contacts", s.handleGetWaContactsFragment)
		r.With(s.requireCSRF).Post("/dashboard/whatsapp/contacts/{chatID}/toggle", s.handlePostToggleWaContact)
		r.With(s.requireCSRF).Post("/dashboard/whatsapp/contacts/{chatID}/agent", s.handlePostAssignWaContactAgent)
		r.Get("/dashboard/whatsapp/chats", s.handleGetWaChatsFragment)
		r.Get("/dashboard/whatsapp/chats/{chatID}/messages", s.handleGetWaMessagesFragment)
		r.With(s.requireCSRF).Post("/dashboard/whatsapp/chats/{chatID}/send-text", s.handlePostSendWaText)
		r.With(s.requireCSRF).Post("/dashboard/whatsapp/chats/{chatID}/send-voice", s.handlePostSendWaVoice)

		// SSE Log stream
		r.Get("/api/logs", s.handleSSELogs)
	})

	return r
}

func (s *Server) handleGetLogin(w http.ResponseWriter, r *http.Request) {
	s.renderTemplate(w, "login.html", map[string]interface{}{
		"Error":     "",
		"CSRFToken": s.ensureCSRFToken(w, r),
	})
}

// clientIP extracts the host portion of RemoteAddr (RealIP middleware has
// already resolved proxy headers upstream).
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) handlePostLogin(w http.ResponseWriter, r *http.Request) {
	if allowed, _ := s.loginLimiter.Allow(clientIP(r)); !allowed {
		s.renderError(w, http.StatusTooManyRequests, "Too many login attempts — try again in a few minutes.")
		return
	}

	username := r.FormValue("username")
	password := r.FormValue("password")

	cookie, err := s.auth.Login(r.Context(), username, password)
	if err != nil {
		s.renderTemplate(w, "login.html", map[string]interface{}{
			"Error":     err.Error(),
			"CSRFToken": s.ensureCSRFToken(w, r),
		})
		return
	}

	http.SetCookie(w, cookie)
	http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
	})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// disconnectAlertThreshold debounces the "WhatsApp disconnected" banner so a
// brief blip during a reconnect (common in dev, per the product spec) doesn't
// immediately alarm the operator — only a sustained drop surfaces the alert.
const disconnectAlertThreshold = 15 * time.Second

// formatDuration renders a time.Duration as a short "1h 5m" / "5m 12s" /
// "42s" string. html/template has no duration arithmetic, so this is done
// in Go and passed to templates as a plain string.
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

// waConnectionData computes the Connection-tab health fields shared by every
// handler that renders the WhatsApp card/page: uptime (while connected) and
// a debounced disconnect alert (while sustained-disconnected).
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

// qrDataURL encodes a WhatsApp pairing QR payload as a PNG data: URI.
// template.URL is required: html/template's URL sanitizer only passes
// http/https/mailto and would rewrite a bare data: URI to "#ZgotmplZ",
// breaking the <img src>. Typing it as template.URL bypasses that filter.
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

func (s *Server) handleGetDashboard(w http.ResponseWriter, r *http.Request) {
	// Fetch recent activities
	activities, err := s.queries.ListRecentWaActivity(r.Context(), 10)
	if err != nil {
		activities = []database.WaActivity{}
	}

	status, _, _ := s.waMgr.GetStatus()

	s.renderTemplate(w, "dashboard.html", map[string]interface{}{
		"Username":   r.Context().Value(UsernameContextKey),
		"Activities": activities,
		"Page":       "dashboard",
		"WAStatus":   string(status),
		"CSRFToken":  s.ensureCSRFToken(w, r),
	})
}

// renderWhatsAppCard renders the whatsapp_card partial (or the full page,
// via overrides["Page"]) with the current connection status plus any
// caller-specific overrides (a just-generated pairing code, a forced
// WAStatus, etc.) merged on top. Centralizes a data shape otherwise repeated
// across every handler that touches the pairing card.
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
		"Partial":             true,
		"CSRFToken":           s.ensureCSRFToken(w, r),
	}
	for k, v := range overrides {
		data[k] = v
	}
	s.renderTemplate(w, "whatsapp.html", data)
}

// waContactRow bundles a WaContact with the CSRFToken its forms need and the
// list of published agents its inline agent-picker offers. The row template
// is invoked two ways: nested inside a {{range}} (where $ still refers to
// the page root) and standalone as an HTMX fragment response (where a
// {{template}} call resets $ to whatever's passed in, not the page root) —
// bundling both fields onto the row itself keeps the template correct
// either way, with no reliance on $.
type waContactRow struct {
	database.WaContact
	CSRFToken       string
	PublishedAgents []database.Agent
}

// fetchPublishedAgents lists agents eligible for contact assignment. Only
// published agents are offered — a draft agent isn't ready to field real
// WhatsApp traffic.
func (s *Server) fetchPublishedAgents(ctx context.Context) []database.Agent {
	agents, err := s.queries.ListPublishedAgents(ctx)
	if err != nil {
		return []database.Agent{}
	}
	return agents
}

// fetchWaContactRows lists WhatsApp contacts, optionally filtered by a
// case-insensitive substring match on name or chat ID. Filtering in Go (not
// a SQL ILIKE query) is a deliberate simplicity choice at the expected scale
// for a single-operator deployment — revisit if contact counts grow large.
func (s *Server) fetchWaContactRows(ctx context.Context, query, csrfToken string) []waContactRow {
	contacts, err := s.queries.ListWaContacts(ctx)
	if err != nil {
		return []waContactRow{}
	}
	publishedAgents := s.fetchPublishedAgents(ctx)

	query = strings.ToLower(query)
	rows := make([]waContactRow, 0, len(contacts))
	for _, c := range contacts {
		if query != "" && !strings.Contains(strings.ToLower(c.Name), query) && !strings.Contains(strings.ToLower(c.ChatID), query) {
			continue
		}
		rows = append(rows, waContactRow{WaContact: c, CSRFToken: csrfToken, PublishedAgents: publishedAgents})
	}
	return rows
}

func (s *Server) handleGetWhatsAppPage(w http.ResponseWriter, r *http.Request) {
	token := s.ensureCSRFToken(w, r)
	chats, err := s.queries.ListWaChatsSummary(r.Context())
	if err != nil {
		chats = []database.WaChatSummary{}
	}
	s.renderWhatsAppCard(w, r, map[string]interface{}{
		"Username":  r.Context().Value(UsernameContextKey),
		"Page":      "whatsapp",
		"Partial":   false,
		"Contacts":  s.fetchWaContactRows(r.Context(), "", token),
		"Chats":     chats,
		"CSRFToken": token,
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

	// Fetch-then-reuse: CreateOrUpdateWaContact upserts all mutable fields
	// at once, so a "just flip enabled" action must preserve the fields it
	// isn't changing (same pattern as handlePostUpdateWorkflow's GetAgent).
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

	s.renderTemplate(w, "whatsapp.html", map[string]interface{}{
		"Row": waContactRow{
			WaContact:       updated,
			CSRFToken:       s.ensureCSRFToken(w, r),
			PublishedAgents: s.fetchPublishedAgents(r.Context()),
		},
		"PartialView": "contact_row",
		"Partial":     true,
	})
}

// handlePostAssignWaContactAgent sets (or clears) a contact's assigned agent
// and prompt override. Only published agents are accepted — if the operator
// somehow submits an unpublished/deleted agent id (e.g. a stale form after
// the agent was unpublished), the request is rejected rather than silently
// assigning an agent that isn't ready to serve traffic.
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

	s.renderTemplate(w, "whatsapp.html", map[string]interface{}{
		"Row": waContactRow{
			WaContact:       updated,
			CSRFToken:       s.ensureCSRFToken(w, r),
			PublishedAgents: s.fetchPublishedAgents(r.Context()),
		},
		"PartialView": "contact_row",
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

	// Normalize phone number: strip out all non-digit characters
	var sb strings.Builder
	for _, char := range phone {
		if char >= '0' && char <= '9' {
			sb.WriteRune(char)
		}
	}
	phone = sb.String()

	// Validate phone number length: must be between 8 and 15 digits
	if len(phone) < 8 || len(phone) > 15 {
		http.Error(w, "Invalid phone number length (must be 8-15 digits)", http.StatusBadRequest)
		return
	}

	prettyCode, err := s.waMgr.RequestPairingCode(phone)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	s.renderWhatsAppCard(w, r, map[string]interface{}{
		"WAStatus": "pairing_ready",
		"WAQR":     "",
		"WAPair":   prettyCode,
	})
}

// handlePostWhatsAppLogout unlinks the current WhatsApp device. Destructive —
// the dashboard requires a confirmation dialog before submitting this.
func (s *Server) handlePostWhatsAppLogout(w http.ResponseWriter, r *http.Request) {
	if err := s.waMgr.Logout(r.Context()); err != nil {
		w.Write([]byte(fmt.Sprintf("<div class='bg-red-900 border border-red-500 text-red-200 px-4 py-3 rounded'>Logout failed: %v</div>", err)))
		return
	}
	s.renderWhatsAppCard(w, r, nil)
}

// handlePostWhatsAppRepair starts a fresh pairing session ("Generate new
// QR"), for after a logout or after a QR session's rotated codes all
// time out and the channel closes.
func (s *Server) handlePostWhatsAppRepair(w http.ResponseWriter, r *http.Request) {
	qrChan, err := s.waMgr.RearmQR(r.Context())
	if err != nil {
		w.Write([]byte(fmt.Sprintf("<div class='bg-red-900 border border-red-500 text-red-200 px-4 py-3 rounded'>Could not start a new pairing session: %v</div>", err)))
		return
	}
	if err := s.waMgr.Connect(r.Context()); err != nil {
		w.Write([]byte(fmt.Sprintf("<div class='bg-red-900 border border-red-500 text-red-200 px-4 py-3 rounded'>Failed to reconnect: %v</div>", err)))
		return
	}
	// A background context: the request's context is canceled once this
	// handler returns, but the QR stream must keep running for the ~2
	// minutes it takes whatsmeow to exhaust all of its rotated codes.
	go s.waMgr.StreamQRToState(context.Background(), qrChan, nil)

	s.renderWhatsAppCard(w, r, nil)
}

// waMessagesThreadLimit caps how many messages load per page in the chat
// thread view (cursor-paginated via ?before_seq on subsequent loads).
const waMessagesThreadLimit = 50

func (s *Server) handleGetWaChatsFragment(w http.ResponseWriter, r *http.Request) {
	chats, err := s.queries.ListWaChatsSummary(r.Context())
	if err != nil {
		chats = []database.WaChatSummary{}
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
	// ListWaMessagesByChat returns newest-first (for cursor pagination);
	// reverse to oldest-first for natural top-to-bottom thread rendering.
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}

	s.renderTemplate(w, "whatsapp.html", map[string]interface{}{
		"ChatID":      chatID,
		"Messages":    messages,
		"CSRFToken":   s.ensureCSRFToken(w, r),
		"PartialView": "messages",
		"Partial":     true,
	})
}

// handlePostSendWaText sends a manual text message from the dashboard,
// logging it to wa_messages with sender "operator" (distinguishing it from
// the bot's automated replies) regardless of send success/failure, so a
// failed send is still visible in the thread rather than silently dropped.
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

// handlePostSendWaVoice synthesizes the given text to speech and sends it as
// a WhatsApp voice note — reusing the same TTS + WavToOpus pipeline main.go
// already uses for automated audio replies, just triggered by an operator
// action instead of an inbound voice message.
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
			// Archive the operator-sent voice note (nil store no-ops).
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

func (s *Server) handleGetLogsPage(w http.ResponseWriter, r *http.Request) {
	s.renderTemplate(w, "logs.html", map[string]interface{}{
		"Username": r.Context().Value(UsernameContextKey),
		"Page":     "logs",
	})
}

func (s *Server) handleGetWorkflowsPage(w http.ResponseWriter, r *http.Request) {
	s.renderWorkflowsPage(w, r, "")
}

// renderWorkflowsPage re-fetches the agent list and renders workflow.html,
// optionally surfacing errMsg in the page's #feedback banner. Shared by the
// GET page load and the create-agent handler's validation/failure paths so
// a failed create still shows the (unchanged) agent list plus the reason.
// agentRow decorates an Agent with the derived "unpublished changes" flag
// (published agents whose config was edited after the last publish, D4).
type agentRow struct {
	database.Agent
	HasUnpublishedChanges bool
}

func (s *Server) renderWorkflowsPage(w http.ResponseWriter, r *http.Request, errMsg string) {
	agents, err := s.queries.ListAgents(r.Context())
	if err != nil {
		agents = []database.Agent{}
	}

	rows := make([]agentRow, 0, len(agents))
	for _, a := range agents {
		unpublished := a.Status == "published" &&
			(a.LastPublished == nil || a.LastEdited.After(*a.LastPublished))
		rows = append(rows, agentRow{Agent: a, HasUnpublishedChanges: unpublished})
	}

	s.renderTemplate(w, "workflow.html", map[string]interface{}{
		"Username":  r.Context().Value(UsernameContextKey),
		"Agents":    rows,
		"Page":      "workflows",
		"CSRFToken": s.ensureCSRFToken(w, r),
		"Error":     errMsg,
	})
}

// handlePostCreateWorkflow creates a new agent from just a name, leaving
// every other agents-table column on its schema.sql default (system prompt,
// greeting/failure messages, etc. are then edited in place via the existing
// per-agent update form).
func (s *Server) handlePostCreateWorkflow(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		s.renderWorkflowsPage(w, r, "Agent name is required.")
		return
	}

	idBytes := make([]byte, 8)
	if _, err := rand.Read(idBytes); err != nil {
		http.Error(w, "Failed to generate agent id", http.StatusInternalServerError)
		return
	}
	id := "agent_" + hex.EncodeToString(idBytes)

	_, err := s.queries.CreateAgent(r.Context(), database.CreateAgentParams{
		ID:            id,
		Name:          name,
		ProjectID:     "Default Project (****75e1)",
		HostingRegion: "Europe",
		Status:        "draft",
		Template:      "Blank Template",
		ModelType:     "asr-llm-tts",
		Asr:           []byte(`{"vendor":"deepgram","model":"nova-3","language":"en"}`),
		Llm:           []byte(`{"vendor":"openai","url":"https://api.openai.com/v1/chat/completions","model":"gpt-4o-mini"}`),
		Tts:           []byte(`{"vendor":"minimax","model":"speech-2.8-turbo","voice":"Radiant Girl"}`),
		TurnDetection: true,
		StartOfSpeech: true,
		EndOfSpeech:   true,
		MaxHistory:    10,
		McpServers:    []byte(`[]`),
		Skills:        []byte(`[]`),
	})
	if err != nil {
		s.renderWorkflowsPage(w, r, fmt.Sprintf("Failed to create agent: %v", err))
		return
	}

	http.Redirect(w, r, "/dashboard/workflows", http.StatusSeeOther)
}

// validatePublishTransition enforces the D4 publish gate: an agent may only
// move (or stay) published when its system prompt is non-empty. Returns a
// human-readable reason when publishing must be blocked, or "" when allowed.
func validatePublishTransition(requestedStatus, systemPrompt string) string {
	if requestedStatus != "published" {
		return ""
	}
	if strings.TrimSpace(systemPrompt) == "" {
		return "Cannot publish: the system prompt is empty. Write the agent's instructions first."
	}
	return ""
}

func (s *Server) handlePostUpdateWorkflow(w http.ResponseWriter, r *http.Request) {
	agentID := r.FormValue("id")
	name := r.FormValue("name")
	prompt := r.FormValue("system_prompt")
	greeting := r.FormValue("greeting_message")
	failure := r.FormValue("failure_message")
	requestedStatus := r.FormValue("status")

	// Get current agent to preserve json parameters
	agent, err := s.queries.GetAgent(r.Context(), agentID)
	if err != nil {
		http.Error(w, "Agent not found", http.StatusNotFound)
		return
	}

	// Status is optional for backward compatibility: an absent field keeps
	// the current status. Anything other than draft/published is rejected.
	switch requestedStatus {
	case "":
		requestedStatus = agent.Status
	case "draft", "published":
	default:
		w.Write([]byte("<div class='bg-red-900 border border-red-500 text-red-200 px-4 py-3 rounded'>Invalid status value.</div>"))
		return
	}

	if reason := validatePublishTransition(requestedStatus, prompt); reason != "" {
		w.Write([]byte(fmt.Sprintf("<div class='bg-red-900 border border-red-500 text-red-200 px-4 py-3 rounded'>%s</div>", template.HTMLEscapeString(reason))))
		return
	}

	arg := database.UpdateAgentWorkflowParams{
		ID:              agentID,
		Name:            name,
		SystemPrompt:    prompt,
		GreetingMessage: greeting,
		FailureMessage:  failure,
		Asr:             agent.Asr,
		Llm:             agent.Llm,
		Tts:             agent.Tts,
		MaxHistory:      agent.MaxHistory,
		Status:          requestedStatus,
	}

	_, err = s.queries.UpdateAgentWorkflow(r.Context(), arg)
	if err != nil {
		w.Write([]byte(fmt.Sprintf("<div class='bg-red-900 border border-red-500 text-red-200 px-4 py-3 rounded'>Failed: %v</div>", err)))
		return
	}

	w.Write([]byte("<div class='bg-emerald-900 border border-emerald-500 text-emerald-200 px-4 py-3 rounded'>Workflow updated successfully!</div>"))
}

func (s *Server) handleSSELogs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	logChan := make(chan string, 50)
	s.logBroker.Subscribe(logChan)
	defer s.logBroker.Unsubscribe(logChan)

	log.Println("Dashboard: SSE Client connected to logs channel.")

	for {
		select {
		case line := <-logChan:
			fmt.Fprintf(w, "data: %s\n\n", line)
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// securityHeaders sets a Content-Security-Policy and standard hardening
// headers on every response. script-src is restricted to 'self' plus the
// pinned, SRI-verified HTMX CDN origin. The QR code is a server-rendered PNG
// data: URI (see img-src), so no client-side QR library/CDN is needed.
// style-src needs 'unsafe-inline' because Google Fonts injects an inline
// stylesheet.
func securityHeaders(next http.Handler) http.Handler {
	const csp = "default-src 'self'; " +
		"script-src 'self' https://unpkg.com; " +
		"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; " +
		"font-src https://fonts.gstatic.com; " +
		"img-src 'self' data:; " +
		"connect-src 'self'; " +
		"frame-ancestors 'none'; " +
		"base-uri 'self'; " +
		"object-src 'none'"

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		next.ServeHTTP(w, r)
	})
}

// reportPanics forwards HTTP handler panics to the error monitor, then
// re-panics so chi's Recoverer still produces the 500 response.
func reportPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil && rec != http.ErrAbortHandler {
				monitor.ReportPanic("web:"+r.URL.Path, rec)
				panic(rec)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// LogBroker implements a simple thread-safe broker to stream logs.
type LogBroker struct {
	subscribers map[chan string]bool
	publish     chan string
	subscribe   chan chan string
	unsubscribe chan chan string
	mu          sync.RWMutex
}

func NewLogBroker() *LogBroker {
	return &LogBroker{
		subscribers: make(map[chan string]bool),
		publish:     make(chan string, 100),
		subscribe:   make(chan chan string),
		unsubscribe: make(chan chan string),
	}
}

func (b *LogBroker) Start() {
	for {
		select {
		case ch := <-b.subscribe:
			b.mu.Lock()
			b.subscribers[ch] = true
			b.mu.Unlock()
		case ch := <-b.unsubscribe:
			b.mu.Lock()
			delete(b.subscribers, ch)
			close(ch)
			b.mu.Unlock()
		case val := <-b.publish:
			b.mu.RLock()
			for ch := range b.subscribers {
				select {
				case ch <- val:
				default:
					// Drop log line if client is reading too slowly
				}
			}
			b.mu.RUnlock()
		}
	}
}

func (b *LogBroker) Subscribe(ch chan string) {
	b.subscribe <- ch
}

func (b *LogBroker) Unsubscribe(ch chan string) {
	b.unsubscribe <- ch
}

// Write satisfies io.Writer to pipe log prints directly into the publish channel.
func (b *LogBroker) Write(p []byte) (n int, err error) {
	line := string(p)
	select {
	case b.publish <- line:
	default:
		// Drop log lines if channel is full to prevent daemon deadlock
	}
	return len(p), nil
}
