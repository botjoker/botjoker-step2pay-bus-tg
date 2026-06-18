package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// sseMessage — сериализуемое представление события для SSE/Redis.
type sseMessage struct {
	Type  string `json:"type"` // text | tool_call | done | error | operator | file
	Text  string `json:"text,omitempty"`
	Tool  string `json:"tool,omitempty"`
	Error string `json:"error,omitempty"`
	// Поля файла-вложения (type=file, F1-1).
	URL      string `json:"url,omitempty"`
	Filename string `json:"filename,omitempty"`
	MIME     string `json:"mime,omitempty"`
}

// SSEHub публикует и доставляет события диалога через Redis Pub/Sub
// (канал conv:{cid}:events), что позволяет масштабировать рантайм горизонтально.
type SSEHub struct {
	rdb *redis.Client
}

func NewSSEHub(rdb *redis.Client) *SSEHub { return &SSEHub{rdb: rdb} }

func convChannel(convID uuid.UUID) string { return "conv:" + convID.String() + ":events" }

// Publish отправляет событие в канал диалога.
func (h *SSEHub) Publish(ctx context.Context, convID uuid.UUID, msg sseMessage) error {
	if h.rdb == nil {
		return fmt.Errorf("sse hub: redis not configured")
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return h.rdb.Publish(ctx, convChannel(convID), b).Err()
}

// PublishEvent — публичный публикатор (для Engine из agentstore, без доступа к
// приватному sseMessage). Реализует agentstore.EventSink структурно.
func (h *SSEHub) PublishEvent(ctx context.Context, convID uuid.UUID, evType, text, tool, errStr string) error {
	return h.Publish(ctx, convID, sseMessage{Type: evType, Text: text, Tool: tool, Error: errStr})
}

// Stream подписывается на канал и пишет события в ResponseWriter в формате SSE,
// пока не придёт done/error или не закроется соединение.
func (h *SSEHub) Stream(ctx context.Context, w http.ResponseWriter, convID uuid.UUID) error {
	if h.rdb == nil {
		return fmt.Errorf("sse hub: redis not configured")
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		return fmt.Errorf("streaming unsupported")
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	sub := h.rdb.Subscribe(ctx, convChannel(convID))
	defer sub.Close()
	ch := sub.Channel()

	for {
		select {
		case <-ctx.Done():
			return nil
		case m, ok := <-ch:
			if !ok {
				return nil
			}
			fmt.Fprintf(w, "data: %s\n\n", m.Payload)
			flusher.Flush()

			var parsed sseMessage
			if json.Unmarshal([]byte(m.Payload), &parsed) == nil {
				if parsed.Type == "done" || parsed.Type == "error" {
					return nil
				}
			}
		}
	}
}
