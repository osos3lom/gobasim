// Package mcp is a minimal Model Context Protocol client speaking JSON-RPC 2.0
// over HTTP. It performs the initialize handshake, discovers a server's tools and
// read-only resources, and invokes tools — enough for the workflow engine to
// expose an agent's registered MCP servers to the model loop.
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"
)

// protocolVersion is the MCP revision this client negotiates.
const protocolVersion = "2024-11-05"

// defaultTimeout bounds every JSON-RPC round trip so a slow or hung MCP server
// can never pin a message-processing goroutine.
const defaultTimeout = 15 * time.Second

// Client is a single MCP server connection.
type Client struct {
	name string
	url  string
	http *http.Client
	id   atomic.Int64
}

// NewClient builds a client for the named server at url. The url is expected to
// have been validated (scheme/SSRF) by the caller before construction.
func NewClient(name, url string) *Client {
	return &Client{
		name: name,
		url:  url,
		http: &http.Client{Timeout: defaultTimeout},
	}
}

// Name returns the operator-assigned server name.
func (c *Client) Name() string { return c.name }

// Tool is one tool advertised by tools/list.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
	Annotations *struct {
		ReadOnlyHint bool `json:"readOnlyHint"`
	} `json:"annotations,omitempty"`
}

// ReadOnly reports whether the server hinted this tool has no side effects. When
// absent, tools are treated as write-capable (the safe default).
func (t Tool) ReadOnly() bool {
	return t.Annotations != nil && t.Annotations.ReadOnlyHint
}

// Resource is one read-only resource advertised by resources/list.
type Resource struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description"`
	MimeType    string `json:"mimeType"`
}

type rpcRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      int64       `json:"id"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("mcp rpc error %d: %s", e.Code, e.Message) }

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// call performs one JSON-RPC request and returns the raw result payload.
func (c *Client) call(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	body, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: c.id.Add(1), Method: method, Params: params})
	if err != nil {
		return nil, fmt.Errorf("mcp: marshal %s: %w", method, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("mcp: build %s request: %w", method, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mcp: %s request failed: %w", method, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("mcp: %s returned HTTP %d", method, resp.StatusCode)
	}

	var out rpcResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("mcp: decode %s response: %w", method, err)
	}
	if out.Error != nil {
		return nil, out.Error
	}
	return out.Result, nil
}

// Initialize performs the MCP handshake. It must succeed before tools/resources
// are listed.
func (c *Client) Initialize(ctx context.Context) error {
	_, err := c.call(ctx, "initialize", map[string]interface{}{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]interface{}{"name": "sawt-go", "version": "1.0"},
	})
	return err
}

// ListTools returns the server's advertised tools.
func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	raw, err := c.call(ctx, "tools/list", map[string]interface{}{})
	if err != nil {
		return nil, err
	}
	var out struct {
		Tools []Tool `json:"tools"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("mcp: decode tools/list: %w", err)
	}
	return out.Tools, nil
}

// ListResources returns the server's read-only resources.
func (c *Client) ListResources(ctx context.Context) ([]Resource, error) {
	raw, err := c.call(ctx, "resources/list", map[string]interface{}{})
	if err != nil {
		return nil, err
	}
	var out struct {
		Resources []Resource `json:"resources"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("mcp: decode resources/list: %w", err)
	}
	return out.Resources, nil
}

// CallTool invokes a tool and returns the raw JSON result payload.
func (c *Client) CallTool(ctx context.Context, name string, args map[string]interface{}) (json.RawMessage, error) {
	if args == nil {
		args = map[string]interface{}{}
	}
	return c.call(ctx, "tools/call", map[string]interface{}{"name": name, "arguments": args})
}
