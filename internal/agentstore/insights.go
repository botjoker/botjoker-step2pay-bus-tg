package agentstore

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/botjoker/sambacrm-business-tg/internal/llm"
	"github.com/botjoker/sambacrm-business-tg/internal/storage"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// InsightsService — пост-анализ диалога дешёвой моделью (на платформенных ключах).
type InsightsService struct {
	q     *storage.Queries
	cheap llm.LLMProvider
	model string
}

func NewInsightsService(q *storage.Queries, cheap llm.LLMProvider, model string) *InsightsService {
	return &InsightsService{q: q, cheap: cheap, model: model}
}

const insightsPrompt = `Проанализируй диалог между AI-агентом (роль «Агент») и клиентом (роль «Клиент»).

ВАЖНО про извлечение данных (contact_extracted, intake_extracted):
- Бери ТОЛЬКО то, что КЛИЕНТ сообщил О СЕБЕ (имя, телефон, email, ответы на вопросы).
- НИКОГДА не бери данные из реплик Агента (его имя, приветствие, примеры, предложения).
  Если Агент представился (напр. «меня зовут Алексей») — это НЕ имя клиента, не записывай.
- Если клиент явно не назвал значение — оставь поле пустым. Не угадывай и не придумывай.

Верни ТОЛЬКО валидный JSON:
{
  "tags": ["..."],
  "sentiment": "positive|neutral|negative|upset|mixed",
  "sentiment_confidence": 0.0,
  "primary_intent": "question|complaint|purchase_intent|support|consultation",
  "secondary_intents": [],
  "topics": ["..."],
  "short_summary": "2 предложения",
  "long_summary": "1 параграф",
  "contact_extracted": {"email":"","phone":"","name":""},
  "next_step_suggestion": "",
  "converted": false,
  "conversion_tool": ""
}`

type insightsJSON struct {
	Tags                []string          `json:"tags"`
	Sentiment           string            `json:"sentiment"`
	SentimentConfidence float64           `json:"sentiment_confidence"`
	PrimaryIntent       string            `json:"primary_intent"`
	SecondaryIntents    []string          `json:"secondary_intents"`
	Topics              []string          `json:"topics"`
	ShortSummary        string            `json:"short_summary"`
	LongSummary         string            `json:"long_summary"`
	ContactExtracted    map[string]string `json:"contact_extracted"`
	NextStepSuggestion  string            `json:"next_step_suggestion"`
	Converted           *bool             `json:"converted"`
	ConversionTool      string            `json:"conversion_tool"`
	IntakeExtracted     map[string]string `json:"intake_extracted"`
}

// contactFactKeys — ключи контакта, которые из contact_extracted пишутся в факты.
var contactFactKeys = []string{"name", "phone", "email"}

// DueConversations возвращает диалоги, которым пора посчитать insights.
func (s *InsightsService) DueConversations(ctx context.Context, limit int32) ([]uuid.UUID, error) {
	rows, err := s.q.ListDueForInsights(ctx, limit)
	if err != nil {
		return nil, err
	}
	out := make([]uuid.UUID, 0, len(rows))
	for _, r := range rows {
		out = append(out, fromUUID(r.ID))
	}
	return out, nil
}

