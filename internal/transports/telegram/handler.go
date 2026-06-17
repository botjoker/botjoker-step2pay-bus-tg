package telegram

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/botjoker/sambacrm-business-tg/internal/llm"
	"github.com/botjoker/sambacrm-business-tg/internal/mdfmt"
	"github.com/google/uuid"
	tele "gopkg.in/telebot.v3"
)

// HandleUpdate обрабатывает входящий Telegram-апдейт: резолвит диалог, запускает
// агента и стримит ответ через редактирование одного сообщения.
func (m *Manager) HandleUpdate(ctx context.Context, channelID uuid.UUID, update *tele.Update) error {
	ref, ok := m.get(channelID)
	if !ok {
		return nil // бот не запущен — игнорируем
	}

	if update.Message == nil {
		return nil // MVP: только текстовые сообщения
	}
	msg := update.Message

	externalUserID := strconv.FormatInt(msg.Sender.ID, 10)
	externalChatID := strconv.FormatInt(msg.Chat.ID, 10)

	convID, _, _, err := m.runner.StartChannelConversation(ctx, channelID, externalUserID, externalChatID)
	if err != nil {
		slog.Error("telegram: start conversation", "err", err)
		return err
	}

	// Вложения (фото). В telebot.v3 нет FileURLByID — берём FilePath через
	// FileByID и собираем публичный file-URL Telegram с токеном бота.
	var attachments []llm.Attachment
	if msg.Photo != nil {
		if f, err := ref.bot.FileByID(msg.Photo.FileID); err == nil && f.FilePath != "" {
			url := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", ref.token, f.FilePath)
			attachments = append(attachments, llm.Attachment{Type: "image", URL: url, MIMEType: "image/jpeg"})
		}
	}

	stream, err := m.runner.RunConversation(ctx, convID, msg.Text, attachments)
	if err != nil {
		slog.Error("telegram: run conversation", "err", err)
		return err
	}

	m.streamToChat(ref.bot, msg.Chat, stream)
	return nil
}

// streamToChat шлёт плейсхолдер и периодически редактирует его накопленным текстом.
func (m *Manager) streamToChat(bot *tele.Bot, chat *tele.Chat, stream <-chan llm.StreamEvent) {
	sent, err := bot.Send(chat, "…")
	if err != nil {
		slog.Error("telegram: send placeholder", "err", err)
		// всё равно дренируем стрим, чтобы не подвиснуть
		for range stream {
		}
		return
	}

	var (
		mu   sync.Mutex
		acc  strings.Builder
		done = make(chan struct{})
	)

	// Промежуточные правки — чистым текстом (частичный Markdown в стриме ломал бы
	// HTML-разметку); финальная — Telegram-HTML с откатом на plain при ошибке парсинга.
	flush := func(final bool) {
		mu.Lock()
		text := acc.String()
		mu.Unlock()
		if text == "" {
			return
		}
		if final {
			htmlText := mdfmt.ToTelegramHTML(clampTelegram(text))
			if _, err := bot.Edit(sent, htmlText, tele.ModeHTML); err != nil {
				slog.Debug("telegram: edit html, fallback to plain", "err", err)
				if _, err2 := bot.Edit(sent, clampTelegram(mdfmt.ToPlain(text))); err2 != nil {
					slog.Debug("telegram: edit plain", "err", err2)
				}
			}
			return
		}
		if _, err := bot.Edit(sent, clampTelegram(mdfmt.ToPlain(text))); err != nil {
			slog.Debug("telegram: edit", "err", err)
		}
	}

	ticker := time.NewTicker(editInterval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				flush(false)
			case <-done:
				flush(true)
				return
			}
		}
	}()

	for ev := range stream {
		switch ev.Type {
		case llm.EventText:
			mu.Lock()
			acc.WriteString(ev.Text)
			mu.Unlock()
		case llm.EventError:
			mu.Lock()
			acc.WriteString("\n\n_(ошибка генерации)_")
			mu.Unlock()
		}
	}
	close(done)
}

// clampTelegram обрезает текст до лимита Telegram (4096 символов).
func clampTelegram(s string) string {
	const limit = 4096
	if len(s) <= limit {
		return s
	}
	return s[:limit]
}
