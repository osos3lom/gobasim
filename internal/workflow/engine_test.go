package workflow

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sawt-go/config"
	"sawt-go/internal/erp"
)

// newTestEngine returns an engine whose LLM calls are served by fake, with an
// unconfigured ERP client (tool calls return UNCONFIGURED without network).
func newTestEngine(fake completionFn) *WorkflowEngine {
	e := NewWorkflowEngine(&config.Config{NimModel: "test-model"}, erp.NewClient("http://localhost:0", ""), nil)
	e.complete = fake
	return e
}

func linkedState(text string) *State {
	return &State{
		Messages:      []Message{{Role: "user", Content: text}},
		ActorIdentity: &erp.Identity{UID: "u1", OrgIDs: []string{"org1"}},
		ChatID:        "123@s.whatsapp.net",
	}
}

func TestClassifyIntentCleansLLMOutput(t *testing.T) {
	cases := []struct {
		llmOutput string
		want      string
	}{
		{"operations", "operations"},
		{" Operations.\n", "operations"},
		{"ACCOUNTING", "accounting"},
		{"administration!", "administration"},
		{"other", "other"},
		{"a whole sentence instead of one word", "operations"}, // safe default
		{"", "operations"}, // safe default
	}

	for _, tc := range cases {
		e := newTestEngine(func(ctx context.Context, m []Message, tools []ToolDefinition, temp float32, maxTokens int) (*Message, error) {
			return &Message{Role: "assistant", Content: tc.llmOutput}, nil
		})
		state := linkedState("hello")
		if err := e.ClassifyIntent(context.Background(), state); err != nil {
			t.Fatalf("ClassifyIntent(%q) unexpected error: %v", tc.llmOutput, err)
		}
		if state.Intent != tc.want {
			t.Errorf("ClassifyIntent(%q) = %q, want %q", tc.llmOutput, state.Intent, tc.want)
		}
	}
}

func TestClassifyIntentDefaultsToOperationsOnLLMError(t *testing.T) {
	e := newTestEngine(func(ctx context.Context, m []Message, tools []ToolDefinition, temp float32, maxTokens int) (*Message, error) {
		return nil, fmt.Errorf("llm down")
	})
	state := linkedState("hello")
	if err := e.ClassifyIntent(context.Background(), state); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state.Intent != "operations" {
		t.Fatalf("expected fallback intent 'operations', got %q", state.Intent)
	}
}

func TestExecuteUnlinkedUserGetsNotLinkedReply(t *testing.T) {
	e := newTestEngine(func(ctx context.Context, m []Message, tools []ToolDefinition, temp float32, maxTokens int) (*Message, error) {
		return &Message{Role: "assistant", Content: "operations"}, nil
	})
	state := linkedState("update task t1")
	state.ActorIdentity = nil

	if err := e.Execute(context.Background(), state); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(state.FinalReply, "linked") {
		t.Fatalf("expected 'not linked' reply, got %q", state.FinalReply)
	}
}

func TestExecuteToolLoopHappyPath(t *testing.T) {
	call := 0
	e := newTestEngine(func(ctx context.Context, m []Message, tools []ToolDefinition, temp float32, maxTokens int) (*Message, error) {
		call++
		switch call {
		case 1: // ClassifyIntent
			return &Message{Role: "assistant", Content: "operations"}, nil
		case 2: // first loop iteration: request one tool
			return &Message{Role: "assistant", ToolCalls: []ToolCall{{
				ID:   "tc1",
				Type: "function",
				Function: ToolFunction{
					Name:      "get_horse",
					Arguments: `{"nameQuery":"Najm"}`,
				},
			}}}, nil
		default: // second iteration: final answer
			return &Message{Role: "assistant", Content: "الحصان نجم موجود في الإسطبل A-12"}, nil
		}
	})

	state := linkedState("أين نجم؟")
	if err := e.Execute(context.Background(), state); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if state.FinalReply != "الحصان نجم موجود في الإسطبل A-12" {
		t.Fatalf("unexpected final reply: %q", state.FinalReply)
	}
	if len(state.ToolResults) != 1 {
		t.Fatalf("expected 1 tool result, got %d", len(state.ToolResults))
	}
	if state.ToolResults[0]["tool"] != "get_horse" {
		t.Fatalf("expected get_horse tool result, got %v", state.ToolResults[0])
	}
}

func TestExecuteToolLoopStopsAtMaxIterations(t *testing.T) {
	call := 0
	e := newTestEngine(func(ctx context.Context, m []Message, tools []ToolDefinition, temp float32, maxTokens int) (*Message, error) {
		call++
		if call == 1 { // ClassifyIntent
			return &Message{Role: "assistant", Content: "operations"}, nil
		}
		// Always request another tool call — the loop must cut off.
		return &Message{Role: "assistant", ToolCalls: []ToolCall{{
			ID:       fmt.Sprintf("tc%d", call),
			Type:     "function",
			Function: ToolFunction{Name: "list_tasks", Arguments: `{}`},
		}}}, nil
	})

	state := linkedState("list everything forever")
	if err := e.Execute(context.Background(), state); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if state.FinalReply == "" {
		t.Fatal("expected a fallback reply when the loop hits maxIterations")
	}
	if len(state.ToolResults) != 4 {
		t.Fatalf("expected exactly maxIterations(4) tool results, got %d", len(state.ToolResults))
	}
}

func TestExecuteAccountingAndAdministrationAreStubbed(t *testing.T) {
	for _, intent := range []string{"accounting", "administration"} {
		e := newTestEngine(func(ctx context.Context, m []Message, tools []ToolDefinition, temp float32, maxTokens int) (*Message, error) {
			return &Message{Role: "assistant", Content: intent}, nil
		})
		state := linkedState("some request")
		if err := e.Execute(context.Background(), state); err != nil {
			t.Fatalf("unexpected error for %s: %v", intent, err)
		}
		if state.FinalReply == "" {
			t.Fatalf("expected a stub reply for %s intent", intent)
		}
	}
}

func TestChatCompletionsFallsBackToSecondProvider(t *testing.T) {
	// Primary always errors.
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer failing.Close()

	// Fallback succeeds.
	working := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"from fallback"}}]}`))
	}))
	defer working.Close()

	e := NewWorkflowEngine(&config.Config{}, erp.NewClient("http://localhost:0", ""), nil)
	e.providers = []llmProvider{
		{Name: "primary", BaseURL: failing.URL, APIKey: "k1", Model: "m1"},
		{Name: "fallback", BaseURL: working.URL, APIKey: "k2", Model: "m2"},
	}

	msg, err := e.chatCompletions(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, 0, 10)
	if err != nil {
		t.Fatalf("expected fallback to succeed, got error: %v", err)
	}
	if msg.Content != "from fallback" {
		t.Fatalf("expected fallback content, got %q", msg.Content)
	}
}

func TestChatCompletionsErrorsWhenNoProviders(t *testing.T) {
	e := NewWorkflowEngine(&config.Config{}, erp.NewClient("http://localhost:0", ""), nil)
	e.providers = nil

	if _, err := e.chatCompletions(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, 0, 10); err == nil {
		t.Fatal("expected an error when no LLM providers are configured")
	}
}
