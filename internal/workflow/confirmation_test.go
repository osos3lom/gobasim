package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"sawt-go/config"
	"sawt-go/database"
	"sawt-go/internal/erp"
)

// Extend fakeQuerier (declared in memory_test.go) with the pending-confirmation methods.
type confirmFakeQuerier struct {
	fakeQuerier
	pending    *database.PendingConfirmation
	hadPending bool
}

func (f *confirmFakeQuerier) UpsertPendingConfirmation(ctx context.Context, arg database.UpsertPendingConfirmationParams) error {
	f.pending = &database.PendingConfirmation{
		ChatID:        arg.ChatID,
		ToolID:        arg.ToolID,
		Args:          arg.Args,
		OrgID:         arg.OrgID,
		ActingUserUid: arg.ActingUserUid,
		Description:   arg.Description,
		ExpiresAt:     arg.ExpiresAt,
	}
	f.hadPending = true
	return nil
}

func (f *confirmFakeQuerier) GetPendingConfirmation(ctx context.Context, chatID string) (database.PendingConfirmation, error) {
	if f.pending == nil || f.pending.ChatID != chatID {
		return database.PendingConfirmation{}, fmt.Errorf("no rows")
	}
	return *f.pending, nil
}

func (f *confirmFakeQuerier) DeletePendingConfirmation(ctx context.Context, chatID string) error {
	f.pending = nil
	return nil
}

// GetWaContact is called by executeOperations for prompt resolution.
func (f *confirmFakeQuerier) GetWaContact(ctx context.Context, chatID string) (database.WaContact, error) {
	return database.WaContact{}, fmt.Errorf("no rows")
}

func newConfirmTestEngine(q database.Querier, fake completionFn) *WorkflowEngine {
	e := NewWorkflowEngine(&config.Config{NimModel: "test-model"}, erp.NewClient("http://localhost:0", ""), q)
	e.complete = fake
	return e
}

func TestRiskyToolCallRequestsConfirmationInsteadOfExecuting(t *testing.T) {
	q := &confirmFakeQuerier{}
	call := 0
	e := newConfirmTestEngine(q, func(ctx context.Context, m []Message, tools []ToolDefinition, temp float32, maxTokens int) (*Message, error) {
		call++
		if call == 1 { // ClassifyIntent
			return &Message{Role: "assistant", Content: "operations"}, nil
		}
		return &Message{Role: "assistant", ToolCalls: []ToolCall{{
			ID:       "tc1",
			Type:     "function",
			Function: ToolFunction{Name: "update_task_status", Arguments: `{"taskId":"t42","status":"completed"}`},
		}}}, nil
	})

	state := linkedState("mark task t42 as done")
	if err := e.Execute(context.Background(), state); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if q.pending == nil {
		t.Fatal("expected a pending confirmation to be stored")
	}
	if q.pending.ToolID != "update_task_status" {
		t.Fatalf("expected pending update_task_status, got %q", q.pending.ToolID)
	}
	if !strings.Contains(state.FinalReply, "تأكيد") {
		t.Fatalf("expected a confirmation question, got %q", state.FinalReply)
	}
	// The audit trail must show the request, and nothing must have executed.
	if len(state.ToolResults) != 1 {
		t.Fatalf("expected exactly 1 audit entry, got %d", len(state.ToolResults))
	}
	out, _ := state.ToolResults[0]["output"].(map[string]interface{})
	if out["status"] != "pending_confirmation" {
		t.Fatalf("expected pending_confirmation audit status, got %v", out)
	}
}

func TestAffirmationExecutesPendingAction(t *testing.T) {
	args, _ := json.Marshal(map[string]interface{}{"taskId": "t42", "status": "completed"})
	q := &confirmFakeQuerier{pending: &database.PendingConfirmation{
		ChatID:        "123@s.whatsapp.net",
		ToolID:        "update_task_status",
		Args:          args,
		OrgID:         "org1",
		ActingUserUid: "u1",
		Description:   "تحديث حالة المهمة t42",
		ExpiresAt:     time.Now().Add(5 * time.Minute),
	}}

	e := newConfirmTestEngine(q, func(ctx context.Context, m []Message, tools []ToolDefinition, temp float32, maxTokens int) (*Message, error) {
		t.Fatal("LLM must not be called when resolving a confirmation")
		return nil, nil
	})

	state := linkedState("نعم")
	if err := e.Execute(context.Background(), state); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if q.pending != nil {
		t.Fatal("expected pending confirmation to be cleared after execution")
	}
	if len(state.ToolResults) != 1 || state.ToolResults[0]["status"] != "confirmed_executed" {
		t.Fatalf("expected confirmed_executed audit entry, got %v", state.ToolResults)
	}
	// The unconfigured ERP client returns ok=false/UNCONFIGURED, so the reply
	// must surface a failure rather than claim success.
	if !strings.Contains(state.FinalReply, "تعذر") {
		t.Fatalf("expected a failure reply from unconfigured ERP, got %q", state.FinalReply)
	}
}

