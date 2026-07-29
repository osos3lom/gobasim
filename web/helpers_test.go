package web

import (
	"net/http"
	"net/http/httptest"
	"regexp"
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

// The dashboard is entirely htmx-driven, so htmx must be served from our own
// origin and embedded in the binary. A previous revision shipped a 215-byte
// stub here that merely re-requested the CDN it was supposed to replace, and
// the file was missing from the //go:embed list entirely — meaning this route
// 404'd in the built binary and any CDN outage silently made the console
// read-only. These assertions lock that shut.
func TestStaticHtmxIsEmbeddedAndServed(t *testing.T) {
	db := &mockDBTX{}
	server, _ := setupTestServer(t, db)

	req := httptest.NewRequest(http.MethodGet, "/static/htmx.min.js", nil)
	w := httptest.NewRecorder()
	server.GetRouter().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /static/htmx.min.js = %d, want 200 (is it in the //go:embed list?)", w.Code)
	}
	body := w.Body.String()
	if len(body) < 40000 {
		t.Errorf("htmx.min.js is %d bytes — too small to be the real library", len(body))
	}
	if !strings.Contains(body, "htmx") {
		t.Error("served asset does not look like htmx")
	}
	if strings.Contains(body, "unpkg.com") {
		t.Error("served htmx still points at the CDN — the circular stub is back")
	}
}

// The layout must load htmx locally, not from a third-party CDN.
func TestLayoutLoadsHtmxLocally(t *testing.T) {
	layout, err := templatesFS.ReadFile("templates/layout.html")
	if err != nil {
		t.Fatalf("read layout.html: %v", err)
	}
	if !strings.Contains(string(layout), `src="/static/htmx.min.js"`) {
		t.Error("layout.html does not load htmx from /static/")
	}
	// Match an actual remote load, not any mention — the surrounding comment
	// legitimately names the CDN it replaced.
	if strings.Contains(string(layout), `src="https://unpkg.com`) {
		t.Error("layout.html still loads a script from unpkg.com")
	}
}

// The CSP allows scripts only from 'self'. That is only safe to keep if no
// template reintroduces an inline <script> or an on*= handler — both are
// blocked outright by this policy, and the failure is silent in the browser.
// Behaviour belongs in web/static/*.js, bound via data-attributes (ui.js).
func TestTemplatesContainNoInlineScripts(t *testing.T) {
	entries, err := templatesFS.ReadDir("templates")
	if err != nil {
		t.Fatalf("read templates dir: %v", err)
	}
	inlineHandler := regexp.MustCompile(`\son(click|change|submit|input|load|error)\s*=`)

	for _, e := range entries {
		body, err := templatesFS.ReadFile("templates/" + e.Name())
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		src := string(body)

		// An inline block is <script> with no src= attribute.
		for _, tag := range regexp.MustCompile(`<script[^>]*>`).FindAllString(src, -1) {
			if !strings.Contains(tag, "src=") {
				t.Errorf("%s: inline <script> block is blocked by the CSP — move it to web/static/*.js", e.Name())
			}
		}
		if m := inlineHandler.FindString(src); m != "" {
			t.Errorf("%s: inline event handler %q is blocked by the CSP — use a data-attribute handled in ui.js", e.Name(), strings.TrimSpace(m))
		}
		// htmx implements hx-on via new Function(), which needs 'unsafe-eval'.
		if strings.Contains(src, "hx-on") {
			t.Errorf("%s: hx-on requires 'unsafe-eval'; use an htmx event listener in a static .js file", e.Name())
		}
	}
}

func TestCSPDisallowsInlineAndEvalScripts(t *testing.T) {
	rec := httptest.NewRecorder()
	securityHeaders(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).
		ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	csp := rec.Header().Get("Content-Security-Policy")
	if csp == "" {
		t.Fatal("no Content-Security-Policy header set")
	}
	if !strings.Contains(csp, "script-src 'self';") {
		t.Errorf("expected script-src to be exactly 'self', got: %s", csp)
	}
	// 'unsafe-inline' remains legitimate for style-src (Tailwind emits inline
	// styles), so check the script-src directive specifically rather than the
	// whole header.
	scriptSrc := csp
	if i := strings.Index(csp, "script-src "); i >= 0 {
		scriptSrc = csp[i:]
		if j := strings.Index(scriptSrc, ";"); j >= 0 {
			scriptSrc = scriptSrc[:j]
		}
	}
	for _, banned := range []string{"'unsafe-inline'", "'unsafe-eval'", "unpkg.com"} {
		if strings.Contains(scriptSrc, banned) {
			t.Errorf("script-src must not allow %s, got: %s", banned, scriptSrc)
		}
	}
}
