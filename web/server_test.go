package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"sawt-go/config"
	"sawt-go/database"
	"sawt-go/internal/speech"
	waClient "sawt-go/internal/whatsmeow"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/crypto/bcrypt"
)

// Mock implementation of database.DBTX interface
type mockDBTX struct {
	queryRowFunc func(ctx context.Context, sql string, args ...interface{}) pgx.Row
	queryFunc    func(ctx context.Context, sql string, args ...interface{}) (pgx.Rows, error)
	execFunc     func(ctx context.Context, sql string, args ...interface{}) (pgconn.CommandTag, error)
}

func (m *mockDBTX) QueryRow(ctx context.Context, sql string, args ...interface{}) pgx.Row {
	if m.queryRowFunc != nil {
		return m.queryRowFunc(ctx, sql, args...)
	}
	return &mockRow{}
}

func (m *mockDBTX) Query(ctx context.Context, sql string, args ...interface{}) (pgx.Rows, error) {
	if m.queryFunc != nil {
		return m.queryFunc(ctx, sql, args...)
	}
	return &mockRows{}, nil
}

func (m *mockDBTX) Exec(ctx context.Context, sql string, args ...interface{}) (pgconn.CommandTag, error) {
	if m.execFunc != nil {
		return m.execFunc(ctx, sql, args...)
	}
	return pgconn.CommandTag{}, nil
}

// Mock implementation of pgx.Row
type mockRow struct {
	scanFunc func(dest ...interface{}) error
}

func (r *mockRow) Scan(dest ...interface{}) error {
	if r.scanFunc != nil {
		return r.scanFunc(dest...)
	}
	return nil
}

// Mock implementation of pgx.Rows
type mockRows struct {
	nextFunc func() bool
	scanFunc func(dest ...interface{}) error
	errFunc  func() error
}

