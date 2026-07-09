package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"sawt-go/database"
	"sawt-go/internal/trace"
	"strings"
	"time"
)

// confirmationTTL is how long a pending confirmation stays actionable.
const confirmationTTL = 10 * time.Minute

// toolRisk mirrors the mshalia tool contract's risk facet. Reads are low;
// anything that mutates ERP state is medium or high and requires an explicit
// user confirmation before execution. Unknown tools default to medium so a
// newly added tool can never silently bypass confirmation.
var toolRisk = map[string]string{
	// operations
	"get_horse":             "low",
	"get_care_plan":         "low",
	"list_tasks":            "low",
	"update_task_status":    "medium",
	"list_horses":           "low",
	"list_stalls":           "low",
	"get_stall_availability": "low",
	"assign_stall":          "medium",
	"register_horse":        "medium",
	"check_in_horse":        "medium",
	"check_out_horse":       "medium",
	"report_incident":       "medium",
	"list_incidents":        "low",
	"book_vet_appointment":  "medium",
	"record_treatment_plan": "medium",

	// accounting — financial writes are always high
	"list_invoices":  "low",
	"get_invoice":    "low",
	"record_expense": "high",
	"record_payment": "high",

	// administration
	"list_clients":   "low",
	"get_client":     "low",
	"list_contracts": "low",
	"get_contract":   "low",

	// client self-service (all reads are low)
	"list_my_horses":    "low",
	"get_my_horse":      "low",
	"list_my_invoices":  "low",
	"get_my_balance":    "low",
	"get_my_statement":  "low",
	"list_my_contracts": "low",

	// sales
	"list_available_horses": "low",
	"list_available_stalls": "low",
	"list_packages":         "low",
	"book_tour":             "medium",
	"submit_inquiry":        "medium",

	// breeding
	"list_breeding_stock":  "low",
	"book_breeding":        "medium",
	"get_pregnancy_status": "low",
	"list_foals":           "low",
	"recommend_bloodline":  "low",
}

func riskOf(toolID string) string {
	if r, ok := toolRisk[toolID]; ok {
		return r
	}
	return "medium"
}

// affirmations are the exact (normalized) replies accepted as a confirmation.
// Anything else cancels the pending action.
var affirmations = map[string]bool{
	"نعم": true, "أكد": true, "اكد": true, "تأكيد": true, "تاكيد": true,
	"تمام": true, "موافق": true, "ايوه": true, "أيوه": true, "اي": true,
	"yes": true, "y": true, "ok": true, "okay": true, "confirm": true,
	"sure": true, "proceed": true, "do it": true,
}

func isAffirmation(text string) bool {
	normalized := strings.ToLower(strings.Trim(strings.TrimSpace(text), ".,!؟?"))
	return affirmations[normalized]
}

// describePendingAction builds the human-readable restatement of the action
// the user is being asked to confirm.
func describePendingAction(toolID string, args map[string]interface{}) string {
	switch toolID {
	case "update_task_status":
		return fmt.Sprintf("تحديث حالة المهمة %v إلى \"%v\"", args["taskId"], args["status"])
	case "record_expense":
		return fmt.Sprintf("تسجيل مصروف بمبلغ %v ريال في فئة \"%v\"", args["amount"], args["category"])
	case "record_payment":
		return fmt.Sprintf("تسجيل دفعة بمبلغ %v ريال على الفاتورة %v", args["amount"], args["invoiceId"])
	default:
		argsJSON, _ := json.Marshal(args)
		return fmt.Sprintf("تنفيذ العملية %s بالمعطيات %s", toolID, string(argsJSON))
	}
}

