package llm

import "strings"

// Тарифы GigaChat в рублях за 1000 токенов (ориентир; правки — здесь).
// У Сбера цена за вход/выход обычно одинаковая per-model.
var gigaRubPer1kTokens = map[string]float64{
	"gigachat-lite": 0.20, // GigaChat Lite
	"gigachat":      0.60, // GigaChat Pro
	"gigachat-pro":  0.60,
	"gigachat-max":  1.20, // GigaChat Max
}

func gigaModelKey(model string) string {
	low := strings.ToLower(model)
	switch {
	case strings.Contains(low, "max"):
		return "gigachat-max"
	case strings.Contains(low, "pro"):
		return "gigachat-pro"
	case strings.Contains(low, "lite"):
		return "gigachat-lite"
	default:
		return "gigachat"
	}
}

// gigaCostUSD считает стоимость вызова в USD (курс — rubPerUSD из yandex_pricing).
func gigaCostUSD(model string, tokensIn, tokensOut int) float64 {
	rubPer1k, ok := gigaRubPer1kTokens[gigaModelKey(model)]
	if !ok {
		rubPer1k = 0.60
	}
	total := float64(tokensIn + tokensOut)
	rub := total / 1000.0 * rubPer1k
	return rub / rubPerUSD()
}