func (r *mockRows) Close() {}
func (r *mockRows) Err() error {
	if r.errFunc != nil {
		return r.errFunc()
	}
	return nil
}
func (r *mockRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (r *mockRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (r *mockRows) Next() bool {
	if r.nextFunc != nil {
		return r.nextFunc()
	}
	return false
}
func (r *mockRows) Scan(dest ...interface{}) error {
	if r.scanFunc != nil {
		return r.scanFunc(dest...)
	}
	return nil
}
func (r *mockRows) Values() ([]interface{}, error) { return nil, nil }
func (r *mockRows) RawValues() [][]byte            { return nil }
func (r *mockRows) Conn() *pgx.Conn                { return nil }

func setupTestServer(t *testing.T, db *mockDBTX) (*Server, *config.Config) {
	cfg := &config.Config{
		SessionSecret: "test-secret-that-is-32-chars-long-or-more",
		SecureCookie:  false,
		AdminUsername: "admin",
	}
	queries := database.New(db)
	waMgr := waClient.NewWhatsAppManager("postgres://mock")
	ttsOrch := speech.NewTTSOrchestrator(cfg)

	server := NewServer(cfg, queries, waMgr, ttsOrch)
	return server, cfg
}

func TestGetLogin(t *testing.T) {
	db := &mockDBTX{}
	server, _ := setupTestServer(t, db)

	req := httptest.NewRequest("GET", "/login", nil)
	w := httptest.NewRecorder()

	server.GetRouter().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	body := w.Body.String()
	if !strings.Contains(body, "Sign in to control your conversational ERP") {
		t.Errorf("expected response to contain login form header, got %s", body)
	}
}

func TestPostLogin_Success(t *testing.T) {
	// Generate bcrypt password hash
	passwordHash, err := bcrypt.GenerateFromPassword([]byte("correct-password"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("failed to generate bcrypt hash: %v", err)
	}

	db := &mockDBTX{
		queryRowFunc: func(ctx context.Context, sql string, args ...interface{}) pgx.Row {
			if strings.Contains(sql, "SELECT id, username, password_hash, created_at FROM users") {
				return &mockRow{
					scanFunc: func(dest ...interface{}) error {
						*dest[0].(*int32) = 1
						*dest[1].(*string) = "admin"
						*dest[2].(*string) = string(passwordHash)
						*dest[3].(*time.Time) = time.Now()
						return nil
					},
				}
			}
			return &mockRow{}
		},
	}
	server, _ := setupTestServer(t, db)

	// Obtain CSRF token
	reqGet := httptest.NewRequest("GET", "/login", nil)
	wGet := httptest.NewRecorder()
	server.GetRouter().ServeHTTP(wGet, reqGet)
	csrfCookie := wGet.Result().Cookies()

	form := url.Values{}
	form.Add("username", "admin")
	form.Add("password", "correct-password")

	// Exclude CSRF verification from this simple test by stubbing ensureCSRFToken / requireCSRF or manually passing the token.
	// Wait, requireCSRF middleware reads cookie "csrf_token" and form value "csrf_token".
	// Let's check how requireCSRF is implemented in csrf.go. Let's see if we can get a valid token.
	// If we read the body of login, does it have a csrf_token hidden field? Yes!
	var token string
	body := wGet.Body.String()
	tokenMarker := `name="csrf_token" value="`
	idx := strings.Index(body, tokenMarker)
	if idx != -1 {
		start := idx + len(tokenMarker)
		end := strings.Index(body[start:], `"`)
		if end != -1 {
			token = body[start : start+end]
		}
	}

	reqPost := httptest.NewRequest("POST", "/login", strings.NewReader(form.Encode()))
	reqPost.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	// Put the cookie and form value in the request
	for _, c := range csrfCookie {
		reqPost.AddCookie(c)
	}
	if token != "" {
		form.Add("csrf_token", token)
		reqPost.Body = ioNopCloser{strings.NewReader(form.Encode())}
	}

	wPost := httptest.NewRecorder()
	server.GetRouter().ServeHTTP(wPost, reqPost)

	if wPost.Code != http.StatusSeeOther {
		t.Errorf("expected status 302/Redirect, got %d. Body: %s", wPost.Code, wPost.Body.String())
	}

	redirectLocation := wPost.Header().Get("Location")
	if redirectLocation != "/dashboard" {
		t.Errorf("expected redirect to /dashboard, got %q", redirectLocation)
	}
}

// Minimal reader wrapper since strings.NewReader doesn't implement io.ReadCloser
type ioNopCloser struct {
	*strings.Reader
}

func (ioNopCloser) Close() error { return nil }

func TestGetDashboard_RedirectsWhenLoggedOut(t *testing.T) {
	db := &mockDBTX{}
	server, _ := setupTestServer(t, db)

	req := httptest.NewRequest("GET", "/dashboard", nil)
	w := httptest.NewRecorder()

	server.GetRouter().ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Errorf("expected status 302, got %d", w.Code)
	}

	loc := w.Header().Get("Location")
	if loc != "/login" {
		t.Errorf("expected redirect to /login, got %q", loc)
	}
}

func TestGetDashboard_RenderSuccessWhenLoggedIn(t *testing.T) {
	db := &mockDBTX{}
	server, cfg := setupTestServer(t, db)

	req := httptest.NewRequest("GET", "/dashboard", nil)
	w := httptest.NewRecorder()

	// Authenticate via cookie
	auth := NewAuthManager(cfg, database.New(db))
	cookieVal := auth.GenerateCookieValue("admin", time.Hour)
	req.AddCookie(&http.Cookie{
		Name:  SessionCookieName,
		Value: cookieVal,
	})

	server.GetRouter().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	body := w.Body.String()
	if !strings.Contains(body, "Overview") {
		t.Errorf("expected body to contain 'Overview', got:\n%s", body)
	}
}

func TestGetWorkflows_RenderSuccess(t *testing.T) {
	db := &mockDBTX{}
	server, cfg := setupTestServer(t, db)

	req := httptest.NewRequest("GET", "/dashboard/workflows", nil)
	w := httptest.NewRecorder()

	// Authenticate
	auth := NewAuthManager(cfg, database.New(db))
	cookieVal := auth.GenerateCookieValue("admin", time.Hour)
	req.AddCookie(&http.Cookie{
		Name:  SessionCookieName,
		Value: cookieVal,
	})

	server.GetRouter().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	body := w.Body.String()
	if !strings.Contains(body, "Workflows") {
		t.Errorf("expected body to contain 'Workflows', got:\n%s", body)
	}
}

func TestFormatDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{42 * time.Second, "42s"},
		{5*time.Minute + 12*time.Second, "5m 12s"},
		{1*time.Hour + 5*time.Minute, "1h 5m"},
		{0, "0s"},
	}
	for _, tc := range cases {
		if got := formatDuration(tc.d); got != tc.want {
			t.Errorf("formatDuration(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

func authedRequest(t *testing.T, cfg *config.Config, db *mockDBTX, method, path string, body string) *http.Request {
	t.Helper()
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	auth := NewAuthManager(cfg, database.New(db))
	cookieVal := auth.GenerateCookieValue("admin", time.Hour)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: cookieVal})
	return req
}

func TestPostWhatsAppLogout_RequiresCSRF(t *testing.T) {
	db := &mockDBTX{}
	server, cfg := setupTestServer(t, db)

	req := authedRequest(t, cfg, db, "POST", "/dashboard/whatsapp/logout", "")
	w := httptest.NewRecorder()

	server.GetRouter().ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 without a CSRF token, got %d. Body: %s", w.Code, w.Body.String())
	}
}

func TestPostWhatsAppRepair_RequiresCSRF(t *testing.T) {
	db := &mockDBTX{}
	server, cfg := setupTestServer(t, db)

	req := authedRequest(t, cfg, db, "POST", "/dashboard/whatsapp/repair", "")
	w := httptest.NewRecorder()

	server.GetRouter().ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 without a CSRF token, got %d. Body: %s", w.Code, w.Body.String())
	}
}

