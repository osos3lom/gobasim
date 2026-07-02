package monitor

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"runtime/debug"
	"sawt-go/internal/trace"
	"time"
)

var webhookURL string

// Init configures the error-report webhook (Slack/Discord/generic JSON
// receiver). Empty means log-only.
func Init(url string) {
	webhookURL = url
	if url != "" {
		log.Println("Monitor: error reporting webhook configured.")
	} else {
		log.Println("Monitor: no ERROR_WEBHOOK_URL set — errors are logged only.")
	}
}

type report struct {
	Component string `json:"component"`
	Error     string `json:"error"`
	TraceID   string `json:"trace_id,omitempty"`
	Stack     string `json:"stack,omitempty"`
	Ts        string `json:"ts"`
	Text      string `json:"text"` // Slack/Discord-compatible summary
}

// ReportError logs the failure and, when configured, posts it to the webhook
// asynchronously so the caller's latency is unaffected.
func ReportError(ctx context.Context, component string, err error) {
	if err == nil {
		return
	}
	trace.Logf(ctx, "Monitor: [%s] %v", component, err)
	dispatch(report{
		Component: component,
		Error:     err.Error(),
		TraceID:   trace.ID(ctx),
		Ts:        time.Now().UTC().Format(time.RFC3339),
		Text:      "sawt-gateway error in " + component + ": " + err.Error(),
	})
}

// ReportPanic reports a recovered panic with its stack.
func ReportPanic(component string, recovered interface{}) {
	stack := string(debug.Stack())
	log.Printf("Monitor: PANIC in [%s]: %v\n%s", component, recovered, stack)
	dispatch(report{
		Component: component,
		Error:     "panic: " + toString(recovered),
		Stack:     stack,
		Ts:        time.Now().UTC().Format(time.RFC3339),
		Text:      "sawt-gateway PANIC in " + component + ": " + toString(recovered),
	})
}

func toString(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	if e, ok := v.(error); ok {
		return e.Error()
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func dispatch(r report) {
	if webhookURL == "" {
		return
	}
	go func() {
		body, err := json.Marshal(r)
		if err != nil {
			return
		}
		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Post(webhookURL, "application/json", bytes.NewReader(body))
		if err != nil {
			log.Printf("Monitor: failed to post error report: %v", err)
			return
		}
		resp.Body.Close()
	}()
}
