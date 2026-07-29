package workflow

import (
	"context"
	"strings"
	"testing"
)

func TestAddHorseWorkflow_GetAgents(t *testing.T) {
	wf := NewAddHorseWorkflow(nil)
	agents := wf.GetAgents()

	if len(agents) != 2 {
		t.Fatalf("expected 2 agents in AddHorseWorkflow, got %d", len(agents))
	}
	if agents[0].Name != "intake" {
		t.Errorf("expected agent 0 to be 'intake', got %q", agents[0].Name)
	}
	if agents[1].Name != "registrar" {
		t.Errorf("expected agent 1 to be 'registrar', got %q", agents[1].Name)
	}
}

func TestAddHorseWorkflow_Execute(t *testing.T) {
	fakeEngine := &WorkflowEngine{
		complete: func(ctx context.Context, messages []Message, tools []ToolDefinition, temp float32, maxTokens int) (*Message, error) {
			sys := messages[0].Content
			if strings.Contains(sys, "Intake Specialist") {
				return &Message{
					Content: "Structured Intake Notes:\n- Name: Jamilah\n- Breed: Arabian\n- Gender: Mare\n- Owner: Sheikh Ahmed",
				}, nil
			}
			return &Message{
				Content: "✅ Registration complete for Jamilah (Arabian Mare) under owner Sheikh Ahmed. Stall B-04 assigned.",
			}, nil
		},
	}

	wf := NewAddHorseWorkflow(fakeEngine)

	var events []WSMessage
	sender := func(msgType string, agent string, content interface{}) error {
		events = append(events, WSMessage{
			Type:    msgType,
			Agent:   agent,
			Content: content,
		})
		return nil
	}

	err := wf.Execute(context.Background(), "Register new Arabian mare named Jamilah", sender)
	if err != nil {
		t.Fatalf("unexpected workflow execution error: %v", err)
	}

	if len(events) == 0 {
		t.Fatal("expected streamed events, got none")
	}

	// Verify workflow_complete event was emitted
	lastEvent := events[len(events)-1]
	if lastEvent.Type != "workflow_complete" {
		t.Errorf("expected last event type 'workflow_complete', got %q", lastEvent.Type)
	}
}
