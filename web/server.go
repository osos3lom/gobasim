package web

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"runtime"
	"sawt-go/config"
	"sawt-go/database"
	"sawt-go/internal/agentcfg"
	"sawt-go/internal/audio"
	"sawt-go/internal/erp"
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

// staticFS holds embedded compiled assets (Tailwind CSS build output) plus the
// small CSP-clean helper script for the workflows list editors.
// Regenerate web/static/app.css via `npm run build:css` after
// editing web/static/src/input.css or template class names.
//
//go:embed static/app.css static/workflow.js static/whatsapp.js
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
	db           dbPinger          // optional; drives the /readyz DB probe
	erpClient    *erp.Client
}

// dbPinger is the narrow slice of *pgxpool.Pool the readiness probe needs.
type dbPinger interface {
	Ping(context.Context) error
}

// SetVoiceStore attaches the (optional) voice-note archival store so
// operator-sent voice notes are archived like automated replies. A nil store
// is valid — the constructor signature stays stable for existing call sites
// and tests.
func (s *Server) SetVoiceStore(store *voicenotes.Store) {
	s.voiceStore = store
}

// SetDB attaches a DB handle for the /readyz readiness probe. Optional and
// nil-safe (keeps NewServer's signature stable for tests/harness).
func (s *Server) SetDB(p dbPinger) {
	s.db = p
}

// SetERPClient sets the ERP client for identity resolution.
func (s *Server) SetERPClient(client *erp.Client) {
	s.erpClient = client
}

// BlueprintDefaults holds system default settings for new contacts.
type BlueprintDefaults struct {
	DefaultAgentID        string `json:"agent_id"`
	DefaultPromptOverride string `json:"prompt_override"`
	AutoEnable            bool   `json:"auto_enable"`
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

	// Structured logging (C4): a slog logger — text, or JSON when LOG_FORMAT=json
	// — writes to both stdout and the SSE broker so the dashboard log stream keeps
	// working. The stdlib logger (used across the app + libraries) is bridged
	// through slog at INFO, so every line is leveled/structured, not just new ones.
	multiWriter := io.MultiWriter(os.Stdout, broker)
	configureLogging(multiWriter)

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

// configureLogging installs a slog default logger (text, or JSON when
// LOG_FORMAT=json) writing to w, and bridges the stdlib logger through it so
// existing log.Printf output is captured with a level + structure (C4).
func configureLogging(w io.Writer) {
	opts := &slog.HandlerOptions{Level: slog.LevelInfo}
	var h slog.Handler
	if strings.EqualFold(os.Getenv("LOG_FORMAT"), "json") {
		h = slog.NewJSONHandler(w, opts)
	} else {
		h = slog.NewTextHandler(w, opts)
	}
	slog.SetDefault(slog.New(h))
	log.SetFlags(0)
	log.SetOutput(slogBridge{})
}

// slogBridge routes stdlib log output through slog at INFO. slog writes to the
// configured sink (stdout + SSE broker), never back to the stdlib logger, so
// there is no recursion.
type slogBridge struct{}

func (slogBridge) Write(p []byte) (int, error) {
	slog.Default().Info(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

// processStart anchors the /metrics uptime counter.
var processStart = time.Now()

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// handleHealthz is an unauthenticated liveness probe: 200 while the process is
// serving. It does no I/O, so it stays green even if the DB is down.
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{"status": "ok"})
}

// handleReadyz is an unauthenticated readiness probe: it pings the DB and
// reports WhatsApp link state, returning 503 when the DB is unreachable (C3).
func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	dbOK := true
	if s.db != nil {
		if err := s.db.Ping(ctx); err != nil {
			dbOK = false
		}
	}
	waState, _, _ := s.waMgr.GetStatus()

	status := http.StatusOK
	if !dbOK {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, map[string]interface{}{
		"ready":    dbOK,
		"db":       dbOK,
		"whatsapp": string(waState),
	})
}

