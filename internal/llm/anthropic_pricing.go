package llm

import "strings"

// Цены Anthropic в USD за 1000 токенов (вход, выход). Ориентир; правки — здесь.
var anthropicUSDPer1k = map[string]usdRate{
	"claude-3-5-sonnet": {0.003, 0.015},
	"claude-3-5-haiku":  {0.0008, 0.004},
	"claude-3-opus":     {0.015, 0.075},
	"claude-3-haiku":    {0.00025, 0.00125},
	"claude-sonnet-4":   {0.003, 0.015},
	"claude-opus-4":     {0.015, 0.075},
}

func anthropicCostUSD(model string, tokensIn, tokensOut int) float64 {
	low := strings.ToLower(model)
	for key, rate := range anthropicUSDPer1k {
		if strings.HasPrefix(low, key) {
			return float64(tokensIn)/1000.0*rate.in + float64(tokensOut)/1000.0*rate.out
		}
	}
	return 0
}