// Process анализирует один диалог и пишет insights (UPSERT).
func (s *InsightsService) Process(ctx context.Context, convID uuid.UUID) error {
	conv, err := s.q.GetAgentConversation(ctx, toUUID(convID))
	if err != nil {
		return err
	}
	msgs, err := s.q.ListMessagesByConversation(ctx, storage.ListMessagesByConversationParams{
		ConversationID: toUUID(convID),
		Limit:          100,
		Offset:         0,
	})
	if err != nil {
		return err
	}
	if len(msgs) == 0 {
		return nil
	}

	// Для post-processing берём НЕмаскированный текст (content_original) — иначе
	// дешёвая модель увидит `[PHONE]`/`[EMAIL]` вместо реальных значений и
	// извлечёт маску в contact_extracted. fallback на content — для сообщений
	// без PII-редакции (assistant/operator).
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString(roleLabel(m.Role))
		b.WriteString(": ")
		b.WriteString(messageBodyForInsights(m))
		b.WriteString("\n")
	}

	// Схема опросника агента — чтобы дешёвая модель извлекла и значения полей,
	// а не только контакт (на deepseek inline record_intake_fact ненадёжен).
	fields, _ := s.q.ListIntakeFields(ctx, conv.AgentID)

	var pb strings.Builder
	pb.WriteString(insightsPrompt)
	if len(fields) > 0 {
		pb.WriteString("\n\nТакже извлеки значения полей опросника, если их явно назвал КЛИЕНТ о себе. ")
		pb.WriteString(`Добавь в JSON ключ "intake_extracted" — объект {field_key: значение}; `)
		pb.WriteString("включай поле ТОЛЬКО если клиент сам сообщил это значение (не Агент, без догадок). Поля опросника:\n")
		for _, f := range fields {
			pb.WriteString("- ")
			pb.WriteString(f.FieldKey)
			pb.WriteString(" — ")
			pb.WriteString(f.FieldLabel)
			pb.WriteString("\n")
		}
	}
	pb.WriteString("\nДиалог:\n")
	pb.WriteString(b.String())

	res, err := s.cheap.Complete(ctx, llm.CompleteRequest{
		Model:       s.model,
		Messages:    []llm.Message{{Role: llm.RoleUser, Content: pb.String()}},
		JSONMode:    true,
		Temperature: 0.1,
		MaxTokens:   1500,
	})
	if err != nil {
		return err
	}

	var ins insightsJSON
	if err := json.Unmarshal([]byte(extractJSON(res.Content)), &ins); err != nil {
		return fmt.Errorf("insights parse: %w", err)
	}

	contactJSON := marshalJSON(ins.ContactExtracted)

	if err := s.q.UpsertInsights(ctx, storage.UpsertInsightsParams{
		ProfileID:           conv.ProfileID,
		ConversationID:      toUUID(convID),
		Tags:                ins.Tags,
		Sentiment:           toText(ins.Sentiment),
		SentimentConfidence: toNumeric(ins.SentimentConfidence),
		PrimaryIntent:       toText(ins.PrimaryIntent),
		SecondaryIntents:    ins.SecondaryIntents,
		Topics:              ins.Topics,
		ShortSummary:        toText(ins.ShortSummary),
		LongSummary:         toText(ins.LongSummary),
		ContactExtracted:    contactJSON,
		NextStepSuggestion:  toText(ins.NextStepSuggestion),
		Converted:           toNullBool(ins.Converted),
		ConversionTool:      toText(ins.ConversionTool),
		LlmModel:            toText(s.model),
	}); err != nil {
		return err
	}

	// Сбор фактов из извлечённого: контакт + распознанные поля опросника.
	// Best-effort: ошибка записи фактов не валит задачу insights.
	if err := s.writeFacts(ctx, conv, fields, ins); err != nil {
		slog.Warn("insights: write facts failed", "conversation", convID, "err", err)
	}

	// Авто-правило воронки (ЭТАП D2): «горячий» диалог → лид (если включено у агента).
	if err := s.maybeAutoCreateLead(ctx, conv, ins); err != nil {
		slog.Warn("insights: auto-create lead failed", "conversation", convID, "err", err)
	}
	return nil
}

// maybeAutoCreateLead заводит лид из диалога, если у агента включён auto_create_lead
// и диалог «горячий» (есть телефон ИЛИ primary_intent=purchase_intent) и лид ещё не
// привязан. Дедуп по телефону: привязывает диалог к существующему открытому лиду.
func (s *InsightsService) maybeAutoCreateLead(ctx context.Context, conv storage.AgentConversation, ins insightsJSON) error {
	auto, err := s.q.GetAgentAutoCreateLead(ctx, conv.AgentID)
	if err != nil || !auto {
		return err
	}
	if conv.LeadID.Valid {
		return nil // уже привязан
	}

	// «горячий» = есть телефон ИЛИ покупательский интент.
	phone := strings.TrimSpace(ins.ContactExtracted["phone"])
	if phone == "" && ins.PrimaryIntent != "purchase_intent" {
		return nil
	}

	// Дедуп: при наличии телефона привязываем к существующему открытому лиду.
	if phone != "" {
		existing, derr := s.q.FindOpenLeadByPhone(ctx, storage.FindOpenLeadByPhoneParams{
			ProfileID:    conv.ProfileID,
			ContactPhone: toText(phone),
		})
		if derr == nil && existing.Valid {
			return s.q.SetConversationLead(ctx, storage.SetConversationLeadParams{
				ID: conv.ID, LeadID: existing,
			})
		}
	}

	stageID, serr := s.q.GetLeadStartStage(ctx, conv.ProfileID)
	if serr != nil || !stageID.Valid {
		return serr
	}

	// data + контакт из активных фактов.
	facts, _ := s.q.ListConversationFacts(ctx, conv.ID)
	data := map[string]json.RawMessage{}
	var name, email string
	for _, f := range facts {
		if len(f.FieldValue) > 0 {
			data[f.FieldKey] = json.RawMessage(f.FieldValue)
		}
		switch f.FieldKey {
		case "name":
			name = jsonbToString(f.FieldValue)
		case "email":
			email = jsonbToString(f.FieldValue)
		case "phone":
			if phone == "" {
				phone = jsonbToString(f.FieldValue)
			}
		}
	}
	dataJSON, _ := json.Marshal(data)

	leadID, ierr := s.q.InsertLeadAuto(ctx, storage.InsertLeadAutoParams{
		ProfileID:            conv.ProfileID,
		StageID:              stageID,
		Title:                toText(name),
		ContactName:          toText(name),
		ContactPhone:         toText(phone),
		ContactEmail:         toText(email),
		SourceAgentID:        conv.AgentID,
		SourceConversationID: conv.ID,
		Data:                 dataJSON,
		Summary:              toText(ins.ShortSummary),
		Sentiment:            toText(ins.Sentiment),
		PrimaryIntent:        toText(ins.PrimaryIntent),
	})
	if ierr != nil {
		return ierr
	}
	return s.q.SetConversationLead(ctx, storage.SetConversationLeadParams{ID: conv.ID, LeadID: leadID})
}

