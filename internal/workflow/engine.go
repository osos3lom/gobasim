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
	"sawt-go/internal/erp"
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
}

type WorkflowEngine struct {
	cfg       *config.Config
	erpClient *erp.Client
}

func NewWorkflowEngine(cfg *config.Config, erpClient *erp.Client) *WorkflowEngine {
	return &WorkflowEngine{
		cfg:       cfg,
		erpClient: erpClient,
	}
}

// chatCompletions makes a standard HTTP request to the configured OpenAI-compatible LLM endpoint (NVIDIA NIM).
func (e *WorkflowEngine) chatCompletions(ctx context.Context, messages []Message, tools []interface{}, temp float32, maxTokens int) (*Message, error) {
	if e.cfg.NimAPIKey == "" {
		return nil, fmt.Errorf("NIM_API_KEY is not configured")
	}

	url := fmt.Sprintf("%s/chat/completions", e.cfg.NimBaseURL)

	payload := map[string]interface{}{
		"model":       e.cfg.NimModel,
		"messages":    messages,
		"temperature": temp,
		"max_tokens":  maxTokens,
	}
	if len(tools) > 0 {
		payload["tools"] = tools
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

	timeout := 30 * time.Second
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
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

	msg, err := e.chatCompletions(ctx, messages, nil, 0.0, 10)
	if err != nil {
		log.Printf("[workflow] ClassifyIntent failed, defaulting to operations: %v", err)
		state.Intent = "operations"
		return nil
	}

	// Clean classification
	intent := msg.Content
	intent = stringsTrim(intent, " \n\r\t.,!\"'")
	intent = stringsToLower(intent)

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

	messages := append([]Message{{Role: "system", Content: systemPrompt}}, state.Messages...)
	msg, err := e.chatCompletions(ctx, messages, nil, 0.3, 200)
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

	systemPrompt := "You are the operations module of Sawt, an ERP assistant for an Arabian " +
		"horse stable, talking to verified staff over WhatsApp. Use the available " +
		"tools to answer questions about horses, care plans, and tasks, and to " +
		"update task status when asked. Always resolve a horse or task by id via " +
		"get_horse / list_tasks before acting on it — never invent an id. If a " +
		"name search is ambiguous, ask the user to clarify instead of guessing. " +
		"Once you have enough information, stop calling tools and answer directly in plain text, " +
		"in the same language the user used, briefly — the reply may be spoken as a voice note. No markdown."

	// Build the tools structure in OpenAI Tool schema format
	tools := []interface{}{
		map[string]interface{}{
			"type": "function",
			"function": map[string]interface{}{
				"name":        "get_horse",
				"description": "Look up a horse by exact id, or search by name (Arabic or English). Provide horseId OR nameQuery.",
				"parameters": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"horseId":   map[string]string{"type": "string", "description": "The exact database ID of the horse."},
						"nameQuery": map[string]string{"type": "string", "description": "Name search string in English or Arabic."},
					},
				},
			},
		},
		map[string]interface{}{
			"type": "function",
			"function": map[string]interface{}{
				"name":        "get_care_plan",
				"description": "Get a horse's care plan (turnout minutes, feeding schedule, special instructions).",
				"parameters": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"horseId": map[string]string{"type": "string", "description": "The exact database ID of the horse."},
					},
					"required": []string{"horseId"},
				},
			},
		},
		map[string]interface{}{
			"type": "function",
			"function": map[string]interface{}{
				"name":        "list_tasks",
				"description": "List tasks, optionally filtered by status (pending/in-progress/completed/missed), assigneeId, or horseId.",
				"parameters": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"status":     map[string]string{"type": "string", "description": "pending, in-progress, completed, missed"},
						"assigneeId": map[string]string{"type": "string", "description": "User ID of task assignee."},
						"horseId":    map[string]string{"type": "string", "description": "Filter tasks for a specific horse."},
						"limit":      map[string]string{"type": "integer", "description": "Max results to return (default 20)."},
					},
				},
			},
		},
		map[string]interface{}{
			"type": "function",
			"function": map[string]interface{}{
				"name":        "update_task_status",
				"description": "Update a task's status. status must be one of: pending, in-progress, completed, missed.",
				"parameters": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"taskId": map[string]string{"type": "string", "description": "Exact task database ID."},
						"status": map[string]string{"type": "string", "description": "pending, in-progress, completed, missed"},
					},
					"required": []string{"taskId", "status"},
				},
			},
		},
	}

	// Keep history to last 6 messages + system prompt
	historyCount := len(state.Messages)
	if historyCount > 6 {
		historyCount = 6
	}
	messages := append([]Message{{Role: "system", Content: systemPrompt}}, state.Messages[len(state.Messages)-historyCount:]...)

	maxIterations := 4
	for i := 0; i < maxIterations; i++ {
		log.Printf("[workflow] Running LLM iteration %d...", i+1)
		aiMsg, err := e.chatCompletions(ctx, messages, tools, 0.0, 400)
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

// Simple strings helpers to avoid dependency packages

func stringsTrim(s, cutset string) string {
	start := 0
	for start < len(s) && stringsContainsChar(cutset, s[start]) {
		start++
	}
	end := len(s)
	for end > start && stringsContainsChar(cutset, s[end-1]) {
		end--
	}
	return s[start:end]
}

func stringsToLower(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 32
		}
	}
	return string(b)
}

func stringsContainsChar(cutset string, c byte) bool {
	for i := range cutset {
		if cutset[i] == c {
			return true
		}
	}
	return false
}
