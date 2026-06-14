package llm

import "fmt"

// --- wire-формат ответа (OpenAI-совместимый) ---

type gigaResponse struct {
	Choices []gigaChoice `json:"choices"`
	Usage   gigaUsage    `json:"usage"`
	Model   string       `json:"model"`
}

type gigaChoice struct {
	Message      gigaRespMsg `json:"message"`
	Delta        gigaRespMsg `json:"delta"` // для стрима
	FinishReason string      `json:"finish_reason"`
	Index        int         `json:"index"`
}

type gigaRespMsg struct {
	Role             string         `json:"role"`
	Content          string         `json:"content"`
	FunctionCall     *gigaFnCall    `json:"function_call"` // legacy GigaChat
	ToolCalls        []gigaToolCall `json:"tool_calls"`
}

type gigaFnCall struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

type gigaUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// parseGigaResponse маппит ответ GigaChat в общий CompleteResult.
func parseGigaResponse(raw *gigaResponse, model string) (*CompleteResult, error) {
	if raw == nil || len(raw.Choices) == 0 {
		return nil, fmt.Errorf("gigachat: empty choices")
	}
	msg := raw.Choices[0].Message

	res := &CompleteResult{
		Content:   msg.Content,
		TokensIn:  raw.Usage.PromptTokens,
		TokensOut: raw.Usage.CompletionTokens,
		Model:     model,
	}

	// современный формат: tool_calls
	for _, tc := range msg.ToolCalls {
		res.ToolCalls = append(res.ToolCalls, ToolCall{
			ID:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: parseArgs(tc.Function.Arguments),
		})
	}
	// legacy формат: function_call (один вызов)
	if len(res.ToolCalls) == 0 && msg.FunctionCall != nil {
		res.ToolCalls = append(res.ToolCalls, ToolCall{
			ID:        toolCallID("gc", 0),
			Name:      msg.FunctionCall.Name,
			Arguments: msg.FunctionCall.Arguments,
		})
	}

	res.CostUSD = gigaCostUSD(model, res.TokensIn, res.TokensOut)
	return res, nil
}
