package llm

// OpenAI-совместимые провайдеры — тонкие обёртки над openAICompatible (035).
// Отличаются только base_url, ключом и дефолтной моделью. Это и есть выгода
// единого интерфейса: новый провайдер ≈ один конструктор, без своего стрим/
// tool-use кода.

// NewOpenRouter — OpenRouter (https://openrouter.ai/api/v1), агрегатор моделей.
// Key1 = API key, Key2 = base_url (опц.), Key3 = модель (опц., напр.
// "deepseek/deepseek-chat" или "anthropic/claude-3.5-sonnet").
func NewOpenRouter(c Credentials) LLMProvider {
	base := c.Key2
	if base == "" {
		base = "https://openrouter.ai/api/v1"
	}
	model := c.Key3
	if model == "" {
		model = "openai/gpt-4o-mini"
	}
	return newOpenAICompatible("openrouter", c.Key1, base, model,
		withTools(true),
		withVision(true),
		withExtraHeaders(map[string]string{
			"HTTP-Referer": "https://sambacrm.online",
			"X-Title":      "SambaCRM",
		}),
	)
}

// NewDeepSeek — DeepSeek (https://api.deepseek.com), OpenAI-compatible.
// Key1 = API key, Key2 = base_url (опц.), Key3 = модель (deepseek-chat по умолч.).
// deepseek-reasoner (R1) не поддерживает function-calling — отключаем tools.
func NewDeepSeek(c Credentials) LLMProvider {
	base := c.Key2
	if base == "" {
		base = "https://api.deepseek.com"
	}
	model := c.Key3
	if model == "" {
		model = "deepseek-chat"
	}
	tools := model != "deepseek-reasoner"
	return newOpenAICompatible("deepseek", c.Key1, base, model, withTools(tools))
}

// NewTBank — T-Pro / T-Lite (Т-Банк), OpenAI-compatible REST. RU-провайдер.
// Key1 = API key, Key2 = base_url (обязателен, выдаётся Т-Банком), Key3 = модель.
func NewTBank(c Credentials) LLMProvider {
	model := c.Key3
	if model == "" {
		model = "t-pro"
	}
	return newOpenAICompatible("t_pro", c.Key1, c.Key2, model, withTools(true))
}
