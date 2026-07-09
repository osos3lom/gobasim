package web

import (
	"crypto/hmac"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"sawt-go/config"
	"sawt-go/database"
	"sawt-go/internal/monitor"
	"sawt-go/internal/ratelimit"
	waClient "sawt-go/internal/whatsmeow"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	googleProto "google.golang.org/protobuf/proto"
)

// TemplatesFS holds embedded HTML template files.
//
//go:embed templates/*
var templatesFS embed.FS

type Server struct {
	cfg          *config.Config
	queries      *database.Queries
	auth         *AuthManager
	tmpl         *template.Template
	logBroker    *LogBroker
	waMgr        *waClient.WhatsAppManager
	loginLimiter *ratelimit.Limiter
}

func NewServer(cfg *config.Config, queries *database.Queries, waMgr *waClient.WhatsAppManager) *Server {
	// Parse embedded templates
	tmpl := template.Must(template.New("layout").ParseFS(templatesFS, "templates/*.html"))

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
		logBroker:    broker,
		waMgr:        waMgr,
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

func (s *Server) GetRouter() chi.Router {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(reportPanics)
	r.Use(middleware.Recoverer)

	// Auth page routes
	r.Get("/login", s.handleGetLogin)
	r.With(s.requireCSRF).Post("/login", s.handlePostLogin)
	r.Get("/logout", s.handleLogout)

	// Webhook endpoint (public)
	r.Post("/webhook/events", s.handleWebhookEvents)

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
		r.With(s.requireCSRF).Post("/dashboard/contacts/update", s.handlePostContactUpdate)
		r.Get("/dashboard/whatsapp/status", s.handleGetWhatsAppStatus)
		r.With(s.requireCSRF).Post("/dashboard/whatsapp/pair-code", s.handlePostWhatsAppPairCode)

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
		http.Error(w, "Too many login attempts — try again in a few minutes", http.StatusTooManyRequests)
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

func (s *Server) handleGetDashboard(w http.ResponseWriter, r *http.Request) {
	// Fetch recent activities
	activities, err := s.queries.ListRecentWaActivity(r.Context(), 10)
	if err != nil {
		activities = []database.WaActivity{}
	}

	// Fetch contacts
	contacts, err := s.queries.ListWaContacts(r.Context())
	if err != nil {
		contacts = []database.WaContact{}
	}

	// Get WhatsApp Status
	status, qrString, pairCode := s.waMgr.GetStatus()

	s.renderTemplate(w, "dashboard.html", map[string]interface{}{
		"Username":   r.Context().Value(UsernameContextKey),
		"Activities": activities,
		"Contacts":   contacts,
		"Page":       "dashboard",
		"WAStatus":   string(status),
		"WAQR":       qrString,
		"WAPair":     pairCode,
		"CSRFToken":  s.ensureCSRFToken(w, r),
	})
}

func (s *Server) handleGetWhatsAppStatus(w http.ResponseWriter, r *http.Request) {
	status, qrString, pairCode := s.waMgr.GetStatus()

	s.renderTemplate(w, "dashboard.html", map[string]interface{}{
		"WAStatus":  string(status),
		"WAQR":      qrString,
		"WAPair":    pairCode,
		"Partial":   true,
		"CSRFToken": s.ensureCSRFToken(w, r),
	})
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

	s.renderTemplate(w, "dashboard.html", map[string]interface{}{
		"WAStatus":  "pairing_ready",
		"WAQR":      "",
		"WAPair":    prettyCode,
		"Partial":   true,
		"CSRFToken": s.ensureCSRFToken(w, r),
	})
}

func (s *Server) handleGetLogsPage(w http.ResponseWriter, r *http.Request) {
	s.renderTemplate(w, "logs.html", map[string]interface{}{
		"Username": r.Context().Value(UsernameContextKey),
		"Page":     "logs",
	})
}

func (s *Server) handleGetWorkflowsPage(w http.ResponseWriter, r *http.Request) {
	agents, err := s.queries.ListAgents(r.Context())
	if err != nil {
		agents = []database.Agent{}
	}

	s.renderTemplate(w, "workflow.html", map[string]interface{}{
		"Username":  r.Context().Value(UsernameContextKey),
		"Agents":    agents,
		"Page":      "workflows",
		"CSRFToken": s.ensureCSRFToken(w, r),
	})
}

func (s *Server) handlePostUpdateWorkflow(w http.ResponseWriter, r *http.Request) {
	agentID := r.FormValue("id")
	name := r.FormValue("name")
	prompt := r.FormValue("system_prompt")
	greeting := r.FormValue("greeting_message")
	failure := r.FormValue("failure_message")

	// Get current agent to preserve json parameters
	agent, err := s.queries.GetAgent(r.Context(), agentID)
	if err != nil {
		http.Error(w, "Agent not found", http.StatusNotFound)
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
		Status:          agent.Status,
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

func (s *Server) handlePostContactUpdate(w http.ResponseWriter, r *http.Request) {
	chatID := r.FormValue("chat_id")
	if chatID == "" {
		http.Error(w, "chat_id is required", http.StatusBadRequest)
		return
	}

	enabled := r.FormValue("enabled") == "true"
	agentIDVal := r.FormValue("agent_id")
	contactType := r.FormValue("contact_type")

	var agentID *string
	if agentIDVal != "" && agentIDVal != "default" {
		agentID = &agentIDVal
	}

	_, err := s.queries.UpdateWaContactSettings(r.Context(), database.UpdateWaContactSettingsParams{
		ChatID:      chatID,
		Enabled:     enabled,
		AgentID:     agentID,
		ContactType: contactType,
	})
	if err != nil {
		log.Printf("Failed to update contact settings for %s: %v", chatID, err)
		http.Error(w, "Failed to update contact settings", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

type WebhookEvent struct {
	EventType string                 `json:"eventType"`
	Phone     string                 `json:"phone"`
	Payload   map[string]interface{} `json:"payload"`
}

func (s *Server) handleWebhookEvents(w http.ResponseWriter, r *http.Request) {
	sig := r.Header.Get("x-swa-signature")
	tsStr := r.Header.Get("x-swa-timestamp")

	if sig == "" || tsStr == "" {
		http.Error(w, "Unauthorized: missing signature headers", http.StatusUnauthorized)
		return
	}

	// Validate timestamp skew (±5 minutes)
	timestamp, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		http.Error(w, "Unauthorized: invalid timestamp", http.StatusUnauthorized)
		return
	}
	nowMs := time.Now().UnixMilli()
	diff := nowMs - timestamp
	if diff < -300000 || diff > 300000 {
		http.Error(w, "Unauthorized: timestamp skew too large", http.StatusUnauthorized)
		return
	}

	// Read body bytes
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	// Validate signature
	expectedSig := computeSignature(s.cfg.AgentGatewaySecret, tsStr, string(bodyBytes))
	if !hmac.Equal([]byte(sig), []byte(expectedSig)) {
		http.Error(w, "Unauthorized: signature mismatch", http.StatusUnauthorized)
		return
	}

	// Parse event
	var event WebhookEvent
	if err := json.Unmarshal(bodyBytes, &event); err != nil {
		http.Error(w, "Bad Request: invalid JSON", http.StatusBadRequest)
		return
	}

	// Ensure whatsmeow client is connected
	if s.waMgr.Client == nil || !s.waMgr.Client.IsConnected() {
		http.Error(w, "Service Unavailable: WhatsApp not connected", http.StatusServiceUnavailable)
		return
	}

	// Determine notification message
	var messageText string
	switch event.EventType {
	case "payment_reminder":
		messageText = "تذكير: لديك فاتورة مستحقة الدفع. يرجى مراجعة الحساب.\nReminder: You have an outstanding invoice due. Please check your account."
	case "task_overdue_alert":
		messageText = "تنبيه: هناك مهمة متأخرة لم يتم إنجازها بعد.\nAlert: There is an overdue task that requires your attention."
	case "appointment_reminder":
		messageText = "تذكير: لديك موعد مجدول قريباً.\nReminder: You have an upcoming scheduled appointment."
	default:
		messageText = "تنبيه جديد من نظام صوت.\nNew notification from Sawt."
	}

	// Send message via WhatsApp
	jid := types.NewJID(event.Phone, types.DefaultUserServer)
	textMsg := &proto.Message{
		Conversation: googleProto.String(messageText),
	}
	_, err = s.waMgr.Client.SendMessage(r.Context(), jid, textMsg)
	if err != nil {
		log.Printf("Failed to send webhook notification to %s: %v", event.Phone, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"ok":true}`))
}

func computeSignature(secret, timestamp, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp + "." + body))
	return hex.EncodeToString(mac.Sum(nil))
}
