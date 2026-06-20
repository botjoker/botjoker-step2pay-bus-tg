package agentstore

import (
	"context"
	"encoding/json"
	"time"

	"github.com/botjoker/sambacrm-business-tg/internal/runtime"
	"github.com/botjoker/sambacrm-business-tg/internal/storage"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// execLocal выполняет инструменты, обрабатываемые рантаймом (без backend).
func (r *DBToolRegistry) execLocal(ctx context.Context, ec runtime.ToolExecCtx, name string, args map[string]any) (map[string]any, error) {
	switch name {
	case "record_intake_fact":
		return r.recordIntakeFact(ctx, ec, args)
	case "confirm_intake_fact":
		return r.confirmIntakeFact(ctx, ec, args)
	case "get_intake_status":
		return r.getIntakeStatus(ctx, ec)
	case "get_current_time":
		return map[string]any{"now": time.Now().Format(time.RFC3339), "weekday": time.Now().Weekday().String()}, nil
	case "cite_source":
		return r.citeSource(ctx, ec, args)
	default:
		return map[string]any{"error": "unknown local tool: " + name}, nil
	}
}

func (r *DBToolRegistry) recordIntakeFact(ctx context.Context, ec runtime.ToolExecCtx, args map[string]any) (map[string]any, error) {
	key, _ := args["field_key"].(string)
	if key == "" {
		return map[string]any{"error": "field_key required"}, nil
	}
	valueJSON, _ := json.Marshal(args["value"])
	excerpt, _ := args["source_excerpt"].(string)

	// intake_field_id по ключу (если поле описано в схеме); NULL если не найдено.
	fieldID := pgtype.UUID{}
	fields, _ := r.q.ListIntakeFields(ctx, toUUID(ec.AgentID))
	for _, f := range fields {
		if f.FieldKey == key {
			fieldID = f.ID
			break
		}
	}

	// Вытесняем предыдущее активное значение и вставляем новое.
	if err := r.q.DeactivateActiveFact(ctx, storage.DeactivateActiveFactParams{
		ConversationID: toUUID(ec.ConversationID),
		FieldKey:       key,
	}); err != nil {
		return nil, err
	}
	if _, err := r.q.InsertConversationFact(ctx, storage.InsertConversationFactParams{
		ProfileID:       toUUID(ec.ProfileID),
		ConversationID:  toUUID(ec.ConversationID),
		IntakeFieldID:   fieldID,
		FieldKey:        key,
		FieldValue:      valueJSON,
		Confidence:      toNumeric(confidenceArg(args)),
		SourceMessageID: toUUIDOrNull(ec.MessageID),
		SourceExcerpt:   toText(excerpt),
	}); err != nil {
		return nil, err
	}

	remaining := r.remainingRequired(ctx, ec)
	return map[string]any{"status": "recorded", "remaining_required": remaining}, nil
}

func (r *DBToolRegistry) confirmIntakeFact(ctx context.Context, ec runtime.ToolExecCtx, args map[string]any) (map[string]any, error) {
	key, _ := args["field_key"].(string)
	if key == "" {
		return map[string]any{"error": "field_key required"}, nil
	}
	if err := r.q.VerifyFact(ctx, storage.VerifyFactParams{
		ConversationID: toUUID(ec.ConversationID),
		FieldKey:       key,
	}); err != nil {
		return nil, err
	}
	return map[string]any{"status": "verified", "field_key": key}, nil
}

func (r *DBToolRegistry) getIntakeStatus(ctx context.Context, ec runtime.ToolExecCtx) (map[string]any, error) {
	fields, err := r.q.ListIntakeFields(ctx, toUUID(ec.AgentID))
	if err != nil {
		return nil, err
	}
	facts, err := r.q.ListConversationFacts(ctx, toUUID(ec.ConversationID))
	if err != nil {
		return nil, err
	}
	collected := map[string]string{}
	for _, f := range facts {
		collected[f.FieldKey] = jsonbToString(f.FieldValue)
	}
	var missingRequired, missingOptional []string
	for _, f := range fields {
		if _, ok := collected[f.FieldKey]; ok {
			continue
		}
		if f.IsRequired {
			missingRequired = append(missingRequired, f.FieldKey)
		} else {
			missingOptional = append(missingOptional, f.FieldKey)
		}
	}
	return map[string]any{
		"collected":        collected,
		"missing_required": missingRequired,
		"missing_optional": missingOptional,
		"completed":        len(missingRequired) == 0,
	}, nil
}

func (r *DBToolRegistry) remainingRequired(ctx context.Context, ec runtime.ToolExecCtx) []string {
	fields, err := r.q.ListIntakeFields(ctx, toUUID(ec.AgentID))
	if err != nil {
		return nil
	}
	facts, _ := r.q.ListConversationFacts(ctx, toUUID(ec.ConversationID))
	have := map[string]bool{}
	for _, f := range facts {
		have[f.FieldKey] = true
	}
	var out []string
	for _, f := range fields {
		if f.IsRequired && !have[f.FieldKey] {
			out = append(out, f.FieldKey)
		}
	}
	return out
}

// citeSource добавляет цитату источника к текущему assistant-сообщению.
func (r *DBToolRegistry) citeSource(ctx context.Context, ec runtime.ToolExecCtx, args map[string]any) (map[string]any, error) {
	if ec.MessageID == uuid.Nil {
		return map[string]any{"status": "skipped", "reason": "no message context"}, nil
	}
	citation := map[string]any{
		"source_id": args["source_id"],
		"quote":     args["quote"],
	}
	payload, _ := json.Marshal([]any{citation})
	if err := r.q.AppendMessageCitation(ctx, storage.AppendMessageCitationParams{
		ID:      toUUID(ec.MessageID),
		Column2: payload,
	}); err != nil {
		return nil, err
	}
	return map[string]any{"status": "cited"}, nil
}

func confidenceArg(args map[string]any) float64 {
	if c, ok := args["confidence"].(float64); ok {
		return c
	}
	return 1.0
}
