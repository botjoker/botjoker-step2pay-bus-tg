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
	"time"
)

// openAICompatible — общий HTTP-клиент для всех OpenAI-совместимых API
// (OpenAI, OpenRouter, DeepSeek, T-Pro). Реализован на чистом net/http, без
// официального SDK: один формат, отличается base_url / ключом / моделью.
type openAICompatible struct {
	name         string
	apiKey       string
	baseURL      string // без хвостового /chat/completions
	defaultModel string
	supportsTools  bool
	supportsVision bool
	extraHeaders   map[string]string
	client         *http.Client
}

type ocOption func(*openAICompatible)

func withTools(b bool) ocOption  { return func(o *openAICompatible) { o.supportsTools = b } }
func withVision(b bool) ocOption { return func(o *openAICompatible) { o.supportsVision = b } }
func withExtraHeaders(h map[string]string) ocOption {
	return func(o *openAICompatible) { o.extraHeaders = h }
}

func newOpenAICompatible(name, apiKey, baseURL, defaultModel string, opts ...ocOption) *openAICompatible {
	o := &openAICompatible{
		name:         name,
		apiKey:       apiKey,
		baseURL:      strings.TrimRight(baseURL, "/"),
		defaultModel: defaultModel,
		client:       &http.Client{Timeout: 120 * time.Second},
	}
	for _, opt := range opts {
		opt(o)
	}
	return o
}

// NewOpenAI — провайдер OpenAI (api.openai.com). Key1 = API key, Key2 = base_url (опц.).
func NewOpenAI(c Credentials) LLMProvider {
	base := c.Key2
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	model := c.Key3
	if model == "" {
		model = "gpt-4o-mini"
	}
	return newOpenAICompatible("openai", c.Key1, base, model, withTools(true), withVision(true))
}

func (o *openAICompatible) Name() string         { return o.name }
func (o *openAICompatible) SupportsVision() bool { return o.supportsVision }
func (o *openAICompatible) SupportsTools() bool  { return o.supportsTools }

// --- wire-формат ---

type oaMsg struct {
	Role       string       `json:"role"`
	Content    any          `json:"content"` // string или []part (для vision)
	ToolCalls  []oaToolCall `json:"tool_calls,omitempty"`
	ToolCallID string       `json:"tool_call_id,omitempty"`
	Name       string       `json:"name,omitempty"`
}

// oaContent: строка, либо массив частей (text + image_url) при наличии вложений.
func oaContent(m Message) any {
	if len(m.Attachments) == 0 {
		return m.Content
	}
	parts := make([]any, 0, len(m.Attachments)+1)
	if m.Content != "" {
		parts = append(parts, map[string]any{"type": "text", "text": m.Content})
	}
	for _, a := range m.Attachments {
		if a.Type == "image" && a.URL != "" {
			parts = append(parts, map[string]any{
				"type":      "image_url",
				"image_url": map[string]any{"url": a.URL},
			})
		}
	}
	return parts
}

type oaToolCall struct {
	Index    int        `json:"index,omitempty"`
	ID       string     `json:"id,omitempty"`
	Type     string     `json:"type,omitempty"`
	Function oaToolFn   `json:"function"`
}

type oaToolFn struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type oaToolDef struct {
	Type     string  `json:"type"`
	Function oaFnDef `json:"function"`
}

type oaFnDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type oaStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type oaReq struct {
	Model          string           `json:"model"`
	Messages       []oaMsg          `json:"messages"`
	Tools          []oaToolDef      `json:"tools,omitempty"`
	Temperature    float32          `json:"temperature"`
	MaxTokens      int              `json:"max_tokens,omitempty"`
	Stream         bool             `json:"stream"`
	StreamOptions  *oaStreamOptions `json:"stream_options,omitempty"`
	ResponseFormat any              `json:"response_format,omitempty"`
}

