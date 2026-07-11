package workflow

import (
	"context"
	"embed"
	"fmt"
	"strings"

	"sawt-go/database"
	"sawt-go/internal/agentcfg"
)

// skillsFS embeds the local procedural skill files. Bodies are loaded on demand
// (progressive disclosure) rather than dumped into every prompt.
//
//go:embed skills/*.md
var skillsFS embed.FS

const (
	loadSkillTool = "load_skill"
	delegateTool  = "delegate"

	// defaultDelegateTokens is the sub-reasoning ceiling used when an agent
	// enables delegation without setting an explicit token threshold.
	defaultDelegateTokens = 256
)

// str is a small helper for reading a string tool argument.
func str(v interface{}) string { s, _ := v.(string); return s }

// ---------------------------------------------------------------------------
// Skills — progressive disclosure
// ---------------------------------------------------------------------------

// skillBundle holds the enabled-skill prompt manifest, the load_skill tool, and
// a name→embedded-file map for on-demand body loading.
type skillBundle struct {
	tools    []ToolDefinition
	manifest string
	paths    map[string]string
}

func (b skillBundle) owns(name string) bool {
	_, ok := b.paths[name]
	return ok
}

// buildSkills turns an agent's enabled skills into a progressive-disclosure
// bundle: only names + one-line summaries go into the prompt; full bodies load
// on demand via the load_skill tool.
func (e *WorkflowEngine) buildSkills(agent database.Agent) skillBundle {
	bundle := skillBundle{paths: map[string]string{}}
	skills, err := agentcfg.ParseSkills(agent.Skills)
	if err != nil {
		return bundle
	}
	var lines []string
	for _, s := range skills {
		if !s.Enabled || s.Path == "" || s.Name == "" {
			continue
		}
		lines = append(lines, fmt.Sprintf("- %s: %s", s.Name, skillSummary(s.Path)))
		bundle.paths[s.Name] = s.Path
	}
	if len(lines) == 0 {
		return bundle
	}
	bundle.manifest = "\n\nSkills available (call load_skill with the skill name to read the full procedure before using it):\n" + strings.Join(lines, "\n")
	bundle.tools = []ToolDefinition{tool(loadSkillTool,
		"Load the full text of a named skill before following its procedure.",
		map[string]PropertySchema{"skill": {Type: "string", Description: "The skill name from the manifest."}},
		"skill")}
	return bundle
}

// load returns the full body of an enabled skill by name.
func (b skillBundle) load(name string) map[string]interface{} {
	path, ok := b.paths[name]
	if !ok {
		return map[string]interface{}{"ok": false, "error": fmt.Sprintf("unknown or disabled skill %q", name)}
	}
	body, err := skillBody(path)
	if err != nil {
		return map[string]interface{}{"ok": false, "error": err.Error()}
	}
	return map[string]interface{}{"ok": true, "skill": name, "content": body}
}

// skillBody reads a skill file from the embedded registry. path is a base
// filename already normalized by agentcfg (no traversal possible), and embed.FS
// is itself confined to embedded files.
func skillBody(path string) (string, error) {
	b, err := skillsFS.ReadFile(agentcfg.SkillsRoot + "/" + path)
	if err != nil {
		return "", fmt.Errorf("skill %q not found in registry", path)
	}
	return string(b), nil
}

// skillSummary returns the first non-empty line of a skill file for the manifest.
func skillSummary(path string) string {
	body, err := skillBody(path)
	if err != nil {
		return "(unavailable)"
	}
	for _, line := range strings.Split(body, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return "(no description)"
}

// ---------------------------------------------------------------------------
// Sub-agent delegation
// ---------------------------------------------------------------------------

// delegateBundle exposes the delegate tool when an agent permits sub-reasoning.
type delegateBundle struct {
	tools  []ToolDefinition
	sub    agentcfg.SubAgents
	active bool
}

// buildDelegate builds the delegate tool from an agent's sub_agents config. It
// is inert unless delegation is enabled with at least one allowed target.
func (e *WorkflowEngine) buildDelegate(agent database.Agent) delegateBundle {
	sub, err := agentcfg.ParseSubAgents(agent.SubAgents)
	if err != nil || !sub.Enabled || len(sub.AllowedAgents) == 0 {
		return delegateBundle{}
	}
	return delegateBundle{
		active: true,
		sub:    sub,
		tools: []ToolDefinition{tool(delegateTool,
			"Delegate a focused sub-task to another specialist agent ("+strings.Join(sub.AllowedAgents, ", ")+") and return its answer.",
			map[string]PropertySchema{
				"agent": {Type: "string", Description: "Target specialist agent.", Enum: sub.AllowedAgents},
				"task":  {Type: "string", Description: "The self-contained task or question to delegate."},
			}, "agent", "task")},
	}
}

// specByName maps an intent name to its compiled agentSpec.
func specByName(name string) (agentSpec, bool) {
	switch name {
	case "operations":
		return operationsAgent, true
	case "accounting":
		return accountingAgent, true
	case "administration":
		return administrationAgent, true
	case "sales":
		return salesAgent, true
	case "breeding":
		return breedingAgent, true
	case "client":
		return clientAgent, true
	}
	return agentSpec{}, false
}

// runDelegate runs a bounded, tool-free sub-reasoning call against the target
// spec, capped by the agent's configured token threshold. It refuses any target
// outside the allow-list (defense in depth: the tool schema already constrains
// the enum, but a model can still emit an out-of-range value).
func (e *WorkflowEngine) runDelegate(ctx context.Context, sub agentcfg.SubAgents, target, task string) map[string]interface{} {
	if !sub.Allows(target) {
		return map[string]interface{}{"ok": false, "error": fmt.Sprintf("delegation to %q is not permitted", target)}
	}
	spec, ok := specByName(target)
	if !ok {
		return map[string]interface{}{"ok": false, "error": fmt.Sprintf("unknown agent %q", target)}
	}
	maxTok := sub.MaxTokens
	if maxTok <= 0 {
		maxTok = defaultDelegateTokens
	}
	messages := []Message{
		{Role: "system", Content: spec.DefaultPrompt + "\n\nYou are a delegated sub-agent. Answer the task directly and concisely; do not call tools."},
		{Role: "user", Content: task},
	}
	msg, err := e.complete(ctx, messages, nil, 0.2, maxTok)
	if err != nil {
		return map[string]interface{}{"ok": false, "error": err.Error()}
	}
	return map[string]interface{}{"ok": true, "agent": target, "reply": msg.Content}
}
