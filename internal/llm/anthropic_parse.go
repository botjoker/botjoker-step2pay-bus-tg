package llm

// --- wire-формат ответа ---

type antResponse struct {
	ID         string         `json:"id"`
	Type       string         `json:"type"`
	Role       string         `json:"role"`
	Content    []antRespBlock `json:"content"`
	Model      string         `json:"model"`
	StopReason string         `json:"stop_reason"`
	Usage      antUsage       `json:"usage"`
}

type antRespBlock struct {
	Type  string         `json:"type"` // "text" | "tool_use"
	Text  string         `json:"text"`
	ID    string         `json:"id"`
	Name  string         `json:"name"`
	Input map[string]any `json:"input"`
}

type antUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

func parseAnthropicResponse(raw *antResponse, model string) *CompleteResult {
	res := &CompleteResult{
		Model:     model,
		TokensIn:  raw.Usage.InputTokens,
		TokensOut: raw.Usage.OutputTokens,
	}
	var text string
	for _, b := range raw.Content {
		switch b.Type {
		case "text":
			text += b.Text
		case "tool_use":
			res.ToolCalls = append(res.ToolCalls, ToolCall{
				ID:        b.ID,
				Name:      b.Name,
				Arguments: b.Input,
			})
		}
	}
	res.Content = text
	res.CostUSD = anthropicCostUSD(model, res.TokensIn, res.TokensOut)
	return res
}