// requestConfirmation stores the pending action and turns the reply into a
// confirmation question instead of executing the tool.
func (e *WorkflowEngine) requestConfirmation(ctx context.Context, state *State, toolID string, args map[string]interface{}) error {
	argsBytes, err := json.Marshal(args)
	if err != nil {
		return fmt.Errorf("failed to marshal pending args: %w", err)
	}

	description := describePendingAction(toolID, args)

	if err := e.queries.UpsertPendingConfirmation(ctx, database.UpsertPendingConfirmationParams{
		ChatID:        state.ChatID,
		ToolID:        toolID,
		Args:          argsBytes,
		OrgID:         state.ActorIdentity.OrgIDs[0],
		ActingUserUid: state.ActorIdentity.UID,
		Description:   description,
		ExpiresAt:     time.Now().Add(confirmationTTL),
	}); err != nil {
		return fmt.Errorf("failed to store pending confirmation: %w", err)
	}

	// Audit the request itself, not just the eventual outcome.
	state.ToolResults = append(state.ToolResults, map[string]interface{}{
		"tool":   toolID,
		"args":   args,
		"output": map[string]interface{}{"status": "pending_confirmation"},
	})

	state.FinalReply = fmt.Sprintf("هل تريد تأكيد: %s؟ أرسل \"نعم\" للتأكيد أو أي رد آخر للإلغاء.", description)
	trace.Logf(ctx, "[workflow] Confirmation requested for %s tool '%s' on chat %s", riskOf(toolID), toolID, state.ChatID)
	return nil
}

// resolvePendingConfirmation checks whether this chat is waiting on a yes/no
// and handles the incoming message accordingly. It reports handled=true when
// the message was consumed by the confirmation flow.
func (e *WorkflowEngine) resolvePendingConfirmation(ctx context.Context, state *State) (bool, error) {
	if e.queries == nil || state.ChatID == "" {
		return false, nil
	}

	pending, err := e.queries.GetPendingConfirmation(ctx, state.ChatID)
	if err != nil {
		// No pending row — the common case.
		return false, nil
	}

	// Expired confirmations are cancelled silently; the new message is then
	// processed as a fresh request.
	if time.Now().After(pending.ExpiresAt) {
		_ = e.queries.DeletePendingConfirmation(ctx, state.ChatID)
		state.ToolResults = append(state.ToolResults, map[string]interface{}{
			"tool":   pending.ToolID,
			"output": map[string]interface{}{"status": "confirmation_expired"},
		})
		trace.Logf(ctx, "[workflow] Pending confirmation for '%s' on chat %s expired", pending.ToolID, state.ChatID)
		return false, nil
	}

	var lastUserText string
	for i := len(state.Messages) - 1; i >= 0; i-- {
		if state.Messages[i].Role == "user" {
			lastUserText = state.Messages[i].Content
			break
		}
	}

	if err := e.queries.DeletePendingConfirmation(ctx, state.ChatID); err != nil {
		return false, fmt.Errorf("failed to clear pending confirmation: %w", err)
	}

	if !isAffirmation(lastUserText) {
		state.ToolResults = append(state.ToolResults, map[string]interface{}{
			"tool":   pending.ToolID,
			"output": map[string]interface{}{"status": "cancelled"},
		})
		state.FinalReply = "حسناً، ألغيت العملية. أرسل طلبك من جديد إذا أردت تنفيذ شيء آخر."
		trace.Logf(ctx, "[workflow] Pending confirmation for '%s' on chat %s cancelled by user", pending.ToolID, state.ChatID)
		return true, nil
	}

	var args map[string]interface{}
	if err := json.Unmarshal(pending.Args, &args); err != nil {
		args = map[string]interface{}{}
	}

	result, err := e.erpClient.CallTool(ctx, pending.ToolID, pending.OrgID, pending.ActingUserUid, args)
	if err != nil {
		result = map[string]interface{}{"ok": false, "error": err.Error()}
	}

	state.ToolResults = append(state.ToolResults, map[string]interface{}{
		"tool":   pending.ToolID,
		"args":   args,
		"output": result,
		"status": "confirmed_executed",
	})

	if ok, _ := result["ok"].(bool); ok {
		state.FinalReply = fmt.Sprintf("تم: %s ✅", pending.Description)
	} else {
		errText, _ := result["error"].(string)
		if errText == "" {
			errText = "خطأ غير معروف"
		}
		state.FinalReply = fmt.Sprintf("تعذر تنفيذ العملية: %s", errText)
	}

	trace.Logf(ctx, "[workflow] Confirmed action '%s' executed on chat %s", pending.ToolID, state.ChatID)
	return true, nil
}
