package llm

import (
	"fmt"
	"strconv"
)

// --- wire-формат ответа ---

type yandexResponse struct {
	Result yandexResult `json:"result"`
}

type yandexResult struct {
	Alternatives []yandexAlternative `json:"alternatives"`
	Usage        yandexUsage         `json:"usage"`
	ModelVersion string              `json:"modelVersion"`
}

type yandexAlternative struct {
	Message yandexRespMsg `json:"message"`
	Status  string        `json:"status"`
}

type yandexRespMsg struct {
	Role         string              `json:"role"`
	Text         string              `json:"text"`
	ToolCallList *yandexToolCallList `json:"toolCallList"`
}

// Yandex отдаёт токены строками.
type yandexUsage struct {
	InputTextTokens  string `json:"inputTextTokens"`
	CompletionTokens string `json:"completionTokens"`
	TotalTokens      string `json:"totalTokens"`
}

func atoiSafe(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// parseYandexResponse маппит ответ Yandex в общий CompleteResult.
func parseYandexResponse(raw *yandexResponse, model string) (*CompleteResult, error) {
	if raw == nil || len(raw.Result.Alternatives) == 0 {
		return nil, fmt.Errorf("yandex: empty alternatives")
	}
	msg := raw.Result.Alternatives[0].Message

	res := &CompleteResult{
		Content:   msg.Text,
		TokensIn:  atoiSafe(raw.Result.Usage.InputTextTokens),
		TokensOut: atoiSafe(raw.Result.Usage.CompletionTokens),
		Model:     model,
	}

	if msg.ToolCallList != nil {
		for i, tc := range msg.ToolCallList.ToolCalls {
			res.ToolCalls = append(res.ToolCalls, ToolCall{
				ID:        toolCallID("yc", i),
				Name:      tc.FunctionCall.Name,
				Arguments: tc.FunctionCall.Arguments,
			})
		}
	}

	res.CostUSD = yandexCostUSD(model, res.TokensIn, res.TokensOut)
	return res, nil
}
