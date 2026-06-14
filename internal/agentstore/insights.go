package agentstore

import (
	"context"
	"encoding/json"
	"fmt"
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

const insightsPrompt = `Проанализируй диалог между AI-агентом и пользователем.
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
}

Диалог:
%s`

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
}

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

	var b strings.Builder
	for _, m := range msgs {
		b.WriteString(m.Role)
		b.WriteString(": ")
		b.WriteString(fromText(m.Content))
		b.WriteString("\n")
	}

	res, err := s.cheap.Complete(ctx, llm.CompleteRequest{
		Model:       s.model,
		Messages:    []llm.Message{{Role: llm.RoleUser, Content: fmt.Sprintf(insightsPrompt, b.String())}},
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

	return s.q.UpsertInsights(ctx, storage.UpsertInsightsParams{
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
	})
}

func toNullBool(b *bool) pgtype.Bool {
	if b == nil {
		return pgtype.Bool{}
	}
	return pgtype.Bool{Bool: *b, Valid: true}
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
