package agentstore

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/botjoker/sambacrm-business-tg/internal/llm"
	"github.com/botjoker/sambacrm-business-tg/internal/runtime"
	"github.com/botjoker/sambacrm-business-tg/internal/storage"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// LoadAgentConfig читает строку agents и маппит в runtime.AgentConfig.
// Возвращает также (providerName, model, llm_credentials_id) для постройки
// LLM-провайдера в cmd/agent. credID = uuid.Nil, если кредал не задан.
func LoadAgentConfig(ctx context.Context, q *storage.Queries, agentID uuid.UUID) (runtime.AgentConfig, string, string, uuid.UUID, error) {
	a, err := q.GetAgent(ctx, toUUID(agentID))
	if err != nil {
		return runtime.AgentConfig{}, "", "", uuid.Nil, err
	}
	cfg := runtime.AgentConfig{
		AgentID:          fromUUID(a.ID),
		ProfileID:        fromUUID(a.ProfileID),
		Persona:          a.Persona,
		LLMModel:         a.LlmModel,
		Temperature:      float32(numericToFloat(a.LlmTemperature)),
		MaxTokens:        int4ToInt(a.LlmMaxTokens),
		MaxIterations:    int4ToInt(a.LlmMaxIterations),
		ExtractorModel:   fromText(a.ExtractorModel),
		RagEnabled:       a.RagEnabled,
		RagTopK:          int4ToInt(a.RagTopK),
		RagMinScore:      float32(numericToFloat(a.RagMinScore)),
		DefaultLang:      fromText(a.DefaultLanguage),
		AutoDetectLang:   a.AutoDetectLanguage.Bool,
		AllowedLanguages: a.AllowedLanguages,
		VisionEnabled:    a.VisionEnabled.Bool,
	}

	// Платные документы агента (AC-3). Промах не критичен — фича просто не
	// попадёт в промпт; не роняем загрузку агента.
	if docs, derr := q.ListAgentSellableDocuments(ctx, storage.ListAgentSellableDocumentsParams{
		ProfileID: a.ProfileID,
		AgentID:   a.ID,
	}); derr == nil {
		cfg.SellableDocs = mapSellableDocs(docs)
	}

	return cfg, a.LlmProvider, a.LlmModel, fromUUID(a.LlmCredentialsID), nil
}

// tplVar — описание переменной шаблона (document_templates.variables[]).
type tplVar struct {
	Key      string `json:"key"`
	Required bool   `json:"required"`
}

// mapSellableDocs преобразует строки document_templates в runtime.SellableDoc,
// раскладывая описание переменных на required/optional ключи.
func mapSellableDocs(rows []storage.ListAgentSellableDocumentsRow) []runtime.SellableDoc {
	out := make([]runtime.SellableDoc, 0, len(rows))
	for _, r := range rows {
		var vars []tplVar
		if len(r.Variables) > 0 {
			_ = json.Unmarshal(r.Variables, &vars)
		}
		var required, optional []string
		for _, v := range vars {
			if v.Key == "" {
				continue
			}
			if v.Required {
				required = append(required, v.Key)
			} else {
				optional = append(optional, v.Key)
			}
		}
		out = append(out, runtime.SellableDoc{
			TemplateID:   fromUUID(r.ID).String(),
			Name:         r.Name,
			DocType:      r.DocType,
			Price:        fmt.Sprintf("%.2f", numericToFloat(r.PriceAmount)),
			Currency:     r.Currency,
			Disclaimer:   fromText(r.Disclaimer),
			RequiredVars: required,
			OptionalVars: optional,
		})
	}
	return out
}

// LoadProviderCredentials читает credential тенанта, расшифровывает key1/2/3
// ключом AGENT_SECRETS_KEY и возвращает llm.Credentials. Семантика key1/2/3 —
// по провайдеру (см. llm/factory.go). Key3 проставляется = model (override
// модели для OpenAI-совместимых; для остальных безвреден), как и в ENV-пути.
func LoadProviderCredentials(ctx context.Context, q *storage.Queries, credID uuid.UUID, agentSecretsKey, model string) (*llm.Credentials, error) {
	row, err := q.GetCredential(ctx, toUUID(credID))
	if err != nil {
		return nil, err
	}

	dec := func(t pgtype.Text) (string, error) {
		if !t.Valid || t.String == "" {
			return "", nil
		}
		return decryptSecret(t.String, agentSecretsKey)
	}

	k1, err := dec(row.Key1)
	if err != nil {
		return nil, err
	}
	k2, err := dec(row.Key2)
	if err != nil {
		return nil, err
	}
	k3, err := dec(row.Key3)
	if err != nil {
		return nil, err
	}

	creds := &llm.Credentials{
		ID:    credID,
		Key1:  k1,
		Key2:  k2,
		Key3:  model, // override модели (OpenAI-семейство); как в ENV-пути
		Extra: map[string]string{},
	}
	// Если в кредале реально лежит 3-й ключ — кладём его в Extra, чтобы не терять.
	if k3 != "" {
		creds.Extra["key3"] = k3
	}
	// scope для GigaChat — из metadata.scope, иначе дефолт PERS.
	creds.Extra["scope"] = "GIGACHAT_API_PERS"
	return creds, nil
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