func TestNonAffirmationCancelsPendingAction(t *testing.T) {
	args, _ := json.Marshal(map[string]interface{}{"taskId": "t42", "status": "completed"})
	q := &confirmFakeQuerier{pending: &database.PendingConfirmation{
		ChatID:    "123@s.whatsapp.net",
		ToolID:    "update_task_status",
		Args:      args,
		OrgID:     "org1",
		ExpiresAt: time.Now().Add(5 * time.Minute),
	}}

	e := newConfirmTestEngine(q, func(ctx context.Context, m []Message, tools []ToolDefinition, temp float32, maxTokens int) (*Message, error) {
		t.Fatal("LLM must not be called when cancelling a confirmation")
		return nil, nil
	})

	state := linkedState("لا خليها زي ما هي")
	if err := e.Execute(context.Background(), state); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if q.pending != nil {
		t.Fatal("expected pending confirmation to be cleared after cancellation")
	}
	if len(state.ToolResults) != 1 {
		t.Fatalf("expected 1 audit entry, got %d", len(state.ToolResults))
	}
	out, _ := state.ToolResults[0]["output"].(map[string]interface{})
	if out["status"] != "cancelled" {
		t.Fatalf("expected cancelled audit status, got %v", out)
	}
	if !strings.Contains(state.FinalReply, "ألغيت") {
		t.Fatalf("expected cancellation reply, got %q", state.FinalReply)
	}
}

func TestExpiredConfirmationIsDroppedAndMessageProcessedNormally(t *testing.T) {
	args, _ := json.Marshal(map[string]interface{}{"taskId": "t42", "status": "completed"})
	q := &confirmFakeQuerier{pending: &database.PendingConfirmation{
		ChatID:    "123@s.whatsapp.net",
		ToolID:    "update_task_status",
		Args:      args,
		ExpiresAt: time.Now().Add(-time.Minute), // already expired
	}}

	e := newConfirmTestEngine(q, func(ctx context.Context, m []Message, tools []ToolDefinition, temp float32, maxTokens int) (*Message, error) {
		// Normal processing resumes: classify as other, then general chat.
		if len(m) == 2 && strings.Contains(m[0].Content, "Classify") {
			return &Message{Role: "assistant", Content: "other"}, nil
		}
		return &Message{Role: "assistant", Content: "أهلاً!"}, nil
	})

	state := linkedState("مرحبا")
	if err := e.Execute(context.Background(), state); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if q.pending != nil {
		t.Fatal("expected expired confirmation to be deleted")
	}
	if state.FinalReply != "أهلاً!" {
		t.Fatalf("expected the message to be processed normally after expiry, got %q", state.FinalReply)
	}
}

func TestIsAffirmation(t *testing.T) {
	for _, yes := range []string{"نعم", "نعم.", " تأكيد ", "yes", "OK", "Confirm"} {
		if !isAffirmation(yes) {
			t.Errorf("expected %q to be an affirmation", yes)
		}
	}
	for _, no := range []string{"لا", "no", "maybe", "نعم ولكن غير الموعد", ""} {
		if isAffirmation(no) {
			t.Errorf("expected %q NOT to be an affirmation", no)
		}
	}
}

func TestUnknownToolDefaultsToMediumRisk(t *testing.T) {
	if riskOf("some_future_tool") != "medium" {
		t.Fatal("unknown tools must default to medium risk (confirmation required)")
	}
	if riskOf("get_horse") != "low" {
		t.Fatal("get_horse must stay low risk")
	}
}

func TestRecordExpenseRequiresConfirmation(t *testing.T) {
	q := &confirmFakeQuerier{}
	call := 0
	e := newConfirmTestEngine(q, func(ctx context.Context, m []Message, tools []ToolDefinition, temp float32, maxTokens int) (*Message, error) {
		call++
		if call == 1 { // ClassifyIntent
			return &Message{Role: "assistant", Content: "accounting"}, nil
		}
		return &Message{Role: "assistant", ToolCalls: []ToolCall{{
			ID:       "tc1",
			Type:     "function",
			Function: ToolFunction{Name: "record_expense", Arguments: `{"amount":1200,"category":"feed"}`},
		}}}, nil
	})

	state := linkedState("سجل فاتورة علف بـ 1200 ريال")
	if err := e.Execute(context.Background(), state); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if q.pending == nil || q.pending.ToolID != "record_expense" {
		t.Fatalf("expected pending record_expense confirmation, got %+v", q.pending)
	}
	// The restatement must include the amount so the user confirms the real number.
	if !strings.Contains(state.FinalReply, "1200") {
		t.Fatalf("expected the amount restated in the confirmation, got %q", state.FinalReply)
	}
}
