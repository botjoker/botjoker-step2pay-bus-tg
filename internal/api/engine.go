package api

import (
	"context"

	"github.com/botjoker/sambacrm-business-tg/internal/llm"
	"github.com/botjoker/sambacrm-business-tg/internal/runtime"
	"github.com/google/uuid"
)

// Engine — абстракция исполнения агента, чтобы api-пакет не зависел напрямую от
// sqlc/agentstore. Реальная реализация (загрузка конфигурации агента, постройка
// LLM-провайдера, runtime.Agent.Run) живёт в internal/agentstore (требует sqlc)
// и подключается в cmd/agent через Server.SetEngine.
type Engine interface {
	// StartConversation резолвит канал по web_slug и возвращает (или создаёт)
	// диалог для внешнего пользователя.
	StartConversation(ctx context.Context, webSlug, externalUserID string) (StartResult, error)

	// HandleMessage асинхронно обрабатывает сообщение: запускает агента и
	// публикует StreamEvent в SSE-хаб канала conv:{cid}.
	HandleMessage(ctx context.Context, conversationID uuid.UUID, text string) error

	// Test — playground: прогон одного сообщения для агента без транспорта,
	// возвращает канал событий напрямую.
	Test(ctx context.Context, agentID uuid.UUID, message string) (<-chan llm.StreamEvent, error)

	// TriggerIngest ставит задачу индексации источника знаний.
	TriggerIngest(ctx context.Context, sourceID, profileID uuid.UUID) error

	// OperatorMessage пересылает сообщение оператора в транспорт (live takeover).
	// att != nil → к сообщению прикрепляется файл-вложение (документ, F1-1).
	OperatorMessage(ctx context.Context, conversationID, operatorAccountID uuid.UUID, text, mode string, att *runtime.Attachment) error
}

// StartResult — ответ StartConversation.
type StartResult struct {
	ConversationID uuid.UUID
	ProfileID      uuid.UUID
	AgentID        uuid.UUID
}
