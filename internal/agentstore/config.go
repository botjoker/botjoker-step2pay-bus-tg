package agentstore

import (
	"context"

	"github.com/botjoker/sambacrm-business-tg/internal/runtime"
	"github.com/botjoker/sambacrm-business-tg/internal/storage"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// LoadAgentConfig читает строку agents и маппит в runtime.AgentConfig.
// Возвращает также (providerName, model) для постройки LLM-провайдера в cmd/agent.
func LoadAgentConfig(ctx context.Context, q *storage.Queries, agentID uuid.UUID) (runtime.AgentConfig, string, string, error) {
	a, err := q.GetAgent(ctx, toUUID(agentID))
	if err != nil {
		return runtime.AgentConfig{}, "", "", err
	}
	cfg := runtime.AgentConfig{
		AgentID:          fromUUID(a.ID),
		ProfileID:        fromUUID(a.ProfileID),
		Persona:          a.Persona,
		LLMModel:         a.LlmModel,
		Temperature:      float32(numericToFloat(a.LlmTemperature)),
		MaxTokens:        int4ToInt(a.LlmMaxTokens),
		MaxIterations:    int4ToInt(a.LlmMaxIterations),
		RagEnabled:       a.RagEnabled,
		RagTopK:          int4ToInt(a.RagTopK),
		RagMinScore:      float32(numericToFloat(a.RagMinScore)),
		DefaultLang:      fromText(a.DefaultLanguage),
		AutoDetectLang:   a.AutoDetectLanguage.Bool,
		AllowedLanguages: a.AllowedLanguages,
		VisionEnabled:    a.VisionEnabled.Bool,
	}
	return cfg, a.LlmProvider, a.LlmModel, nil
}

func numericToFloat(n pgtype.Numeric) float64 {
	f, err := n.Float64Value()
	if err != nil || !f.Valid {
		return 0
	}
	return f.Float64
}

func int4ToInt(n pgtype.Int4) int {
	if !n.Valid {
		return 0
	}
	return int(n.Int32)
}
