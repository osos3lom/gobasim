package workflow

import (
	"context"
	"testing"

	"sawt-go/database"
	"sawt-go/internal/agentcfg"
)

// fakeQ implements only the two querier methods the provider resolver touches.
// Embedding database.Querier satisfies the interface; any other method call
// would panic, which is the intended tripwire — the resolver must not reach for
// anything else.
type fakeQ struct {
	database.Querier
	contact    database.WaContact
	contactErr error
	agents     map[string]database.Agent
}

func (f fakeQ) GetWaContact(_ context.Context, _ string) (database.WaContact, error) {
	return f.contact, f.contactErr
}

func (f fakeQ) GetAgent(_ context.Context, id string) (database.Agent, error) {
	a, ok := f.agents[id]
	if !ok {
		return database.Agent{}, context.Canceled // any error → "not found" for the test
	}
	return a, nil
}

func strptr(s string) *string { return &s }

func TestWithAgentProvidersPrefersAgentThenFallsBack(t *testing.T) {
	t.Setenv("TEST_LLM_KEY", "secret-value")

	agentLLM := agentcfg.LLM{Vendor: "nim", URL: "https://nim.example.com/v1/", APIKeyEnv: "TEST_LLM_KEY", Model: "llama-3.1"}
	q := fakeQ{
		contact: database.WaContact{ChatID: "chat1", AgentID: strptr("agent_a")},
		agents:  map[string]database.Agent{"agent_a": {ID: "agent_a", Llm: agentLLM.Marshal()}},
	}
	e := &WorkflowEngine{queries: q, providers: []llmProvider{{Name: "env-openai"}}}

	ctx := e.withAgentProviders(context.Background(), "chat1")
	got := providersFromCtx(ctx)
	if len(got) != 2 {
		t.Fatalf("expected 2 providers (agent + env fallback), got %d: %+v", len(got), got)
	}
	if got[0].Name != "agent:nim" || got[0].Model != "llama-3.1" || got[0].APIKey != "secret-value" {
		t.Errorf("primary provider wrong: %+v", got[0])
	}
	if got[0].BaseURL != "https://nim.example.com/v1" {
		t.Errorf("expected trailing slash trimmed, got %q", got[0].BaseURL)
	}
	if got[1].Name != "env-openai" {
		t.Errorf("expected env cascade as fallback, got %+v", got[1])
	}
}

func TestWithAgentProvidersFallsBackWhenKeyMissing(t *testing.T) {
	// api_key_env references an unset variable → agent provider is skipped and the
	// context is left untouched so chatCompletions uses the env cascade.
	agentLLM := agentcfg.LLM{Vendor: "nim", URL: "https://nim.example.com/v1", APIKeyEnv: "UNSET_LLM_KEY", Model: "llama-3.1"}
	q := fakeQ{
		contact: database.WaContact{ChatID: "chat1", AgentID: strptr("agent_a")},
		agents:  map[string]database.Agent{"agent_a": {ID: "agent_a", Llm: agentLLM.Marshal()}},
	}
	e := &WorkflowEngine{queries: q, providers: []llmProvider{{Name: "env-openai"}}}

	ctx := e.withAgentProviders(context.Background(), "chat1")
	if got := providersFromCtx(ctx); got != nil {
		t.Fatalf("expected no per-request providers (fallback to env), got %+v", got)
	}
}

func TestWithAgentProvidersNilQuerierIsNoop(t *testing.T) {
	e := &WorkflowEngine{queries: nil, providers: []llmProvider{{Name: "env"}}}
	ctx := e.withAgentProviders(context.Background(), "chat1")
	if got := providersFromCtx(ctx); got != nil {
		t.Fatalf("nil querier must be a no-op, got %+v", got)
	}
}
