package workflow

import (
	"context"
	"fmt"
	"sawt-go/database"
	"sawt-go/internal/trace"
	"strings"
	"time"
)

const (
	// defaultRecentTurns is how many stored turns are replayed into the LLM
	// context when the chat's agent has no max_history configured (D2).
	defaultRecentTurns = 8
	// maxRecentTurnsCap bounds agent-configured max_history so a misconfigured
	// agent cannot grow LLM prompts (and memory) without limit on a 1 GB host.
	maxRecentTurnsCap = 20
	// summarizeThreshold is the number of unsummarized turns that triggers a
	// rolling-summary pass.
	summarizeThreshold = 20
	// keepRecentUnsummarized is how many of the newest turns stay verbatim
	// (outside the summary) after a summary pass.
	keepRecentUnsummarized = 8
	// maxUnsummarizedFetch bounds how many turns a single load will pull.
	maxUnsummarizedFetch = 128
)

// LoadConversation returns the rolling summary and the most recent turns for a
// chat, ready to prepend to the current user message.
func (e *WorkflowEngine) LoadConversation(ctx context.Context, chatID string) (string, []Message, error) {
	if e.queries == nil || chatID == "" {
		return "", nil, nil
	}

	// Missing state row simply means a brand-new conversation.
	convState, err := e.queries.GetConversationState(ctx, chatID)
	if err != nil {
		convState = database.ConversationState{ChatID: chatID}
	}

	turns, err := e.queries.ListConversationTurnsAfter(ctx, database.ListConversationTurnsAfterParams{
		ChatID:  chatID,
		AfterID: convState.SummarizedThrough,
		Limit:   maxUnsummarizedFetch,
	})
	if err != nil {
		return convState.Summary, nil, fmt.Errorf("failed to load conversation turns: %w", err)
	}

	limit := e.conversationTurnLimit(ctx, chatID)
	if len(turns) > limit {
		turns = turns[len(turns)-limit:]
	}

	messages := make([]Message, 0, len(turns))
	for _, t := range turns {
		messages = append(messages, Message{Role: t.Role, Content: t.Content})
	}
	return convState.Summary, messages, nil
}

// conversationTurnLimit resolves how many stored turns to replay for a chat:
// the assigned agent's max_history when set (capped at maxRecentTurnsCap),
// otherwise defaultRecentTurns. Lookup failures fall back silently — memory
// depth must never break the reply path.
func (e *WorkflowEngine) conversationTurnLimit(ctx context.Context, chatID string) int {
	contact, err := e.queries.GetWaContact(ctx, chatID)
	if err != nil || contact.AgentID == nil || *contact.AgentID == "" {
		return defaultRecentTurns
	}
	agent, err := e.queries.GetAgent(ctx, *contact.AgentID)
	if err != nil || agent.MaxHistory <= 0 {
		return defaultRecentTurns
	}
	limit := int(agent.MaxHistory)
	if limit > maxRecentTurnsCap {
		limit = maxRecentTurnsCap
	}
	return limit
}

// SaveTurns persists the user/assistant exchange and, when enough turns have
// accumulated, folds older ones into the rolling summary in the background.
func (e *WorkflowEngine) SaveTurns(ctx context.Context, chatID, userText, assistantText string) {
	if e.queries == nil || chatID == "" {
		return
	}

	if userText != "" {
		if _, err := e.queries.CreateConversationTurn(ctx, database.CreateConversationTurnParams{
			ChatID: chatID, Role: "user", Content: userText,
		}); err != nil {
			trace.Logf(ctx, "[memory] failed to persist user turn for %s: %v", chatID, err)
		}
	}
	if assistantText != "" {
		if _, err := e.queries.CreateConversationTurn(ctx, database.CreateConversationTurnParams{
			ChatID: chatID, Role: "assistant", Content: assistantText,
		}); err != nil {
			trace.Logf(ctx, "[memory] failed to persist assistant turn for %s: %v", chatID, err)
		}
	}

	// Summarization is best-effort and must not add latency to the reply path.
	// It runs off the engine's app-lifetime base context (cancelled on shutdown)
	// rather than a detached context.Background(), and carries the message's trace
	// id so its log lines correlate with the turn that triggered it.
	traceID := trace.ID(ctx)
	base := e.baseCtx
	if base == nil {
		base = context.Background()
	}
	go func() {
		bgCtx, cancel := context.WithTimeout(base, 60*time.Second)
		defer cancel()
		if traceID != "" {
			bgCtx = trace.With(bgCtx, traceID)
		}
		if err := e.summarizeIfNeeded(bgCtx, chatID); err != nil {
			trace.Logf(bgCtx, "[memory] rolling summary failed for %s: %v", chatID, err)
		}
	}()
}

func (e *WorkflowEngine) summarizeIfNeeded(ctx context.Context, chatID string) error {
	convState, err := e.queries.GetConversationState(ctx, chatID)
	if err != nil {
		convState = database.ConversationState{ChatID: chatID}
	}

	turns, err := e.queries.ListConversationTurnsAfter(ctx, database.ListConversationTurnsAfterParams{
		ChatID:  chatID,
		AfterID: convState.SummarizedThrough,
		Limit:   maxUnsummarizedFetch,
	})
	if err != nil {
		return fmt.Errorf("failed to list turns: %w", err)
	}
	if len(turns) < summarizeThreshold {
		return nil
	}

	toFold := turns[:len(turns)-keepRecentUnsummarized]

	var transcript strings.Builder
	for _, t := range toFold {
		transcript.WriteString(t.Role)
		transcript.WriteString(": ")
		transcript.WriteString(t.Content)
		transcript.WriteString("\n")
	}

	systemPrompt := "You maintain a running summary of a WhatsApp conversation between a user and an " +
		"ERP assistant for a horse stable. Merge the previous summary with the new turns into one " +
		"updated summary of at most 150 words. Preserve names, ids, amounts, task states, and any " +
		"unresolved requests. Reply with only the summary text, in the conversation's language."

	userPrompt := fmt.Sprintf("Previous summary:\n%s\n\nNew turns:\n%s", convState.Summary, transcript.String())

	msg, err := e.complete(ctx, []Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt},
	}, nil, 0.0, 300)
	if err != nil {
		return fmt.Errorf("summary LLM call failed: %w", err)
	}
	if strings.TrimSpace(msg.Content) == "" {
		return fmt.Errorf("summary LLM call returned empty content")
	}

	if err := e.queries.UpsertConversationState(ctx, database.UpsertConversationStateParams{
		ChatID:            chatID,
		Summary:           strings.TrimSpace(msg.Content),
		SummarizedThrough: toFold[len(toFold)-1].ID,
	}); err != nil {
		return fmt.Errorf("failed to store summary: %w", err)
	}

	trace.Logf(ctx, "[memory] folded %d turns into the rolling summary for %s", len(toFold), chatID)
	return nil
}
