package workflow

import (
	"context"
	"strings"
	"testing"
)

// evalScenario is one scripted conversation run through the full Execute()
// pipeline with a deterministic fake LLM. These catch an obviously broken
// system prompt, router, or tool schema before it reaches a real user.
type evalScenario struct {
	name string
	// userText is the inbound WhatsApp message.
	userText string
	// intent is what the (faked) classifier returns.
	intent string
	// llmToolCall, when set, is the tool the faked model requests on the
	// first loop iteration (with llmToolArgs as raw JSON).
	llmToolCall string
	llmToolArgs string
	// finalContent is the faked model's final plain-text answer.
	finalContent string
	// wantToolExecuted asserts a tool result with this name was recorded.
	wantToolExecuted string
	// wantConfirmation asserts the reply is a confirmation question and no
	// tool executed.
	wantConfirmation bool
	// wantReplyContains asserts on the final reply text.
	wantReplyContains string
}

func TestEvalScriptedConversations(t *testing.T) {
	scenarios := []evalScenario{
		{
			name:              "greeting stays out of the tool loop",
			userText:          "صباح الخير",
			intent:            "other",
			finalContent:      "صباح النور! كيف أقدر أساعدك؟",
			wantReplyContains: "صباح النور",
		},
		{
			name:             "horse lookup executes a read tool",
			userText:         "أين الحصان نجم؟",
			intent:           "operations",
			llmToolCall:      "get_horse",
			llmToolArgs:      `{"nameQuery":"نجم"}`,
			finalContent:     "نجم في الإسطبل A-12",
			wantToolExecuted: "get_horse",
			wantReplyContains: "A-12",
		},
		{
			name:             "task list executes a read tool",
			userText:         "شو المهام المعلقة اليوم؟",
			intent:           "operations",
			llmToolCall:      "list_tasks",
			llmToolArgs:      `{"status":"pending"}`,
			finalContent:     "عندك ثلاث مهام معلقة",
			wantToolExecuted: "list_tasks",
			wantReplyContains: "مهام",
		},
		{
			name:             "task completion requires confirmation",
			userText:         "خلصت مهمة تنظيف الإسطبل، علمها منجزة",
			intent:           "operations",
			llmToolCall:      "update_task_status",
			llmToolArgs:      `{"taskId":"t7","status":"completed"}`,
			wantConfirmation: true,
		},
		{
			name:             "expense recording requires confirmation with amount restated",
			userText:         "سجل فاتورة علف بألف ومئتين ريال",
			intent:           "accounting",
			llmToolCall:      "record_expense",
			llmToolArgs:      `{"amount":1200,"category":"feed"}`,
			wantConfirmation: true,
			wantReplyContains: "1200",
		},
		{
			name:             "invoice query executes a read tool",
			userText:         "كم فاتورة غير مدفوعة عندنا؟",
			intent:           "accounting",
			llmToolCall:      "list_invoices",
			llmToolArgs:      `{"status":"overdue"}`,
			finalContent:     "عندكم فاتورتان متأخرتان",
			wantToolExecuted: "list_invoices",
			wantReplyContains: "فاتورتان",
		},
		{
			name:             "client lookup executes a read tool",
			userText:         "أعطني بيانات العميل أبو فهد",
			intent:           "administration",
			llmToolCall:      "list_clients",
			llmToolArgs:      `{"nameQuery":"أبو فهد"}`,
			finalContent:     "العميل أبو فهد لديه حصانان",
			wantToolExecuted: "list_clients",
			wantReplyContains: "أبو فهد",
		},
	}

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			q := &confirmFakeQuerier{}
			call := 0
			e := newConfirmTestEngine(q, func(ctx context.Context, m []Message, tools []ToolDefinition, temp float32, maxTokens int) (*Message, error) {
				call++
				if call == 1 { // ClassifyIntent
					return &Message{Role: "assistant", Content: sc.intent}, nil
				}
				if sc.llmToolCall != "" && call == 2 {
					return &Message{Role: "assistant", ToolCalls: []ToolCall{{
						ID:       "tc1",
						Type:     "function",
						Function: ToolFunction{Name: sc.llmToolCall, Arguments: sc.llmToolArgs},
					}}}, nil
				}
				return &Message{Role: "assistant", Content: sc.finalContent}, nil
			})

			state := linkedState(sc.userText)
			if err := e.Execute(context.Background(), state); err != nil {
				t.Fatalf("Execute failed: %v", err)
			}

			if sc.wantConfirmation {
				if q.pending == nil {
					t.Fatal("expected a pending confirmation")
				}
				if !strings.Contains(state.FinalReply, "تأكيد") {
					t.Fatalf("expected a confirmation question, got %q", state.FinalReply)
				}
				for _, tr := range state.ToolResults {
					if tr["status"] == "confirmed_executed" {
						t.Fatal("nothing may execute before the user confirms")
					}
				}
			}

			if sc.wantToolExecuted != "" {
				found := false
				for _, tr := range state.ToolResults {
					if tr["tool"] == sc.wantToolExecuted {
						found = true
					}
				}
				if !found {
					t.Fatalf("expected tool %q to be executed, results: %v", sc.wantToolExecuted, state.ToolResults)
				}
			}

			if sc.wantReplyContains != "" && !strings.Contains(state.FinalReply, sc.wantReplyContains) {
				t.Fatalf("expected reply to contain %q, got %q", sc.wantReplyContains, state.FinalReply)
			}
		})
	}
}
