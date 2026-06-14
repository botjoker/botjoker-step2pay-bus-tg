package llm

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Anthropic SSE-события (data-объекты, различаем по полю "type").
type antStreamEvent struct {
	Type  string `json:"type"`
	Index int    `json:"index"`

	// message_start
	Message *struct {
		Usage antUsage `json:"usage"`
	} `json:"message"`

	// content_block_start
	ContentBlock *struct {
		Type string `json:"type"` // "text" | "tool_use"
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"content_block"`

	// content_block_delta
	Delta *struct {
		Type        string `json:"type"` // "text_delta" | "input_json_delta"
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`

	// message_delta
	Usage *antUsage `json:"usage"`
}

func (a *Anthropic) streamImpl(ctx context.Context, req StreamRequest, out chan<- StreamEvent) {
	body := a.buildRequest(req.Model, req.Messages, req.Tools, req.Temperature, req.MaxTokens, true)
	httpReq, err := a.newHTTPRequest(ctx, body)
	if err != nil {
		out <- StreamEvent{Type: EventError, Error: err}
		return
	}
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := a.client.Do(httpReq)
	if err != nil {
		out <- StreamEvent{Type: EventError, Error: err}
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		out <- StreamEvent{Type: EventError, Error: fmt.Errorf("anthropic %d: %s", resp.StatusCode, string(b))}
		return
	}

	toolAcc := newToolCallAccumulator()
	var tokensIn, tokensOut int

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue // строки "event:" игнорируем — тип берём из data
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}

		var ev antStreamEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			continue
		}

		switch ev.Type {
		case "message_start":
			if ev.Message != nil {
				tokensIn = ev.Message.Usage.InputTokens
			}
		case "content_block_start":
			if ev.ContentBlock != nil && ev.ContentBlock.Type == "tool_use" {
				toolAcc.add(ev.Index, ev.ContentBlock.ID, ev.ContentBlock.Name, "")
			}
		case "content_block_delta":
			if ev.Delta == nil {
				continue
			}
			switch ev.Delta.Type {
			case "text_delta":
				if ev.Delta.Text != "" {
					out <- StreamEvent{Type: EventText, Text: ev.Delta.Text}
				}
			case "input_json_delta":
				toolAcc.add(ev.Index, "", "", ev.Delta.PartialJSON)
			}
		case "message_delta":
			if ev.Usage != nil {
				tokensOut = ev.Usage.OutputTokens
			}
		case "message_stop":
			// конец
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

	for _, tc := range toolAcc.finalize("an") {
		tcCopy := tc
		out <- StreamEvent{Type: EventToolCall, ToolCall: &tcCopy}
	}
	out <- StreamEvent{Type: EventDone, TokensIn: tokensIn, TokensOut: tokensOut}
}
