package llm

// --- wire-формат ответа (OpenAI-совместимый) ---

type oaResponse struct {
	Choices []oaChoice `json:"choices"`
	Usage   *oaUsage   `json:"usage"`
	Model   string     `json:"model"`
}

type oaChoice struct {
	Message      oaRespMsg `json:"message"`
	Delta        oaRespMsg `json:"delta"`
	FinishReason string    `json:"finish_reason"`
	Index        int       `json:"index"`
}

type oaRespMsg struct {
	Role      string       `json:"role"`
	Content   string       `json:"content"`
	ToolCalls []oaToolCall `json:"tool_calls"`
}

type oaUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

func (o *openAICompatible) parseResponse(raw *oaResponse, model string) *CompleteResult {
	res := &CompleteResult{Model: model}
	if raw.Usage != nil {
		res.TokensIn = raw.Usage.PromptTokens
		res.TokensOut = raw.Usage.CompletionTokens
	}
	if len(raw.Choices) > 0 {
		msg := raw.Choices[0].Message
		res.Content = msg.Content
		for i, tc := range msg.ToolCalls {
			id := tc.ID
			if id == "" {
				id = toolCallID(o.shortPrefix(), i)
			}
			res.ToolCalls = append(res.ToolCalls, ToolCall{
				ID:        id,
				Name:      tc.Function.Name,
				Arguments: parseArgs(tc.Function.Arguments),
			})
		}
	}
	res.CostUSD = openAICostUSD(o.name, model, res.TokensIn, res.TokensOut)
	return res
}
