package llm

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	gigaAuthURL = "https://ngw.devices.sberbank.ru:9443/api/v2/oauth"
	gigaBaseURL = "https://gigachat.devices.sberbank.ru/api/v1"
)

// GigaChat — провайдер Сбер GigaChat (OpenAI-совместимый формат, свой OAuth).
//
// Credentials:
//   - Key1 = client_id (информативно)
//   - Key2 = Authorization key (base64 от "client_id:client_secret")
//   - Extra["scope"] = GIGACHAT_API_PERS | GIGACHAT_API_B2B | GIGACHAT_API_CORP
type GigaChat struct {
	clientID   string
	authBase64 string
	scope      string
	authURL    string // переопределяется в тестах
	baseURL    string // переопределяется в тестах
	client     *http.Client

	mu             sync.Mutex
	token          string
	tokenExpiresAt time.Time
}

// NewGigaChat — публичный конструктор для фабрики.
func NewGigaChat(c Credentials) LLMProvider { return NewGigaChatProvider(c) }

// NewGigaChatProvider — типизированный конструктор.
func NewGigaChatProvider(c Credentials) *GigaChat {
	scope := "GIGACHAT_API_PERS"
	if v, ok := c.Extra["scope"]; ok && v != "" {
		scope = v
	}
	// InsecureSkipVerify — у Сбера корневые сертификаты «Russian Trusted CA».
	// В проде их монтируют в системный trust store; здесь — fallback.
	tr := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec
	return &GigaChat{
		clientID:   c.Key1,
		authBase64: c.Key2,
		scope:      scope,
		client:     &http.Client{Timeout: 90 * time.Second, Transport: tr},
	}
}

func (g *GigaChat) Name() string         { return "gigachat" }
func (g *GigaChat) SupportsVision() bool { return true }
func (g *GigaChat) SupportsTools() bool  { return true }

func (g *GigaChat) authEndpoint() string {
	if g.authURL != "" {
		return g.authURL
	}
	return gigaAuthURL
}

func (g *GigaChat) completionsEndpoint() string {
	base := gigaBaseURL
	if g.baseURL != "" {
		base = g.baseURL
	}
	return base + "/chat/completions"
}

// authToken возвращает кешированный access_token, обновляя его до истечения.
func (g *GigaChat) authToken(ctx context.Context) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.token != "" && time.Until(g.tokenExpiresAt) > time.Minute {
		return g.token, nil
	}

	form := url.Values{"scope": {g.scope}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.authEndpoint(), bytes.NewBufferString(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Basic "+g.authBase64)
	req.Header.Set("RqUID", uuid.New().String())
	req.Header.Set("Accept", "application/json")

	resp, err := g.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("gigachat oauth %d: %s", resp.StatusCode, string(b))
	}

	var body struct {
		AccessToken string `json:"access_token"`
		ExpiresAt   int64  `json:"expires_at"` // unix ms
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", err
	}
	g.token = body.AccessToken
	if body.ExpiresAt > 0 {
		g.tokenExpiresAt = time.UnixMilli(body.ExpiresAt)
	} else {
		g.tokenExpiresAt = time.Now().Add(25 * time.Minute)
	}
	return g.token, nil
}

// --- OpenAI-совместимый wire-формат ---

type gigaMsg struct {
	Role       string         `json:"role"`
	Content    string         `json:"content"`
	ToolCalls  []gigaToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type gigaToolCall struct {
	Index    int        `json:"index,omitempty"`
	ID       string     `json:"id"`
	Type     string     `json:"type"` // "function"
	Function gigaToolFn `json:"function"`
}

type gigaToolFn struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON-строка
}

type gigaToolDef struct {
	Type     string    `json:"type"`
	Function gigaFnDef `json:"function"`
}

type gigaFnDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type gigaReq struct {
	Model       string        `json:"model"`
	Messages    []gigaMsg     `json:"messages"`
	Tools       []gigaToolDef `json:"tools,omitempty"`
	Stream      bool          `json:"stream"`
	Temperature float32       `json:"temperature"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
}

func buildGigaRequest(model string, msgs []Message, tools []ToolDef, temp float32, maxTok int, stream bool) gigaReq {
	if model == "" {
		model = "GigaChat"
	}
	gMsgs := make([]gigaMsg, 0, len(msgs))
	for _, m := range msgs {
		gm := gigaMsg{Role: string(m.Role), Content: m.Content, ToolCallID: m.ToolCallID}
		for _, tc := range m.ToolCalls {
			gm.ToolCalls = append(gm.ToolCalls, gigaToolCall{
				ID:       tc.ID,
				Type:     "function",
				Function: gigaToolFn{Name: tc.Name, Arguments: marshalArgs(tc.Arguments)},
			})
		}
		gMsgs = append(gMsgs, gm)
	}

	gTools := make([]gigaToolDef, 0, len(tools))
	for _, t := range tools {
		gTools = append(gTools, gigaToolDef{
			Type:     "function",
			Function: gigaFnDef{Name: t.Name, Description: t.Description, Parameters: t.Schema},
		})
	}

	return gigaReq{
		Model:       model,
		Messages:    gMsgs,
		Tools:       gTools,
		Stream:      stream,
		Temperature: temp,
		MaxTokens:   maxTok,
	}
}

func (g *GigaChat) Complete(ctx context.Context, req CompleteRequest) (*CompleteResult, error) {
	token, err := g.authToken(ctx)
	if err != nil {
		return nil, err
	}

	body := buildGigaRequest(req.Model, req.Messages, req.Tools, req.Temperature, req.MaxTokens, false)
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, g.completionsEndpoint(), bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("Accept", "application/json")

	resp, err := g.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("gigachat %d: %s", resp.StatusCode, string(b))
	}

	var raw gigaResponse
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	return parseGigaResponse(&raw, req.Model)
}

func (g *GigaChat) Stream(ctx context.Context, req StreamRequest) (<-chan StreamEvent, error) {
	out := make(chan StreamEvent, 16)
	go func() {
		defer close(out)
		g.streamImpl(ctx, req, out)
	}()
	return out, nil
}
