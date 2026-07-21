package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"sawt-go/database"

	"github.com/jackc/pgx/v5"
)

func newFormRequest(t *testing.T, values url.Values) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(values.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := r.ParseForm(); err != nil {
		t.Fatalf("failed to parse form: %v", err)
	}
	return r
}

func TestBuildUpdateParams_MinimalFormUsesDefaults(t *testing.T) {
	db := &mockDBTX{}
	server, _ := setupTestServer(t, db)

	agent := database.Agent{Name: "Stored Name", Status: "draft", Asr: []byte(`{}`)}
	r := newFormRequest(t, url.Values{
		"id":               {"agent_1"},
		"system_prompt":    {"be helpful"},
		"greeting_message": {"hi"},
	})

	arg, reason := server.buildUpdateParams(r, agent)
	if reason != "" {
		t.Fatalf("expected no validation error, got %q", reason)
	}
	if arg.Name != "Stored Name" {
		t.Errorf("expected Name to fall back to stored value, got %q", arg.Name)
	}
	if arg.Status != "draft" {
		t.Errorf("expected Status to fall back to stored value, got %q", arg.Status)
	}
	if string(arg.Asr) != "{}" {
		t.Errorf("expected Asr preserved from stored agent, got %q", string(arg.Asr))
	}
}

func TestBuildUpdateParams_NameOverride(t *testing.T) {
	db := &mockDBTX{}
	server, _ := setupTestServer(t, db)

	agent := database.Agent{Name: "Stored Name", Status: "draft"}
	r := newFormRequest(t, url.Values{"name": {"New Name"}})

	arg, reason := server.buildUpdateParams(r, agent)
	if reason != "" {
		t.Fatalf("unexpected validation error: %q", reason)
	}
	if arg.Name != "New Name" {
		t.Errorf("Name = %q, want New Name", arg.Name)
	}
}

func TestBuildUpdateParams_InvalidStatus(t *testing.T) {
	db := &mockDBTX{}
	server, _ := setupTestServer(t, db)

	agent := database.Agent{Name: "n", Status: "draft"}
	r := newFormRequest(t, url.Values{"status": {"bogus"}})

	_, reason := server.buildUpdateParams(r, agent)
	if reason == "" {
		t.Error("expected an error for an invalid status value")
	}
}

func TestBuildUpdateParams_PublishRequiresPrompt(t *testing.T) {
	db := &mockDBTX{}
	server, _ := setupTestServer(t, db)

	agent := database.Agent{Name: "n", Status: "draft"}
	r := newFormRequest(t, url.Values{"status": {"published"}, "system_prompt": {"   "}})

	_, reason := server.buildUpdateParams(r, agent)
	if reason == "" {
		t.Error("expected publishing with a blank prompt to be blocked")
	}
}

func TestBuildUpdateParams_InvalidLLMVendor(t *testing.T) {
	db := &mockDBTX{}
	server, _ := setupTestServer(t, db)

	agent := database.Agent{Name: "n", Status: "draft"}
	r := newFormRequest(t, url.Values{"llm_vendor": {"not-a-real-vendor"}})

	_, reason := server.buildUpdateParams(r, agent)
	if reason == "" {
		t.Error("expected an unknown llm vendor to be rejected")
	}
}

func TestBuildUpdateParams_InvalidClarificationJSON(t *testing.T) {
	db := &mockDBTX{}
	server, _ := setupTestServer(t, db)

	agent := database.Agent{Name: "n", Status: "draft"}
	r := newFormRequest(t, url.Values{"clarification_tool_overrides": {"not-json"}})

	_, reason := server.buildUpdateParams(r, agent)
	if reason == "" {
		t.Error("expected invalid clarification tool-override JSON to be rejected")
	}
}

func TestHandlePostCreateWorkflow_EmptyNameRerendersError(t *testing.T) {
	db := &mockDBTX{}
	server, _ := setupTestServer(t, db)

	r := newFormRequest(t, url.Values{"name": {"   "}})
	w := httptest.NewRecorder()
	server.handlePostCreateWorkflow(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (re-render with error)", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Agent name is required") {
		t.Errorf("expected error message in body, got %q", w.Body.String())
	}
}

func TestHandlePostCreateWorkflow_Success(t *testing.T) {
	db := &mockDBTX{}
	server, _ := setupTestServer(t, db)

	r := newFormRequest(t, url.Values{"name": {"New Agent"}})
	w := httptest.NewRecorder()
	server.handlePostCreateWorkflow(w, r)

	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303 redirect on success", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/dashboard/workflows" {
		t.Errorf("Location = %q", loc)
	}
}

func TestHandlePostUpdateWorkflow_AgentNotFound(t *testing.T) {
	db := &mockDBTX{
		queryRowFunc: func(ctx context.Context, sql string, args ...interface{}) pgx.Row {
			return &mockRow{scanFunc: func(dest ...interface{}) error { return errors.New("no rows") }}
		},
	}
	server, _ := setupTestServer(t, db)

	r := newFormRequest(t, url.Values{"id": {"missing-agent"}})
	w := httptest.NewRecorder()
	server.handlePostUpdateWorkflow(w, r)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 when the agent doesn't exist", w.Code)
	}
}

func TestHandlePostUpdateWorkflow_Success(t *testing.T) {
	db := &mockDBTX{}
	server, _ := setupTestServer(t, db)

	r := newFormRequest(t, url.Values{
		"id":            {"agent_1"},
		"name":          {"Updated Agent"},
		"system_prompt": {"be helpful"},
	})
	w := httptest.NewRecorder()
	server.handlePostUpdateWorkflow(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Workflow updated successfully") {
		t.Errorf("expected success banner, got %q", w.Body.String())
	}
}

func TestHandlePostUpdateWorkflow_MalformedForm(t *testing.T) {
	db := &mockDBTX{}
	server, _ := setupTestServer(t, db)

	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("%zz"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	server.handlePostUpdateWorkflow(w, r)

	if !strings.Contains(w.Body.String(), "Malformed form submission") {
		t.Errorf("expected malformed-form error, got %q", w.Body.String())
	}
}
