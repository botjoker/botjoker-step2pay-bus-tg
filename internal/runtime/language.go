package runtime

import "unicode"

// detectLanguage определяет язык пользователя (ISO 639-1). Эвристика по
// диапазонам символов; если язык не входит в allowed_languages — возвращаем
// default (рантайм ответит на нём + дисклеймер из system prompt).
//
// Кэширование в conversation.context.user_language делается на уровне wiring
// (03D), здесь — чистая, тестируемая функция.
func (a *Agent) detectLanguage(text string) string {
	if !a.cfg.AutoDetectLang {
		return a.cfg.DefaultLang
	}
	detected := simpleDetect(text)
	if detected == "" {
		return a.cfg.DefaultLang
	}
	if len(a.cfg.AllowedLanguages) > 0 && !isAllowed(detected, a.cfg.AllowedLanguages) {
		return a.cfg.DefaultLang
	}
	return detected
}

// simpleDetect — эвристический детектор ru/en/kk/ar. "" если не уверен.
func simpleDetect(text string) string {
	var cyrillic, latin, arabic, kazakh int
	for _, r := range text {
		switch {
		case r == 'ң', r == 'ғ', r == 'қ', r == 'ұ', r == 'ү', r == 'ө', r == 'ә', r == 'і', r == 'һ':
			kazakh++
			cyrillic++
		case unicode.Is(unicode.Cyrillic, r):
			cyrillic++
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
			latin++
		case unicode.Is(unicode.Arabic, r):
			arabic++
		}
	}

	switch {
	case kazakh > 2:
		return "kk"
	case arabic > 5:
		return "ar"
	case cyrillic > latin*2:
		return "ru"
	case latin > cyrillic*2:
		return "en"
	default:
		return ""
	}
}

func isAllowed(lang string, allowed []string) bool {
	for _, a := range allowed {
		if a == lang {
			return true
		}
	}
	return false
}
