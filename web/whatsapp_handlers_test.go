package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func withChatID(r *http.Request, chatID string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("chatID", chatID)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

func TestHandleGetWhatsAppStatus(t *testing.T) {
	db := &mockDBTX{}
	server, _ := setupTestServer(t, db)

	r := httptest.NewRequest(http.MethodGet, "/dashboard/whatsapp/status", nil)
	w := httptest.NewRecorder()
	server.handleGetWhatsAppStatus(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestHandlePostWhatsAppPairCode_MissingPhone(t *testing.T) {
	db := &mockDBTX{}
	server, _ := setupTestServer(t, db)

	r := newFormRequest(t, url.Values{})
	w := httptest.NewRecorder()
	server.handlePostWhatsAppPairCode(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for missing phone", w.Code)
	}
}

func TestHandlePostWhatsAppPairCode_InvalidLength(t *testing.T) {
	db := &mockDBTX{}
	server, _ := setupTestServer(t, db)

	r := newFormRequest(t, url.Values{"phone": {"123"}})
	w := httptest.NewRecorder()
	server.handlePostWhatsAppPairCode(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for too-short phone", w.Code)
	}
}

func TestHandlePostWhatsAppPairCode_ClientNotInitialized(t *testing.T) {
	db := &mockDBTX{}
	server, _ := setupTestServer(t, db)

	// setupTestServer's waMgr never calls Initialize, so the pairing code
	// request should surface a clean 500 instead of panicking.
	r := newFormRequest(t, url.Values{"phone": {"+966 50-000-0001"}})
	w := httptest.NewRecorder()
	server.handlePostWhatsAppPairCode(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 when the WhatsApp client isn't initialized", w.Code)
	}
}

func TestHandlePostWhatsAppRepair_ClientNotInitialized(t *testing.T) {
	db := &mockDBTX{}
	server, _ := setupTestServer(t, db)

	r := newFormRequest(t, url.Values{})
	w := httptest.NewRecorder()
	server.handlePostWhatsAppRepair(w, r)

	// RearmQR fails fast (client not initialized) and the handler reports it
	// inline rather than panicking.
	if !strings.Contains(w.Body.String(), "Could not start a new pairing session") {
		t.Errorf("expected inline pairing-session error, got %q", w.Body.String())
	}
}

func TestHandleGetWaContactsFragment(t *testing.T) {
	db := &mockDBTX{}
	server, _ := setupTestServer(t, db)

	r := httptest.NewRequest(http.MethodGet, "/dashboard/whatsapp/contacts", nil)
	w := httptest.NewRecorder()
	server.handleGetWaContactsFragment(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200. Body: %s", w.Code, w.Body.String())
	}
}

func TestHandleGetWaChatsFragment(t *testing.T) {
	db := &mockDBTX{}
	server, _ := setupTestServer(t, db)

	r := httptest.NewRequest(http.MethodGet, "/dashboard/whatsapp/chats", nil)
	w := httptest.NewRecorder()
	server.handleGetWaChatsFragment(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200. Body: %s", w.Code, w.Body.String())
	}
}

func TestHandlePostSendWaText_RequiresText(t *testing.T) {
	db := &mockDBTX{}
	server, _ := setupTestServer(t, db)

	r := withChatID(newFormRequest(t, url.Values{"text": {"   "}}), "1234@s.whatsapp.net")
	w := httptest.NewRecorder()
	server.handlePostSendWaText(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for blank text", w.Code)
	}
}

func TestHandlePostSendWaText_SendFailsButMessageIsRecorded(t *testing.T) {
	db := &mockDBTX{}
	server, _ := setupTestServer(t, db)

	// waMgr's client isn't initialized, so SendTextMessage fails — the
	// handler should still record the message (status "failed") and render.
	r := withChatID(newFormRequest(t, url.Values{"text": {"hello there"}}), "1234@s.whatsapp.net")
	w := httptest.NewRecorder()
	server.handlePostSendWaText(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200. Body: %s", w.Code, w.Body.String())
	}
}
