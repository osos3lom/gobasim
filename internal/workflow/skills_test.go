package workflow

import (
	"context"
	"strings"
	"testing"

	"sawt-go/database"
	"sawt-go/internal/agentcfg"
)

func TestBuildSkillsProgressiveDisclosure(t *testing.T) {
	e := &WorkflowEngine{}
	agent := database.Agent{Skills: []byte(`[{"name":"Accounting","path":"accounting.md","enabled":true},{"name":"Disabled","path":"operations.md","enabled":false}]`)}

	b := e.buildSkills(agent)
	if len(b.tools) != 1 || b.tools[0].Function.Name != loadSkillTool {
		t.Fatalf("expected load_skill tool, got %+v", b.tools)
	}
	// Manifest lists the enabled skill's name + a one-line summary, not the body.
	if !strings.Contains(b.manifest, "Accounting:") {
		t.Errorf("manifest missing enabled skill: %q", b.manifest)
	}
	if strings.Contains(b.manifest, "Disabled") {
		t.Error("disabled skill leaked into manifest")
	}
	if !b.owns("Accounting") || b.owns("Disabled") {
		t.Fatalf("ownership wrong: %+v", b.paths)
	}

	// Full body loads only on demand.
	res := b.load("Accounting")
	if ok, _ := res["ok"].(bool); !ok {
		t.Fatalf("load failed: %+v", res)
	}
	if body, _ := res["content"].(string); !strings.Contains(body, "Reconcile") {
		t.Errorf("unexpected skill body: %q", body)
	}
	if unknown := b.load("Nope"); unknown["ok"] != false {
		t.Error("expected error for unknown skill")
	}
}

func TestRunDelegateGuardsAllowList(t *testing.T) {
	e := &WorkflowEngine{}
	e.complete = func(_ context.Context, msgs []Message, _ []ToolDefinition, _ float32, maxTokens int) (*Message, error) {
		if maxTokens != 1500 {
			t.Errorf("expected token ceiling 1500, got %d", maxTokens)
		}
		return &Message{Role: "assistant", Content: "sub-answer"}, nil
	}
	sub := agentcfg.SubAgents{Enabled: true, MaxTokens: 1500, AllowedAgents: []string{"accounting"}}

	ok := e.runDelegate(context.Background(), sub, "accounting", "how much is owed?")
	if v, _ := ok["ok"].(bool); !v || ok["reply"] != "sub-answer" {
		t.Fatalf("expected successful delegation, got %+v", ok)
	}

	denied := e.runDelegate(context.Background(), sub, "breeding", "x")
	if v, _ := denied["ok"].(bool); v {
		t.Fatalf("expected delegation to breeding to be refused, got %+v", denied)
	}
}

func TestBuildDelegateInertWhenDisabled(t *testing.T) {
	e := &WorkflowEngine{}
	agent := database.Agent{SubAgents: []byte(`{"enabled":false,"max_tokens":0,"allowed_agents":[]}`)}
	if b := e.buildDelegate(agent); b.active || len(b.tools) != 0 {
		t.Fatalf("expected inert delegate bundle, got %+v", b)
	}
}
