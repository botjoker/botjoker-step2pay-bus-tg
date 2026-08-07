package pii

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRegexRedact(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string // подстрока-замена, которая должна присутствовать
		gone string // подстрока, которой быть НЕ должно
	}{
		{"email", "пиши на ivan@mail.ru сегодня", "[EMAIL]", "ivan@mail.ru"},
		{"vk link", "мой профиль https://vk.com/ivan.petrov", "[VK]", "vk.com/ivan.petrov"},
		{"vk handle", "напишите мне @ivan_petrov", "[VK]", "@ivan_petrov"},
		{"phone +7", "мой номер +7 (999) 123-45-67 звоните", "[PHONE]", "999"},
		{"phone 8", "тел 8 999 123 45 67", "[PHONE]", "123"},
		{"card", "карта 4276 3800 1234 5678 оплата", "[CARD]", "4276"},
		{"snils", "снилс 112-233-445 95 вот", "[SNILS]", "112-233-445"},
		{"passport", "паспорт 4509 123456 выдан", "[PASSPORT]", "123456"},
		{"inn", "инн 7707083893 организации", "[INN]", "7707083893"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			red, log := localRegexRedact(c.in)
			if !strings.Contains(red, c.want) {
				t.Errorf("redacted %q does not contain %q", red, c.want)
			}
			if c.gone != "" && strings.Contains(red, c.gone) {
				t.Errorf("redacted %q still contains %q", red, c.gone)
			}
			if len(log) == 0 {
				t.Errorf("expected redaction log entries")
			}
		})
	}
}

func TestRegexRedact_Clean(t *testing.T) {
	red, log := localRegexRedact("привет, как дела?")
	if red != "привет, как дела?" {
		t.Errorf("clean text changed: %q", red)
	}
	if log != nil {
		t.Errorf("expected nil log for clean text, got %v", log)
	}
}

func TestRedact_PureRegexMode(t *testing.T) {
	c := New("") // без sidecar
	red, log, err := c.Redact(context.Background(), "email a@b.com")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(red, "[EMAIL]") {
		t.Errorf("red = %q", red)
	}
	if log == nil || log["count"] != 1 {
		t.Errorf("log = %v", log)
	}
}

func TestRedact_HTTPSidecar(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"redacted": "контакт [EMAIL]",
			"log":      []map[string]any{{"type": "email", "replacement": "[EMAIL]"}},
		})
	}))
	defer srv.Close()

	c := New(srv.URL)
	red, log, err := c.Redact(context.Background(), "контакт x@y.ru")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if red != "контакт [EMAIL]" {
		t.Errorf("red = %q", red)
	}
	if log == nil || log["count"] != 1 {
		t.Errorf("log = %v", log)
	}
}

func TestRedact_FallbackOnSidecarDown(t *testing.T) {
	// несуществующий адрес → должен сработать regex-fallback
	c := New("http://127.0.0.1:1") // closed port
	red, _, err := c.Redact(context.Background(), "почта z@z.ru")
	if err != nil {
		t.Fatalf("fallback should not error: %v", err)
	}
	if !strings.Contains(red, "[EMAIL]") {
		t.Errorf("fallback regex did not apply: %q", red)
	}
}
