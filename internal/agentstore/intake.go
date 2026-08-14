package agentstore

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/botjoker/sambacrm-business-tg/internal/pii"
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
			Key:             r.FieldKey,
			Label:           r.FieldLabel,
			Type:            r.FieldType,
			Required:        r.IsRequired,
			WhyWeAsk:        fromText(r.WhyWeAsk),
			ElicitationHint: fromText(r.ElicitationHint),
			AskPriority:     int(r.AskPriority),
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

func (s *DBIntake) IsCompleted(ctx context.Context, convID uuid.UUID) (bool, error) {
	conv, err := s.q.GetAgentConversation(ctx, toUUID(convID))
	if err != nil {
		return false, err
	}
	var state struct {
		ContactReceived bool `json:"contact_received"`
	}
	if len(conv.Context) == 0 {
		return false, nil
	}
	if err := json.Unmarshal(conv.Context, &state); err != nil {
		return false, err
	}
	return state.ContactReceived, nil
}

// LoadPreviousFacts — cross-conversation memory. Требует sqlc-регенерации
// (make sqlc) после добавления ListPreviousFactsForContact в queries.sql.
func (s *DBIntake) LoadPreviousFacts(ctx context.Context, agentID, convID uuid.UUID) ([]runtime.PreviousFact, error) {
	rows, err := s.q.ListPreviousFactsForContact(ctx, storage.ListPreviousFactsForContactParams{
		AgentID: toUUID(agentID),
		ID:      toUUID(convID),
	})
	if err != nil {
		return nil, err
	}
	out := make([]runtime.PreviousFact, 0, len(rows))
	for _, r := range rows {
		out = append(out, runtime.PreviousFact{
			Key:        r.FieldKey,
			Value:      jsonbToString(r.FieldValue),
			Verified:   r.IsVerified.Bool,
			FromConvAt: r.FromConvAt.Time,
		})
	}
	return out, nil
}

// CaptureFromRedaction — «заведомый захват» контактов: если PII-редактор нашёл
// в сообщении телефон/email/VK — сразу пишем факт в те поля опросника, чьи
// field_type совпадают с семантическим типом сущности (phone/email/vk). Значение
// берётся из RedactionEntry.Original — реального (не маскированного) текста.
// Не трогает verified факты и не перезаписывает, если значение не изменилось.
func (s *DBIntake) CaptureFromRedaction(ctx context.Context, req runtime.ContactCaptureRequest) error {
	if req.RedactionLog == nil {
		return nil
	}
	entries := extractEntries(req.RedactionLog)
	if len(entries) == 0 {
		return nil
	}

	rows, err := s.q.ListIntakeFields(ctx, toUUID(req.AgentID))
	if err != nil {
		return err
	}
	// field_type → первое поле опросника такого типа. Порядок — по ask_priority
	// (ListIntakeFields возвращает в этом порядке), поэтому берём самое приоритетное.
	byType := map[string]storage.AgentIntakeField{}
	for _, r := range rows {
		t := strings.ToLower(strings.TrimSpace(r.FieldType))
		if t == "" {
			continue
		}
		if _, ok := byType[t]; !ok {
			byType[t] = r
		}
	}
	if len(byType) == 0 {
		return nil
	}

	active := map[string]storage.AgentConversationFact{}
	if existing, err := s.q.ListConversationFacts(ctx, toUUID(req.ConversationID)); err == nil {
		for _, f := range existing {
			active[f.FieldKey] = f
		}
	}

	// Дедупликация по типу в рамках одного сообщения — берём первый найденный.
	seen := map[string]bool{}
	for _, e := range entries {
		if e.Original == "" || seen[e.Type] {
			continue
		}
		field, ok := byType[strings.ToLower(e.Type)]
		if !ok {
			continue
		}
		seen[e.Type] = true

		if ex, ok := active[field.FieldKey]; ok {
			if ex.IsVerified.Bool {
				continue
			}
			if jsonbToString(ex.FieldValue) == e.Original {
				continue
			}
		}

		value := e.Original
		if normalized, normalizeErr := validateAndNormalize(e.Original, &field); normalizeErr == nil {
			value = normalized
		}
		valueJSON, err := json.Marshal(value)
		if err != nil {
			continue
		}
		if err := s.q.DeactivateActiveFact(ctx, storage.DeactivateActiveFactParams{
			ConversationID: toUUID(req.ConversationID),
			FieldKey:       field.FieldKey,
		}); err != nil {
			return err
		}
		if _, err := s.q.InsertConversationFact(ctx, storage.InsertConversationFactParams{
			ProfileID:       toUUID(req.ProfileID),
			ConversationID:  toUUID(req.ConversationID),
			IntakeFieldID:   field.ID,
			FieldKey:        field.FieldKey,
			FieldValue:      valueJSON,
			Confidence:      toNumeric(0.95),
			SourceMessageID: toUUIDOrNull(req.SourceMessageID),
			SourceExcerpt:   toText("auto (pii regex)"),
		}); err != nil {
			return err
		}
	}
	return nil
}

// extractEntries тянет []pii.RedactionEntry из map[string]any лога, который
// возвращает pii.Client. Поддерживаем оба формата: типизированный slice (regex
// mode) и []any с map'ами (если лог придёт через JSON от sidecar).
func extractEntries(log map[string]any) []pii.RedactionEntry {
	raw, ok := log["entries"]
	if !ok || raw == nil {
		return nil
	}
	switch v := raw.(type) {
	case []pii.RedactionEntry:
		return v
	case []any:
		out := make([]pii.RedactionEntry, 0, len(v))
		for _, item := range v {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			e := pii.RedactionEntry{}
			if t, ok := m["type"].(string); ok {
				e.Type = t
			}
			if r, ok := m["replacement"].(string); ok {
				e.Replacement = r
			}
			if o, ok := m["original"].(string); ok {
				e.Original = o
			}
			out = append(out, e)
		}
		return out
	}
	return nil
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
