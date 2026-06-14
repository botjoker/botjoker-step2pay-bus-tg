package runtime

import (
	"encoding/json"

	"github.com/botjoker/sambacrm-business-tg/internal/llm"
)

// appendAssistantWithTools добавляет ассистентское сообщение с tool-calls
// в историю для следующей итерации цикла.
func appendAssistantWithTools(msgs []llm.Message, text string, calls []llm.ToolCall) []llm.Message {
	return append(msgs, llm.Message{
		Role:      llm.RoleAssistant,
		Content:   text,
		ToolCalls: calls,
	})
}

// appendToolResults добавляет результаты инструментов (role=tool) в историю.
func appendToolResults(msgs []llm.Message, results []llm.Message) []llm.Message {
	return append(msgs, results...)
}

// marshalToolResult сериализует результат инструмента в строку для LLM.
func marshalToolResult(res map[string]any) string {
	if res == nil {
		return "{}"
	}
	b, err := json.Marshal(res)
	if err != nil {
		return `{"error":"failed to marshal tool result"}`
	}
	return string(b)
}
