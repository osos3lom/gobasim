package monitor

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"sawt-go/internal/trace"
)

func resetWebhook(t *testing.T) {
	t.Helper()
	prev := webhookURL
	t.Cleanup(func() { webhookURL = prev })
}

func TestInit(t *testing.T) {
	resetWebhook(t)
	Init("")
	if webhookURL != "" {
		t.Errorf("expected empty webhookURL, got %q", webhookURL)
	}
	Init("https://example.com/hook")
	if webhookURL != "https://example.com/hook" {
		t.Errorf("expected webhookURL set, got %q", webhookURL)
	}
}

func TestReportError_NilIsNoop(t *testing.T) {
	resetWebhook(t)
	Init("")
	// Should not panic or dispatch anything.
	ReportError(context.Background(), "test-component", nil)
}

func TestReportError_DispatchesToWebhook(t *testing.T) {
	resetWebhook(t)

	received := make(chan report, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rpt report
		if err := json.NewDecoder(r.Body).Decode(&rpt); err != nil {
			t.Errorf("failed to decode webhook payload: %v", err)
		}
		received <- rpt
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	Init(srv.URL)

	ctx := trace.With(context.Background(), "trace-xyz")
	ReportError(ctx, "my-component", errors.New("boom"))

	select {
	case rpt := <-received:
		if rpt.Component != "my-component" {
			t.Errorf("Component = %q", rpt.Component)
		}
		if rpt.Error != "boom" {
			t.Errorf("Error = %q", rpt.Error)
		}
		if rpt.TraceID != "trace-xyz" {
			t.Errorf("TraceID = %q", rpt.TraceID)
		}
		if rpt.Text == "" {
			t.Error("expected non-empty Text summary")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for webhook post")
	}
}

func TestReportPanic_DispatchesToWebhook(t *testing.T) {
	resetWebhook(t)

	received := make(chan report, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rpt report
		_ = json.NewDecoder(r.Body).Decode(&rpt)
		received <- rpt
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	Init(srv.URL)
	ReportPanic("panicking-component", "something broke")

	select {
	case rpt := <-received:
		if rpt.Component != "panicking-component" {
			t.Errorf("Component = %q", rpt.Component)
		}
		if rpt.Stack == "" {
			t.Error("expected non-empty Stack")
		}
		if rpt.Error != "panic: something broke" {
			t.Errorf("Error = %q", rpt.Error)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for webhook post")
	}
}

func TestReportError_NoWebhookConfigured(t *testing.T) {
	resetWebhook(t)
	Init("")
	// Should log only, no network call, no panic.
	ReportError(context.Background(), "comp", errors.New("no webhook set"))
}

func TestToString(t *testing.T) {
	if got := toString("plain string"); got != "plain string" {
		t.Errorf("toString(string) = %q", got)
	}
	if got := toString(errors.New("an error")); got != "an error" {
		t.Errorf("toString(error) = %q", got)
	}
	if got := toString(42); got != "42" {
		t.Errorf("toString(int) = %q", got)
	}
}