func TestPostWhatsAppLogout_ClientNotInitialized(t *testing.T) {
	db := &mockDBTX{}
	server, cfg := setupTestServer(t, db)

	// Obtain a valid CSRF cookie+token the same way the real form does.
	getReq := authedRequest(t, cfg, db, "GET", "/dashboard/whatsapp", "")
	getW := httptest.NewRecorder()
	server.GetRouter().ServeHTTP(getW, getReq)
	csrfCookies := getW.Result().Cookies()

	var token string
	body := getW.Body.String()
	if idx := strings.Index(body, `name="csrf_token" value="`); idx != -1 {
		start := idx + len(`name="csrf_token" value="`)
		if end := strings.Index(body[start:], `"`); end != -1 {
			token = body[start : start+end]
		}
	}
	if token == "" {
		t.Fatal("could not extract csrf token from /dashboard/whatsapp response")
	}

	form := url.Values{}
	form.Add("csrf_token", token)
	req := authedRequest(t, cfg, db, "POST", "/dashboard/whatsapp/logout", form.Encode())
	for _, c := range csrfCookies {
		req.AddCookie(c)
	}
	w := httptest.NewRecorder()

	server.GetRouter().ServeHTTP(w, req)

	// setupTestServer's waMgr never calls Initialize, so Client is nil —
	// Logout should surface that as an inline error fragment, not panic.
	if !strings.Contains(w.Body.String(), "Logout failed") {
		t.Errorf("expected an inline 'Logout failed' error fragment, got:\n%s", w.Body.String())
	}
}

