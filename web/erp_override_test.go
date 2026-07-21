package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestHandlePostSetWaContactErpOverride_NoErpClientConfigured(t *testing.T) {
	db := &mockDBTX{}
	server, _ := setupTestServer(t, db)

	r := withChatID(newFormRequest(t, url.Values{"erp_phone_override": {"966500000001"}}), "1234@s.whatsapp.net")
	w := httptest.NewRecorder()
	server.handlePostSetWaContactErpOverride(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200. Body: %s", w.Code, w.Body.String())
	}
}

func TestHandlePostSetWaContactErpOverride_PilotView(t *testing.T) {
	db := &mockDBTX{}
	server, _ := setupTestServer(t, db)

	r := withChatID(newFormRequest(t, url.Values{"erp_phone_override": {""}, "view": {"pilot"}}), "1234@s.whatsapp.net")
	w := httptest.NewRecorder()
	server.handlePostSetWaContactErpOverride(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200. Body: %s", w.Code, w.Body.String())
	}
}

func TestHandlePostResolveWaContactIdentity_NoErpClientConfigured(t *testing.T) {
	db := &mockDBTX{}
	server, _ := setupTestServer(t, db)

	r := withChatID(httptest.NewRequest(http.MethodPost, "/", nil), "1234@s.whatsapp.net")
	w := httptest.NewRecorder()
	server.handlePostResolveWaContactIdentity(w, r)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 when no ERP client is configured", w.Code)
	}
}
