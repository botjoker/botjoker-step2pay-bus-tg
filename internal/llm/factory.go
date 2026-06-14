package llm

import (
	"fmt"

	"github.com/google/uuid"
)

// Credentials — раскрытые API-ключи (после decrypt'а из credentials таблицы).
//
// Семантика полей зависит от провайдера:
//   - yandex_gpt: Key1 = API key (или IAM token), Key2 = folder_id
//   - gigachat:   Key1 = Authorization key (base64 client_id:client_secret), Key2 = scope
//   - openai:     Key1 = API key, Key2 = base_url (опц.)
//   - anthropic:  Key1 = API key
//   - openrouter: Key1 = API key
//   - t_pro:      Key1 = API key, Key2 = base_url
type Credentials struct {
	ID    uuid.UUID
	Key1  string // основной ключ
	Key2  string // secret / folder_id / scope / base_url
	Key3  string // дополнительный (если нужен)
	Extra map[string]string
}

// ProfilePolicy — данные из core_profiles (sovereign_mode, allowed_*).
type ProfilePolicy struct {
	AllowedLLMProviders []string
	AllowedEmbeddings   []string
	SovereignMode       bool
	DataResidency       string // 'ru' | 'eu' | 'any'
}

// NewProvider — создаёт инстанс LLMProvider по имени.
// Проверяет, что provider разрешён политикой профиля.
func NewProvider(name string, creds Credentials, policy ProfilePolicy) (LLMProvider, error) {
	if err := CheckAllowed(name, policy); err != nil {
		return nil, err
	}

	switch name {
	case "yandex_gpt":
		return NewYandexGPT(creds), nil
	case "gigachat":
		return NewGigaChat(creds), nil
	case "openai":
		return NewOpenAI(creds), nil
	case "anthropic":
		return NewAnthropic(creds), nil
	case "openrouter":
		return NewOpenRouter(creds), nil
	case "deepseek":
		return NewDeepSeek(creds), nil
	case "t_pro":
		return NewTBank(creds), nil
	default:
		return nil, fmt.Errorf("unknown provider: %s", name)
	}
}

// Конструкторы OpenAI-совместимых провайдеров (OpenRouter / DeepSeek / T-Pro) —
// в openai_compatible.go. Локальные модели (Ollama/vLLM) — НЕ в MVP.
