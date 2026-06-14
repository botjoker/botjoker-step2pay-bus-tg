package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const yandexBaseURL = "https://llm.api.cloud.yandex.net/foundationModels/v1"

// YandexGPT — провайдер Yandex Cloud Foundation Models.
//
// Credentials:
//   - Key1 = Api-Key (или IAM-токен — см. authHeader)
//   - Key2 = folder_id (для сборки gpt://{folder}/{model}/latest)
//   - Key3 = "iam", если Key1 это IAM-токен (иначе считаем Api-Key)
type YandexGPT struct {
	apiKey   string
	folderID string
	useIAM   bool
	baseURL  string // переопределяется в тестах; по умолчанию yandexBaseURL
	client   *http.Client
}

func (y *YandexGPT) endpoint() string {
	if y.baseURL != "" {
		return y.baseURL + "/completion"
	}
	return yandexBaseURL + "/completion"
}

// NewYandexGPT — публичный конструктор для фабрики.
func NewYandexGPT(c Credentials) LLMProvider { return NewYandexGPTProvider(c) }

// NewYandexGPTProvider — типизированный конструктор.
func NewYandexGPTProvider(c Credentials) *YandexGPT {
	return &YandexGPT{
		apiKey:   c.Key1,
		folderID: c.Key2,
		useIAM:   strings.EqualFold(c.Key3, "iam"),
		client:   &http.Client{Timeout: 90 * time.Second},
	}
}

func (y *YandexGPT) Name() string         { return "yandex_gpt" }
func (y *YandexGPT) SupportsVision() bool { return true } // yandexgpt-vision
func (y *YandexGPT) SupportsTools() bool  { return true } // function-calling (YandexGPT 4 Pro+)

// --- wire-формат запроса ---

type yandexReq struct {
	ModelURI          string        `json:"modelUri"`
	CompletionOptions yandexOptions `json:"completionOptions"`
	Messages          []yandexMsg   `json:"messages"`
	Tools             []yandexTool  `json:"tools,omitempty"`
}

type yandexOptions struct {
	Stream      bool    `json:"stream"`
	Temperature float32 `json:"temperature"`
	MaxTokens   int     `json:"maxTokens,string"` // Yandex принимает строкой
}

type yandexMsg struct {
	Role           string              `json:"role,omitempty"`
	Text           string              `json:"text,omitempty"`
	ToolCallList   *yandexToolCallList `json:"toolCallList,omitempty"`
	ToolResultList *yandexToolResList  `json:"toolResultList,omitempty"`
}

type yandexToolCallList struct {
	ToolCalls []yandexToolCall `json:"toolCalls"`
}

type yandexToolCall struct {
	FunctionCall yandexFunctionCall `json:"functionCall"`
}

type yandexFunctionCall struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

type yandexToolResList struct {
	ToolResults []yandexToolResult `json:"toolResults"`
}

type yandexToolResult struct {
	FunctionResult yandexFunctionResult `json:"functionResult"`
}

type yandexFunctionResult struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

type yandexTool struct {
	Function yandexFunction `json:"function"`
}

type yandexFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

func (y *YandexGPT) authHeader() string {
	if y.useIAM {
		return "Bearer " + y.apiKey
	}
	return "Api-Key " + y.apiKey
}

func (y *YandexGPT) buildModelURI(model string) string {
	if strings.HasPrefix(model, "gpt://") {
		return model
	}
	if model == "" {
		model = "yandexgpt-lite/latest"
	}
	if !strings.Contains(model, "/") {
		model += "/latest"
	}
	return fmt.Sprintf("gpt://%s/%s", y.folderID, model)
}

// buildRequest конвертирует общий формат сообщений в yandex-специфичный.
func (y *YandexGPT) buildRequest(model string, msgs []Message, tools []ToolDef, temp float32, maxTok int, stream bool) yandexReq {
	yMsgs := make([]yandexMsg, 0, len(msgs))
	for _, m := range msgs {
		switch m.Role {
		case RoleTool:
			// tool-result: отдельное сообщение без роли, с toolResultList
			yMsgs = append(yMsgs, yandexMsg{
				ToolResultList: &yandexToolResList{ToolResults: []yandexToolResult{{
					FunctionResult: yandexFunctionResult{Name: m.Name, Content: m.Content},
				}}},
			})
		case RoleAssistant:
			ym := yandexMsg{Role: "assistant", Text: m.Content}
			if len(m.ToolCalls) > 0 {
				calls := make([]yandexToolCall, 0, len(m.ToolCalls))
				for _, tc := range m.ToolCalls {
					calls = append(calls, yandexToolCall{
						FunctionCall: yandexFunctionCall{Name: tc.Name, Arguments: tc.Arguments},
					})
				}
				ym.ToolCallList = &yandexToolCallList{ToolCalls: calls}
			}
			yMsgs = append(yMsgs, ym)
		default:
			yMsgs = append(yMsgs, yandexMsg{Role: string(m.Role), Text: m.Content})
		}
	}

	yTools := make([]yandexTool, 0, len(tools))
	for _, t := range tools {
		yTools = append(yTools, yandexTool{Function: yandexFunction{
			Name: t.Name, Description: t.Description, Parameters: t.Schema,
		}})
	}

	return yandexReq{
		ModelURI: y.buildModelURI(model),
		CompletionOptions: yandexOptions{
			Stream:      stream,
			Temperature: temp,
			MaxTokens:   maxTok,
		},
		Messages: yMsgs,
		Tools:    yTools,
	}
}

func (y *YandexGPT) Complete(ctx context.Context, req CompleteRequest) (*CompleteResult, error) {
	body := y.buildRequest(req.Model, req.Messages, req.Tools, req.Temperature, req.MaxTokens, false)

	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, y.endpoint(), bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", y.authHeader())
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := y.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, decodeYandexError(resp)
	}

	var raw yandexResponse
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("yandex decode: %w", err)
	}
	return parseYandexResponse(&raw, req.Model)
}

func (y *YandexGPT) Stream(ctx context.Context, req StreamRequest) (<-chan StreamEvent, error) {
	body := y.buildRequest(req.Model, req.Messages, req.Tools, req.Temperature, req.MaxTokens, true)
	out := make(chan StreamEvent, 16)
	go func() {
		defer close(out)
		y.streamImpl(ctx, body, req.Model, out)
	}()
	return out, nil
}

func decodeYandexError(resp *http.Response) error {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&e)
	msg := e.Error.Message
	if msg == "" {
		msg = e.Message
	}
	return fmt.Errorf("yandex http %d: %s", resp.StatusCode, msg)
}