func TestPostToggleWaContact_PreservesOtherFields(t *testing.T) {
	agentID := "agent_default"
	promptOverride := "be extra polite"
	var capturedArgs []interface{}

	db := &mockDBTX{
		queryRowFunc: func(ctx context.Context, sql string, args ...interface{}) pgx.Row {
			switch {
			case strings.Contains(sql, "FROM wa_contacts") && strings.Contains(sql, "WHERE chat_id = $1 LIMIT 1"):
				// GetWaContact: return an existing contact with AgentID/PromptOverride set and Enabled=true.
				return &mockRow{
					scanFunc: func(dest ...interface{}) error {
						*dest[0].(*string) = "1234@s.whatsapp.net"
						*dest[1].(*string) = "Layla"
						*dest[2].(*bool) = true
						*dest[3].(**string) = &agentID
						*dest[4].(**string) = &promptOverride
						*dest[5].(*time.Time) = time.Now()
						return nil
					},
				}
			case strings.Contains(sql, "INSERT INTO wa_contacts"):
				// CreateOrUpdateWaContact: capture the args passed through.
				capturedArgs = args
				return &mockRow{
					scanFunc: func(dest ...interface{}) error {
						*dest[0].(*string) = args[0].(string)
						*dest[1].(*string) = args[1].(string)
						*dest[2].(*bool) = args[2].(bool)
						*dest[3].(**string) = args[3].(*string)
						*dest[4].(**string) = args[4].(*string)
						*dest[5].(*time.Time) = time.Now()
						return nil
					},
				}
			}
			return &mockRow{}
		},
	}
	server, cfg := setupTestServer(t, db)

	getReq := authedRequest(t, cfg, db, "GET", "/dashboard/whatsapp", "")
	getW := httptest.NewRecorder()
	server.GetRouter().ServeHTTP(getW, getReq)
	csrfCookies := getW.Result().Cookies()
	var token string
	if idx := strings.Index(getW.Body.String(), `name="csrf_token" value="`); idx != -1 {
		start := idx + len(`name="csrf_token" value="`)
		if end := strings.Index(getW.Body.String()[start:], `"`); end != -1 {
			token = getW.Body.String()[start : start+end]
		}
	}

	form := url.Values{}
	form.Add("csrf_token", token)
	req := authedRequest(t, cfg, db, "POST", "/dashboard/whatsapp/contacts/1234@s.whatsapp.net/toggle", form.Encode())
	for _, c := range csrfCookies {
		req.AddCookie(c)
	}
	w := httptest.NewRecorder()
	server.GetRouter().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d. Body: %s", w.Code, w.Body.String())
	}
	if capturedArgs == nil {
		t.Fatal("expected CreateOrUpdateWaContact to be called")
	}
	if capturedArgs[2].(bool) != false {
		t.Errorf("expected Enabled to flip from true to false, got %v", capturedArgs[2])
	}
	if capturedArgs[3].(*string) != &agentID {
		t.Errorf("expected AgentID to be preserved unchanged, got %v", capturedArgs[3])
	}
	if capturedArgs[4].(*string) != &promptOverride {
		t.Errorf("expected PromptOverride to be preserved unchanged, got %v", capturedArgs[4])
	}
}

