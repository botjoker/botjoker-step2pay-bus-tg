package agentstore

import (
	"context"

	"github.com/botjoker/sambacrm-business-tg/internal/llm"
	"github.com/botjoker/sambacrm-business-tg/internal/runtime"
	"github.com/botjoker/sambacrm-business-tg/internal/storage"
	"github.com/google/uuid"
)

// DBMemory реализует runtime.Memory поверх sqlc.
type DBMemory struct {
	q              *storage.Queries
	maxHistoryMsgs int
}

// NewMemory создаёт память с дефолтным окном в 20 сообщений.
func NewMemory(q *storage.Queries) *DBMemory {
	return &DBMemory{q: q, maxHistoryMsgs: 20}
}

var _ runtime.Memory = (*DBMemory)(nil)

// Load возвращает summary (как system) + последние N сообщений в хронологическом
// порядке. tool_calls в историю не реконструируются (короткая память — текст).
func (m *DBMemory) Load(ctx context.Context, convID uuid.UUID) ([]llm.Message, error) {
	var out []llm.Message

	conv, err := m.q.GetAgentConversation(ctx, toUUID(convID))
	if err == nil && conv.Summary.Valid && conv.Summary.String != "" {
		out = append(out, llm.Message{
			Role:    llm.RoleSystem,
			Content: "Контекст предыдущей беседы:\n" + conv.Summary.String,
		})
	}

	raw, err := m.q.LastMessages(ctx, storage.LastMessagesParams{
		ConversationID: toUUID(convID),
		Limit:          int32(m.maxHistoryMsgs),
	})
	if err != nil {
		return out, err
	}

	// LastMessages идёт по убыванию — разворачиваем.
	for i := len(raw) - 1; i >= 0; i-- {
		r := raw[i]
		role := llm.Role(r.Role)
		// operator-сообщения показываем модели как assistant.
		if role == "operator" {
			role = llm.RoleAssistant
		}
		// system из истории пропускаем (system собирается заново в prompt.go).
		if role == llm.RoleSystem {
			continue
		}
		out = append(out, llm.Message{
			Role:       role,
			Content:    fromText(r.Content),
			ToolCallID: fromText(r.ToolCallID),
		})
	}
	return out, nil
}