// handleMetrics is an unauthenticated, low-cardinality JSON metrics snapshot
// (no new dependency). It exposes only non-sensitive process/pipeline counters.
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	waState, _, _ := s.waMgr.GetStatus()
	vnUploaded, vnFailed := s.voiceStore.Stats() // nil-safe: returns 0,0
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"uptime_seconds":       int(time.Since(processStart).Seconds()),
		"goroutines":           runtime.NumGoroutine(),
		"whatsapp_state":       string(waState),
		"voice_notes_uploaded": vnUploaded,
		"voice_notes_failed":   vnFailed,
	})
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
	r.Use(capturePeerIP) // must precede RealIP: records the true TCP peer (C5)
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

	// Unauthenticated health/metrics probes (C3) for uptime checks + LBs.
	r.Get("/healthz", s.handleHealthz)
	r.Get("/readyz", s.handleReadyz)
	r.Get("/metrics", s.handleMetrics)

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
		r.With(s.requireCSRF).Post("/dashboard/whatsapp/contacts/{chatID}/erp-override", s.handlePostSetWaContactErpOverride)
		r.With(s.requireCSRF).Post("/dashboard/whatsapp/contacts/{chatID}/resolve-identity", s.handlePostResolveWaContactIdentity)
		r.With(s.requireCSRF).Post("/dashboard/whatsapp/settings", s.handlePostWhatsAppSettings)
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

type peerIPKeyType struct{}

var peerIPKey peerIPKeyType

// capturePeerIP records the real TCP peer address into the request context
// before middleware.RealIP overwrites RemoteAddr from X-Forwarded-For. The login
// limiter keys on this real peer so a spoofed X-Forwarded-For can't mint an
// unlimited supply of fresh login buckets (C5).
func capturePeerIP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), peerIPKey, host)))
	})
}

// peerIP returns the real TCP peer captured by capturePeerIP, falling back to
// clientIP (the RealIP-resolved address) when unavailable.
func peerIP(r *http.Request) string {
	if v, ok := r.Context().Value(peerIPKey).(string); ok && v != "" {
		return v
	}
	return clientIP(r)
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
	CSRFToken          string
	PublishedAgents    []database.Agent
	ErpUnresolvedLabel string
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
		rows = append(rows, waContactRow{
			WaContact:          c,
			CSRFToken:          csrfToken,
			PublishedAgents:    publishedAgents,
			ErpUnresolvedLabel: erpUnresolvedLabel(c.ErpUnresolvedReason),
		})
	}
	return rows
}

// agentIDValue dereferences a nullable assigned-agent id into a plain string
// ("" when unset). The pilot panel's brain <select> compares this against each
// option's id to mark the current selection; html/template can't compare a
// *string against a string option value directly, so the pilot render maps must
// pass the dereferenced value as "AgentIDStr".
func agentIDValue(id *string) string {
	if id == nil {
		return ""
	}
	return *id
}

// erpUnresolvedLabel turns a wa_contacts.erp_unresolved_reason value into an
// operator-facing label. Same *string-vs-string issue as agentIDValue: the
// template can't do {{eq .ErpUnresolvedReason "no_match"}} directly, so the
// comparison happens here and the result is passed in already as a string.
func erpUnresolvedLabel(reason *string) string {
	if reason == nil {
		return "not yet resolved"
	}
	switch *reason {
	case erp.UnresolvedPhoneUnverified:
		return "phone unverified"
	case erp.UnresolvedNoMatch:
		return "no ERP match"
	default:
		return *reason
	}
}

