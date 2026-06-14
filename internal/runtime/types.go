package runtime

import (
	"context"

	"github.com/botjoker/sambacrm-business-tg/internal/llm"
	"github.com/google/uuid"
)

// AgentConfig — снимок конфигурации агента (из таблицы agents), нужный рантайму.
type AgentConfig struct {
	AgentID          uuid.UUID
	ProfileID        uuid.UUID
	Persona          string
	LLMModel         string
	Temperature      float32
	MaxTokens        int
	MaxIterations    int
	RagEnabled       bool
	RagTopK          int
	RagMinScore      float32
	DefaultLang      string
	AutoDetectLang   bool
	AllowedLanguages []string
	VisionEnabled    bool
}

// RunRequest — входящее сообщение для обработки.
type RunRequest struct {
	ConversationID uuid.UUID
	UserMessage    string
	Attachments    []llm.Attachment
}

// --- доменные структуры ---

// RAGChunk — фрагмент знаний из rag-сервиса.
type RAGChunk struct {
	Content string
	Source  RAGSource
	Score   float32
}

// RAGSource — источник чанка (для цитат).
type RAGSource struct {
	ID    uuid.UUID
	Title string
}

// RAGSearchRequest — запрос в rag-сервис.
type RAGSearchRequest struct {
	ProfileID uuid.UUID
	AgentID   uuid.UUID
	Query     string
	TopK      int
	MinScore  float32
}

// IntakeField — поле опросника (из agent_intake_fields).
type IntakeField struct {
	Key        string
	Label      string
	Type       string
	Required   bool
	WhyWeAsk   string
	AskPriority int
}

// Fact — собранный факт диалога (из agent_conversation_facts).
type Fact struct {
	Key      string
	Value    string
	Verified bool
}

// FewShotExample — пример из feedback (promoted_to_few_shot).
type FewShotExample struct {
	User      string
	Assistant string
}

// BillingDelta — приращение для биллинг-счётчика.
type BillingDelta struct {
	ProfileID  uuid.UUID
	Messages   int
	TokensIn   int64
	TokensOut  int64
	CostUSD    float64
	Embeddings int
	Proactive  int
}

// ToolExecCtx — контекст выполнения инструмента.
type ToolExecCtx struct {
	ProfileID      uuid.UUID
	AgentID        uuid.UUID
	ConversationID uuid.UUID
	MessageID      uuid.UUID
}

// RecordedMessage — что записываем в agent_messages.
type RecordedMessage struct {
	ConversationID   uuid.UUID
	ProfileID        uuid.UUID
	Role             string
	Content          string
	ContentOriginal  string
	ToolCalls        []llm.ToolCall
	ToolCallID       string
	TokensIn         int
	TokensOut        int
	CostUSD          float64
	LatencyMs        int64
	LLMModel          string
	LLMProvider       string
	ResponseLanguage  string
	OperatorAccountID uuid.UUID // для role=operator (live takeover)
	RedactionApplied  bool
	RedactionLog      map[string]any
}

// --- интерфейсы зависимостей (dependency injection) ---
// Реальные имплементации: 038 (Memory), 039 (ToolRegistry), 03A (PII),
// 03B (TakeoverGate), 03E (BillingTracker), + sqlc-адаптеры (MessageRecorder,
// IntakeStore) подключаются в 03D после генерации sqlc.

// ToolRegistry — реестр и диспетчер инструментов.
type ToolRegistry interface {
	SchemasFor(ctx context.Context, agentID uuid.UUID) ([]llm.ToolDef, error)
	Execute(ctx context.Context, ec ToolExecCtx, name string, args map[string]any) (map[string]any, error)
}

// RAGClient — клиент к sambacrm-agent-rag.
type RAGClient interface {
	Search(ctx context.Context, req RAGSearchRequest) ([]RAGChunk, error)
}

// PIIClient — редакция PII. Возвращает (отредактированный текст, лог, ошибка).
type PIIClient interface {
	Redact(ctx context.Context, text string) (string, map[string]any, error)
}

// IntakeStore — доступ к схеме опросника и собранным фактам.
type IntakeStore interface {
	LoadSchema(ctx context.Context, agentID uuid.UUID) ([]IntakeField, error)
	LoadFacts(ctx context.Context, convID uuid.UUID) ([]Fact, error)
}

// BillingTracker — учёт расхода и хард-кап.
type BillingTracker interface {
	IsHardCapHit(ctx context.Context, profileID uuid.UUID) (bool, error)
	Track(ctx context.Context, d BillingDelta) error
}

// Memory — короткая память диалога (история сообщений для LLM).
type Memory interface {
	Load(ctx context.Context, convID uuid.UUID) ([]llm.Message, error)
}

// TakeoverGate — проверка активного оператора (live takeover).
type TakeoverGate interface {
	IsActive(ctx context.Context, convID uuid.UUID) (bool, error)
}

// MessageRecorder — запись сообщений в БД. Возвращает id вставленного сообщения.
type MessageRecorder interface {
	Record(ctx context.Context, m RecordedMessage) (uuid.UUID, error)
}

// FewShotStore — примеры для few-shot (из feedback).
type FewShotStore interface {
	Load(ctx context.Context, agentID uuid.UUID) ([]FewShotExample, error)
}
