package vk

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/botjoker/sambacrm-business-tg/internal/llm"
	"github.com/google/uuid"
)

// vkCallback — обёртка Callback API.
type vkCallback struct {
	Type    string          `json:"type"`
	Object  json.RawMessage `json:"object"`
	GroupID int64           `json:"group_id"`
	Secret  string          `json:"secret"`
}

type vkMessageObject struct {
	Message struct {
		FromID      int64  `json:"from_id"`
		PeerID      int64  `json:"peer_id"`
		Text        string `json:"text"`
		Attachments []struct {
			Type  string `json:"type"`
			Photo struct {
				Sizes []struct {
					URL string `json:"url"`
				} `json:"sizes"`
			} `json:"photo"`
		} `json:"attachments"`
	} `json:"message"`
}

// HandleCallback обрабатывает Callback API и возвращает строку-ответ для VK
// ("ok" / confirmation-код). Само сообщение обрабатывается асинхронно.
func (m *Manager) HandleCallback(ctx context.Context, channelID uuid.UUID, body []byte) (string, error) {
	var cb vkCallback
	if err := json.Unmarshal(body, &cb); err != nil {
		return "", err
	}

	ch, ok := m.get(channelID)
	if !ok {
		return "ok", nil // канал не запущен — молча принимаем
	}

	// Подтверждение Callback-сервера.
	if cb.Type == "confirmation" {
		if ch.Confirmation != "" {
			return ch.Confirmation, nil
		}
		return "ok", nil
	}

	// Проверка secret.
	if ch.SecretKey != "" && cb.Secret != ch.SecretKey {
		return "ok", nil // не наш запрос — не раскрываем детали
	}

	if cb.Type != "message_new" {
		return "ok", nil
	}

	var obj vkMessageObject
	if err := json.Unmarshal(cb.Object, &obj); err != nil {
		return "ok", nil
	}

	// Обработка в фоне; VK ждёт быстрый "ok".
	go m.process(context.Background(), channelID, ch, obj)
	return "ok", nil
}

func (m *Manager) process(ctx context.Context, channelID uuid.UUID, ch Channel, obj vkMessageObject) {
	var attach []llm.Attachment
	for _, a := range obj.Message.Attachments {
		if a.Type == "photo" && len(a.Photo.Sizes) > 0 {
			best := a.Photo.Sizes[len(a.Photo.Sizes)-1]
			attach = append(attach, llm.Attachment{Type: "image", URL: best.URL, MIMEType: "image/jpeg"})
		}
	}

	externalUser := strconv.FormatInt(obj.Message.FromID, 10)
	externalChat := strconv.FormatInt(obj.Message.PeerID, 10)

	convID, _, _, err := m.runner.StartChannelConversation(ctx, channelID, externalUser, externalChat)
	if err != nil {
		return
	}
	stream, err := m.runner.RunConversation(ctx, convID, obj.Message.Text, attach)
	if err != nil {
		return
	}

	// VK без edit — копим и шлём одним сообщением.
	var acc strings.Builder
	var formURL string
	formRequested := false
	formSent := false
	for ev := range stream {
		if ev.Type == llm.EventText {
			if !formRequested {
				acc.WriteString(ev.Text)
			}
		} else if ev.Type == llm.EventToolCall && ev.ToolCall != nil && ev.ToolCall.Name == "request_form" {
			formRequested = true
			acc.Reset()
			formURL, _ = ev.ToolCall.Arguments["secure_form_url"].(string)
			if formURL != "" && ev.Text != "" {
				formSent = m.sendMessageWithForm(ctx, ch, obj.Message.PeerID, ev.Text, formURL) == nil
			}
		}
	}
	if acc.Len() > 0 || (formURL != "" && !formSent) {
		text := acc.String()
		if text == "" {
			text = "Чтобы продолжить, заполните данные в защищённой форме."
		}
		if formSent {
			formURL = ""
		}
		_ = m.sendMessageWithForm(ctx, ch, obj.Message.PeerID, text, formURL)
	}
}
