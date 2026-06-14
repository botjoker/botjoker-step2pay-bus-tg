package agentstore

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/botjoker/sambacrm-business-tg/internal/runtime"
	"github.com/botjoker/sambacrm-business-tg/internal/storage"
	"github.com/google/uuid"
)

// DBIntake реализует runtime.IntakeStore поверх sqlc.
type DBIntake struct {
	q *storage.Queries
}

func NewIntake(q *storage.Queries) *DBIntake { return &DBIntake{q: q} }

var _ runtime.IntakeStore = (*DBIntake)(nil)

func (s *DBIntake) LoadSchema(ctx context.Context, agentID uuid.UUID) ([]runtime.IntakeField, error) {
	rows, err := s.q.ListIntakeFields(ctx, toUUID(agentID))
	if err != nil {
		return nil, err
	}
	out := make([]runtime.IntakeField, 0, len(rows))
	for _, r := range rows {
		out = append(out, runtime.IntakeField{
			Key:         r.FieldKey,
			Label:       r.FieldLabel,
			Type:        r.FieldType,
			Required:    r.IsRequired,
			WhyWeAsk:    fromText(r.WhyWeAsk),
			AskPriority: int(r.AskPriority),
		})
	}
	return out, nil
}

func (s *DBIntake) LoadFacts(ctx context.Context, convID uuid.UUID) ([]runtime.Fact, error) {
	rows, err := s.q.ListConversationFacts(ctx, toUUID(convID))
	if err != nil {
		return nil, err
	}
	out := make([]runtime.Fact, 0, len(rows))
	for _, r := range rows {
		out = append(out, runtime.Fact{
			Key:      r.FieldKey,
			Value:    jsonbToString(r.FieldValue),
			Verified: r.IsVerified.Bool,
		})
	}
	return out, nil
}

// jsonbToString разворачивает jsonb-значение факта в человекочитаемую строку.
func jsonbToString(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return string(b)
	}
	switch t := v.(type) {
	case string:
		return t
	default:
		return fmt.Sprint(t)
	}
}
