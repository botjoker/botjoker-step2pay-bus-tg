// Package pii — редакция PII. На MVP — regex в Go (sidecar sambacrm-agent-pii
// опционален, V1.1). Реализует runtime.PIIClient.
package pii

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"time"
)

// RedactionEntry — запись о произведённой замене.
// Original — исходное значение до маски. Используется downstream для захвата
// контактов в опросник (см. IntakeStore.CaptureFromRedaction). Наружу (в лог
// диалога) уходит только в БД рантайма, в LLM не попадает.
type RedactionEntry struct {
	Type        string `json:"type"`
	Replacement string `json:"replacement"`
	Original    string `json:"original,omitempty"`
}

// Client — HTTP-обёртка над pii-sidecar с regex-fallback.
// baseURL пустой → работаем только в regex-режиме (sidecar не дёргаем).
type Client struct {
	baseURL  string
	client   *http.Client
	fallback bool
}

// New создаёт клиент. baseURL == "" → чистый regex-режим (для MVP по умолчанию).
func New(baseURL string) *Client {
	return &Client{
		baseURL:  baseURL,
		client:   &http.Client{Timeout: 2 * time.Second},
		fallback: true,
	}
}

// Redact возвращает (отредактированный текст, лог-map или nil, ошибка).
// Лог nil означает «ничего не редактировалось» (runtime по этому ставит флаг).
func (c *Client) Redact(ctx context.Context, text string) (string, map[string]any, error) {
	if c.baseURL == "" {
		return c.regacted(text)
	}

	body, _ := json.Marshal(map[string]string{"text": text, "locale": "ru"})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/redact", bytes.NewReader(body))
	if err != nil {
		return c.regacted(text)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		if c.fallback {
			return c.regacted(text)
		}
		return text, nil, err
	}
	defer resp.Body.Close()

	var result struct {
		Redacted string           `json:"redacted"`
		Log      []RedactionEntry `json:"log"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return c.regacted(text)
	}
	return result.Redacted, toLog(result.Log), nil
}

func (c *Client) regacted(text string) (string, map[string]any, error) {
	red, entries := localRegexRedact(text)
	return red, toLog(entries), nil
}

func toLog(entries []RedactionEntry) map[string]any {
	if len(entries) == 0 {
		return nil
	}
	return map[string]any{
		"count":   len(entries),
		"entries": entries,
		"engine":  "regex",
	}
}

// RU PII паттерны. Порядок применения важен (более специфичные — раньше).
var (
	reEmail       = regexp.MustCompile(`[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}`)
	reCard        = regexp.MustCompile(`\b\d{4}[ -]?\d{4}[ -]?\d{4}[ -]?\d{4}\b`)
	rePhoneRU     = regexp.MustCompile(`(?:\+7[ \-]?\(?\d{3}\)?[ \-]?\d{3}[ \-]?\d{2}[ \-]?\d{2}|\b8[ \-]?\(?\d{3}\)?[ \-]?\d{3}[ \-]?\d{2}[ \-]?\d{2})\b`)
	rePhoneDigits = regexp.MustCompile(`\b\d{10,15}\b`)
	reVKLink      = regexp.MustCompile(`(?i)(?:https?://)?(?:www\.)?vk\.com/[a-zA-Z0-9_.-]+`)
	reVKHandle    = regexp.MustCompile(`@[a-zA-Z][a-zA-Z0-9_.-]{2,}`)
	reSNILS       = regexp.MustCompile(`\b\d{3}-\d{3}-\d{3} ?\d{2}\b`)
	rePassp       = regexp.MustCompile(`\b\d{4} \d{6}\b`)
	// ИНН маскируем только при явной подписи. Первая группа сохраняет пробел
	// или знак перед словом, вторая содержит только исходное значение ИНН.
	reINN = regexp.MustCompile(`(?i)(^|[^\p{L}\p{N}_])инн\s*[:№#-]?\s*(\d{10}|\d{12})\b`)
)

func localRegexRedact(text string) (string, []RedactionEntry) {
	var log []RedactionEntry
	apply := func(re *regexp.Regexp, typeName, replacement string) {
		text = re.ReplaceAllStringFunc(text, func(match string) string {
			log = append(log, RedactionEntry{Type: typeName, Replacement: replacement, Original: match})
			return replacement
		})
	}
	applyINN := func() {
		text = reINN.ReplaceAllStringFunc(text, func(match string) string {
			parts := reINN.FindStringSubmatch(match)
			if len(parts) != 3 {
				return match
			}
			log = append(log, RedactionEntry{Type: "inn", Replacement: "[INN]", Original: parts[2]})
			return parts[1] + "[INN]"
		})
	}
	// Более специфичные идентификаторы применяем до телефона, чтобы числовой
	// идентификатор с явной подписью не превратился в контакт.
	apply(reEmail, "email", "[EMAIL]")
	apply(reVKLink, "vk", "[VK]")
	apply(reVKHandle, "vk", "[VK]")
	apply(reCard, "card", "[CARD]")
	apply(reSNILS, "snils", "[SNILS]")
	apply(rePassp, "passport", "[PASSPORT]")
	applyINN()
	apply(rePhoneRU, "phone", "[PHONE]")
	// Оставшиеся непрерывные последовательности телефонной длины считаем
	// телефоном. В частности, это покрывает номер без +7/8 из формы или чата.
	apply(rePhoneDigits, "phone", "[PHONE]")
	return text, log
}
