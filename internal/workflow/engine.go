package workflow

import (
	"context"
	"encoding/json"
	"fmt"
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
	// Resume carries durable slot-filling context (F-1 fix) when this turn is
	// resuming a parked tool call that was missing required args.
	// executeToolLoop, when this is set, seeds the LLM with the known args and
	// still-missing fields instead of starting the tool call cold.
	Resume *ResumeCollecting
}

// ResumeCollecting carries a "collecting" pending_confirmations row's state
// into the tool-calling loop across turns. See clarification.go.
type ResumeCollecting struct {
	ToolID        string
	KnownArgs     map[string]interface{}
	MissingFields []string
	RoundsSoFar   int32
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
	// baseCtx is the app-lifetime context background workers (the rolling
	// summarizer) derive from, so they are cancelled on shutdown instead of
	// running on a detached context.Background(). Set via SetBaseContext.
	baseCtx context.Context
}

// SetBaseContext sets the parent context for the engine's background workers
// (currently the rolling summarizer). Pass main's app context so a shutdown
// cancels in-flight summary work.
func (e *WorkflowEngine) SetBaseContext(ctx context.Context) {
	if ctx != nil {
		e.baseCtx = ctx
	}
}

func NewWorkflowEngine(cfg *config.Config, erpClient *erp.Client, queries database.Querier) *WorkflowEngine {
	e := &WorkflowEngine{
		cfg:       cfg,
		erpClient: erpClient,
		queries:   queries,
		baseCtx:   context.Background(),
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
	if cfg.GroqAPIKey != "" {
		e.providers = append(e.providers, llmProvider{
			Name: "groq", BaseURL: "https://api.groq.com/openai/v1", APIKey: cfg.GroqAPIKey, Model: "llama-3.3-70b-versatile",
		})
		log.Println("Workflow: Groq LLM provider registered (Rank 3).")
	} else {
		log.Println("Workflow: Groq LLM provider skipped (GROQ_API_KEY not set).")
	}

	e.complete = e.chatCompletions
	return e
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
		trace.Logf(ctx, "[workflow] ClassifyIntent failed, defaulting to other: %v", err)
		state.Intent = "other"
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
		state.Intent = "other" // Fallback to general chat, not tool-calling
	}

	trace.Logf(ctx, "[workflow] Classified intent: '%s'", state.Intent)
	return nil
}