func (o *openAICompatible) buildRequest(model string, msgs []Message, tools []ToolDef, temp float32, maxTok int, stream bool) oaReq {
	if model == "" {
		model = o.defaultModel
	}
	oaMsgs := make([]oaMsg, 0, len(msgs))
	for _, m := range msgs {
		om := oaMsg{Role: string(m.Role), Content: oaContent(m), ToolCallID: m.ToolCallID, Name: m.Name}
		for _, tc := range m.ToolCalls {
			om.ToolCalls = append(om.ToolCalls, oaToolCall{
				ID:       tc.ID,
				Type:     "function",
				Function: oaToolFn{Name: tc.Name, Arguments: marshalArgs(tc.Arguments)},
			})
		}
		oaMsgs = append(oaMsgs, om)
	}
	var oaTools []oaToolDef
	if o.supportsTools {
		for _, t := range tools {
			oaTools = append(oaTools, oaToolDef{
				Type:     "function",
				Function: oaFnDef{Name: t.Name, Description: t.Description, Parameters: t.Schema},
			})
		}
	}
	req := oaReq{
		Model:       model,
		Messages:    oaMsgs,
		Tools:       oaTools,
		Temperature: temp,
		MaxTokens:   maxTok,
		Stream:      stream,
	}
	if stream {
		req.StreamOptions = &oaStreamOptions{IncludeUsage: true}
	}
	return req
}

func (o *openAICompatible) newHTTPRequest(ctx context.Context, body any) (*http.Request, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/chat/completions", bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+o.apiKey)
	for k, v := range o.extraHeaders {
		req.Header.Set(k, v)
	}
	return req, nil
}

func (o *openAICompatible) Complete(ctx context.Context, req CompleteRequest) (*CompleteResult, error) {
	body := o.buildRequest(req.Model, req.Messages, req.Tools, req.Temperature, req.MaxTokens, false)
	switch {
	case req.JSONSchema != nil:
		// Strict structured output — поддерживается на OpenAI-совместимых.
		// На провайдерах, которые не понимают json_schema type, вернётся 400 —
		// вызывающему стоит fallback'нуться на JSONMode при ошибке.
		name := req.JSONSchema.Name
		if name == "" {
			name = "response"
		}
		body.ResponseFormat = map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name":   name,
				"strict": req.JSONSchema.Strict,
				"schema": req.JSONSchema.Schema,
			},
		}
	case req.JSONMode:
		body.ResponseFormat = map[string]any{"type": "json_object"}
	}
	httpReq, err := o.newHTTPRequest(ctx, body)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Accept", "application/json")

	resp, err := o.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("%s %d: %s", o.name, resp.StatusCode, string(b))
	}

	var raw oaResponse
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	return o.parseResponse(&raw, body.Model), nil
}

func (o *openAICompatible) Stream(ctx context.Context, req StreamRequest) (<-chan StreamEvent, error) {
	out := make(chan StreamEvent, 16)
	go func() {
		defer close(out)
		o.streamImpl(ctx, req, out)
	}()
	return out, nil
}

func (o *openAICompatible) streamImpl(ctx context.Context, req StreamRequest, out chan<- StreamEvent) {
	body := o.buildRequest(req.Model, req.Messages, req.Tools, req.Temperature, req.MaxTokens, true)
	httpReq, err := o.newHTTPRequest(ctx, body)
	if err != nil {
		out <- StreamEvent{Type: EventError, Error: err}
		return
	}
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := o.client.Do(httpReq)
	if err != nil {
		out <- StreamEvent{Type: EventError, Error: err}
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		out <- StreamEvent{Type: EventError, Error: fmt.Errorf("%s %d: %s", o.name, resp.StatusCode, string(b))}
		return
	}

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
		var chunk oaResponse
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if chunk.Usage != nil && chunk.Usage.TotalTokens > 0 {
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

	for _, tc := range toolAcc.finalize(o.shortPrefix()) {
		tcCopy := tc
		out <- StreamEvent{Type: EventToolCall, ToolCall: &tcCopy}
	}
	out <- StreamEvent{Type: EventDone, TokensIn: tokensIn, TokensOut: tokensOut}
}

func (o *openAICompatible) shortPrefix() string {
	if len(o.name) >= 2 {
		return o.name[:2]
	}
	return "oa"
}
