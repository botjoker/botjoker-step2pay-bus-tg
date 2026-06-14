package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

// streamImpl стримит ответ Yandex.
//
// Особенность Yandex: при stream=true сервер шлёт newline-delimited JSON, где
// КАЖДЫЙ чанк содержит ПОЛНЫЙ накопленный текст (не дельту). Поэтому считаем
// дельту сами: delta = new_text[len(prev):].
func (y *YandexGPT) streamImpl(ctx context.Context, body yandexReq, model string, out chan<- StreamEvent) {
	buf, err := json.Marshal(body)
	if err != nil {
		out <- StreamEvent{Type: EventError, Error: err}
		return
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, y.endpoint(), bytes.NewReader(buf))
	if err != nil {
		out <- StreamEvent{Type: EventError, Error: err}
		return
	}
	httpReq.Header.Set("Authorization", y.authHeader())
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := y.client.Do(httpReq)
	if err != nil {
		out <- StreamEvent{Type: EventError, Error: err}
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		out <- StreamEvent{Type: EventError, Error: decodeYandexError(resp)}
		return
	}

	scanner := bufio.NewScanner(resp.Body)
	// чанки могут быть крупными — увеличиваем буфер до 1 МБ
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var prevText string
	var tokensIn, tokensOut int
	var lastMsg yandexRespMsg

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var chunk yandexResponse
		if err := json.Unmarshal([]byte(line), &chunk); err != nil {
			// пропускаем нераспознанные строки (keep-alive и т.п.)
			continue
		}
		if len(chunk.Result.Alternatives) == 0 {
			continue
		}

		msg := chunk.Result.Alternatives[0].Message
		lastMsg = msg
		tokensIn = atoiSafe(chunk.Result.Usage.InputTextTokens)
		tokensOut = atoiSafe(chunk.Result.Usage.CompletionTokens)

		if len(msg.Text) > len(prevText) && strings.HasPrefix(msg.Text, prevText) {
			delta := msg.Text[len(prevText):]
			out <- StreamEvent{Type: EventText, Text: delta}
		} else if msg.Text != prevText && msg.Text != "" {
			// текст не является префиксным продолжением — шлём целиком
			out <- StreamEvent{Type: EventText, Text: msg.Text}
		}
		prevText = msg.Text

		select {
		case <-ctx.Done():
			out <- StreamEvent{Type: EventError, Error: ctx.Err()}
			return
		default:
		}
	}

	if err := scanner.Err(); err != nil {
		out <- StreamEvent{Type: EventError, Error: err}
		return
	}

	// финальные tool-calls (если модель их вернула)
	if lastMsg.ToolCallList != nil {
		for i, tc := range lastMsg.ToolCallList.ToolCalls {
			tcCopy := ToolCall{
				ID:        toolCallID("yc", i),
				Name:      tc.FunctionCall.Name,
				Arguments: tc.FunctionCall.Arguments,
			}
			out <- StreamEvent{Type: EventToolCall, ToolCall: &tcCopy}
		}
	}

	out <- StreamEvent{Type: EventDone, TokensIn: tokensIn, TokensOut: tokensOut}
}
