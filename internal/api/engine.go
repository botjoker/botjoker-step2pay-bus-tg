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

	// History возвращает видимую историю диалога для веб-виджета (восстановление
	// при перезагрузке/реконнекте SSE).
	History(ctx context.Context, conversationID uuid.UUID, limit int) ([]ChatHistoryMessage, error)

	// IntakeForm возвращает только ещё не заполненные поля опросника. Значения
	// контактов через этот контракт никогда не передаются в LLM.
	IntakeForm(ctx context.Context, conversationID uuid.UUID) ([]IntakeFormField, error)

	// SubmitIntake валидирует и сохраняет ответы формы напрямую в хранилище.
	SubmitIntake(ctx context.Context, conversationID uuid.UUID, submission IntakeSubmission) error

	ConsentDocument(ctx context.Context, conversationID uuid.UUID) (ConsentDocument, error)
}

// ChatHistoryMessage — одна реплика истории для веб-виджета.
type ChatHistoryMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// IntakeFormField — безопасное публичное описание поля, без внутренних
// validation/prompt-настроек агента.
type IntakeFormField struct {
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	Type     string   `json:"type"`
	Required bool     `json:"required"`
	Why      string   `json:"why,omitempty"`
	Options  []string `json:"options,omitempty"`
}

type IntakeSubmission struct {
	Values         map[string]any
	ConsentGranted bool
	UserAgent      string
}

type ConsentDocument struct {
	Title      string `json:"title"`
	Body       string `json:"body"`
	Version    string `json:"version"`
	TemplateID uuid.UUID
}

type IntakeValidationError struct{ Message string }

func (e *IntakeValidationError) Error() string { return e.Message }

// StartResult — ответ StartConversation.
type StartResult struct {
	ConversationID uuid.UUID
	ProfileID      uuid.UUID
	AgentID        uuid.UUID
}
