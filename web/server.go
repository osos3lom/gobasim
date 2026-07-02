package web

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"sawt-go/config"
	"sawt-go/database"
	waClient "sawt-go/internal/whatsmeow"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// TemplatesFS holds embedded HTML template files.
//go:embed templates/*
var templatesFS embed.FS

type Server struct {
	cfg         *config.Config
	queries     *database.Queries
	auth        *AuthManager
	tmpl        *template.Template
	logBroker   *LogBroker
	waMgr       *waClient.WhatsAppManager
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
		cfg:       cfg,
		queries:   queries,
		auth:      NewAuthManager(cfg, queries),
		tmpl:      tmpl,
		logBroker: broker,
		waMgr:     waMgr,
	}
}

func (s *Server) GetRouter() chi.Router {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	// Auth page routes
	r.Get("/login", s.handleGetLogin)
	r.Post("/login", s.handlePostLogin)
	r.Get("/logout", s.handleLogout)

	// Protected routes
	r.Group(func(r chi.Router) {
		r.Use(s.auth.RequireAuth)

		r.Get("/", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
		})
		r.Get("/dashboard", s.handleGetDashboard)
		r.Get("/dashboard/logs", s.handleGetLogsPage)
		r.Get("/dashboard/workflows", s.handleGetWorkflowsPage)
		r.Post("/dashboard/workflows/update", s.handlePostUpdateWorkflow)
		r.Get("/dashboard/whatsapp/status", s.handleGetWhatsAppStatus)
		r.Post("/dashboard/whatsapp/pair-code", s.handlePostWhatsAppPairCode)
		
		// SSE Log stream
		r.Get("/api/logs", s.handleSSELogs)
	})

	return r
}

func (s *Server) handleGetLogin(w http.ResponseWriter, r *http.Request) {
	s.tmpl.ExecuteTemplate(w, "login.html", map[string]interface{}{
		"Error": "",
	})
}

func (s *Server) handlePostLogin(w http.ResponseWriter, r *http.Request) {
	username := r.FormValue("username")
	password := r.FormValue("password")

	cookie, err := s.auth.Login(r.Context(), username, password)
	if err != nil {
		s.tmpl.ExecuteTemplate(w, "login.html", map[string]interface{}{
			"Error": err.Error(),
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

	s.tmpl.ExecuteTemplate(w, "dashboard.html", map[string]interface{}{
		"Username":   r.Context().Value("username"),
		"Activities": activities,
		"Contacts":   contacts,
		"Page":       "dashboard",
		"WAStatus":   string(status),
		"WAQR":       qrString,
		"WAPair":     pairCode,
	})
}

func (s *Server) handleGetWhatsAppStatus(w http.ResponseWriter, r *http.Request) {
	status, qrString, pairCode := s.waMgr.GetStatus()
	
	s.tmpl.ExecuteTemplate(w, "dashboard.html", map[string]interface{}{
		"WAStatus": string(status),
		"WAQR":     qrString,
		"WAPair":   pairCode,
		"Partial":  true,
	})
}

func (s *Server) handlePostWhatsAppPairCode(w http.ResponseWriter, r *http.Request) {
	phone := r.FormValue("phone")
	if phone == "" {
		http.Error(w, "Phone number is required", http.StatusBadRequest)
		return
	}

	prettyCode, err := s.waMgr.RequestPairingCode(phone)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	s.tmpl.ExecuteTemplate(w, "dashboard.html", map[string]interface{}{
		"WAStatus": "pairing_ready",
		"WAQR":     "",
		"WAPair":   prettyCode,
		"Partial":  true,
	})
}

func (s *Server) handleGetLogsPage(w http.ResponseWriter, r *http.Request) {
	s.tmpl.ExecuteTemplate(w, "logs.html", map[string]interface{}{
		"Username": r.Context().Value("username"),
		"Page":     "logs",
	})
}

func (s *Server) handleGetWorkflowsPage(w http.ResponseWriter, r *http.Request) {
	agents, err := s.queries.ListAgents(r.Context())
	if err != nil {
		agents = []database.Agent{}
	}

	s.tmpl.ExecuteTemplate(w, "workflow.html", map[string]interface{}{
		"Username": r.Context().Value("username"),
		"Agents":   agents,
		"Page":     "workflows",
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
	b.publish <- line
	return len(p), nil
}
