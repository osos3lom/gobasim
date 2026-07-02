package database

import (
	"context"
)

type Querier interface {
	GetUserByUsername(ctx context.Context, username string) (User, error)
	CreateUser(ctx context.Context, arg CreateUserParams) (User, error)
	GetSettings(ctx context.Context) (Setting, error)
	UpdateSettings(ctx context.Context, arg UpdateSettingsParams) error
	CreateSttHistory(ctx context.Context, arg CreateSttHistoryParams) error
	GetSttHistory(ctx context.Context, limit int32) ([]SttHistory, error)
	CreateTtsHistory(ctx context.Context, arg CreateTtsHistoryParams) error
	GetTtsHistory(ctx context.Context, limit int32) ([]TtsHistory, error)
	CreateWebhookLog(ctx context.Context, arg CreateWebhookLogParams) error
	GetAgent(ctx context.Context, id string) (Agent, error)
	ListAgents(ctx context.Context) ([]Agent, error)
	CreateAgent(ctx context.Context, arg CreateAgentParams) (Agent, error)
	UpdateAgentWorkflow(ctx context.Context, arg UpdateAgentWorkflowParams) (Agent, error)
	GetWaContact(ctx context.Context, chatID string) (WaContact, error)
	CreateOrUpdateWaContact(ctx context.Context, arg CreateOrUpdateWaContactParams) (WaContact, error)
	ListWaContacts(ctx context.Context) ([]WaContact, error)
	CreateWaActivity(ctx context.Context, arg CreateWaActivityParams) error
	ListRecentWaActivity(ctx context.Context, limit int32) ([]WaActivity, error)
	CreateConversationTurn(ctx context.Context, arg CreateConversationTurnParams) (ConversationTurn, error)
	ListConversationTurnsAfter(ctx context.Context, arg ListConversationTurnsAfterParams) ([]ConversationTurn, error)
	GetConversationState(ctx context.Context, chatID string) (ConversationState, error)
	UpsertConversationState(ctx context.Context, arg UpsertConversationStateParams) error
	UpsertPendingConfirmation(ctx context.Context, arg UpsertPendingConfirmationParams) error
	GetPendingConfirmation(ctx context.Context, chatID string) (PendingConfirmation, error)
	DeletePendingConfirmation(ctx context.Context, chatID string) error
}

var _ Querier = (*Queries)(nil)
