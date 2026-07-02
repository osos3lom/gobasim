package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sawt-go/config"
	"sawt-go/database"
	"sawt-go/internal/erp"
	"strings"
	"time"
)

type Message struct {
	Role       string      `json:"role"`
	Content    string      `json:"content,omitempty"`
	Name       string      `json:"name,omitempty"`
	ToolCallID string      `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall  `json:"tool_calls,omitempty"`
}

type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

type ToolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON string
}

type State struct {
	Messages      []Message
	Intent        string
	ActorIdentity *erp.Identity
	ToolResults   []map[string]interface{}
	FinalReply    string
	ChatID        string
	// Summary is the rolling summary of older conversation turns that no
	// longer fit in the replayed message window.
	Summary string
}

// NIM / OpenAI Chat Completion API Request & Tool schema structs
type ChatCompletionRequest struct {
	Model       string           `json:"model"`
	Messages    []Message        `json:"messages"`
	Temperature float32          `json:"temperature"`
	MaxTokens   int              `json:"max_tokens"`
	Tools       []ToolDefinition `json:"tools,omitempty"`
}

type ToolDefinition struct {
	Type     string             `json:"type"`
	Function FunctionDefinition `json:"function"`
}

type FunctionDefinition struct {
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Parameters  ParametersSchema `json:"parameters"`
}

type ParametersSchema struct {
	Type       string                    `json:"type"`
	Properties map[string]PropertySchema `json:"properties"`
	Required   []string                  `json:"required,omitempty"`
}

type PropertySchema struct {
	Type        string   `json:"type"`
	Description string   `json:"description"`
	Enum        []string `json:"enum,omitempty"`
}

// completionFn is the signature of a chat-completions call. The engine talks
// to the LLM exclusively through this, so tests can inject a fake.
type completionFn func(ctx context.Context, messages []Message, tools []ToolDefinition, temp float32, maxTokens int) (*Message, error)

type WorkflowEngine struct {
	cfg        *config.Config
	erpClient  *erp.Client
	httpClient *http.Client
	queries    database.Querier
	complete   completionFn
}

func NewWorkflowEngine(cfg *config.Config, erpClient *erp.Client, queries database.Querier) *WorkflowEngine {
	e := &WorkflowEngine{
		cfg:       cfg,
		erpClient: erpClient,
		queries:   queries,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
	e.complete = e.chatCompletions
	return e
}

// chatCompletions makes a standard HTTP request to the configured OpenAI-compatible LLM endpoint (NVIDIA NIM).
func (e *WorkflowEngine) chatCompletions(ctx context.Context, messages []Message, tools []ToolDefinition, temp float32, maxTokens int) (*Message, error) {
	if e.cfg.NimAPIKey == "" {
		return nil, fmt.Errorf("NIM_API_KEY is not configured")
	}

	url := fmt.Sprintf("%s/chat/completions", e.cfg.NimBaseURL)

	payload := ChatCompletionRequest{
		Model:       e.cfg.NimModel,
		Messages:    messages,
		Temperature: temp,
		MaxTokens:   maxTokens,
		Tools:       tools,
	}

	jsonBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(jsonBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.cfg.NimAPIKey)

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request failed: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("LLM API returned status %d: %s", resp.StatusCode, string(respBytes))
	}

	var responseStruct struct {
		Choices []struct {
			Message Message `json:"message"`
		} `json:"choices"`
	}

	if err := json.Unmarshal(respBytes, &responseStruct); err != nil {
		return nil, fmt.Errorf("failed to unmarshal LLM response: %w, payload: %s", err, string(respBytes))
	}

	if len(responseStruct.Choices) == 0 {
		return nil, fmt.Errorf("LLM returned empty choices list")
	}

	return &responseStruct.Choices[0].Message, nil
}

// ClassifyIntent routes the conversation by identifying user intentions.
func (e *WorkflowEngine) ClassifyIntent(ctx context.Context, state *State) error {
	if len(state.Messages) == 0 {
		state.Intent = "other"
		return nil
	}

	// Find the last human message
	var lastHumanText string
	for i := len(state.Messages) - 1; i >= 0; i-- {
		if state.Messages[i].Role == "user" {
			lastHumanText = state.Messages[i].Content
			break
		}
	}

	if lastHumanText == "" {
		state.Intent = "other"
		return nil
	}

	systemPrompt := "Classify a WhatsApp message sent to a horse-stable ERP assistant. " +
		"Reply with exactly one word, nothing else:\n" +
		"operations = horses, stalls, tasks, health, vet, inventory\n" +
		"accounting = invoices, bills, payments, financial reports\n" +
		"administration = clients, contracts, scheduling, documents\n" +
		"other = greetings, small talk, anything unrelated to the stable"

	messages := []Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: lastHumanText},
	}

	msg, err := e.complete(ctx, messages, nil, 0.0, 10)
	if err != nil {
		log.Printf("[workflow] ClassifyIntent failed, defaulting to operations: %v", err)
		state.Intent = "operations"
		return nil
	}

	// Clean classification using standard library strings package
	intent := msg.Content
	intent = strings.Trim(intent, " \n\r\t.,!\"'")
	intent = strings.ToLower(intent)

	// Validate intent
	switch intent {
	case "operations", "accounting", "administration", "other":
		state.Intent = intent
	default:
		state.Intent = "operations" // Safe default
	}

	log.Printf("[workflow] Classified intent: '%s'", state.Intent)
	return nil
}

func (e *WorkflowEngine) Execute(ctx context.Context, state *State) error {
	// 0. A chat waiting on a yes/no consumes this message first.
	if handled, err := e.resolvePendingConfirmation(ctx, state); err != nil {
		log.Printf("[workflow] Pending-confirmation resolution failed: %v", err)
	} else if handled {
		return nil
	}

	// 1. Classify Intent
	if err := e.ClassifyIntent(ctx, state); err != nil {
		return err
	}

	// 2. Route based on classification
	switch state.Intent {
	case "operations":
		return e.executeOperations(ctx, state)
	case "accounting":
		state.FinalReply = "عذراً، نظام المحاسبة غير مرتبط حالياً بهذا المساعد. يرجى مراجعة لوحة التحكم.\n" +
			"Sorry, the accounting module is not connected to this assistant yet."
		return nil
	case "administration":
		state.FinalReply = "عذراً، نظام الإدارة والوثائق غير مرتبط حالياً. سنعمل على إضافته قريباً.\n" +
			"Sorry, the administration module is not connected to this assistant yet."
		return nil
	default: // other / small talk
		return e.executeGeneralChat(ctx, state)
	}
}

func (e *WorkflowEngine) executeGeneralChat(ctx context.Context, state *State) error {
	systemPrompt := "You are Sawt, a friendly assistant for an Arabian horse stable. " +
		"Reply briefly and naturally in the language the user used (Arabic or English) without any markdown formatting."
	if state.Summary != "" {
		systemPrompt += "\n\nSummary of the conversation so far:\n" + state.Summary
	}

	messages := append([]Message{{Role: "system", Content: systemPrompt}}, state.Messages...)
	msg, err := e.complete(ctx, messages, nil, 0.3, 200)
	if err != nil {
		return err
	}

	state.FinalReply = msg.Content
	return nil
}

func (e *WorkflowEngine) executeOperations(ctx context.Context, state *State) error {
	if state.ActorIdentity == nil || len(state.ActorIdentity.OrgIDs) == 0 {
		state.FinalReply = "هذا الرقم غير مرتبط بحساب في النظام بعد. يرجى التواصل مع الإدارة لربط رقمك.\n" +
			"This number isn't linked to an ERP account yet — please ask an admin to link it."
		return nil
	}
	orgID := state.ActorIdentity.OrgIDs[0]

	// 1. Resolve System Instructions dynamically
	systemPrompt := "You are the operations module of Sawt, an ERP assistant for an Arabian " +
		"horse stable, talking to verified staff over WhatsApp. Use the available " +
		"tools to answer questions about horses, care plans, and tasks, and to " +
		"update task status when asked. Always resolve a horse or task by id via " +
		"get_horse / list_tasks before acting on it — never invent an id. If a " +
		"name search is ambiguous, ask the user to clarify instead of guessing. " +
		"Once you have enough information, stop calling tools and answer directly in plain text, " +
		"in the same language the user used, briefly — the reply may be spoken as a voice note. No markdown."

	if e.queries != nil && state.ChatID != "" {
		contact, err := e.queries.GetWaContact(ctx, state.ChatID)
		if err == nil {
			if contact.PromptOverride != nil && *contact.PromptOverride != "" {
				systemPrompt = *contact.PromptOverride
			} else if contact.AgentID != nil && *contact.AgentID != "" {
				agent, err := e.queries.GetAgent(ctx, *contact.AgentID)
				if err == nil && agent.SystemPrompt != "" {
					systemPrompt = agent.SystemPrompt
				}
			} else {
				// Fall back to the default agent prompt if set
				agent, err := e.queries.GetAgent(ctx, "default")
				if err == nil && agent.SystemPrompt != "" {
					systemPrompt = agent.SystemPrompt
				}
			}
		}
	}

	// Build the tools structure in OpenAI Tool schema format using typed structs
	tools := []ToolDefinition{
		{
			Type: "function",
			Function: FunctionDefinition{
				Name:        "get_horse",
				Description: "Look up a horse by exact id, or search by name (Arabic or English). Provide horseId OR nameQuery.",
				Parameters: ParametersSchema{
					Type: "object",
					Properties: map[string]PropertySchema{
						"horseId":   {Type: "string", Description: "The exact database ID of the horse."},
						"nameQuery": {Type: "string", Description: "Name search string in English or Arabic."},
					},
				},
			},
		},
		{
			Type: "function",
			Function: FunctionDefinition{
				Name:        "get_care_plan",
				Description: "Get a horse's care plan (turnout minutes, feeding schedule, special instructions).",
				Parameters: ParametersSchema{
					Type: "object",
					Properties: map[string]PropertySchema{
						"horseId": {Type: "string", Description: "The exact database ID of the horse."},
					},
					Required: []string{"horseId"},
				},
			},
		},
		{
			Type: "function",
			Function: FunctionDefinition{
				Name:        "list_tasks",
				Description: "List tasks, optionally filtered by status (pending/in-progress/completed/missed), assigneeId, or horseId.",
				Parameters: ParametersSchema{
					Type: "object",
					Properties: map[string]PropertySchema{
						"status":     {Type: "string", Description: "pending, in-progress, completed, missed"},
						"assigneeId": {Type: "string", Description: "User ID of task assignee."},
						"horseId":    {Type: "string", Description: "Filter tasks for a specific horse."},
						"limit":      {Type: "integer", Description: "Max results to return (default 20)."},
					},
				},
			},
		},
		{
			Type: "function",
			Function: FunctionDefinition{
				Name:        "update_task_status",
				Description: "Update a task's status. status must be one of: pending, in-progress, completed, missed.",
				Parameters: ParametersSchema{
					Type: "object",
					Properties: map[string]PropertySchema{
						"taskId": {Type: "string", Description: "Exact task database ID."},
						"status": {Type: "string", Description: "pending, in-progress, completed, missed"},
					},
					Required: []string{"taskId", "status"},
				},
			},
		},
	}

	if state.Summary != "" {
		systemPrompt += "\n\nSummary of the conversation so far:\n" + state.Summary
	}

	// Keep history to last 8 messages + system prompt
	historyCount := len(state.Messages)
	if historyCount > 8 {
		historyCount = 8
	}
	messages := append([]Message{{Role: "system", Content: systemPrompt}}, state.Messages[len(state.Messages)-historyCount:]...)

	maxIterations := 4
	for i := 0; i < maxIterations; i++ {
		log.Printf("[workflow] Running LLM iteration %d...", i+1)
		aiMsg, err := e.complete(ctx, messages, tools, 0.0, 400)
		if err != nil {
			return fmt.Errorf("LLM operations execution call failed: %w", err)
		}

		messages = append(messages, *aiMsg)

		if len(aiMsg.ToolCalls) == 0 {
			// No further tool calls, this is our final answer
			state.FinalReply = aiMsg.Content
			return nil
		}

		// Execute tool calls
		for _, call := range aiMsg.ToolCalls {
			log.Printf("[workflow] LLM requested tool call: '%s' with arguments: %s", call.Function.Name, call.Function.Arguments)

			// Parse arguments
			var args map[string]interface{}
			if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
				args = make(map[string]interface{})
			}

			// Risky (medium/high) tools never execute directly: park the call
			// and ask the user to confirm; the next message resolves it.
			if riskOf(call.Function.Name) != "low" && e.queries != nil {
				if err := e.requestConfirmation(ctx, state, call.Function.Name, args); err != nil {
					return fmt.Errorf("failed to request confirmation: %w", err)
				}
				return nil
			}

			// Call ERP gateway tool
			toolRes, err := e.erpClient.CallTool(ctx, call.Function.Name, orgID, state.ActorIdentity.UID, args)
			if err != nil {
				toolRes = map[string]interface{}{"ok": false, "error": err.Error()}
			}

			state.ToolResults = append(state.ToolResults, map[string]interface{}{
				"tool":   call.Function.Name,
				"args":   args,
				"output": toolRes,
			})

			resBytes, _ := json.Marshal(toolRes)
			
			// Append response to history
			toolMessage := Message{
				Role:       "tool",
				Name:       call.Function.Name,
				ToolCallID: call.ID,
				Content:    string(resBytes),
			}
			messages = append(messages, toolMessage)
		}
	}

	state.FinalReply = "لم أتمكن من إكمال طلبك، تكرر استدعاء المهام بأسلوب غير صحيح."
	return nil
}
