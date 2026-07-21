package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSign(t *testing.T) {
	got := sign("secret", "1000", `{"a":1}`)
	m := hmac.New(sha256.New, []byte("secret"))
	m.Write([]byte("1000." + `{"a":1}`))
	want := hex.EncodeToString(m.Sum(nil))
	if got != want {
		t.Errorf("sign() = %q, want %q", got, want)
	}
}

func TestOkAndErrBody(t *testing.T) {
	o := ok(map[string]any{"x": 1})
	if o["ok"] != true {
		t.Errorf("ok() missing ok=true: %v", o)
	}
	e := errBody("bad", "CODE")
	if e["ok"] != false || e["error"] != "bad" || e["code"] != "CODE" {
		t.Errorf("errBody() = %v", e)
	}
}

func TestGetenv(t *testing.T) {
	t.Setenv("MOCKERP_TEST_VAR", "")
	if got := getenv("MOCKERP_TEST_VAR", "default"); got != "default" {
		t.Errorf("getenv fallback = %q", got)
	}
	t.Setenv("MOCKERP_TEST_VAR", "value")
	if got := getenv("MOCKERP_TEST_VAR", "default"); got != "value" {
		t.Errorf("getenv override = %q", got)
	}
}

func TestWriteJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	writeJSON(rec, http.StatusTeapot, map[string]any{"a": 1})
	if rec.Code != http.StatusTeapot {
		t.Errorf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode body: %v", err)
	}
	if body["a"] != float64(1) {
		t.Errorf("body = %v", body)
	}
}

func TestMockTool(t *testing.T) {
	cases := []string{
		"get_horse", "get_my_horse", "get_care_plan", "list_tasks",
		"list_horses", "list_available_horses", "list_breeding_stock",
		"list_stalls", "list_available_stalls", "get_stall_availability",
		"list_incidents", "list_invoices", "list_my_invoices", "get_invoice",
		"list_clients", "get_client", "list_contracts", "list_my_contracts",
		"get_contract", "get_my_balance", "get_my_statement", "list_packages",
		"list_foals", "get_pregnancy_status", "recommend_bloodline",
		"update_task_status", "assign_stall", "register_horse", "check_in_horse",
		"check_out_horse", "report_incident", "book_vet_appointment",
		"record_treatment_plan", "record_expense", "record_payment", "book_tour",
		"submit_inquiry", "book_breeding",
		"totally_unknown_tool",
	}
	for _, toolID := range cases {
		t.Run(toolID, func(t *testing.T) {
			got := mockTool(toolID, map[string]interface{}{"k": "v"})
			if got["ok"] != true {
				t.Errorf("mockTool(%q) not ok: %v", toolID, got)
			}
			if _, hasData := got["data"]; !hasData {
				t.Errorf("mockTool(%q) missing data: %v", toolID, got)
			}
		})
	}
}

func TestMockTool_WriteToolsEchoArgs(t *testing.T) {
	args := map[string]interface{}{"note": "hello"}
	got := mockTool("register_horse", args)
	data, ok := got["data"].(map[string]any)
	if !ok {
		t.Fatalf("expected map data, got %T", got["data"])
	}
	if data["toolId"] != "register_horse" {
		t.Errorf("toolId = %v", data["toolId"])
	}
}

func TestGuard_RejectsNonPost(t *testing.T) {
	h := guard("secret", func(w http.ResponseWriter, r *http.Request, body []byte) {
		t.Fatal("inner handler should not be called")
	})
	req := httptest.NewRequest(http.MethodGet, "/api/agent/v1/tools/list_horses", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}

func TestGuard_RejectsBadSignature(t *testing.T) {
	h := guard("secret", func(w http.ResponseWriter, r *http.Request, body []byte) {
		t.Fatal("inner handler should not be called")
	})
	req := httptest.NewRequest(http.MethodPost, "/api/agent/v1/tools/list_horses", strings.NewReader(`{}`))
	req.Header.Set("x-swa-timestamp", strconv.FormatInt(time.Now().UnixMilli(), 10))
	req.Header.Set("x-swa-signature", "not-the-right-signature")
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestGuard_RejectsStaleTimestamp(t *testing.T) {
	secret := "secret"
	body := `{}`
	staleTs := strconv.FormatInt(time.Now().Add(-10*time.Minute).UnixMilli(), 10)
	sig := sign(secret, staleTs, body)

	h := guard(secret, func(w http.ResponseWriter, r *http.Request, b []byte) {
		t.Fatal("inner handler should not be called for stale timestamp")
	})
	req := httptest.NewRequest(http.MethodPost, "/api/agent/v1/tools/list_horses", strings.NewReader(body))
	req.Header.Set("x-swa-timestamp", staleTs)
	req.Header.Set("x-swa-signature", sig)
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestGuard_AcceptsValidSignature(t *testing.T) {
	secret := "secret"
	body := `{"orgId":"org_test"}`
	ts := strconv.FormatInt(time.Now().UnixMilli(), 10)
	sig := sign(secret, ts, body)

	called := false
	h := guard(secret, func(w http.ResponseWriter, r *http.Request, b []byte) {
		called = true
		if string(b) != body {
			t.Errorf("body = %q, want %q", string(b), body)
		}
		w.WriteHeader(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodPost, "/api/agent/v1/tools/list_horses", strings.NewReader(body))
	req.Header.Set("x-swa-timestamp", ts)
	req.Header.Set("x-swa-signature", sig)
	rec := httptest.NewRecorder()
	h(rec, req)

	if !called {
		t.Error("expected inner handler to be called for a valid signature")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}
