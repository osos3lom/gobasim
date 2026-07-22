package web

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strconv"
	"strings"

	"sawt-go/database"
	"sawt-go/internal/agentcfg"
)

type agentRow struct {
	database.Agent
	HasUnpublishedChanges          bool
	LLM                            agentcfg.LLM
	TTS                            agentcfg.TTS
	SubAgents                      agentcfg.SubAgents
	SkillsJSON                     string
	MCPJSON                        string
	ClarificationRules             agentcfg.ClarificationRules
	ClarificationToolOverridesJSON string
	ClarificationDeriveRulesJSON   string
}

// KnownDelegateAgents lists the intent specs a sub-agent delegation may target,
// rendered as checkboxes in the capabilities block.
var KnownDelegateAgents = []string{"operations", "accounting", "administration", "sales", "breeding", "client"}

// DelegateChecked reports whether a delegate agent is in this row's allow-list,
// so the template can pre-check its box. Membership is shown regardless of the
// enabled flag, so an operator sees the saved allow-list even while delegation
// is toggled off.
func (a agentRow) DelegateChecked(name string) bool {
	return contains(a.SubAgents.AllowedAgents, name)
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

func (s *Server) handleGetWorkflowsPage(w http.ResponseWriter, r *http.Request) {
	s.renderWorkflowsPage(w, r, "")
}

func (s *Server) renderWorkflowsPage(w http.ResponseWriter, r *http.Request, errMsg string) {
	agents, err := s.queries.ListAgents(r.Context())
	if err != nil {
		agents = []database.Agent{}
	}

	rows := make([]agentRow, 0, len(agents))
	for _, a := range agents {
		unpublished := a.Status == "published" &&
			(a.LastPublished == nil || a.LastEdited.After(*a.LastPublished))
		llm, err := agentcfg.ParseLLM(a.Llm)
		if err != nil {
			llm = agentcfg.DefaultLLM()
		}
		tts, err := agentcfg.ParseTTS(a.Tts)
		if err != nil {
			tts = agentcfg.DefaultTTS()
		}
		sub, err := agentcfg.ParseSubAgents(a.SubAgents)
		if err != nil {
			sub = agentcfg.DefaultSubAgents()
		}
		cr, err := agentcfg.ParseClarificationRules(a.ClarificationRules)
		if err != nil {
			cr = agentcfg.DefaultClarificationRules()
		}
		toolOverridesJSON, _ := json.Marshal(cr.ToolOverrides)
		deriveRulesJSON, _ := json.Marshal(cr.DeriveRules)
		rows = append(rows, agentRow{
			Agent:                          a,
			HasUnpublishedChanges:          unpublished,
			LLM:                            llm,
			TTS:                            tts,
			SubAgents:                      sub,
			SkillsJSON:                     jsonOrDefault(a.Skills, "[]"),
			MCPJSON:                        jsonOrDefault(a.McpServers, "[]"),
			ClarificationRules:             cr,
			ClarificationToolOverridesJSON: jsonOrDefault(toolOverridesJSON, "[]"),
			ClarificationDeriveRulesJSON:   jsonOrDefault(deriveRulesJSON, "[]"),
		})
	}

	activity, err := s.queries.ListRecentWaActivity(r.Context(), 20)
	if err != nil {
		activity = []database.WaActivity{}
	}

	s.renderTemplate(w, "workflow.html", map[string]interface{}{
		"Username":       r.Context().Value(UsernameContextKey),
		"Agents":         rows,
		"Activity":       activity,
		"Metrics":        s.waMetricsData(r.Context()),
		"Page":           "workflows",
		"CSRFToken":      s.ensureCSRFToken(w, r),
		"Error":          errMsg,
		"DelegateAgents": KnownDelegateAgents,
		"HistoryDefault": agentcfg.DefaultHistory,
		"HistoryMax":     agentcfg.MaxHistory,
		"SubTokensMax":   agentcfg.MaxSubAgentTokens,
	})
}

func jsonOrDefault(raw []byte, fallback string) string {
	if len(raw) == 0 {
		return fallback
	}
	return string(raw)
}

func (s *Server) handlePostCreateWorkflow(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		s.renderWorkflowsPage(w, r, "Agent name is required.")
		return
	}

	idBytes := make([]byte, 8)
	if _, err := rand.Read(idBytes); err != nil {
		http.Error(w, "Failed to generate agent id", http.StatusInternalServerError)
		return
	}
	id := "agent_" + hex.EncodeToString(idBytes)

	_, err := s.queries.CreateAgent(r.Context(), database.CreateAgentParams{
		ID:                 id,
		Name:               name,
		ProjectID:          "Default Project (****75e1)",
		HostingRegion:      "Europe",
		Status:             "draft",
		Template:           "Blank Template",
		ModelType:          "asr-llm-tts",
		Asr:                agentcfg.DefaultASR().Marshal(),
		Llm:                agentcfg.DefaultLLM().Marshal(),
		Tts:                agentcfg.DefaultTTS().Marshal(),
		TurnDetection:      true,
		StartOfSpeech:      true,
		EndOfSpeech:        true,
		MaxHistory:         int32(agentcfg.DefaultHistory),
		McpServers:         []byte(`[]`),
		Skills:             []byte(`[]`),
		SubAgents:          agentcfg.DefaultSubAgents().Marshal(),
		ClarificationRules: agentcfg.DefaultClarificationRules().Marshal(),
	})
	if err != nil {
		log.Printf("web: failed to create agent %q: %v", name, err)
		s.renderWorkflowsPage(w, r, "Failed to create the agent. Please try again.")
		return
	}

	http.Redirect(w, r, "/dashboard/workflows", http.StatusSeeOther)
}

