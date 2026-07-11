package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"sawt-go/internal/agentcfg"
	"sawt-go/internal/mcp"
	"sawt-go/internal/trace"
)

// mcpBundle is the set of MCP tools connected for one request: the tool
// definitions to expose to the model, a name→client dispatch map, per-tool
// read-only flags for risk gating, and any read-only resources folded into the
// prompt.
type mcpBundle struct {
	tools     []ToolDefinition
	dispatch  map[string]*mcp.Client
	readonly  map[string]bool
	resources string
}

// owns reports whether a tool name is served by a connected MCP server.
func (b mcpBundle) owns(name string) bool {
	_, ok := b.dispatch[name]
	return ok
}

// risk returns an MCP tool's confirmation risk: read-only tools are "low"
// (execute inline), everything else is "medium" (routed through the confirmation
// gate, the same default unknown ERP tools get). ok is false for non-MCP tools.
func (b mcpBundle) risk(name string) (string, bool) {
	if !b.owns(name) {
		return "", false
	}
	if b.readonly[name] {
		return "low", true
	}
	return "medium", true
}

// connectMCP resolves the chat's agent and connects each enabled MCP server,
// aggregating their tools and read-only resources. It is best-effort: a server
// that fails to initialize or list is logged and skipped so it never breaks the
// turn, and an agent with no MCP servers returns an empty bundle at near-zero
// cost (the common case). Stored URLs were SSRF-validated on save, so private
// hosts are permitted here.
func (e *WorkflowEngine) connectMCP(ctx context.Context, chatID string) mcpBundle {
	bundle := mcpBundle{dispatch: map[string]*mcp.Client{}, readonly: map[string]bool{}}

	agent, ok := e.resolveAgent(ctx, chatID)
	if !ok {
		return bundle
	}
	servers, err := agentcfg.ParseMCPServers(agent.McpServers, true)
	if err != nil || len(servers) == 0 {
		return bundle
	}

	var resourceLines []string
	for _, s := range servers {
		if !s.Enabled {
			continue
		}
		client := mcp.NewClient(s.Name, s.URL)
		if err := client.Initialize(ctx); err != nil {
			trace.Logf(ctx, "[workflow] MCP server %q initialize failed, skipping: %v", s.Name, err)
			continue
		}
		tools, err := client.ListTools(ctx)
		if err != nil {
			trace.Logf(ctx, "[workflow] MCP server %q tools/list failed, skipping: %v", s.Name, err)
			continue
		}
		for _, t := range tools {
			if bundle.owns(t.Name) {
				continue // first server to claim a name wins
			}
			def, ok := mcpToolToDefinition(t)
			if !ok {
				continue
			}
			bundle.tools = append(bundle.tools, def)
			bundle.dispatch[t.Name] = client
			bundle.readonly[t.Name] = t.ReadOnly()
		}
		if resources, err := client.ListResources(ctx); err == nil {
			for _, r := range resources {
				resourceLines = append(resourceLines, fmt.Sprintf("- %s (%s): %s", r.Name, r.URI, r.Description))
			}
		}
	}
	if len(resourceLines) > 0 {
		bundle.resources = "\n\nRead-only MCP resources available:\n" + strings.Join(resourceLines, "\n")
	}
	return bundle
}

// mcpToolToDefinition converts an MCP tool's JSON-schema input into the engine's
// tool-definition shape. A schema that cannot be parsed is skipped (ok=false).
func mcpToolToDefinition(t mcp.Tool) (ToolDefinition, bool) {
	params := ParametersSchema{Type: "object", Properties: map[string]PropertySchema{}}
	if len(t.InputSchema) > 0 {
		if err := json.Unmarshal(t.InputSchema, &params); err != nil {
			return ToolDefinition{}, false
		}
	}
	if params.Type == "" {
		params.Type = "object"
	}
	if params.Properties == nil {
		params.Properties = map[string]PropertySchema{}
	}
	return ToolDefinition{
		Type:     "function",
		Function: FunctionDefinition{Name: t.Name, Description: t.Description, Parameters: params},
	}, true
}

// callMCPTool invokes an MCP tool and normalizes its result into the map shape
// the tool loop records and replays to the model.
func callMCPTool(ctx context.Context, client *mcp.Client, name string, args map[string]interface{}) map[string]interface{} {
	raw, err := client.CallTool(ctx, name, args)
	if err != nil {
		return map[string]interface{}{"ok": false, "error": err.Error()}
	}
	var out map[string]interface{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return map[string]interface{}{"ok": true, "result": string(raw)}
	}
	if _, has := out["ok"]; !has {
		out["ok"] = true
	}
	return out
}

// executeConfirmedTool routes a confirmed tool call to the right executor: an
// MCP-owned tool goes to its server, everything else to the ERP gateway. It is
// used by the confirmation resolver so a write-capable MCP tool that was parked
// for confirmation actually reaches its server on the user's "yes".
func (e *WorkflowEngine) executeConfirmedTool(ctx context.Context, chatID, toolID, orgID, uid string, args map[string]interface{}) (map[string]interface{}, error) {
	bundle := e.connectMCP(ctx, chatID)
	if bundle.owns(toolID) {
		return callMCPTool(ctx, bundle.dispatch[toolID], toolID, args), nil
	}
	return e.erpClient.CallTool(ctx, toolID, orgID, uid, args)
}
