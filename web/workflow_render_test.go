package web

import (
	"bytes"
	"html/template"
	"strings"
	"testing"

	"sawt-go/database"
	"sawt-go/internal/agentcfg"
)

// TestWorkflowTemplateRendersFourBlocks exercises the populated per-agent path of
// workflow.html (the range body the distinct-content test skips because it passes
// no agents). It guards every new field reference — typed LLM/TTS inputs, the
// list-editor data-init JSON, sub-agent checkboxes, and the $.Delegate* /
// $.History* page keys — against template execution errors and wrong pipelines.
func TestWorkflowTemplateRendersFourBlocks(t *testing.T) {
	tmpl := template.Must(template.New("layout").ParseFS(templatesFS, "templates/*.html"))

	row := agentRow{
		Agent: database.Agent{
			ID:           "agent_test",
			Name:         "Stable Concierge",
			Status:       "published",
			SystemPrompt: "You are Sawt.",
			MaxHistory:   12,
		},
		LLM:        agentcfg.LLM{Vendor: "nim", URL: "https://integrate.api.nvidia.com/v1", APIKeyEnv: "NIM_API_KEY", Model: "meta/llama-3.1-70b-instruct"},
		TTS:        agentcfg.TTS{Vendor: "google", LanguageCode: "ar-XA", VoiceName: "ar-XA-Wavenet-B", Gender: "FEMALE", Model: "Wavenet", Speed: 1.0},
		SubAgents:  agentcfg.SubAgents{Enabled: true, MaxTokens: 1500, AllowedAgents: []string{"accounting"}},
		SkillsJSON: `[{"name":"Accounting","path":"accounting.md","enabled":true}]`,
		MCPJSON:    `[{"name":"erp","url":"https://mcp.example.com/rpc","enabled":true}]`,
	}

	data := map[string]interface{}{
		"Page":           "workflows",
		"CSRFToken":      "tok",
		"Agents":         []agentRow{row},
		"DelegateAgents": KnownDelegateAgents,
		"HistoryDefault": agentcfg.DefaultHistory,
		"HistoryMax":     agentcfg.MaxHistory,
		"SubTokensMax":   agentcfg.MaxSubAgentTokens,
	}

	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "workflow.html", data); err != nil {
		t.Fatalf("ExecuteTemplate error: %v", err)
	}
	out := buf.String()

	wants := []string{
		"LLM Brain &amp; Telemetry",
		"Agent SOUL",
		"Capabilities",
		"Aegis Audio",
		`name="llm_vendor"`,
		`name="max_history" value="12"`,
		`name="tts_language_code" value="ar-XA"`,
		`data-field="skills"`,
		`data-field="mcp_servers"`,
		"accounting.md", // skills data-init JSON survived attribute escaping
		`name="sub_agents_max_tokens" value="1500"`,
		"/static/workflow.js",
	}
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("rendered workflow.html missing %q", w)
		}
	}

	// The NIM vendor option must be pre-selected.
	if !strings.Contains(out, `value="nim" selected`) {
		t.Error("expected NIM vendor option to be selected")
	}
	// The enabled sub-agent checkbox must be checked.
	if !strings.Contains(out, `name="sub_agents_enabled" class="h-4 w-4 accent-indigo-500" checked`) {
		t.Error("expected sub_agents_enabled checkbox to be checked")
	}
}
