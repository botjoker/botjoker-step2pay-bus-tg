package llm

import "fmt"

// RUProviders — российские LLM-провайдеры (для sovereign_mode).
var RUProviders = map[string]bool{
	"yandex_gpt": true,
	"gigachat":   true,
	"t_pro":      true,
	"cotype":     true,
}

// CheckAllowed — проверяет, что provider разрешён в данном профиле.
//
// Правила:
//  1. В sovereign_mode допускаются только RU-провайдеры.
//  2. Provider должен присутствовать в profile.allowed_llm_providers.
//     Пустой список allowed_llm_providers трактуется как "ничего не разрешено"
//     (явный allow-list — безопасный дефолт).
func CheckAllowed(provider string, p ProfilePolicy) error {
	if p.SovereignMode && !RUProviders[provider] {
		return fmt.Errorf("provider %q not allowed in sovereign mode (only RU providers)", provider)
	}
	for _, allowed := range p.AllowedLLMProviders {
		if allowed == provider {
			return nil
		}
	}
	return fmt.Errorf("provider %q not in profile.allowed_llm_providers", provider)
}