func (s *Server) handleGetWhatsAppPage(w http.ResponseWriter, r *http.Request) {
	token := s.ensureCSRFToken(w, r)
	// Chat identity (ContactErpRole/DisplayName/OrgID/UnresolvedReason) comes
	// straight off the persisted wa_contacts columns via the join in
	// ListWaChatsSummary — no live ERP call on page render. Identity is kept
	// current by the inbound message handler and a contact's "Resolve now"
	// action, not by re-resolving on every dashboard load.
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

	view := r.FormValue("view")
	if view == "pilot" {
		// ERP identity (WaContact.Erp*) is read straight off the row — no
		// live ERP call here; that's the "Resolve now" action's job.
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

// renderWaContactAfterErpAction re-renders a contact after an ERP
// link/override action, in either the pilot-panel or contact-row shape
// (matching handlePostToggleWaContact / handlePostAssignWaContactAgent).
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

// handlePostSetWaContactErpOverride sets (or, given an empty value, clears)
// the phone number an operator wants used for ERP identity resolution
// instead of the one derived from the WhatsApp chat_id — for contacts whose
// WhatsApp number doesn't match what's registered in the ERP. It then
// immediately re-resolves so the operator sees the effect in one step.
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
		if _, err := erp.ResolveAndPersistContactIdentity(r.Context(), s.erpClient, s.queries, chatID, contact.ErpPhoneOverride); err != nil {
			monitor.ReportError(r.Context(), "identity", err)
		} else if refreshed, err := s.queries.GetWaContact(r.Context(), chatID); err == nil {
			contact = refreshed
		}
	}

	s.renderWaContactAfterErpAction(w, r, contact)
}

// handlePostResolveWaContactIdentity re-resolves a contact's ERP identity on
// demand ("Resolve now"), using erp_phone_override when set. Unlike the
// dashboard's read paths, this always makes a live ERP call.
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

	if _, err := erp.ResolveAndPersistContactIdentity(r.Context(), s.erpClient, s.queries, chatID, contact.ErpPhoneOverride); err != nil {
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
	// ListWaMessagesByChat returns newest-first (for cursor pagination);
	// reverse to oldest-first for natural top-to-bottom thread rendering.
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}

	// We only load the full layout when not paginating historical messages
	if beforeSeq > 0 {
		s.renderTemplate(w, "whatsapp.html", map[string]interface{}{
			"ChatID":      chatID,
			"Messages":    messages,
			"CSRFToken":   s.ensureCSRFToken(w, r),
			"PartialView": "messages",
			"Partial":     true,
		})
		return
	}

	contact, err := s.queries.GetWaContact(r.Context(), chatID)
	if err != nil {
		contact = database.WaContact{
			ChatID: chatID,
		}
	}

	// The 24h SLA window is derived from the newest inbound message in the
	// loaded page (waMessagesThreadLimit). If more than that many bot/operator
	// messages have been sent since the last inbound, no inbound appears in the
	// page and the window is reported "Closed" even if it's technically still
	// open — an acceptable edge at current scale; a dedicated "latest inbound
	// timestamp" query would make it exact.
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
		"ChatID":             chatID,
		"Messages":           messages,
		"CSRFToken":          s.ensureCSRFToken(w, r),
		"WindowOpen":         windowOpen,
		"WindowClosesIn":     windowClosesIn,
		"WaContact":          contact,
		"PublishedAgents":    s.fetchPublishedAgents(r.Context()),
		"AgentIDStr":         agentIDValue(contact.AgentID),
		"ErpUnresolvedLabel": erpUnresolvedLabel(contact.ErpUnresolvedReason),
		"PartialView":        "thread_pilot",
		"Partial":            true,
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
// (published agents whose config was edited after the last publish, D4) and the
// parsed four-block config the template renders as typed form fields. LLM/TTS/
// SubAgents render as structured inputs; the skills and MCP-server lists are
// handed to the client-side list editor as JSON (see web/static/workflow.js).
type agentRow struct {
	database.Agent
	HasUnpublishedChanges          bool
	LLM                            agentcfg.LLM
	TTS                            agentcfg.TTS
	SubAgents                      agentcfg.SubAgents
	SkillsJSON                     string
	MCPJSON                        string
	ClarificationRules             agentcfg.ClarificationRules
	ClarificationToolOverridesJSON string
	ClarificationDeriveRulesJSON   string
}

// KnownDelegateAgents lists the intent specs a sub-agent delegation may target,
// rendered as checkboxes in the capabilities block.
var KnownDelegateAgents = []string{"operations", "accounting", "administration", "sales", "breeding", "client"}