func validatePublishTransition(requestedStatus, systemPrompt string) string {
	if requestedStatus != "published" {
		return ""
	}
	if strings.TrimSpace(systemPrompt) == "" {
		return "Cannot publish: the system prompt is empty. Write the agent's instructions first."
	}
	return ""
}

func feedbackErr(w http.ResponseWriter, reason string) {
	_, _ = fmt.Fprintf(w, "<div class='bg-red-900 border border-red-500 text-red-200 px-4 py-3 rounded'>%s</div>", template.HTMLEscapeString(reason))
}

func formBool(r *http.Request, name string) bool {
	return r.FormValue(name) != ""
}

func (s *Server) handlePostUpdateWorkflow(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		feedbackErr(w, "Malformed form submission.")
		return
	}
	agentID := r.FormValue("id")

	agent, err := s.queries.GetAgent(r.Context(), agentID)
	if err != nil {
		http.Error(w, "Agent not found", http.StatusNotFound)
		return
	}

	arg, reason := s.buildUpdateParams(r, agent)
	if reason != "" {
		feedbackErr(w, reason)
		return
	}

	if _, err = s.queries.UpdateAgentWorkflow(r.Context(), arg); err != nil {
		log.Printf("web: failed to update agent workflow %q: %v", agentID, err)
		feedbackErr(w, "Failed to update the workflow. Please try again.")
		return
	}

	_, _ = w.Write([]byte("<div class='bg-emerald-900 border border-emerald-500 text-emerald-200 px-4 py-3 rounded'>Workflow updated successfully!</div>"))
}

