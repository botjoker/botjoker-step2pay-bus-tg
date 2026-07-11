// Package safety — эвристики для защиты от prompt injection и jailbreak-попыток.
// Не заменяет полноценный ML-модератор, но отлавливает большинство «низкоуровневых»
// атак: попытки заставить агента раскрыть системный промпт, поменять роль,
// проигнорировать инструкции. Используется как первый барьер до отправки в LLM.
package safety

import (
	"regexp"
	"strings"
	"unicode"
)

// InjectionReport — результат проверки одного сообщения пользователя.
type InjectionReport struct {
	Suspicious bool     // score >= threshold
	Score      float64  // 0..1
	Patterns   []string // сработавшие правила (для лога/аудита)
}

var (
	// EN
	reIgnorePrevEN = regexp.MustCompile(`(?i)\b(ignore|disregard|forget|override)\b[^.]{0,30}\b(all|previous|prior|above|earlier|these|the)\b[^.]{0,20}\b(instructions?|rules?|prompts?|system)\b`)
	reShowPromptEN = regexp.MustCompile(`(?i)\b(reveal|show|print|repeat|display|leak)\b[^.]{0,20}\b(system\s*prompt|initial\s*prompt|your\s*(prompt|instructions|rules))\b`)
	reRolePlayEN   = regexp.MustCompile(`(?i)\b(you\s+are\s+(now|no\s+longer)|act\s+as|roleplay\s+as|pretend\s+to\s+be|behave\s+as)\b[^.]{0,40}\b(dan|jailbroken|unrestricted|evil|no\s*restrictions|no\s*rules|developer\s*mode)\b`)
	reOverrideEN   = regexp.MustCompile(`(?i)\b(new|updated)\s+instructions?\b[^.]{0,30}(:|—|-)`)

	// RU
	reIgnorePrevRU = regexp.MustCompile(`(?i)\b(игнорируй|забудь|отбрось|обойди|переопредели)\b[^.]{0,30}\b(все|всё|предыдущие|прошлые|вышеуказанные|эти)?\s*(инструкции|правила|промпт|системный)\b`)
	reShowPromptRU = regexp.MustCompile(`(?i)\b(покажи|скажи|назови|выведи|повтори)\b[^.]{0,25}\b(системный\s*промпт|начальный\s*промпт|твои\s*инструкции|твой\s*промпт|правила)\b`)
	reRolePlayRU   = regexp.MustCompile(`(?i)\bтеперь\s+ты\b[^.]{0,40}\b(другой|другая|злой|без\s*ограничений|dan|jailbroken)\b`)
	reOverrideRU   = regexp.MustCompile(`(?i)\b(новые|обновлённые)\s+инструкции\b[^.]{0,30}(:|—|-)`)

	// Общие подозрительные признаки
	reMarkerTag = regexp.MustCompile(`(?i)</?(system|assistant|instructions?)\s*>`)
)

// Detect — эвристика по подозрительным паттернам. Score = 0.35 за каждое
// совпадение, cap 1.0. Threshold suspicious = 0.35 (одно попадание уже флаг).
func Detect(text string) InjectionReport {
	if strings.TrimSpace(text) == "" {
		return InjectionReport{}
	}
	checks := []struct {
		name string
		re   *regexp.Regexp
	}{
		{"ignore-instructions-en", reIgnorePrevEN},
		{"show-prompt-en", reShowPromptEN},
		{"roleplay-en", reRolePlayEN},
		{"override-instructions-en", reOverrideEN},
		{"ignore-instructions-ru", reIgnorePrevRU},
		{"show-prompt-ru", reShowPromptRU},
		{"roleplay-ru", reRolePlayRU},
		{"override-instructions-ru", reOverrideRU},
		{"marker-tag-injection", reMarkerTag},
	}
	var patterns []string
	for _, c := range checks {
		if c.re.MatchString(text) {
			patterns = append(patterns, c.name)
		}
	}
	// Аномалия: большая доля не-буквенных/не-пробельных символов может быть
	// закодированной атакой (base64/hex/url-encoded).
	if len(text) > 60 && nonAlphaRatio(text) > 0.5 {
		patterns = append(patterns, "high-entropy-payload")
	}
	score := float64(len(patterns)) * 0.35
	if score > 1.0 {
		score = 1.0
	}
	return InjectionReport{
		Suspicious: score >= 0.35,
		Score:      score,
		Patterns:   patterns,
	}
}

func nonAlphaRatio(text string) float64 {
	var total, non int
	for _, r := range text {
		if unicode.IsSpace(r) {
			continue
		}
		total++
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !unicode.IsPunct(r) {
			non++
		}
	}
	if total == 0 {
		return 0
	}
	return float64(non) / float64(total)
}
