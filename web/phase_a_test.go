package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// --- D3: logout must be POST + CSRF ---

func TestLogout_GETIsRejected(t *testing.T) {
	db := &mockDBTX{}
	server, cfg := setupTestServer(t, db)

	req := authedRequest(t, cfg, db, "GET", "/logout", "")
	w := httptest.NewRecorder()
	server.GetRouter().ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 for GET /logout (state change must be POST), got %d", w.Code)
	}
}

func TestLogout_POSTRequiresCSRF(t *testing.T) {
	db := &mockDBTX{}
	server, cfg := setupTestServer(t, db)

	req := authedRequest(t, cfg, db, "POST", "/logout", "")
	w := httptest.NewRecorder()
	server.GetRouter().ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 for POST /logout without CSRF token, got %d. Body: %s", w.Code, w.Body.String())
	}
}

func TestLogout_POSTWithCSRFClearsSession(t *testing.T) {
	db := &mockDBTX{}
	server, cfg := setupTestServer(t, db)

	// Obtain a valid CSRF cookie+token the same way the real sidebar form does.
	getReq := authedRequest(t, cfg, db, "GET", "/dashboard/workflows", "")
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
		t.Fatal("could not extract csrf token from /dashboard/workflows response")
	}

	form := url.Values{}
	form.Add("csrf_token", token)
	req := authedRequest(t, cfg, db, "POST", "/logout", form.Encode())
	for _, c := range csrfCookies {
		req.AddCookie(c)
	}
	w := httptest.NewRecorder()
	server.GetRouter().ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect after logout, got %d", w.Code)
	}
	var cleared bool
	for _, c := range w.Result().Cookies() {
		if c.Name == SessionCookieName && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Error("expected the session cookie to be cleared (MaxAge < 0)")
	}
}

// --- D4: publish gate ---

func TestValidatePublishTransition(t *testing.T) {
	tests := []struct {
		name      string
		status    string
		prompt    string
		wantBlock bool
	}{
		{"publish with prompt allowed", "published", "You are an assistant.", false},
		{"publish with empty prompt blocked", "published", "", true},
		{"publish with whitespace prompt blocked", "published", "   \n\t", true},
		{"draft with empty prompt allowed", "draft", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reason := validatePublishTransition(tt.status, tt.prompt)
			if (reason != "") != tt.wantBlock {
				t.Errorf("validatePublishTransition(%q, %q) = %q, wantBlock=%v", tt.status, tt.prompt, reason, tt.wantBlock)
			}
		})
	}
}