func (s *Server) buildUpdateParams(r *http.Request, agent database.Agent) (database.UpdateAgentWorkflowParams, string) {
	prompt := r.FormValue("system_prompt")

	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		name = agent.Name
	}

	status := r.FormValue("status")
	switch status {
	case "":
		status = agent.Status
	case "draft", "published":
	default:
		return database.UpdateAgentWorkflowParams{}, "Invalid status value."
	}
	if reason := validatePublishTransition(status, prompt); reason != "" {
		return database.UpdateAgentWorkflowParams{}, reason
	}

	llm := agentcfg.LLM{
		Vendor:    r.FormValue("llm_vendor"),
		URL:       r.FormValue("llm_url"),
		APIKeyEnv: r.FormValue("llm_api_key_env"),
		Model:     r.FormValue("llm_model"),
	}
	if err := llm.Validate(); err != nil {
		return database.UpdateAgentWorkflowParams{}, err.Error()
	}
	maxHistory := agentcfg.ClampHistory(atoiDefault(r.FormValue("max_history"), agentcfg.DefaultHistory))

	tts := agentcfg.TTS{
		Vendor:       r.FormValue("tts_vendor"),
		LanguageCode: r.FormValue("tts_language_code"),
		VoiceName:    r.FormValue("tts_voice_name"),
		Gender:       r.FormValue("tts_gender"),
		Model:        r.FormValue("tts_model"),
		Speed:        float32(atofDefault(r.FormValue("tts_speed"), 1.0)),
	}
	if err := tts.Validate(); err != nil {
		return database.UpdateAgentWorkflowParams{}, err.Error()
	}

	allowPrivate := !s.cfg.SecureCookie
	mcpServers, err := agentcfg.ParseMCPServers([]byte(emptyToArray(r.FormValue("mcp_servers"))), allowPrivate)
	if err != nil {
		return database.UpdateAgentWorkflowParams{}, err.Error()
	}
	skills, err := agentcfg.ParseSkills([]byte(emptyToArray(r.FormValue("skills"))))
	if err != nil {
		return database.UpdateAgentWorkflowParams{}, err.Error()
	}
	sub := agentcfg.SubAgents{
		Enabled:       formBool(r, "sub_agents_enabled"),
		MaxTokens:     atoiDefault(r.FormValue("sub_agents_max_tokens"), 0),
		AllowedAgents: r.Form["sub_agents_allowed"],
	}
	if err := sub.Validate(); err != nil {
		return database.UpdateAgentWorkflowParams{}, err.Error()
	}

	var toolOverrides []agentcfg.ToolClarificationOverride
	if err := json.Unmarshal([]byte(emptyToArray(r.FormValue("clarification_tool_overrides"))), &toolOverrides); err != nil {
		return database.UpdateAgentWorkflowParams{}, "Invalid clarification tool-override data."
	}
	var deriveRules []agentcfg.DeriveRuleConfig
	if err := json.Unmarshal([]byte(emptyToArray(r.FormValue("clarification_derive_rules"))), &deriveRules); err != nil {
		return database.UpdateAgentWorkflowParams{}, "Invalid clarification derive-rule data."
	}
	clarificationEnabled := formBool(r, "clarification_enabled")
	cr := agentcfg.ClarificationRules{
		Enabled:       &clarificationEnabled,
		ToolOverrides: toolOverrides,
		DeriveRules:   deriveRules,
	}
	if err := cr.Validate(); err != nil {
		return database.UpdateAgentWorkflowParams{}, err.Error()
	}

	return database.UpdateAgentWorkflowParams{
		ID:                        r.FormValue("id"),
		Name:                      name,
		SystemPrompt:              prompt,
		GreetingMessage:           r.FormValue("greeting_message"),
		FailureMessage:            r.FormValue("failure_message"),
		Asr:                       agent.Asr,
		Llm:                       llm.Marshal(),
		Tts:                       tts.Marshal(),
		MaxHistory:                int32(maxHistory),
		Status:                    status,
		McpServers:                agentcfg.MarshalMCPServers(mcpServers),
		Skills:                    agentcfg.MarshalSkills(skills),
		SubAgents:                 sub.Marshal(),
		TurnDetection:             formBool(r, "turn_detection"),
		StartOfSpeech:             formBool(r, "start_of_speech"),
		EndOfSpeech:               formBool(r, "end_of_speech"),
		SelectiveAttentionLocking: formBool(r, "selective_attention_locking"),
		FillerWords:               formBool(r, "filler_words"),
		ClarificationRules:        cr.Marshal(),
	}, ""
}

func emptyToArray(s string) string {
	if strings.TrimSpace(s) == "" {
		return "[]"
	}
	return s
}

func atoiDefault(s string, def int) int {
	if v, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return v
	}
	return def
}

func atofDefault(s string, def float64) float64 {
	if v, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
		return v
	}
	return def
}