func TestPostAssignWaContactAgent_RejectsUnpublishedAgent(t *testing.T) {
	createOrUpdateCalled := false

	db := &mockDBTX{
		queryRowFunc: func(ctx context.Context, sql string, args ...interface{}) pgx.Row {
			switch {
			case strings.Contains(sql, "FROM wa_contacts") && strings.Contains(sql, "WHERE chat_id = $1 LIMIT 1"):
				return &mockRow{
					scanFunc: func(dest ...interface{}) error {
						*dest[0].(*string) = "1234@s.whatsapp.net"
						*dest[1].(*string) = "Layla"
						*dest[2].(*bool) = true
						*dest[3].(**string) = nil
						*dest[4].(**string) = nil
						*dest[5].(*time.Time) = time.Now()
						return nil
					},
				}
			case strings.Contains(sql, "SELECT") && strings.Contains(sql, "FROM agents") && strings.Contains(sql, "WHERE id ="):
				// GetAgent: return a draft (unpublished) agent.
				return &mockRow{
					scanFunc: func(dest ...interface{}) error {
						*dest[0].(*string) = "agent_draft"
						*dest[1].(*string) = "Draft Agent"
						*dest[4].(*string) = "draft" // Status field
						return nil
					},
				}
			case strings.Contains(sql, "INSERT INTO wa_contacts"):
				createOrUpdateCalled = true
			}
			return &mockRow{}
		},
	}
	server, cfg := setupTestServer(t, db)

	getReq := authedRequest(t, cfg, db, "GET", "/dashboard/whatsapp", "")
	getW := httptest.NewRecorder()
	server.GetRouter().ServeHTTP(getW, getReq)
	csrfCookies := getW.Result().Cookies()
	var token string
	if idx := strings.Index(getW.Body.String(), `name="csrf_token" value="`); idx != -1 {
		start := idx + len(`name="csrf_token" value="`)
		if end := strings.Index(getW.Body.String()[start:], `"`); end != -1 {
			token = getW.Body.String()[start : start+end]
		}
	}

	form := url.Values{}
	form.Add("csrf_token", token)
	form.Add("agent_id", "agent_draft")
	req := authedRequest(t, cfg, db, "POST", "/dashboard/whatsapp/contacts/1234@s.whatsapp.net/agent", form.Encode())
	for _, c := range csrfCookies {
		req.AddCookie(c)
	}
	w := httptest.NewRecorder()
	server.GetRouter().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for an unpublished agent, got %d. Body: %s", w.Code, w.Body.String())
	}
	if createOrUpdateCalled {
		t.Error("expected CreateOrUpdateWaContact NOT to be called when the agent is unpublished")
	}
}

func TestHandleGetWaMessagesFragment_Pagination(t *testing.T) {
	var capturedArgs []interface{}

	db := &mockDBTX{
		queryFunc: func(ctx context.Context, sql string, args ...interface{}) (pgx.Rows, error) {
			if strings.Contains(sql, "FROM wa_messages") {
				capturedArgs = args
			}
			return &mockRows{}, nil
		},
	}
	server, cfg := setupTestServer(t, db)

	req := authedRequest(t, cfg, db, "GET", "/dashboard/whatsapp/chats/1234@s.whatsapp.net/messages?before_seq=42", "")
	w := httptest.NewRecorder()
	server.GetRouter().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d. Body: %s", w.Code, w.Body.String())
	}
	if capturedArgs == nil {
		t.Fatal("expected ListWaMessagesByChat to be called")
	}
	if capturedArgs[0] != "1234@s.whatsapp.net" {
		t.Errorf("expected chat_id arg %q, got %v", "1234@s.whatsapp.net", capturedArgs[0])
	}
	if capturedArgs[1] != int64(42) {
		t.Errorf("expected before_seq arg 42, got %v", capturedArgs[1])
	}
	if capturedArgs[2] != int32(waMessagesThreadLimit) {
		t.Errorf("expected limit arg %d, got %v", waMessagesThreadLimit, capturedArgs[2])
	}
}

func TestGetWhatsAppPage_RenderSuccess(t *testing.T) {
	db := &mockDBTX{}
	server, cfg := setupTestServer(t, db)

	req := httptest.NewRequest("GET", "/dashboard/whatsapp", nil)
	w := httptest.NewRecorder()

	// Authenticate
	auth := NewAuthManager(cfg, database.New(db))
	cookieVal := auth.GenerateCookieValue("admin", time.Hour)
	req.AddCookie(&http.Cookie{
		Name:  SessionCookieName,
		Value: cookieVal,
	})

	server.GetRouter().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	body := w.Body.String()
	if !strings.Contains(body, "WhatsApp Linking") {
		t.Errorf("expected body to contain 'WhatsApp Linking', got:\n%s", body)
	}
}

