package agentstore

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/botjoker/sambacrm-business-tg/internal/llm"
	"github.com/botjoker/sambacrm-business-tg/internal/storage"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// DigestService — еженедельный дайджест по тенанту (агрегаты + LLM-резюме).
type DigestService struct {
	q     *storage.Queries
	cheap llm.LLMProvider
	model string
}

func NewDigestService(q *storage.Queries, cheap llm.LLMProvider, model string) *DigestService {
	return &DigestService{q: q, cheap: cheap, model: model}
}

const digestPrompt = `Сгенерируй еженедельный отчёт для бизнес-владельца по работе AI-агента.
Метрики за неделю:
- Диалогов с инсайтами: %d
- Конверсий: %d
- Распределение настроений: %s
- Топ-темы: %s
- Топ-теги: %s

Формат:
1. Краткое резюме (3-4 предложения, осмысленные выводы, не голые цифры).
2. Что важно знать (3-5 буллетов).
3. Что улучшить (2-3 рекомендации).
Пиши на русском, в деловом тоне.`

// ProfileIDs возвращает тенантов с агентами (для планировщика).
func (s *DigestService) ProfileIDs(ctx context.Context) ([]uuid.UUID, error) {
	rows, err := s.q.ListAgentProfileIds(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]uuid.UUID, 0, len(rows))
	for _, r := range rows {
		out = append(out, fromUUID(r))
	}
	return out, nil
}

// GenerateAndStore собирает агрегаты за последнюю неделю, генерирует отчёт
// и пишет его в agent_insight_digests. Отправка email — TODO (см. 111).
func (s *DigestService) GenerateAndStore(ctx context.Context, profileID uuid.UUID, now time.Time) error {
	end := now
	start := now.AddDate(0, 0, -7)

	rows, err := s.q.ListInsightsForDigest(ctx, storage.ListInsightsForDigestParams{
		ProfileID:  toUUID(profileID),
		PeriodFrom: pgtype.Timestamptz{Time: start, Valid: true},
		PeriodTo:   pgtype.Timestamptz{Time: end, Valid: true},
	})
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil // нечего суммировать
	}

	sentiment := map[string]int{}
	tagCount := map[string]int{}
	topicCount := map[string]int{}
	conversions := 0
	for _, r := range rows {
		if r.Sentiment.Valid {
			sentiment[r.Sentiment.String]++
		}
		if r.Converted.Valid && r.Converted.Bool {
			conversions++
		}
		for _, t := range r.Tags {
			tagCount[t]++
		}
		for _, t := range r.Topics {
			topicCount[t]++
		}
	}

	metrics := map[string]any{
		"conversations": len(rows),
		"conversions":   conversions,
		"sentiment":     sentiment,
		"top_tags":      topN(tagCount, 5),
		"top_topics":    topN(topicCount, 5),
	}

	report := ""
	res, err := s.cheap.Complete(ctx, llm.CompleteRequest{
		Model: s.model,
		Messages: []llm.Message{{Role: llm.RoleUser, Content: fmt.Sprintf(
			digestPrompt,
			len(rows), conversions,
			fmtMap(sentiment), strings.Join(topN(topicCount, 5), ", "), strings.Join(topN(tagCount, 5), ", "),
		)}},
		Temperature: 0.4,
		MaxTokens:   1200,
	})
	if err == nil {
		report = res.Content
	}

	metricsJSON, _ := json.Marshal(metrics)
	insightsJSON, _ := json.Marshal(map[string]any{"report": report})

	return s.q.InsertDigest(ctx, storage.InsertDigestParams{
		ProfileID:   toUUID(profileID),
		AgentID:     pgtype.UUID{}, // по всему тенанту
		PeriodStart: pgtype.Date{Time: start, Valid: true},
		PeriodEnd:   pgtype.Date{Time: end, Valid: true},
		Metrics:     metricsJSON,
		Insights:    insightsJSON,
	})
}

func topN(m map[string]int, n int) []string {
	type kv struct {
		k string
		v int
	}
	arr := make([]kv, 0, len(m))
	for k, v := range m {
		arr = append(arr, kv{k, v})
	}
	sort.Slice(arr, func(i, j int) bool { return arr[i].v > arr[j].v })
	out := make([]string, 0, n)
	for i := 0; i < len(arr) && i < n; i++ {
		out = append(out, arr[i].k)
	}
	return out
}

func fmtMap(m map[string]int) string {
	parts := make([]string, 0, len(m))
	for k, v := range m {
		parts = append(parts, fmt.Sprintf("%s:%d", k, v))
	}
	return strings.Join(parts, ", ")
}
