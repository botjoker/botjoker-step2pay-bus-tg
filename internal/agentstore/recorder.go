package agentstore

import (
	"context"
	"encoding/json"

	"github.com/botjoker/sambacrm-business-tg/internal/runtime"
	"github.com/botjoker/sambacrm-business-tg/internal/storage"
	"github.com/google/uuid"
)

// DBRecorder реализует runtime.MessageRecorder поверх sqlc (InsertMessage).
type DBRecorder struct {
	q *storage.Queries
}

func NewRecorder(q *storage.Queries) *DBRecorder { return &DBRecorder{q: q} }

var _ runtime.MessageRecorder = (*DBRecorder)(nil)

func (r *DBRecorder) Record(ctx context.Context, m runtime.RecordedMessage) (uuid.UUID, error) {
	params := storage.InsertMessageParams{
		ProfileID:        toUUID(m.ProfileID),
		ConversationID:   toUUID(m.ConversationID),
		Role:             m.Role,
		Content:          toText(m.Content),
		ContentOriginal:  toText(m.ContentOriginal),
		ToolCalls:        marshalJSON(m.ToolCalls),
		ToolCallID:       toText(m.ToolCallID),
		DetectedLanguage:  toText(""),
		ResponseLanguage:  toText(m.ResponseLanguage),
		OperatorAccountID: toUUIDOrNull(m.OperatorAccountID),
		RedactionApplied:  toBool(m.RedactionApplied),
		RedactionLog:     marshalJSON(m.RedactionLog),
		TokensIn:         toInt4(m.TokensIn),
		TokensOut:        toInt4(m.TokensOut),
		CostUsd:          toNumeric(m.CostUSD),
		LatencyMs:        toInt4(int(m.LatencyMs)),
		LlmModel:         toText(m.LLMModel),
		LlmProvider:      toText(m.LLMProvider),
	}
	row, err := r.q.InsertMessage(ctx, params)
	if err != nil {
		return uuid.Nil, err
	}
	return fromUUID(row.ID), nil
}

// marshalJSON сериализует значение в []byte для jsonb-колонок.
// nil / пустые значения → nil (NULL в БД).
func marshalJSON(v any) []byte {
	if v == nil {
		return nil
	}
	switch t := v.(type) {
	case []byte:
		return t
	case map[string]any:
		if len(t) == 0 {
			return nil
		}
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	if string(b) == "null" {
		return nil
	}
	return b
}