func TestGetLogs_RenderSuccess(t *testing.T) {
	db := &mockDBTX{}
	server, cfg := setupTestServer(t, db)

	req := httptest.NewRequest("GET", "/dashboard/logs", nil)
	w := httptest.NewRecorder()

	// Authenticate
	auth := NewAuthManager(cfg, database.New(db))
	cookieVal := auth.GenerateCookieValue("admin", time.Hour)
	req.AddCookie(&http.Cookie{
		Name:  SessionCookieName,
		Value: cookieVal,
	})

	server.GetRouter().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	body := w.Body.String()
	if !strings.Contains(body, "System Logs") && !strings.Contains(body, "Live Event Logger") {
		t.Errorf("expected body to contain logs title, got:\n%s", body)
	}
}

func TestPostWhatsAppSettings_RequiresCSRF(t *testing.T) {
	db := &mockDBTX{}
	server, cfg := setupTestServer(t, db)

	req := authedRequest(t, cfg, db, "POST", "/dashboard/whatsapp/settings", "agent_id=agent_test")
	w := httptest.NewRecorder()
	server.GetRouter().ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected status 403 (Forbidden) due to missing CSRF, got %d", w.Code)
	}
}

func TestPostWhatsAppSettings_Success(t *testing.T) {
	var execCalled bool
	var capturedArgs []interface{}

	db := &mockDBTX{
		queryRowFunc: func(ctx context.Context, sql string, args ...interface{}) pgx.Row {
			if strings.Contains(sql, "SELECT id, tts_model, model_ids, default_speed, bot_config, assistant_agent_id FROM settings") {
				return &mockRow{
					scanFunc: func(dest ...interface{}) error {
						*dest[0].(*int32) = 1
						*dest[1].(*string) = "tts"
						*dest[2].(*[]byte) = []byte("{}")
						*dest[3].(*float32) = 1.0
						*dest[4].(*[]byte) = []byte("{}")
						*dest[5].(**string) = nil
						return nil
					},
				}
			}
			if strings.Contains(sql, "SELECT") && strings.Contains(sql, "FROM agents") && strings.Contains(sql, "WHERE id =") {
				return &mockRow{
					scanFunc: func(dest ...interface{}) error {
						*dest[0].(*string) = "agent_test"
						*dest[1].(*string) = "Agent Test"
						*dest[4].(*string) = "published"
						return nil
					},
				}
			}
			return &mockRow{}
		},
		execFunc: func(ctx context.Context, sql string, args ...interface{}) (pgconn.CommandTag, error) {
			if strings.Contains(sql, "UPDATE settings") {
				execCalled = true
				capturedArgs = args
			}
			return pgconn.CommandTag{}, nil
		},
	}
	server, cfg := setupTestServer(t, db)

	// Fetch page to get CSRF token
	getReq := authedRequest(t, cfg, db, "GET", "/dashboard/whatsapp", "")
	getW := httptest.NewRecorder()
	server.GetRouter().ServeHTTP(getW, getReq)
	csrfCookies := getW.Result().Cookies()
	var token string
	if idx := strings.Index(getW.Body.String(), `name="csrf_token" value="`); idx != -1 {
		start := idx + len(`name="csrf_token" value="`)
		if end := strings.Index(getW.Body.String()[start:], `"`); end != -1 {
			token = getW.Body.String()[start : start+end]
		}
	}

	form := url.Values{}
	form.Add("csrf_token", token)
	form.Add("agent_id", "agent_test")
	form.Add("prompt_override", "Custom prompt")
	form.Add("auto_enable", "on")

	req := authedRequest(t, cfg, db, "POST", "/dashboard/whatsapp/settings", form.Encode())
	for _, c := range csrfCookies {
		req.AddCookie(c)
	}
	w := httptest.NewRecorder()
	server.GetRouter().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d. Body: %s", w.Code, w.Body.String())
	}
	if !execCalled {
		t.Fatal("expected UpdateSettings to be called")
	}
	if !strings.Contains(string(capturedArgs[3].([]byte)), `"agent_id":"agent_test"`) {
		t.Errorf("expected bot_config to contain agent_test, got %s", string(capturedArgs[3].([]byte)))
	}
}