// DelegateChecked reports whether a delegate agent is in this row's allow-list,
// so the template can pre-check its box. Membership is shown regardless of the
// enabled flag, so an operator sees the saved allow-list even while delegation
// is toggled off.
func (a agentRow) DelegateChecked(name string) bool {
	return contains(a.SubAgents.AllowedAgents, name)
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
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
		// Parse errors here are non-fatal for rendering: fall back to defaults so
		// a legacy/placeholder blob still shows editable fields the operator can fix.
		llm, err := agentcfg.ParseLLM(a.Llm)
		if err != nil {
			llm = agentcfg.DefaultLLM()
		}
		tts, err := agentcfg.ParseTTS(a.Tts)
		if err != nil {
			tts = agentcfg.DefaultTTS()
		}
		sub, err := agentcfg.ParseSubAgents(a.SubAgents)
		if err != nil {
			sub = agentcfg.DefaultSubAgents()
		}
		cr, err := agentcfg.ParseClarificationRules(a.ClarificationRules)
		if err != nil {
			cr = agentcfg.DefaultClarificationRules()
		}
		toolOverridesJSON, _ := json.Marshal(cr.ToolOverrides)
		deriveRulesJSON, _ := json.Marshal(cr.DeriveRules)
		rows = append(rows, agentRow{
			Agent:                          a,
			HasUnpublishedChanges:          unpublished,
			LLM:                            llm,
			TTS:                            tts,
			SubAgents:                      sub,
			SkillsJSON:                     jsonOrDefault(a.Skills, "[]"),
			MCPJSON:                        jsonOrDefault(a.McpServers, "[]"),
			ClarificationRules:             cr,
			ClarificationToolOverridesJSON: jsonOrDefault(toolOverridesJSON, "[]"),
			ClarificationDeriveRulesJSON:   jsonOrDefault(deriveRulesJSON, "[]"),
		})
	}

	s.renderTemplate(w, "workflow.html", map[string]interface{}{
		"Username":       r.Context().Value(UsernameContextKey),
		"Agents":         rows,
		"Page":           "workflows",
		"CSRFToken":      s.ensureCSRFToken(w, r),
		"Error":          errMsg,
		"DelegateAgents": KnownDelegateAgents,
		"HistoryDefault": agentcfg.DefaultHistory,
		"HistoryMax":     agentcfg.MaxHistory,
		"SubTokensMax":   agentcfg.MaxSubAgentTokens,
	})
}

// jsonOrDefault returns raw JSON bytes as a string, substituting a fallback when
// the column is empty, so the client editor always receives parseable JSON.
func jsonOrDefault(raw []byte, fallback string) string {
	if len(raw) == 0 {
		return fallback
	}
	return string(raw)
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
		// Seed with real, provider-aligned defaults (agentcfg) rather than the
		// former placeholder blobs (minimax / "Radiant Girl") that matched no
		// implemented provider.
		Asr:                agentcfg.DefaultASR().Marshal(),
		Llm:                agentcfg.DefaultLLM().Marshal(),
		Tts:                agentcfg.DefaultTTS().Marshal(),
		TurnDetection:      true,
		StartOfSpeech:      true,
		EndOfSpeech:        true,
		MaxHistory:         int32(agentcfg.DefaultHistory),
		McpServers:         []byte(`[]`),
		Skills:             []byte(`[]`),
		SubAgents:          agentcfg.DefaultSubAgents().Marshal(),
		ClarificationRules: agentcfg.DefaultClarificationRules().Marshal(),
	})
	if err != nil {
		log.Printf("web: failed to create agent %q: %v", name, err)
		s.renderWorkflowsPage(w, r, "Failed to create the agent. Please try again.")
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

// feedbackErr writes the red HTMX #feedback snippet with an escaped reason.
func feedbackErr(w http.ResponseWriter, reason string) {
	w.Write([]byte(fmt.Sprintf("<div class='bg-red-900 border border-red-500 text-red-200 px-4 py-3 rounded'>%s</div>", template.HTMLEscapeString(reason))))
}

// formBool reads an HTML checkbox: present (any non-empty value) means checked.
func formBool(r *http.Request, name string) bool {
	return r.FormValue(name) != ""
}

func (s *Server) handlePostUpdateWorkflow(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		feedbackErr(w, "Malformed form submission.")
		return
	}
	agentID := r.FormValue("id")

	// Fetch the current row: ASR is not one of the four editable blocks, so it is
	// preserved as-is, and the status default falls back to the stored value.
	agent, err := s.queries.GetAgent(r.Context(), agentID)
	if err != nil {
		http.Error(w, "Agent not found", http.StatusNotFound)
		return
	}

	arg, reason := s.buildUpdateParams(r, agent)
	if reason != "" {
		feedbackErr(w, reason)
		return
	}

	if _, err = s.queries.UpdateAgentWorkflow(r.Context(), arg); err != nil {
		log.Printf("web: failed to update agent workflow %q: %v", agentID, err)
		feedbackErr(w, "Failed to update the workflow. Please try again.")
		return
	}

	w.Write([]byte("<div class='bg-emerald-900 border border-emerald-500 text-emerald-200 px-4 py-3 rounded'>Workflow updated successfully!</div>"))
}

