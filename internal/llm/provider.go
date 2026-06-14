// Package llm — multi-provider LLM abstraction. См. шаги 032–035.
package llm

import "context"

// Role в чат-диалоге с LLM.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message — один кусок контекста для LLM.
type Message struct {
	Role        Role
	Content     string
	ToolCalls   []ToolCall   // только для RoleAssistant
	ToolCallID  string       // только для RoleTool
	Name        string       // имя tool (для RoleTool — некоторые провайдеры требуют)
	Attachments []Attachment // изображения и т.п.
}

// Attachment — мультимодальное вложение.
type Attachment struct {
	Type     string // "image" | "audio"
	URL      string // signed URL или data: URI
	MIMEType string
}

// ToolDef — определение инструмента для LLM (JSON Schema).
type ToolDef struct {
	Name        string
	Description string
	Schema      map[string]any // JSON Schema (object)
}

// ToolCall — что LLM хочет выполнить.
type ToolCall struct {
	ID        string
	Name      string
	Arguments map[string]any
}

// StreamEvent — события стрима генерации.
type StreamEvent struct {
	Type      string // "text" | "tool_call" | "done" | "error"
	Text      string
	ToolCall  *ToolCall
	Error     error
	TokensIn  int
	TokensOut int
}

// Event types для StreamEvent.Type.
const (
	EventText     = "text"
	EventToolCall = "tool_call"
	EventDone     = "done"
	EventError    = "error"
)

// CompleteResult — результат не-стримингового вызова.
type CompleteResult struct {
	Content   string
	ToolCalls []ToolCall
	TokensIn  int
	TokensOut int
	CostUSD   float64
	Model     string
}

// LLMProvider — общий контракт для всех провайдеров.
type LLMProvider interface {
	Name() string

	// Stream — стримит ответ. Канал закрывается на done/error.
	Stream(ctx context.Context, req StreamRequest) (<-chan StreamEvent, error)

	// Complete — синхронный (короткие задачи: insights, summary, language detect).
	Complete(ctx context.Context, req CompleteRequest) (*CompleteResult, error)

	// SupportsVision — есть ли мультимодальность.
	SupportsVision() bool

	// SupportsTools — поддерживает ли function-calling.
	SupportsTools() bool
}

// StreamRequest — запрос на стриминговую генерацию.
type StreamRequest struct {
	Model       string
	Messages    []Message
	Tools       []ToolDef
	Temperature float32
	MaxTokens   int
}

// CompleteRequest — запрос на синхронную генерацию.
type CompleteRequest struct {
	Model       string
	Messages    []Message
	Tools       []ToolDef
	Temperature float32
	MaxTokens   int
	JSONMode    bool // принудительный JSON-вывод (если провайдер умеет)
}
