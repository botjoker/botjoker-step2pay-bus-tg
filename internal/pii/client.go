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
type RedactionEntry struct {
	Type        string `json:"type"`
	Replacement string `json:"replacement"`
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
	reEmail = regexp.MustCompile(`[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}`)
	reCard  = regexp.MustCompile(`\b\d{4}[ -]?\d{4}[ -]?\d{4}[ -]?\d{4}\b`)
	rePhone = regexp.MustCompile(`(\+7|8)[ \-]?\(?\d{3}\)?[ \-]?\d{3}[ \-]?\d{2}[ \-]?\d{2}`)
	reSNILS = regexp.MustCompile(`\b\d{3}-\d{3}-\d{3} ?\d{2}\b`)
	rePassp = regexp.MustCompile(`\b\d{4} \d{6}\b`) // паспорт пишут с пробелом; без пробела 10 цифр → ИНН
	reINN   = regexp.MustCompile(`\b(\d{12}|\d{10})\b`)
)

func localRegexRedact(text string) (string, []RedactionEntry) {
	var log []RedactionEntry
	apply := func(re *regexp.Regexp, typeName, replacement string) {
		text = re.ReplaceAllStringFunc(text, func(string) string {
			log = append(log, RedactionEntry{Type: typeName, Replacement: replacement})
			return replacement
		})
	}
	// email и телефон — первыми (содержат цифры, но однозначны).
	apply(reEmail, "email", "[EMAIL]")
	apply(reCard, "card", "[CARD]")
	apply(rePhone, "phone", "[PHONE]")
	apply(reSNILS, "snils", "[SNILS]")
	apply(rePassp, "passport", "[PASSPORT]")
	apply(reINN, "inn", "[INN]")
	return text, log
}