// writeFacts апсертит факты диалога из результатов insights (контакт + поля
// опросника), не перезаписывая подтверждённые пользователем (is_verified) и
// неизменившиеся значения. Та же дедупликация активного факта, что у тул-колла.
func (s *InsightsService) writeFacts(ctx context.Context, conv storage.AgentConversation, fields []storage.AgentIntakeField, ins insightsJSON) error {
	candidates := map[string]string{}
	for _, k := range contactFactKeys {
		if v := strings.TrimSpace(ins.ContactExtracted[k]); v != "" {
			candidates[k] = v
		}
	}
	for k, v := range ins.IntakeExtracted {
		if k = strings.TrimSpace(k); k == "" {
			continue
		}
		if v = strings.TrimSpace(v); v != "" {
			candidates[k] = v
		}
	}
	if len(candidates) == 0 {
		return nil
	}

	// Активные факты диалога — чтобы не трогать verified и не дублировать значение.
	active := map[string]storage.AgentConversationFact{}
	if existing, err := s.q.ListConversationFacts(ctx, conv.ID); err == nil {
		for _, f := range existing {
			active[f.FieldKey] = f
		}
	}
	// field_key → intake_field_id (NULL для контактных полей вне схемы).
	fieldID := map[string]pgtype.UUID{}
	for _, f := range fields {
		fieldID[f.FieldKey] = f.ID
	}

	for key, val := range candidates {
		if ex, ok := active[key]; ok {
			if ex.IsVerified.Bool {
				continue // не перезаписываем подтверждённое пользователем
			}
			if jsonbToString(ex.FieldValue) == val {
				continue // значение не изменилось
			}
		}
		valueJSON, err := json.Marshal(val)
		if err != nil {
			continue
		}
		if err := s.q.DeactivateActiveFact(ctx, storage.DeactivateActiveFactParams{
			ConversationID: conv.ID,
			FieldKey:       key,
		}); err != nil {
			return err
		}
		if _, err := s.q.InsertConversationFact(ctx, storage.InsertConversationFactParams{
			ProfileID:      conv.ProfileID,
			ConversationID: conv.ID,
			IntakeFieldID:  fieldID[key],
			FieldKey:       key,
			FieldValue:     valueJSON,
			Confidence:     toNumeric(0.7),
			SourceExcerpt:  toText("auto (insights)"),
		}); err != nil {
			return err
		}
	}
	return nil
}

func toNullBool(b *bool) pgtype.Bool {
	if b == nil {
		return pgtype.Bool{}
	}
	return pgtype.Bool{Bool: *b, Valid: true}
}

// roleLabel переводит роль сообщения в явную метку для промпта insights —
// чтобы дешёвая модель не путала, кто что сказал (и не записала данные Агента).
func roleLabel(role string) string {
	switch role {
	case "user":
		return "Клиент"
	case "assistant":
		return "Агент"
	case "operator":
		return "Оператор"
	default:
		return role
	}
}

// messageBodyForInsights возвращает исходный текст сообщения (content_original)
// если он есть, иначе — редактированный content. Обеспечивает извлечение
// реальных телефонов/email в insights/writeFacts.
func messageBodyForInsights(m storage.AgentMessage) string {
	if orig := fromText(m.ContentOriginal); strings.TrimSpace(orig) != "" {
		return orig
	}
	return fromText(m.Content)
}

// extractJSON вырезает JSON-объект из ответа (на случай обёрток ```json).
func extractJSON(s string) string {
	start := strings.IndexByte(s, '{')
	end := strings.LastIndexByte(s, '}')
	if start >= 0 && end > start {
		return s[start : end+1]
	}
	return s
}
