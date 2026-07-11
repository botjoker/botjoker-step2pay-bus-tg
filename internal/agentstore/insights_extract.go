package agentstore

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/botjoker/sambacrm-business-tg/internal/llm"
	"github.com/botjoker/sambacrm-business-tg/internal/storage"
)

// structuredOutputProviders — провайдеры, поддерживающие strict-mode
// response_format=json_schema. Для остальных используется JSONMode fallback.
// DeepSeek/T-Pro формально OpenAI-совместимы, но json_schema ими не всегда
// корректно обрабатывается — на них включаем только json_object.
var structuredOutputProviders = map[string]bool{
	"openai": true,
}

func isStructuredOutputCapable(name string) bool {
	return structuredOutputProviders[strings.ToLower(strings.TrimSpace(name))]
}

// buildExtractSchema генерирует JSON schema под конкретный набор missing-полей
// опросника. Все поля nullable — модель обязана вернуть каждое, но может
// поставить null если клиент значения не назвал.
func buildExtractSchema(missing []storage.AgentIntakeField) *llm.JSONSchemaSpec {
	props := make(map[string]any, len(missing))
	required := make([]string, 0, len(missing))
	for _, f := range missing {
		typ := jsonTypeFor(f.FieldType)
		props[f.FieldKey] = map[string]any{
			"type":        []string{typ, "null"},
			"description": firstNonEmpty(f.FieldLabel, f.FieldKey),
		}
		required = append(required, f.FieldKey)
	}
	inner := map[string]any{
		"type":                 "object",
		"properties":           props,
		"required":             required,
		"additionalProperties": false,
	}
	return &llm.JSONSchemaSpec{
		Name:   "intake_extraction",
		Strict: true,
		Schema: map[string]any{
			"type":                 "object",
			"properties":           map[string]any{"extracted": inner},
			"required":             []string{"extracted"},
			"additionalProperties": false,
		},
	}
}

func jsonTypeFor(fieldType string) string {
	switch strings.ToLower(strings.TrimSpace(fieldType)) {
	case "number":
		return "number"
	case "boolean":
		return "boolean"
	default:
		// phone/email/date/enum/string/multiline → строка. Валидация
		// формата — детерминированно в validateAndNormalize.
		return "string"
	}
}

func firstNonEmpty(a, b string) string {
	if a = strings.TrimSpace(a); a != "" {
		return a
	}
	return b
}

// parseExtractedWithRetry разбирает JSON-ответ модели. Если не парсится — один
// retry с более строгим промптом (без markdown-обёрток). Дешёвые модели
// нередко оборачивают JSON в ```json ... ``` — retry чинит без эскалации.
// Возвращает содержимое поля "extracted" (nil если пусто/parse failed совсем).
func parseExtractedWithRetry(ctx context.Context, s *InsightsService, req llm.CompleteRequest, firstContent string) (map[string]any, error) {
	if m := tryParseExtracted(firstContent); m != nil {
		return m, nil
	}

	retryReq := req
	retryReq.Messages = append([]llm.Message{}, req.Messages...)
	retryReq.Messages = append(retryReq.Messages, llm.Message{
		Role:    llm.RoleUser,
		Content: "Верни ТОЛЬКО валидный JSON без markdown-разметки и без пояснений. Формат: {\"extracted\": {...}}.",
	})
	retryReq.Temperature = 0.0

	retryRes, err := s.cheap.Complete(ctx, retryReq)
	if err != nil {
		return nil, err
	}
	if m := tryParseExtracted(retryRes.Content); m != nil {
		return m, nil
	}
	return nil, jsonParseError(retryRes.Content)
}

func tryParseExtracted(content string) map[string]any {
	trimmed := extractJSON(content)
	if trimmed == "" {
		return nil
	}
	var out struct {
		Extracted map[string]any `json:"extracted"`
	}
	if err := json.Unmarshal([]byte(trimmed), &out); err != nil {
		return nil
	}
	// Нулевые значения фильтруем, чтобы вниз по потоку не тратить время.
	filtered := map[string]any{}
	for k, v := range out.Extracted {
		if v == nil {
			continue
		}
		if s, ok := v.(string); ok && strings.TrimSpace(s) == "" {
			continue
		}
		filtered[k] = v
	}
	return filtered
}

type jsonErr struct{ raw string }

func (e jsonErr) Error() string {
	sample := e.raw
	if len(sample) > 200 {
		sample = sample[:200] + "..."
	}
	return "unable to parse JSON: " + sample
}

func jsonParseError(raw string) error { return jsonErr{raw: raw} }
