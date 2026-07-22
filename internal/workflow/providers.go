package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"sawt-go/database"
	"sawt-go/internal/agentcfg"
	"sawt-go/internal/trace"
)

type providersCtxKey struct{}

func providersFromCtx(ctx context.Context) []llmProvider {
	p, _ := ctx.Value(providersCtxKey{}).([]llmProvider)
	return p
}

func (e *WorkflowEngine) resolveAgent(ctx context.Context, chatID string) (database.Agent, bool) {
	if e.queries == nil || chatID == "" {
		return database.Agent{}, false
	}
	if contact, err := e.queries.GetWaContact(ctx, chatID); err == nil &&
		contact.AgentID != nil && *contact.AgentID != "" {
		if agent, err := e.queries.GetAgent(ctx, *contact.AgentID); err == nil {
			return agent, true
		}
	}
	if agent, err := e.queries.GetAgent(ctx, "default"); err == nil {
		return agent, true
	}
	return database.Agent{}, false
}

func (e *WorkflowEngine) ResolveTTS(ctx context.Context, chatID string) agentcfg.TTS {
	agent, ok := e.resolveAgent(ctx, chatID)
	if !ok {
		return agentcfg.DefaultTTS()
	}
	tts, err := agentcfg.ParseTTS(agent.Tts)
	if err != nil {
		return agentcfg.DefaultTTS()
	}
	return tts
}

func (e *WorkflowEngine) withAgentProviders(ctx context.Context, chatID string) context.Context {
	agent, ok := e.resolveAgent(ctx, chatID)
	if !ok {
		return ctx
	}
	cfg, err := agentcfg.ParseLLM(agent.Llm)
	if err != nil {
		return ctx
	}
	key := os.Getenv(cfg.APIKeyEnv)
	if key == "" || cfg.Model == "" || cfg.URL == "" {
		return ctx
	}
	primary := llmProvider{
		Name:    "agent:" + cfg.Vendor,
		BaseURL: strings.TrimRight(cfg.URL, "/"),
		APIKey:  key,
		Model:   cfg.Model,
	}
	return context.WithValue(ctx, providersCtxKey{}, append([]llmProvider{primary}, e.providers...))
}

func (e *WorkflowEngine) chatCompletions(ctx context.Context, messages []Message, tools []ToolDefinition, temp float32, maxTokens int) (*Message, error) {
	providers := providersFromCtx(ctx)
	if len(providers) == 0 {
		providers = e.providers
	}
	if len(providers) == 0 {
		return nil, fmt.Errorf("no LLM providers configured (set NIM_API_KEY and/or OPENAI_API_KEY)")
	}

	var lastErr error
	for _, p := range providers {
		msg, err := e.callProvider(ctx, p, messages, tools, temp, maxTokens)
		if err == nil {
			return msg, nil
		}
		trace.Logf(ctx, "[workflow] LLM provider '%s' failed, trying next: %v", p.Name, err)
		lastErr = err
	}
	return nil, fmt.Errorf("all LLM providers failed: %w", lastErr)
}

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
	defer func() { _ = resp.Body.Close() }()

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