func (e *WorkflowEngine) Execute(ctx context.Context, state *State) error {
	// Bind this request's LLM provider chain from the chat's agent config (falls
	// back to the env cascade when the agent has no complete LLM config). Every
	// downstream e.complete call inherits it via ctx.
	ctx = e.withAgentProviders(ctx, state.ChatID)

	// 0. A chat waiting on a yes/no consumes this message first.
	if handled, err := e.resolvePendingConfirmation(ctx, state); err != nil {
		trace.Logf(ctx, "[workflow] Pending-confirmation resolution failed: %v", err)
	} else if handled {
		return nil
	}

	// 0.5. A chat mid-slot-filling (a parked tool call missing required args,
	// F-1 fix) consumes this message next, resuming the same tool loop instead
	// of reclassifying.
	if resumed, err := e.resumeCollecting(ctx, state); err != nil {
		trace.Logf(ctx, "[workflow] Collecting-state resolution failed: %v", err)
	} else if resumed {
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
	// Connect the agent's enabled MCP servers and expose their tools alongside the
	// native ERP tools. Best-effort: unreachable servers are skipped, not fatal.
	bundle := e.connectMCP(ctx, state.ChatID)

	// Per-agent capabilities (skills manifest + sub-agent delegation) come from
	// the resolved agent row. An absent agent yields inert bundles.
	agentRow, _ := e.resolveAgent(ctx, state.ChatID)
	skills := e.buildSkills(agentRow)
	deleg := e.buildDelegate(agentRow)

	var tools []ToolDefinition
	if len(allowedTools) > 0 {
		tools = allowedTools
	}
	tools = append(tools, bundle.tools...)
	tools = append(tools, skills.tools...)
	tools = append(tools, deleg.tools...)

	// Lookup for the required-args validation gate (F-1 fix) below — built
	// once per turn from the same tools this loop exposes to the model, so it
	// transparently covers MCP-owned tools too.
	toolsByName := make(map[string]ToolDefinition, len(tools))
	for _, t := range tools {
		toolsByName[t.Function.Name] = t
	}

	systemPrompt := e.resolveSystemPrompt(ctx, state.ChatID, spec.DefaultPrompt)
	if state.Summary != "" {
		systemPrompt += "\n\nSummary of the conversation so far:\n" + state.Summary
	}
	systemPrompt += bundle.resources
	systemPrompt += skills.manifest
	if state.Resume != nil {
		argsJSON, _ := json.Marshal(state.Resume.KnownArgs)
		systemPrompt += fmt.Sprintf(
			"\n\nYou previously started calling the tool '%s' with these arguments already "+
				"confirmed by the user: %s. The user's next message below answers the "+
				"still-missing field(s): %s. Call '%s' again with ALL fields filled in (the ones "+
				"above plus the new answer) — do not ask again for fields already listed above.",
			state.Resume.ToolID, string(argsJSON), strings.Join(state.Resume.MissingFields, ", "), state.Resume.ToolID)
	}

	// state.Messages is already bounded by the memory subsystem's per-agent
	// max_history (LoadConversation) — no second, hardcoded truncation here.
	messages := append([]Message{{Role: "system", Content: systemPrompt}}, state.Messages...)

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

			name := call.Function.Name

			// Synthetic engine tools (skill disclosure, sub-agent delegation) run
			// in-process, have no side effects on ERP/MCP, and so bypass the
			// confirmation gate.
			var toolRes map[string]interface{}
			switch {
			case name == loadSkillTool:
				toolRes = skills.load(str(args["skill"]))
			case name == delegateTool && deleg.active:
				toolRes = e.runDelegate(ctx, deleg.sub, str(args["agent"]), str(args["task"]))
			default:
				// Required-args validation gate (F-1 fix): before this call is
				// parked or executed, check it against the tool's own declared
				// schema (auto-deriving fields like an English name from an Arabic
				// one where configured). Applies uniformly to both branches below.
				if def, known := toolsByName[name]; known {
					complete, cErr := e.enforceRequiredArgs(ctx, state, spec.Name, def, args)
					if cErr != nil {
						return fmt.Errorf("failed to validate required args for %s: %w", name, cErr)
					}
					if !complete {
						return nil // clarifying question already set as state.FinalReply
					}
				}

				// Risk gate: MCP tools carry their own read-only hint (write-capable
				// ones default to medium), ERP tools use the static risk table.
				// Anything above "low" parks for confirmation instead of executing.
				risk := riskOf(name)
				if r, ok := bundle.risk(name); ok {
					risk = r
				}
				if risk != "low" && e.queries != nil {
					if err := e.requestConfirmation(ctx, state, name, args); err != nil {
						return fmt.Errorf("failed to request confirmation: %w", err)
					}
					return nil
				}

				// Route to the owning executor: MCP-served tools to their server,
				// everything else to the ERP gateway.
				if bundle.owns(name) {
					toolRes = callMCPTool(ctx, bundle.dispatch[name], name, args)
				} else if res, err := e.erpClient.CallTool(ctx, name, orgID, state.ActorIdentity.UID, args); err != nil {
					toolRes = map[string]interface{}{"ok": false, "error": err.Error()}
				} else {
					toolRes = res
				}
			}

			state.ToolResults = append(state.ToolResults, map[string]interface{}{
				"tool":   call.Function.Name,
				"args":   args,
				"output": toolRes,
			})

			// Durable per-step log (C2): a crash mid-loop still leaves a record of
			// what ran, unlike the best-effort wa_activity blob written once at the
			// end. Best-effort — never fails the turn.
			toolStatus := "ok"
			if ok, present := toolRes["ok"].(bool); present && !ok {
				toolStatus = "error"
			}
			e.logToolExecution(ctx, state.ChatID, call.Function.Name, args, toolRes, toolStatus)

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

// logToolExecution durably records one tool execution to tool_executions (C2).
// Best-effort: a logging failure must never break the reply path, and a nil
// querier (unit tests / unconfigured DB) is a no-op.
func (e *WorkflowEngine) logToolExecution(ctx context.Context, chatID, toolID string, args, result map[string]interface{}, status string) {
	if e.queries == nil {
		return
	}
	argsBytes, _ := json.Marshal(args)
	if len(argsBytes) == 0 {
		argsBytes = []byte("{}")
	}
	resBytes, _ := json.Marshal(result)
	if len(resBytes) == 0 {
		resBytes = []byte("{}")
	}
	if err := e.queries.CreateToolExecution(ctx, database.CreateToolExecutionParams{
		ChatID: chatID, ToolID: toolID, Args: argsBytes, Result: resBytes, Status: status,
	}); err != nil {
		trace.Logf(ctx, "[workflow] failed to log tool execution %s/%s: %v", chatID, toolID, err)
	}
}
