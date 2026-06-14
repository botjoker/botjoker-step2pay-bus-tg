package llm

import "strings"

// Цены OpenAI в USD за 1000 токенов (вход, выход). Ориентир; правки — здесь.
type usdRate struct{ in, out float64 }

var openaiUSDPer1k = map[string]usdRate{
	"gpt-4o":      {0.0025, 0.010},
	"gpt-4o-mini": {0.00015, 0.0006},
	"gpt-4.1":     {0.002, 0.008},
	"gpt-4.1-mini": {0.0004, 0.0016},
	"o3-mini":     {0.0011, 0.0044},
}

// deepseekUSDPer1k — DeepSeek (используется при name=="deepseek").
var deepseekUSDPer1k = map[string]usdRate{
	"deepseek-chat":     {0.00027, 0.0011},
	"deepseek-reasoner": {0.00055, 0.00219},
}

// openAICostUSD считает стоимость для OpenAI-совместимых провайдеров.
// Для openrouter стоимость берётся из usage ответа (поле cost), если есть, —
// иначе оценка здесь вернёт 0 (тариф зависит от выбранной модели).
func openAICostUSD(provider, model string, tokensIn, tokensOut int) float64 {
	var table map[string]usdRate
	switch provider {
	case "deepseek":
		table = deepseekUSDPer1k
	case "openai", "t_pro":
		table = openaiUSDPer1k
	default:
		return 0 // openrouter и пр. — стоимость не оцениваем хардкодом
	}
	rate, ok := table[openaiModelKey(table, model)]
	if !ok {
		return 0
	}
	return float64(tokensIn)/1000.0*rate.in + float64(tokensOut)/1000.0*rate.out
}

// openaiModelKey ищет точное совпадение, иначе по префиксу.
func openaiModelKey(table map[string]usdRate, model string) string {
	if _, ok := table[model]; ok {
		return model
	}
	low := strings.ToLower(model)
	for key := range table {
		if strings.HasPrefix(low, key) {
			return key
		}
	}
	return model
}
