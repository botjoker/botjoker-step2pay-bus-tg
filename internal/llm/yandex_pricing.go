package llm

import (
	"os"
	"strconv"
	"strings"
)

// Тарифы YandexGPT в рублях за 1000 токенов (ориентир AGENTS §19; правки —
// здесь, без миграций). Цена за вход/выход у Yandex одинаковая per-model.
var yandexRubPer1kTokens = map[string]float64{
	"yandexgpt-lite": 0.20, // YandexGPT Lite
	"yandexgpt":      0.60, // YandexGPT Pro
	"yandexgpt-32k":  0.60,
	"llama-lite":     0.20,
	"llama":          0.60,
}

// defaultRUBperUSD — курс по умолчанию (можно переопределить env RUB_PER_USD).
const defaultRUBperUSD = 100.0

func rubPerUSD() float64 {
	if v := os.Getenv("RUB_PER_USD"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			return f
		}
	}
	return defaultRUBperUSD
}

// yandexModelKey приводит modelUri/имя модели к ключу тарифа.
func yandexModelKey(model string) string {
	m := model
	if i := strings.Index(m, "://"); i >= 0 {
		m = m[i+3:] // отрезаем gpt://
	}
	// gpt://<folder>/<model>/<version> → берём предпоследний сегмент-имя
	parts := strings.Split(m, "/")
	for _, p := range parts {
		for key := range yandexRubPer1kTokens {
			if p == key {
				return key
			}
		}
	}
	// fallback по подстроке
	low := strings.ToLower(model)
	switch {
	case strings.Contains(low, "lite"):
		return "yandexgpt-lite"
	case strings.Contains(low, "llama"):
		return "llama"
	default:
		return "yandexgpt"
	}
}

// yandexCostUSD считает стоимость вызова в USD.
func yandexCostUSD(model string, tokensIn, tokensOut int) float64 {
	rubPer1k, ok := yandexRubPer1kTokens[yandexModelKey(model)]
	if !ok {
		rubPer1k = 0.60
	}
	totalTokens := float64(tokensIn + tokensOut)
	rub := totalTokens / 1000.0 * rubPer1k
	return rub / rubPerUSD()
}
