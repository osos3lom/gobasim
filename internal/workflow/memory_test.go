package workflow

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"sawt-go/config"
	"sawt-go/database"
	"sawt-go/internal/erp"
)

// fakeQuerier stubs the conversation-memory queries; any other Querier method
// panics via the embedded nil interface, which is fine for these tests.
type fakeQuerier struct {
	database.Querier

	mu       sync.Mutex
	turns    []database.ConversationTurn
	state    database.ConversationState
	hasState bool
	nextID   int64
}

func (f *fakeQuerier) CreateConversationTurn(ctx context.Context, arg database.CreateConversationTurnParams) (database.ConversationTurn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	t := database.ConversationTurn{ID: f.nextID, ChatID: arg.ChatID, Role: arg.Role, Content: arg.Content}
	f.turns = append(f.turns, t)
	return t, nil
}

func (f *fakeQuerier) ListConversationTurnsAfter(ctx context.Context, arg database.ListConversationTurnsAfterParams) ([]database.ConversationTurn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []database.ConversationTurn
	for _, t := range f.turns {
		if t.ChatID == arg.ChatID && t.ID > arg.AfterID {
			out = append(out, t)
		}
		if len(out) >= int(arg.Limit) {
			break
		}
	}
	return out, nil
}

func (f *fakeQuerier) GetConversationState(ctx context.Context, chatID string) (database.ConversationState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.hasState {
		return database.ConversationState{}, fmt.Errorf("no rows")
	}
	return f.state, nil
}

func (f *fakeQuerier) UpsertConversationState(ctx context.Context, arg database.UpsertConversationStateParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state = database.ConversationState{ChatID: arg.ChatID, Summary: arg.Summary, SummarizedThrough: arg.SummarizedThrough}
	f.hasState = true
	return nil
}

func newMemoryTestEngine(q database.Querier, fake completionFn) *WorkflowEngine {
	e := NewWorkflowEngine(&config.Config{NimModel: "test-model"}, erp.NewClient("http://localhost:0", ""), q)
	e.complete = fake
	return e
}

func seedTurns(q *fakeQuerier, chatID string, n int) {
	for i := 0; i < n; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		_, _ = q.CreateConversationTurn(context.Background(), database.CreateConversationTurnParams{
			ChatID: chatID, Role: role, Content: fmt.Sprintf("turn %d", i+1),
		})
	}
}

func TestLoadConversationReturnsRecentTurns(t *testing.T) {
	q := &fakeQuerier{}
	seedTurns(q, "chat1", 12)
	e := newMemoryTestEngine(q, nil)

	summary, msgs, err := e.LoadConversation(context.Background(), "chat1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if summary != "" {
		t.Fatalf("expected empty summary for fresh chat, got %q", summary)
	}
	if len(msgs) != maxRecentTurns {
		t.Fatalf("expected %d recent turns, got %d", maxRecentTurns, len(msgs))
	}
	// Should be the LAST 8 turns (5..12), in order.
	if msgs[0].Content != "turn 5" || msgs[len(msgs)-1].Content != "turn 12" {
		t.Fatalf("expected turns 5..12, got first=%q last=%q", msgs[0].Content, msgs[len(msgs)-1].Content)
	}
}

func TestLoadConversationRespectsSummaryWatermark(t *testing.T) {
	q := &fakeQuerier{}
	seedTurns(q, "chat1", 10)
	q.state = database.ConversationState{ChatID: "chat1", Summary: "earlier context", SummarizedThrough: 6}
	q.hasState = true
	e := newMemoryTestEngine(q, nil)

	summary, msgs, err := e.LoadConversation(context.Background(), "chat1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if summary != "earlier context" {
		t.Fatalf("expected stored summary, got %q", summary)
	}
	if len(msgs) != 4 { // turns 7..10
		t.Fatalf("expected 4 unsummarized turns, got %d", len(msgs))
	}
	if msgs[0].Content != "turn 7" {
		t.Fatalf("expected first unsummarized turn 7, got %q", msgs[0].Content)
	}
}

func TestSaveTurnsPersistsExchange(t *testing.T) {
	q := &fakeQuerier{}
	e := newMemoryTestEngine(q, func(ctx context.Context, m []Message, tools []ToolDefinition, temp float32, maxTokens int) (*Message, error) {
		return &Message{Role: "assistant", Content: "irrelevant"}, nil
	})

	e.SaveTurns(context.Background(), "chat1", "hello", "hi there")

	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.turns) != 2 {
		t.Fatalf("expected 2 persisted turns, got %d", len(q.turns))
	}
	if q.turns[0].Role != "user" || q.turns[1].Role != "assistant" {
		t.Fatalf("expected user then assistant, got %s then %s", q.turns[0].Role, q.turns[1].Role)
	}
}

func TestSummarizeIfNeededFoldsOldTurns(t *testing.T) {
	q := &fakeQuerier{}
	seedTurns(q, "chat1", summarizeThreshold) // exactly at threshold
	e := newMemoryTestEngine(q, func(ctx context.Context, m []Message, tools []ToolDefinition, temp float32, maxTokens int) (*Message, error) {
		return &Message{Role: "assistant", Content: "NEW SUMMARY"}, nil
	})

	if err := e.summarizeIfNeeded(context.Background(), "chat1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.hasState {
		t.Fatal("expected conversation state to be written")
	}
	if q.state.Summary != "NEW SUMMARY" {
		t.Fatalf("expected updated summary, got %q", q.state.Summary)
	}
	wantWatermark := int64(summarizeThreshold - keepRecentUnsummarized)
	if q.state.SummarizedThrough != wantWatermark {
		t.Fatalf("expected watermark %d, got %d", wantWatermark, q.state.SummarizedThrough)
	}
}

func TestSummarizeIfNeededSkipsShortThreads(t *testing.T) {
	q := &fakeQuerier{}
	seedTurns(q, "chat1", summarizeThreshold-1)
	e := newMemoryTestEngine(q, func(ctx context.Context, m []Message, tools []ToolDefinition, temp float32, maxTokens int) (*Message, error) {
		t.Fatal("LLM must not be called below the threshold")
		return nil, nil
	})

	if err := e.summarizeIfNeeded(context.Background(), "chat1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if q.hasState {
		t.Fatal("expected no summary to be written below the threshold")
	}
}

func TestSummaryIsInjectedIntoSystemPrompt(t *testing.T) {
	var captured []Message
	e := newTestEngine(func(ctx context.Context, m []Message, tools []ToolDefinition, temp float32, maxTokens int) (*Message, error) {
		if tools == nil && len(m) == 2 && strings.Contains(m[0].Content, "Classify") {
			return &Message{Role: "assistant", Content: "other"}, nil
		}
		captured = m
		return &Message{Role: "assistant", Content: "ok"}, nil
	})

	state := linkedState("what did we decide?")
	state.Summary = "The user asked about horse Najm's care plan yesterday."

	if err := e.Execute(context.Background(), state); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(captured) == 0 || captured[0].Role != "system" {
		t.Fatal("expected a captured system message")
	}
	if !strings.Contains(captured[0].Content, "Najm's care plan") {
		t.Fatalf("expected summary in system prompt, got %q", captured[0].Content)
	}
}
