package agentstore

import (
	"context"

	"github.com/botjoker/sambacrm-business-tg/internal/runtime"
	"github.com/botjoker/sambacrm-business-tg/internal/storage"
	"github.com/google/uuid"
)

// DBFewShot реализует runtime.FewShotStore: примеры из promoted feedback (101).
type DBFewShot struct {
	q     *storage.Queries
	limit int32
}

func NewFewShot(q *storage.Queries) *DBFewShot {
	return &DBFewShot{q: q, limit: 5}
}

var _ runtime.FewShotStore = (*DBFewShot)(nil)

// Load возвращает до N примеров (user → исправленный/оригинальный assistant).
func (s *DBFewShot) Load(ctx context.Context, agentID uuid.UUID) ([]runtime.FewShotExample, error) {
	rows, err := s.q.ListPromotedFeedback(ctx, storage.ListPromotedFeedbackParams{
		AgentID: toUUID(agentID),
		Limit:   s.limit,
	})
	if err != nil {
		return nil, err
	}

	out := make([]runtime.FewShotExample, 0, len(rows))
	for _, r := range rows {
		msg, err := s.q.GetMessageBrief(ctx, r.MessageID)
		if err != nil {
			continue
		}
		// Ответ-пример: исправление оператора либо оригинальный ответ.
		assistant := fromText(r.Correction)
		if assistant == "" {
			assistant = fromText(msg.Content)
		}
		if assistant == "" {
			continue
		}
		// Реплика пользователя — предшествующее user-сообщение.
		userContent, err := s.q.GetPrecedingUserMessage(ctx, storage.GetPrecedingUserMessageParams{
			ConversationID: msg.ConversationID,
			Before:         msg.CreatedAt,
		})
		user := ""
		if err == nil {
			user = fromText(userContent)
		}
		out = append(out, runtime.FewShotExample{User: user, Assistant: assistant})
	}
	return out, nil
}
