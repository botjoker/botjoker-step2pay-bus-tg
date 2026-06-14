package runtime

import (
	"context"

	"github.com/botjoker/sambacrm-business-tg/internal/llm"
	"github.com/google/uuid"
)

// No-op реализации зависимостей. Используются по умолчанию в NewAgent, пока не
// подключены реальные адаптеры (038/039/03A–03E). Каждая безопасна и не падает.

type noopToolRegistry struct{}

func (noopToolRegistry) SchemasFor(context.Context, uuid.UUID) ([]llm.ToolDef, error) {
	return nil, nil
}
func (noopToolRegistry) Execute(context.Context, ToolExecCtx, string, map[string]any) (map[string]any, error) {
	return map[string]any{"status": "no_tools_configured"}, nil
}

type noopRAG struct{}

func (noopRAG) Search(context.Context, RAGSearchRequest) ([]RAGChunk, error) { return nil, nil }

// noopPII — пропускает текст без изменений (реальная regex-редакция — 03A).
type noopPII struct{}

func (noopPII) Redact(_ context.Context, text string) (string, map[string]any, error) {
	return text, nil, nil
}

type noopIntake struct{}

func (noopIntake) LoadSchema(context.Context, uuid.UUID) ([]IntakeField, error) { return nil, nil }
func (noopIntake) LoadFacts(context.Context, uuid.UUID) ([]Fact, error)         { return nil, nil }

// noopBilling — всегда разрешает (реальный учёт — 03E).
type noopBilling struct{}

func (noopBilling) IsHardCapHit(context.Context, uuid.UUID) (bool, error) { return false, nil }
func (noopBilling) Track(context.Context, BillingDelta) error             { return nil }

// noopMemory — пустая история (реальная — 038).
type noopMemory struct{}

func (noopMemory) Load(context.Context, uuid.UUID) ([]llm.Message, error) { return nil, nil }

// noopTakeover — оператор никогда не активен (реальный gate — 03B).
type noopTakeover struct{}

func (noopTakeover) IsActive(context.Context, uuid.UUID) (bool, error) { return false, nil }

// noopRecorder — ничего не пишет, возвращает свежий uuid (реальный sqlc — 03D).
type noopRecorder struct{}

func (noopRecorder) Record(context.Context, RecordedMessage) (uuid.UUID, error) {
	return uuid.New(), nil
}

type noopFewShot struct{}

func (noopFewShot) Load(context.Context, uuid.UUID) ([]FewShotExample, error) { return nil, nil }
