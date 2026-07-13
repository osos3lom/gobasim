package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"sawt-go/database"
)

// UpsertCollecting/ClaimCollecting extend confirmFakeQuerier (declared in
// confirmation_test.go) exactly the way UpsertPendingConfirmation/
// ClaimPendingConfirmation already do: pending_confirmations is one table
// regardless of status, so the fake reuses the same f.pending slot.

func (f *confirmFakeQuerier) UpsertCollecting(ctx context.Context, arg database.UpsertCollectingParams) error {
	f.pending = &database.PendingConfirmation{
		ChatID:        arg.ChatID,
		ToolID:        arg.ToolID,
		Args:          arg.Args,
		OrgID:         arg.OrgID,
		ActingUserUid: arg.ActingUserUid,
		Description:   arg.Description,
		Status:        "collecting",
		ExpiresAt:     arg.ExpiresAt,
		MissingFields: arg.MissingFields,
		CollectRounds: arg.CollectRounds,
		Intent:        arg.Intent,
	}
	f.hadPending = true
	return nil
}

func (f *confirmFakeQuerier) ClaimCollecting(ctx context.Context, chatID string) (database.PendingConfirmation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pending == nil || f.pending.ChatID != chatID || f.pending.Status != "collecting" {
		return database.PendingConfirmation{}, fmt.Errorf("no rows")
	}
	claimed := *f.pending
	f.pending.Status = "collecting_claimed"
	return claimed, nil
}

// findTool pulls a tool's real ToolDefinition out of an agentSpec by name, so
// tests exercise the actual declared schema rather than a hand-duplicated one.
func findTool(spec agentSpec, name string) ToolDefinition {
	for _, t := range spec.Tools {
		if t.Function.Name == name {
			return t
		}
	}
	panic("tool not found: " + name)
}

