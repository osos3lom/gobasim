package web

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log"
	"log/slog"
	"net/http"
	"os"
	"runtime"
	"sawt-go/config"
	"sawt-go/database"
	"sawt-go/internal/erp"
	"sawt-go/internal/monitor"
	"sawt-go/internal/ratelimit"
	"sawt-go/internal/speech"
	"sawt-go/internal/voicenotes"
	waClient "sawt-go/internal/whatsmeow"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// TemplatesFS holds embedded HTML template files.
//
//go:embed templates/*
var templatesFS embed.FS

// staticFS holds embedded compiled assets (Tailwind CSS build output) plus JS helpers.
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

func (s *Server) SetVoiceStore(store *voicenotes.Store) {
	s.voiceStore = store
}

func (s *Server) SetDB(p dbPinger) {
	s.db = p
}

func (s *Server) SetERPClient(client *erp.Client) {
	s.erpClient = client
}

// BlueprintDefaults holds system default settings for new contacts.
type BlueprintDefaults struct {
	DefaultAgentID        string `json:"agent_id"`
	DefaultPromptOverride string `json:"prompt_override"`
	AutoEnable            bool   `json:"auto_enable"`
}

var templateFuncs = template.FuncMap{
	"waDisplayPhone":   waDisplayPhone,
	"cleanContactName": cleanContactName,
}

func NewServer(cfg *config.Config, queries *database.Queries, waMgr *waClient.WhatsAppManager, ttsOrch *speech.TTSOrchestrator) *Server {
	tmpl := template.Must(template.New("layout").Funcs(templateFuncs).ParseFS(templatesFS, "templates/*.html"))

	static, err := fs.Sub(staticFS, "static")
	if err != nil {
		log.Fatalf("web: failed to load embedded static assets: %v", err)
	}

	broker := NewLogBroker()
	go broker.Start()

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

type slogBridge struct{}

func (slogBridge) Write(p []byte) (int, error) {
	slog.Default().Info(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

var processStart = time.Now()

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{"status": "ok"})
}

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

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	waState, _, _ := s.waMgr.GetStatus()
	vnUploaded, vnFailed := s.voiceStore.Stats()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"uptime_seconds":       int(time.Since(processStart).Seconds()),
		"goroutines":           runtime.NumGoroutine(),
		"whatsapp_state":       string(waState),
		"voice_notes_uploaded": vnUploaded,
		"voice_notes_failed":   vnFailed,
	})
}

func (s *Server) renderTemplate(w http.ResponseWriter, name string, data interface{}) {
	if m, ok := data.(map[string]interface{}); ok {
		if _, exists := m["ErpURL"]; !exists && s.cfg != nil {
			m["ErpURL"] = s.cfg.CanonicalErpURL()
		}
	}
	err := s.tmpl.ExecuteTemplate(w, name, data)
	if err != nil {
		log.Printf("Template execution error: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

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
	r.Use(capturePeerIP)
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

	r.Get("/healthz", s.handleHealthz)
	r.Get("/readyz", s.handleReadyz)
	r.Get("/metrics", s.handleMetrics)

	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.FS(s.static))))

	r.Get("/login", s.handleGetLogin)
	r.With(s.requireCSRF).Post("/login", s.handlePostLogin)
	r.With(s.requireCSRF).Post("/logout", s.handleLogout)

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
		r.With(s.requireCSRF).Post("/dashboard/whatsapp/contacts/{chatID}/send-link-invite", s.handlePostSendWaLinkInvite)
		r.Get("/dashboard/whatsapp/contacts/{chatID}/tools", s.handleGetWaContactTools)
		r.With(s.requireCSRF).Post("/dashboard/whatsapp/settings", s.handlePostWhatsAppSettings)
		r.Get("/dashboard/whatsapp/chats", s.handleGetWaChatsFragment)
		r.Get("/dashboard/whatsapp/chats/{chatID}/messages", s.handleGetWaMessagesFragment)
		r.With(s.requireCSRF).Post("/dashboard/whatsapp/chats/{chatID}/send-text", s.handlePostSendWaText)
		r.With(s.requireCSRF).Post("/dashboard/whatsapp/chats/{chatID}/send-voice", s.handlePostSendWaVoice)

		r.Get("/api/logs", s.handleSSELogs)
	})

	return r
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
			_, _ = fmt.Fprintf(w, "data: %s\n\n", line)
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func securityHeaders(next http.Handler) http.Handler {
	const csp = "default-src 'self'; " +
		"script-src 'self' 'unsafe-inline' 'unsafe-eval' https://unpkg.com; " +
		"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; " +
		"font-src 'self' https://fonts.gstatic.com; " +
		"img-src 'self' data:; " +
		"connect-src 'self' ws: wss:; " +
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

func (b *LogBroker) Write(p []byte) (n int, err error) {
	line := string(p)
	select {
	case b.publish <- line:
	default:
	}
	return len(p), nil
}
