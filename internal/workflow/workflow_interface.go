package workflow

import (
	"context"
)

// AgentInfo represents agent metadata for dynamic frontend UI generation,
// matching AgenticGoKit specs.
type AgentInfo struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Icon        string `json:"icon"`
	Color       string `json:"color"`
	Description string `json:"description"`
}

// WSMessage represents the standard event payload sent across WebSocket connections,
// mirroring AgenticGoKit streaming protocols.
type WSMessage struct {
	Type    string      `json:"type"`              // e.g. "agent_config", "agent_start", "agent_thinking", "chunk", "agent_complete", "workflow_complete", "error"
	Agent   string      `json:"agent,omitempty"`   // Target agent name
	Content interface{} `json:"content,omitempty"` // Message text or payload object
}

// MessageSender is the streaming callback invoked by workflow agents to send
// real-time progress events to the client.
type MessageSender func(msgType string, agent string, content interface{}) error

// WorkflowExecutor defines the contract for multi-agent sequential workflows
// in AgenticGoKit design pattern.
type WorkflowExecutor interface {
	// GetAgents returns the list of agent metadata involved in this workflow.
	GetAgents() []AgentInfo

	// Execute runs the multi-agent workflow sequentially with streaming events.
	Execute(ctx context.Context, userInput string, sendMessage MessageSender) error
}
