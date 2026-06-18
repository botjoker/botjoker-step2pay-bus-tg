package api

import (
	"context"
	"fmt"

	"github.com/botjoker/sambacrm-business-tg/internal/runtime"
	"github.com/google/uuid"
)

// WebSender доставляет операторские сообщения (live takeover) в web-виджет через
// SSE: публикует событие в канал диалога conv:{convID}:events. Реализует
// runtime.TransportSender; для web именно convID — ключ доставки (channelID и
// externalUserID не используются).
type WebSender struct {
	hub *SSEHub
}

func NewWebSender(hub *SSEHub) *WebSender { return &WebSender{hub: hub} }

var _ runtime.TransportSender = (*WebSender)(nil)

func (s *WebSender) SendToConversation(ctx context.Context, convID, _ uuid.UUID, _ string, text string, att *runtime.Attachment) error {
	if s.hub == nil || s.hub.rdb == nil {
		return fmt.Errorf("web sender: SSE (redis) не сконфигурирован")
	}
	// type "file" — виджет рендерит вложение со ссылкой на скачивание (F1-1).
	if att != nil && att.URL != "" {
		return s.hub.Publish(ctx, convID, sseMessage{
			Type:     "file",
			Text:     text,
			URL:      att.URL,
			Filename: att.Filename,
			MIME:     att.MIME,
		})
	}
	// type "operator" — виджет рендерит отдельным пузырём (не дописывает к ответу AI).
	return s.hub.Publish(ctx, convID, sseMessage{Type: "operator", Text: text})
}