// buildUpdateParams parses and validates the four-block workflow form into a
// full UpdateAgentWorkflowParams. It returns a human-readable reason (for the
// #feedback banner) instead of an error when validation fails. Every editable
// column is set explicitly — the UPDATE now writes the JSONB/boolean columns, so
// omitting any would destructively overwrite it with a zero value.
func (s *Server) buildUpdateParams(r *http.Request, agent database.Agent) (database.UpdateAgentWorkflowParams, string) {
	prompt := r.FormValue("system_prompt")

	// Name is editable but must not be blanked; fall back to the stored value.
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		name = agent.Name
	}

	// Status: absent keeps current; only draft/published are accepted.
	status := r.FormValue("status")
	switch status {
	case "":
		status = agent.Status
	case "draft", "published":
	default:
		return database.UpdateAgentWorkflowParams{}, "Invalid status value."
	}
	if reason := validatePublishTransition(status, prompt); reason != "" {
		return database.UpdateAgentWorkflowParams{}, reason
	}

	// Block 1 — LLM Brain & Telemetry.
	llm := agentcfg.LLM{
		Vendor:    r.FormValue("llm_vendor"),
		URL:       r.FormValue("llm_url"),
		APIKeyEnv: r.FormValue("llm_api_key_env"),
		Model:     r.FormValue("llm_model"),
	}
	if err := llm.Validate(); err != nil {
		return database.UpdateAgentWorkflowParams{}, err.Error()
	}
	maxHistory := agentcfg.ClampHistory(atoiDefault(r.FormValue("max_history"), agentcfg.DefaultHistory))

	// Block 4 — Aegis Audio & Speech (TTS).
	tts := agentcfg.TTS{
		Vendor:       r.FormValue("tts_vendor"),
		LanguageCode: r.FormValue("tts_language_code"),
		VoiceName:    r.FormValue("tts_voice_name"),
		Gender:       r.FormValue("tts_gender"),
		Model:        r.FormValue("tts_model"),
		Speed:        float32(atofDefault(r.FormValue("tts_speed"), 1.0)),
	}
	if err := tts.Validate(); err != nil {
		return database.UpdateAgentWorkflowParams{}, err.Error()
	}

	// Block 3 — Capabilities. Skills and MCP servers arrive as JSON from the
	// client list editor; sub-agents from discrete inputs. Private MCP hosts are
	// allowed only outside production (SecureCookie is the production signal).
	allowPrivate := !s.cfg.SecureCookie
	mcpServers, err := agentcfg.ParseMCPServers([]byte(emptyToArray(r.FormValue("mcp_servers"))), allowPrivate)
	if err != nil {
		return database.UpdateAgentWorkflowParams{}, err.Error()
	}
	skills, err := agentcfg.ParseSkills([]byte(emptyToArray(r.FormValue("skills"))))
	if err != nil {
		return database.UpdateAgentWorkflowParams{}, err.Error()
	}
	sub := agentcfg.SubAgents{
		Enabled:       formBool(r, "sub_agents_enabled"),
		MaxTokens:     atoiDefault(r.FormValue("sub_agents_max_tokens"), 0),
		AllowedAgents: r.Form["sub_agents_allowed"],
	}
	if err := sub.Validate(); err != nil {
		return database.UpdateAgentWorkflowParams{}, err.Error()
	}

	// Block 5 — Clarification & Auto-Fill Rules. The two list editors submit
	// JSON the same way skills/mcp_servers do; the agent-wide toggle is an
	// ordinary checkbox.
	var toolOverrides []agentcfg.ToolClarificationOverride
	if err := json.Unmarshal([]byte(emptyToArray(r.FormValue("clarification_tool_overrides"))), &toolOverrides); err != nil {
		return database.UpdateAgentWorkflowParams{}, "Invalid clarification tool-override data."
	}
	var deriveRules []agentcfg.DeriveRuleConfig
	if err := json.Unmarshal([]byte(emptyToArray(r.FormValue("clarification_derive_rules"))), &deriveRules); err != nil {
		return database.UpdateAgentWorkflowParams{}, "Invalid clarification derive-rule data."
	}
	clarificationEnabled := formBool(r, "clarification_enabled")
	cr := agentcfg.ClarificationRules{
		Enabled:       &clarificationEnabled,
		ToolOverrides: toolOverrides,
		DeriveRules:   deriveRules,
	}
	if err := cr.Validate(); err != nil {
		return database.UpdateAgentWorkflowParams{}, err.Error()
	}

	return database.UpdateAgentWorkflowParams{
		ID:                        r.FormValue("id"),
		Name:                      name,
		SystemPrompt:              prompt,
		GreetingMessage:           r.FormValue("greeting_message"),
		FailureMessage:            r.FormValue("failure_message"),
		Asr:                       agent.Asr, // not an editable block; preserved
		Llm:                       llm.Marshal(),
		Tts:                       tts.Marshal(),
		MaxHistory:                int32(maxHistory),
		Status:                    status,
		McpServers:                agentcfg.MarshalMCPServers(mcpServers),
		Skills:                    agentcfg.MarshalSkills(skills),
		SubAgents:                 sub.Marshal(),
		TurnDetection:             formBool(r, "turn_detection"),
		StartOfSpeech:             formBool(r, "start_of_speech"),
		EndOfSpeech:               formBool(r, "end_of_speech"),
		SelectiveAttentionLocking: formBool(r, "selective_attention_locking"),
		FillerWords:               formBool(r, "filler_words"),
		ClarificationRules:        cr.Marshal(),
	}, ""
}

// emptyToArray substitutes an empty JSON array for a blank field so the agentcfg
// list parsers always receive valid JSON.
func emptyToArray(s string) string {
	if strings.TrimSpace(s) == "" {
		return "[]"
	}
	return s
}

func atoiDefault(s string, def int) int {
	if v, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return v
	}
	return def
}

func atofDefault(s string, def float64) float64 {
	if v, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
		return v
	}
	return def
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

func (s *Server) handlePostWhatsAppSettings(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		feedbackErr(w, "Malformed form submission.")
		return
	}

	agentID := strings.TrimSpace(r.FormValue("agent_id"))
	promptOverride := r.FormValue("prompt_override")
	autoEnable := r.FormValue("auto_enable") == "on"

	// Fetch settings to preserve other columns
	settings, err := s.queries.GetSettings(r.Context())
	if err != nil {
		feedbackErr(w, "Failed to load current settings.")
		return
	}

	// Validate agent_id is empty or exists and is published
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

	// Marshal blueprint defaults into JSON
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

	w.Write([]byte("<div class='bg-emerald-900 border border-emerald-500 text-emerald-200 px-4 py-3 rounded'>System defaults saved successfully!</div>"))
}