func TestEnforceRequiredArgs_MissingFieldsAsksUser(t *testing.T) {
	q := &confirmFakeQuerier{}
	e := newConfirmTestEngine(q, func(ctx context.Context, m []Message, tools []ToolDefinition, temp float32, maxTokens int) (*Message, error) {
		return &Message{Role: "assistant", Content: "Najm"}, nil // transliteration call
	})
	state := linkedState("register a horse named نجم")

	def := findTool(operationsAgent, "register_horse")
	args := map[string]interface{}{"nameAr": "نجم"}

	complete, err := e.enforceRequiredArgs(context.Background(), state, "operations", def, args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if complete {
		t.Fatal("expected incomplete (breed/color/gender still missing)")
	}
	if args["nameEn"] != "Najm" {
		t.Fatalf("expected nameEn auto-derived from nameAr, got %q", args["nameEn"])
	}
	if q.pending == nil || q.pending.Status != "collecting" {
		t.Fatalf("expected a durable 'collecting' row, got %+v", q.pending)
	}
	var missing []string
	_ = json.Unmarshal(q.pending.MissingFields, &missing)
	for _, f := range []string{"breed", "color", "gender"} {
		found := false
		for _, m := range missing {
			if m == f {
				found = true
			}
		}
		if !found {
			t.Errorf("expected %q in missing fields, got %v", f, missing)
		}
	}
	for _, f := range missing {
		if f == "nameEn" || f == "nameAr" {
			t.Errorf("nameEn/nameAr must never be asked once nameAr is known, got missing=%v", missing)
		}
	}
	if state.FinalReply == "" {
		t.Fatal("expected a clarifying question set as the reply")
	}
}

func TestEnforceRequiredArgs_DeriveSuccessProceedsToRiskGate(t *testing.T) {
	q := &confirmFakeQuerier{}
	e := newConfirmTestEngine(q, func(ctx context.Context, m []Message, tools []ToolDefinition, temp float32, maxTokens int) (*Message, error) {
		return &Message{Role: "assistant", Content: "Najm"}, nil
	})
	state := linkedState("register a horse named نجم, Arabian, bay, stallion")

	def := findTool(operationsAgent, "register_horse")
	args := map[string]interface{}{"nameAr": "نجم", "breed": "Arabian", "color": "bay", "gender": "stallion"}

	complete, err := e.enforceRequiredArgs(context.Background(), state, "operations", def, args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !complete {
		t.Fatalf("expected complete once nameEn derives and all other fields are present, missing state: %+v", state.FinalReply)
	}
	if args["nameEn"] != "Najm" {
		t.Fatalf("expected nameEn derived, got %q", args["nameEn"])
	}
	if q.pending != nil {
		t.Fatalf("expected no durable row when the call is already complete, got %+v", q.pending)
	}
}

func TestEnforceRequiredArgs_DeriveSourceAlsoMissing(t *testing.T) {
	q := &confirmFakeQuerier{}
	completions := 0
	e := newConfirmTestEngine(q, func(ctx context.Context, m []Message, tools []ToolDefinition, temp float32, maxTokens int) (*Message, error) {
		completions++
		return &Message{Role: "assistant", Content: "should not be called"}, nil
	})
	state := linkedState("register a horse, Arabian, bay, stallion")

	def := findTool(operationsAgent, "register_horse")
	// Neither nameAr nor nameEn given — nameEn can't be derived (no source),
	// so nameAr itself must be asked for; the transliteration helper must
	// never be invoked since there is nothing to transliterate.
	args := map[string]interface{}{"breed": "Arabian", "color": "bay", "gender": "stallion"}

	complete, err := e.enforceRequiredArgs(context.Background(), state, "operations", def, args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if complete {
		t.Fatal("expected incomplete: nameAr is missing and undeliverable")
	}
	if completions != 0 {
		t.Fatalf("transliteration helper must not run when its source field is absent, got %d completion calls", completions)
	}
	var missing []string
	_ = json.Unmarshal(q.pending.MissingFields, &missing)
	if len(missing) != 1 || missing[0] != "nameAr" {
		t.Fatalf("expected missing=[nameAr] (never nameEn), got %v", missing)
	}
}

func TestEnforceRequiredArgs_IdempotencyKeyNeverAsked(t *testing.T) {
	q := &confirmFakeQuerier{}
	e := newConfirmTestEngine(q, func(ctx context.Context, m []Message, tools []ToolDefinition, temp float32, maxTokens int) (*Message, error) {
		t.Fatal("no LLM call should be needed for a synthetic field")
		return nil, nil
	})
	state := linkedState("record a 1200 SAR feed expense")

	def := findTool(accountingAgent, "record_expense")
	args := map[string]interface{}{"amount": 1200.0, "category": "feed"}

	complete, err := e.enforceRequiredArgs(context.Background(), state, "accounting", def, args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !complete {
		t.Fatalf("idempotencyKey must be auto-generated, never asked; state: %q", state.FinalReply)
	}
	if key, _ := args["idempotencyKey"].(string); key == "" {
		t.Fatal("expected a generated idempotencyKey placeholder")
	}
}

func TestEnforceRequiredArgs_DisabledViaAgentConfig(t *testing.T) {
	q := &confirmFakeQuerier{}
	q.agent = &database.Agent{
		ID:                 "default",
		ClarificationRules: []byte(`{"enabled":false}`),
	}
	e := newConfirmTestEngine(q, func(ctx context.Context, m []Message, tools []ToolDefinition, temp float32, maxTokens int) (*Message, error) {
		t.Fatal("no LLM call should happen when the gate is disabled")
		return nil, nil
	})
	state := linkedState("register a horse named نجم")

	def := findTool(operationsAgent, "register_horse")
	args := map[string]interface{}{"nameAr": "نجم"} // deliberately incomplete

	complete, err := e.enforceRequiredArgs(context.Background(), state, "operations", def, args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !complete {
		t.Fatal("expected legacy passthrough (complete=true) when clarification is disabled for the agent")
	}
	if q.pending != nil {
		t.Fatalf("expected no durable row when the gate is disabled, got %+v", q.pending)
	}
}

func TestResumeCollecting_MergeSuccessParksOrdinaryConfirmation(t *testing.T) {
	q := &confirmFakeQuerier{}
	knownArgs, _ := json.Marshal(map[string]interface{}{"nameAr": "نجم", "nameEn": "Najm"})
	missing, _ := json.Marshal([]string{"breed", "color", "gender"})
	q.pending = &database.PendingConfirmation{
		ChatID:        "123@s.whatsapp.net",
		ToolID:        "register_horse",
		Args:          knownArgs,
		OrgID:         "org1",
		ActingUserUid: "u1",
		Status:        "collecting",
		ExpiresAt:     time.Now().Add(5 * time.Minute),
		MissingFields: missing,
		CollectRounds: 1,
		Intent:        "operations",
	}

	call := 0
	e := newConfirmTestEngine(q, func(ctx context.Context, m []Message, tools []ToolDefinition, temp float32, maxTokens int) (*Message, error) {
		call++
		// The system prompt (first message) must carry the resume addendum.
		if call == 1 && !strings.Contains(m[0].Content, "register_horse") {
			t.Fatalf("expected the resume addendum in the system prompt, got: %s", m[0].Content)
		}
		return &Message{Role: "assistant", ToolCalls: []ToolCall{{
			ID: "tc1", Type: "function",
			Function: ToolFunction{Name: "register_horse", Arguments: `{"nameAr":"نجم","nameEn":"Najm","breed":"Arabian","color":"bay","gender":"stallion"}`},
		}}}, nil
	})

	state := linkedState("Arabian, bay, stallion")
	resumed, err := e.resumeCollecting(context.Background(), state)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resumed {
		t.Fatal("expected resumeCollecting to consume this message")
	}
	if q.pending == nil || q.pending.Status != "pending" {
		t.Fatalf("expected the completed call to graduate into an ordinary 'pending' confirmation, got %+v", q.pending)
	}
	if q.pending.ToolID != "register_horse" {
		t.Fatalf("expected the pending confirmation to be for register_horse, got %q", q.pending.ToolID)
	}
}

func TestResumeCollecting_ExpiredRowIsDropped(t *testing.T) {
	q := &confirmFakeQuerier{}
	q.pending = &database.PendingConfirmation{
		ChatID:    "123@s.whatsapp.net",
		ToolID:    "register_horse",
		Status:    "collecting",
		ExpiresAt: time.Now().Add(-1 * time.Minute), // already expired
	}
	e := newConfirmTestEngine(q, func(ctx context.Context, m []Message, tools []ToolDefinition, temp float32, maxTokens int) (*Message, error) {
		t.Fatal("no LLM call should happen for an expired row")
		return nil, nil
	})

	state := linkedState("Arabian, bay, stallion")
	resumed, err := e.resumeCollecting(context.Background(), state)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resumed {
		t.Fatal("an expired collecting row must not be resumed")
	}
	if q.pending != nil {
		t.Fatalf("expected the expired row to be deleted, got %+v", q.pending)
	}
}

func TestResumeCollecting_RoundCapBailsOut(t *testing.T) {
	q := &confirmFakeQuerier{}
	q.pending = &database.PendingConfirmation{
		ChatID:        "123@s.whatsapp.net",
		ToolID:        "register_horse",
		Status:        "collecting",
		ExpiresAt:     time.Now().Add(5 * time.Minute),
		CollectRounds: collectRoundsMax,
	}
	e := newConfirmTestEngine(q, func(ctx context.Context, m []Message, tools []ToolDefinition, temp float32, maxTokens int) (*Message, error) {
		t.Fatal("no LLM call should happen once the round cap is hit")
		return nil, nil
	})

	state := linkedState("still don't have all the details")
	resumed, err := e.resumeCollecting(context.Background(), state)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resumed {
		t.Fatal("expected resumeCollecting to consume this message with a bail-out reply")
	}
	if state.FinalReply == "" {
		t.Fatal("expected a 'please restart' reply")
	}
	if q.pending != nil {
		t.Fatalf("expected the row to be deleted after the round cap, got %+v", q.pending)
	}
}
