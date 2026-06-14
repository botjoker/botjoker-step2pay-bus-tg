package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	anthropicBaseURL = "https://api.anthropic.com/v1"
	anthropicVersion = "2023-06-01"
)

// Anthropic — провайдер Claude через REST (/v1/messages), без официального SDK.
//
// Отличия от OpenAI:
//   - auth: заголовок x-api-key (+ обязательный anthropic-version)
//   - system — отдельное поле, не в messages
//   - контент — массив блоков (text / tool_use / tool_result)
//   - tool arguments — объект (не JSON-строка)
//
// Credentials: Key1 = API key, Key2 = base_url (опц.), Key3 = модель (опц.).
type Anthropic struct {
	apiKey       string
	baseURL      string
	defaultModel string
	client       *http.Client
}

// NewAnthropic — публичный конструктор для фабрики.
func NewAnthropic(c Credentials) LLMProvider { return NewAnthropicProvider(c) }

// NewAnthropicProvider — типизированный конструктор.
func NewAnthropicProvider(c Credentials) *Anthropic {
	base := c.Key2
	if base == "" {
		base = anthropicBaseURL
	}
	model := c.Key3
	if model == "" {
		model = "claude-3-5-sonnet-latest"
	}
	return &Anthropic{
		apiKey:       c.Key1,
		baseURL:      strings.TrimRight(base, "/"),
		defaultModel: model,
		client:       &http.Client{Timeout: 120 * time.Second},
	}
}

func (a *Anthropic) Name() string         { return "anthropic" }
func (a *Anthropic) SupportsVision() bool { return true }
func (a *Anthropic) SupportsTools() bool  { return true }

// --- wire-формат ---

type antReq struct {
	Model     string       `json:"model"`
	System    string       `json:"system,omitempty"`
	Messages  []antMsg     `json:"messages"`
	Tools     []antToolDef `json:"tools,omitempty"`
	MaxTokens int          `json:"max_tokens"`
	Temperature float32    `json:"temperature,omitempty"`
	Stream    bool         `json:"stream,omitempty"`
}

type antMsg struct {
	Role    string     `json:"role"` // "user" | "assistant"
	Content []antBlock `json:"content"`
}

type antBlock struct {
	Type string `json:"type"` // "text" | "tool_use" | "tool_result" | "image"
	// text
	Text string `json:"text,omitempty"`
	// tool_use
	ID    string         `json:"id,omitempty"`
	Name  string         `json:"name,omitempty"`
	Input map[string]any `json:"input,omitempty"`
	// tool_result
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
	// image
	Source *antSource `json:"source,omitempty"`
}

// antSource — источник изображения (url или base64).
type antSource struct {
	Type      string `json:"type"` // "url" | "base64"
	URL       string `json:"url,omitempty"`
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
}

type antToolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

// extractSystem собирает все system-сообщения в одну строку (Anthropic требует
// system отдельным полем), а остальные конвертирует в блоки.
func (a *Anthropic) buildRequest(model string, msgs []Message, tools []ToolDef, temp float32, maxTok int, stream bool) antReq {
	if model == "" {
		model = a.defaultModel
	}
	if maxTok <= 0 {
		maxTok = 2048 // Anthropic требует max_tokens обязательно
	}

	var system []string
	antMsgs := make([]antMsg, 0, len(msgs))
	for _, m := range msgs {
		switch m.Role {
		case RoleSystem:
			if m.Content != "" {
				system = append(system, m.Content)
			}
		case RoleTool:
			antMsgs = append(antMsgs, antMsg{
				Role: "user",
				Content: []antBlock{{
					Type:      "tool_result",
					ToolUseID: m.ToolCallID,
					Content:   m.Content,
				}},
			})
		case RoleAssistant:
			blocks := []antBlock{}
			if m.Content != "" {
				blocks = append(blocks, antBlock{Type: "text", Text: m.Content})
			}
			for _, tc := range m.ToolCalls {
				blocks = append(blocks, antBlock{
					Type:  "tool_use",
					ID:    tc.ID,
					Name:  tc.Name,
					Input: tc.Arguments,
				})
			}
			antMsgs = append(antMsgs, antMsg{Role: "assistant", Content: blocks})
		default: // user
			blocks := make([]antBlock, 0, len(m.Attachments)+1)
			if m.Content != "" {
				blocks = append(blocks, antBlock{Type: "text", Text: m.Content})
			}
			for _, a := range m.Attachments {
				if a.Type == "image" && a.URL != "" {
					blocks = append(blocks, antBlock{Type: "image", Source: &antSource{Type: "url", URL: a.URL}})
				}
			}
			if len(blocks) == 0 {
				blocks = append(blocks, antBlock{Type: "text", Text: m.Content})
			}
			antMsgs = append(antMsgs, antMsg{Role: "user", Content: blocks})
		}
	}

	var antTools []antToolDef
	for _, t := range tools {
		antTools = append(antTools, antToolDef{
			Name: t.Name, Description: t.Description, InputSchema: t.Schema,
		})
	}

	return antReq{
		Model:       model,
		System:      strings.Join(system, "\n\n"),
		Messages:    antMsgs,
		Tools:       antTools,
		MaxTokens:   maxTok,
		Temperature: temp,
		Stream:      stream,
	}
}

func (a *Anthropic) newHTTPRequest(ctx context.Context, body any) (*http.Request, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+"/messages", bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", a.apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)
	return req, nil
}

func (a *Anthropic) Complete(ctx context.Context, req CompleteRequest) (*CompleteResult, error) {
	body := a.buildRequest(req.Model, req.Messages, req.Tools, req.Temperature, req.MaxTokens, false)
	httpReq, err := a.newHTTPRequest(ctx, body)
	if err != nil {
		return nil, err
	}
	resp, err := a.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("anthropic %d: %s", resp.StatusCode, string(b))
	}
	var raw antResponse
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	return parseAnthropicResponse(&raw, body.Model), nil
}

func (a *Anthropic) Stream(ctx context.Context, req StreamRequest) (<-chan StreamEvent, error) {
	out := make(chan StreamEvent, 16)
	go func() {
		defer close(out)
		a.streamImpl(ctx, req, out)
	}()
	return out, nil
}
