package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// stubServer replies to JSON-RPC requests by method, echoing the request id.
func stubServer(t *testing.T, results map[string]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
		}
		_ = json.Unmarshal(body, &req)
		res, ok := results[req.Method]
		w.Header().Set("Content-Type", "application/json")
		if !ok {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + itoa(req.ID) + `,"error":{"code":-32601,"message":"method not found"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + itoa(req.ID) + `,"result":` + res + `}`))
	}))
}

func itoa(i int64) string { return string(rune('0' + i%10)) } // ids stay single-digit in tests

func TestClientInitializeListAndCall(t *testing.T) {
	srv := stubServer(t, map[string]string{
		"initialize":     `{"protocolVersion":"2024-11-05"}`,
		"tools/list":     `{"tools":[{"name":"weather","description":"get weather","inputSchema":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]},"annotations":{"readOnlyHint":true}},{"name":"send","description":"send msg","inputSchema":{"type":"object"}}]}`,
		"resources/list": `{"resources":[{"uri":"mem://notes","name":"Notes","description":"scratch","mimeType":"text/plain"}]}`,
		"tools/call":     `{"content":[{"type":"text","text":"sunny"}],"isError":false}`,
	})
	defer srv.Close()

	c := NewClient("test", srv.URL)
	ctx := context.Background()
	if err := c.Initialize(ctx); err != nil {
		t.Fatalf("initialize: %v", err)
	}

	tools, err := c.ListTools(ctx)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}
	if !tools[0].ReadOnly() {
		t.Error("weather should be read-only")
	}
	if tools[1].ReadOnly() {
		t.Error("send should default to write-capable (no hint)")
	}

	res, err := c.ListResources(ctx)
	if err != nil || len(res) != 1 || res[0].Name != "Notes" {
		t.Fatalf("list resources wrong: %v %+v", err, res)
	}

	out, err := c.CallTool(ctx, "weather", map[string]interface{}{"city": "Riyadh"})
	if err != nil {
		t.Fatalf("call tool: %v", err)
	}
	if !json.Valid(out) {
		t.Fatalf("call result not valid json: %s", out)
	}
}

func TestClientPropagatesRPCError(t *testing.T) {
	srv := stubServer(t, map[string]string{"initialize": `{}`}) // tools/call → method not found
	defer srv.Close()

	c := NewClient("test", srv.URL)
	if _, err := c.CallTool(context.Background(), "x", nil); err == nil {
		t.Fatal("expected rpc error for unknown method")
	}
}
