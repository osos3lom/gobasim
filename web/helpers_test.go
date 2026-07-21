package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientIP(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "203.0.113.5:54321"
	if got := clientIP(r); got != "203.0.113.5" {
		t.Errorf("clientIP = %q, want 203.0.113.5", got)
	}

	r2 := httptest.NewRequest("GET", "/", nil)
	r2.RemoteAddr = "not-a-host-port"
	if got := clientIP(r2); got != "not-a-host-port" {
		t.Errorf("clientIP fallback = %q", got)
	}
}

func TestAgentIDValue(t *testing.T) {
	if got := agentIDValue(nil); got != "" {
		t.Errorf("agentIDValue(nil) = %q", got)
	}
	id := "agent_1"
	if got := agentIDValue(&id); got != "agent_1" {
		t.Errorf("agentIDValue(&id) = %q", got)
	}
}

func TestJsonOrDefault(t *testing.T) {
	if got := jsonOrDefault(nil, "[]"); got != "[]" {
		t.Errorf("jsonOrDefault(nil) = %q", got)
	}
	if got := jsonOrDefault([]byte(`{"a":1}`), "[]"); got != `{"a":1}` {
		t.Errorf("jsonOrDefault(raw) = %q", got)
	}
}

func TestFeedbackErr(t *testing.T) {
	rec := httptest.NewRecorder()
	feedbackErr(rec, "<script>bad</script>")
	body := rec.Body.String()
	if strings.Contains(body, "<script>") {
		t.Errorf("expected reason to be HTML-escaped, got %q", body)
	}
	if !strings.Contains(body, "bg-red-900") {
		t.Errorf("expected error banner styling, got %q", body)
	}
}

func TestFormBool(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("checked=on"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if !formBool(r, "checked") {
		t.Error("expected formBool true when field present")
	}
	if formBool(r, "absent") {
		t.Error("expected formBool false when field absent")
	}
}

func TestEmptyToArray(t *testing.T) {
	if got := emptyToArray(""); got != "[]" {
		t.Errorf("emptyToArray(\"\") = %q", got)
	}
	if got := emptyToArray("   "); got != "[]" {
		t.Errorf("emptyToArray(whitespace) = %q", got)
	}
	if got := emptyToArray(`["a"]`); got != `["a"]` {
		t.Errorf("emptyToArray(non-empty) = %q", got)
	}
}

func TestAtoiDefault(t *testing.T) {
	if got := atoiDefault("42", 0); got != 42 {
		t.Errorf("atoiDefault(valid) = %d", got)
	}
	if got := atoiDefault("not-a-number", 7); got != 7 {
		t.Errorf("atoiDefault(invalid) = %d", got)
	}
	if got := atoiDefault("  9  ", 0); got != 9 {
		t.Errorf("atoiDefault(whitespace) = %d", got)
	}
}

func TestAtofDefault(t *testing.T) {
	if got := atofDefault("1.5", 0); got != 1.5 {
		t.Errorf("atofDefault(valid) = %v", got)
	}
	if got := atofDefault("nope", 2.5); got != 2.5 {
		t.Errorf("atofDefault(invalid) = %v", got)
	}
}
