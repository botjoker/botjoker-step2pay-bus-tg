package agentstore

import (
	"context"
	"encoding/json"
	"strings"
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
	case "request_form":
		status, err := r.getIntakeStatus(ctx, ec)
		if err != nil {
			return nil, err
		}
		missingRequired, _ := status["missing_required"].([]string)
		missingOptional, _ := status["missing_optional"].([]string)
		if len(missingRequired)+len(missingOptional) == 0 {
			return map[string]any{"status": "already_completed"}, nil
		}
		return map[string]any{
			"status":       "shown",
			"ui_component": "intake_form",
			"note":         "Интерфейс уже сообщил о сборе данных и показал форму отдельно от переписки. Не повторяй уведомление, не проси дублировать поля сообщением и не утверждай, что данные собраны, пока пользователь не отправил форму.",
		}, nil
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
	excerpt, _ := args["source_excerpt"].(string)

	// intake_field_id + field_info по ключу (для валидации по field_type).
	fieldID := pgtype.UUID{}
	var fieldInfo *storage.AgentIntakeField
	fields, _ := r.q.ListIntakeFields(ctx, toUUID(ec.AgentID))
	for i := range fields {
		if fields[i].FieldKey == key {
			fieldID = fields[i].ID
			fieldInfo = &fields[i]
			break
		}
	}

	// Валидация значения от LLM. Дешёвые модели (DeepSeek/gpt-4o-mini) часто
	// путают формат — валидируем и нормализуем, при провале просим модель
	// переспросить клиента (возврат ошибки, а не запись в БД).
	rawStr := valueToString(args["value"])
	normalized, verr := validateAndNormalize(rawStr, fieldInfo)

	confidence := confidenceArg(args)
	salvaged := false
	if verr != nil {
		// Спасательный захват: клиент написал контакт «по-своему», strict-формат
		// не сошёлся. Не теряем данные и не гоняем клиента переписывать — пишем
		// сырое значение с низкой уверенностью и пометкой «требует проверки».
		// Задача агента — собрать данные, а не забраковать их из-за формата.
		if raw, ok := salvageValue(rawStr, fieldInfo); ok {
			normalized = raw
			salvaged = true
			confidence = 0.4
			if excerpt == "" {
				excerpt = "raw, требует проверки"
			}
		} else {
			out := map[string]any{
				"error":  "value_invalid",
				"field":  key,
				"reason": verr.Error(),
				"hint":   "переспроси клиента корректное значение в естественной форме, не показывай ему технические подробности",
			}
			if fmt := expectedFormat(fieldInfo); fmt != "" {
				out["expected_format"] = fmt
			}
			return out, nil
		}
	}
	valueJSON, _ := json.Marshal(normalized)

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
		Confidence:      toNumeric(confidence),
		SourceMessageID: toUUIDOrNull(ec.MessageID),
		SourceExcerpt:   toText(excerpt),
	}); err != nil {
		return nil, err
	}

	remaining := r.remainingRequired(ctx, ec)
	responseValue := normalized
	if fieldInfo != nil {
		switch strings.ToLower(strings.TrimSpace(fieldInfo.FieldType)) {
		case "phone":
			responseValue = "[PHONE]"
		case "email":
			responseValue = "[EMAIL]"
		}
	}
	resp := map[string]any{
		"status":             "recorded",
		"normalized_value":   responseValue,
		"remaining_required": remaining,
	}
	if salvaged {
		// Модель должна знать: значение сохранено, но «сырое». Пусть мягко
		// подтвердит его у клиента, но НЕ считает потерянным и не переспрашивает
		// настойчиво.
		resp["status"] = "recorded_unverified"
		resp["note"] = "значение сохранено как есть — не удалось привести к стандартному формату. " +
			"Мягко уточни у клиента, что записал верно, но не теряй его и не требуй переписать."
	}
	return resp, nil
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
	fieldTypes := make(map[string]string, len(fields))
	for _, field := range fields {
		fieldTypes[field.FieldKey] = strings.ToLower(strings.TrimSpace(field.FieldType))
	}
	for _, f := range facts {
		value := jsonbToString(f.FieldValue)
		switch fieldTypes[f.FieldKey] {
		case "phone":
			value = "[PHONE]"
		case "email":
			value = "[EMAIL]"
		}
		collected[f.FieldKey] = value
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
