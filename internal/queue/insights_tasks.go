package queue

import (
	"encoding/json"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
)

// TypeConversationInsights — задача пост-анализа диалога.
const TypeConversationInsights = "insights:conversation"

// TypeWeeklyDigest — задача еженедельного дайджеста (093).
const TypeWeeklyDigest = "insights:weekly_digest"

// InsightsPayload — полезная нагрузка задачи insights.
type InsightsPayload struct {
	ConversationID uuid.UUID `json:"conversation_id"`
}

// NewInsightsTask создаёт задачу анализа диалога.
func NewInsightsTask(cid uuid.UUID) (*asynq.Task, error) {
	p, err := json.Marshal(InsightsPayload{ConversationID: cid})
	if err != nil {
		return nil, err
	}
	return asynq.NewTask(TypeConversationInsights, p), nil
}

// ParseInsightsPayload разбирает payload задачи.
func ParseInsightsPayload(t *asynq.Task) (InsightsPayload, error) {
	var p InsightsPayload
	err := json.Unmarshal(t.Payload(), &p)
	return p, err
}

// WeeklyDigestPayload — нагрузка дайджеста.
type WeeklyDigestPayload struct {
	ProfileID uuid.UUID `json:"profile_id"`
}

// NewWeeklyDigestTask создаёт задачу дайджеста.
func NewWeeklyDigestTask(pid uuid.UUID) (*asynq.Task, error) {
	p, err := json.Marshal(WeeklyDigestPayload{ProfileID: pid})
	if err != nil {
		return nil, err
	}
	return asynq.NewTask(TypeWeeklyDigest, p), nil
}

// ParseWeeklyDigestPayload разбирает payload дайджеста.
func ParseWeeklyDigestPayload(t *asynq.Task) (WeeklyDigestPayload, error) {
	var p WeeklyDigestPayload
	err := json.Unmarshal(t.Payload(), &p)
	return p, err
}
