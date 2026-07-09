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
	"sawt-go/internal/trace"
	"strings"
	"time"
)

type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	Name       string     `json:"name,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
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

// llmProvider is one OpenAI-compatible chat-completions endpoint in the
// fallback chain (same cascade pattern as the STT/TTS orchestrators).
type llmProvider struct {
	Name    string
	BaseURL string
	APIKey  string
	Model   string
}

type WorkflowEngine struct {
	cfg        *config.Config
	erpClient  *erp.Client
	httpClient *http.Client
	queries    database.Querier
	complete   completionFn
	providers  []llmProvider
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

	if cfg.NimAPIKey != "" {
		e.providers = append(e.providers, llmProvider{
			Name: "nim", BaseURL: cfg.NimBaseURL, APIKey: cfg.NimAPIKey, Model: cfg.NimModel,
		})
		log.Println("Workflow: NIM LLM provider registered (Rank 1).")
	} else {
		log.Println("Workflow: NIM LLM provider skipped (NIM_API_KEY not set).")
	}
	if cfg.OpenaiAPIKey != "" {
		e.providers = append(e.providers, llmProvider{
			Name: "openai-compatible", BaseURL: cfg.OpenaiAPIBase, APIKey: cfg.OpenaiAPIKey, Model: cfg.LlmFallbackModel,
		})
		log.Println("Workflow: OpenAI-compatible fallback LLM provider registered (Rank 2).")
	} else {
		log.Println("Workflow: OpenAI-compatible fallback LLM provider skipped (OPENAI_API_KEY not set).")
	}

	e.complete = e.chatCompletions
	return e
}

// chatCompletions cascades through the registered LLM providers, returning the
// first successful completion.
func (e *WorkflowEngine) chatCompletions(ctx context.Context, messages []Message, tools []ToolDefinition, temp float32, maxTokens int) (*Message, error) {
	if len(e.providers) == 0 {
		return nil, fmt.Errorf("no LLM providers configured (set NIM_API_KEY and/or OPENAI_API_KEY)")
	}

	var lastErr error
	for _, p := range e.providers {
		msg, err := e.callProvider(ctx, p, messages, tools, temp, maxTokens)
		if err == nil {
			return msg, nil
		}
		trace.Logf(ctx, "[workflow] LLM provider '%s' failed, trying next: %v", p.Name, err)
		lastErr = err
	}
	return nil, fmt.Errorf("all LLM providers failed: %w", lastErr)
}

// callProvider makes a standard HTTP request to one OpenAI-compatible endpoint.
func (e *WorkflowEngine) callProvider(ctx context.Context, p llmProvider, messages []Message, tools []ToolDefinition, temp float32, maxTokens int) (*Message, error) {
	url := fmt.Sprintf("%s/chat/completions", p.BaseURL)

	payload := ChatCompletionRequest{
		Model:       p.Model,
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
	req.Header.Set("Authorization", "Bearer "+p.APIKey)

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
		"sales = available horses or stalls, service packages, tour bookings, CRM inquiries\n" +
		"breeding = stallion/mare breeding bookings, pregnancy status, foals, bloodlines\n" +
		"other = greetings, small talk, anything unrelated to the stable"

	messages := []Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: lastHumanText},
	}

	msg, err := e.complete(ctx, messages, nil, 0.0, 10)
	if err != nil {
		trace.Logf(ctx, "[workflow] ClassifyIntent failed, defaulting to operations: %v", err)
		state.Intent = "operations"
		return nil
	}

	// Clean classification using standard library strings package
	intent := msg.Content
	intent = strings.Trim(intent, " \n\r\t.,!\"'")
	intent = strings.ToLower(intent)

	// Validate intent
	switch intent {
	case "operations", "accounting", "administration", "sales", "breeding", "other":
		state.Intent = intent
	default:
		state.Intent = "operations" // Safe default
	}

	trace.Logf(ctx, "[workflow] Classified intent: '%s'", state.Intent)
	return nil
}

func (e *WorkflowEngine) Execute(ctx context.Context, state *State) error {
	// 0. A chat waiting on a yes/no consumes this message first.
	if handled, err := e.resolvePendingConfirmation(ctx, state); err != nil {
		trace.Logf(ctx, "[workflow] Pending-confirmation resolution failed: %v", err)
	} else if handled {
		return nil
	}

	// Route client role directly to clientAgent bypassing intent classification
	if state.ActorIdentity != nil && strings.ToLower(state.ActorIdentity.Role) == "client" {
		trace.Logf(ctx, "[workflow] Client role detected. Routing directly to clientAgent tool loop.")
		return e.executeToolLoop(ctx, state, clientAgent)
	}

	// 1. Classify Intent
	if err := e.ClassifyIntent(ctx, state); err != nil {
		return err
	}

	// 2. Route based on classification; each agent only sees its own tools.
	switch state.Intent {
	case "operations":
		return e.executeToolLoop(ctx, state, operationsAgent)
	case "accounting":
		return e.executeToolLoop(ctx, state, accountingAgent)
	case "administration":
		return e.executeToolLoop(ctx, state, administrationAgent)
	case "sales":
		return e.executeToolLoop(ctx, state, salesAgent)
	case "breeding":
		return e.executeToolLoop(ctx, state, breedingAgent)
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

// resolveSystemPrompt returns the effective persona prompt for a chat: the
// contact's prompt override wins, then its assigned agent's prompt, then the
// stored default agent, then the spec's built-in prompt.
func (e *WorkflowEngine) resolveSystemPrompt(ctx context.Context, chatID, fallback string) string {
	systemPrompt := fallback

	if e.queries != nil && chatID != "" {
		contact, err := e.queries.GetWaContact(ctx, chatID)
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
	return systemPrompt
}

// executeToolLoop runs the bounded tool-calling loop for one agent spec. Only
// that agent's tools are exposed to the model; risky calls detour through the
// confirmation flow.
func (e *WorkflowEngine) executeToolLoop(ctx context.Context, state *State, spec agentSpec) error {
	if state.ActorIdentity == nil || len(state.ActorIdentity.OrgIDs) == 0 {
		state.FinalReply = "هذا الرقم غير مرتبط بحساب في النظام بعد. يرجى التواصل مع الإدارة لربط رقمك.\n" +
			"This number isn't linked to an ERP account yet — please ask an admin to link it."
		return nil
	}
	orgID := state.ActorIdentity.OrgIDs[0]

	var allowedTools []ToolDefinition
	for _, t := range spec.Tools {
		minRole := getMinRoleForTool(t.Function.Name)
		if hasRole(state.ActorIdentity.Role, minRole) {
			allowedTools = append(allowedTools, t)
		}
	}
	var tools []ToolDefinition
	if len(allowedTools) > 0 {
		tools = allowedTools
	}

	systemPrompt := e.resolveSystemPrompt(ctx, state.ChatID, spec.DefaultPrompt)
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
		trace.Logf(ctx, "[workflow] Running LLM iteration %d...", i+1)
		aiMsg, err := e.complete(ctx, messages, tools, 0.0, 400)
		if err != nil {
			return fmt.Errorf("LLM %s execution call failed: %w", spec.Name, err)
		}

		messages = append(messages, *aiMsg)

		if len(aiMsg.ToolCalls) == 0 {
			// No further tool calls, this is our final answer
			state.FinalReply = aiMsg.Content
			return nil
		}

		// Execute tool calls
		for _, call := range aiMsg.ToolCalls {
			trace.Logf(ctx, "[workflow] LLM requested tool call: '%s' with arguments: %s", call.Function.Name, call.Function.Arguments)

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
