package workflow

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"sawt-go/database"
)

// mcpStub is a tiny JSON-RPC MCP server exposing one read-only and one
// write-capable tool.
func mcpStub(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
		}
		_ = json.Unmarshal(body, &req)
		w.Header().Set("Content-Type", "application/json")
		id := "1"
		var result string
		switch req.Method {
		case "initialize":
			result = `{"protocolVersion":"2024-11-05"}`
		case "tools/list":
			result = `{"tools":[{"name":"lookup_horse","description":"read","inputSchema":{"type":"object","properties":{"id":{"type":"string"}}},"annotations":{"readOnlyHint":true}},{"name":"push_update","description":"write","inputSchema":{"type":"object"}}]}`
		case "resources/list":
			result = `{"resources":[]}`
		case "tools/call":
			result = `{"ok":true,"content":"done"}`
		default:
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + id + `,"error":{"code":-32601,"message":"nope"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + id + `,"result":` + result + `}`))
	}))
}

func agentWithMCP(url string) database.Agent {
	servers := `[{"name":"erp-mcp","url":"` + url + `","enabled":true}]`
	return database.Agent{ID: "agent_a", McpServers: []byte(servers)}
}

func TestConnectMCPExposesToolsAndRisk(t *testing.T) {
	srv := mcpStub(t)
	defer srv.Close()

	q := fakeQ{
		contact: database.WaContact{ChatID: "chat1", AgentID: strptr("agent_a")},
		agents:  map[string]database.Agent{"agent_a": agentWithMCP(srv.URL)},
	}
	e := &WorkflowEngine{queries: q}

	bundle := e.connectMCP(context.Background(), "chat1")
	if len(bundle.tools) != 2 {
		t.Fatalf("expected 2 MCP tools, got %d", len(bundle.tools))
	}
	if !bundle.owns("lookup_horse") || !bundle.owns("push_update") {
		t.Fatalf("dispatch missing tools: %+v", bundle.dispatch)
	}
	if r, _ := bundle.risk("lookup_horse"); r != "low" {
		t.Errorf("read-only tool risk = %q, want low", r)
	}
	if r, _ := bundle.risk("push_update"); r != "medium" {
		t.Errorf("write tool risk = %q, want medium", r)
	}
	if _, ok := bundle.risk("get_horse"); ok {
		t.Error("native ERP tool must not be reported as MCP-owned")
	}
}

func TestExecuteConfirmedToolRoutesToMCP(t *testing.T) {
	srv := mcpStub(t)
	defer srv.Close()

	q := fakeQ{
		contact: database.WaContact{ChatID: "chat1", AgentID: strptr("agent_a")},
		agents:  map[string]database.Agent{"agent_a": agentWithMCP(srv.URL)},
	}
	e := &WorkflowEngine{queries: q}

	res, err := e.executeConfirmedTool(context.Background(), "chat1", "push_update", "org1", "uid1", map[string]interface{}{"x": 1})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok, _ := res["ok"].(bool); !ok {
		t.Fatalf("expected ok result from MCP server, got %+v", res)
	}
}

func TestConnectMCPNoServersIsEmpty(t *testing.T) {
	q := fakeQ{
		contact: database.WaContact{ChatID: "chat1", AgentID: strptr("agent_a")},
		agents:  map[string]database.Agent{"agent_a": {ID: "agent_a", McpServers: []byte(`[]`)}},
	}
	e := &WorkflowEngine{queries: q}
	bundle := e.connectMCP(context.Background(), "chat1")
	if len(bundle.tools) != 0 || len(bundle.dispatch) != 0 {
		t.Fatalf("expected empty bundle, got %+v", bundle)
	}
}
