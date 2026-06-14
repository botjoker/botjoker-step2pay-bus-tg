package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// streamImpl стримит ответ GigaChat (SSE: "data: {json}\n\n", терминатор "[DONE]").
func (g *GigaChat) streamImpl(ctx context.Context, req StreamRequest, out chan<- StreamEvent) {
	token, err := g.authToken(ctx)
	if err != nil {
		out <- StreamEvent{Type: EventError, Error: err}
		return
	}

	body := buildGigaRequest(req.Model, req.Messages, req.Tools, req.Temperature, req.MaxTokens, true)
	buf, err := json.Marshal(body)
	if err != nil {
		out <- StreamEvent{Type: EventError, Error: err}
		return
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, g.completionsEndpoint(), bytes.NewReader(buf))
	if err != nil {
		out <- StreamEvent{Type: EventError, Error: err}
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := g.client.Do(httpReq)
	if err != nil {
		out <- StreamEvent{Type: EventError, Error: err}
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		out <- StreamEvent{Type: EventError, Error: fmt.Errorf("gigachat %d: %s", resp.StatusCode, string(b))}
		return
	}

	// Аккумулятор tool-calls по индексам (delta стримит аргументы по кускам).
	toolAcc := newToolCallAccumulator()
	var tokensIn, tokensOut int

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}

		var chunk gigaResponse
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if chunk.Usage.TotalTokens > 0 {
			tokensIn = chunk.Usage.PromptTokens
			tokensOut = chunk.Usage.CompletionTokens
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		delta := chunk.Choices[0].Delta

		if delta.Content != "" {
			out <- StreamEvent{Type: EventText, Text: delta.Content}
		}
		for _, tc := range delta.ToolCalls {
			toolAcc.add(tc.Index, tc.ID, tc.Function.Name, tc.Function.Arguments)
		}

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

	for _, tc := range toolAcc.finalize("gc") {
		tcCopy := tc
		out <- StreamEvent{Type: EventToolCall, ToolCall: &tcCopy}
	}
	out <- StreamEvent{Type: EventDone, TokensIn: tokensIn, TokensOut: tokensOut}
}
