package llm

import (
	"encoding/json"
	"strconv"
	"strings"
)

// toolCallID формирует синтетический id для tool-call у провайдеров, которые
// не присылают собственный id (Yandex, GigaChat). prefix — короткий код провайдера.
func toolCallID(prefix string, idx int) string {
	return prefix + "_" + strconv.Itoa(idx)
}

// marshalArgs сериализует аргументы tool-call в JSON-строку
// (OpenAI/GigaChat ждут arguments строкой).
func marshalArgs(args map[string]any) string {
	if args == nil {
		return "{}"
	}
	b, err := json.Marshal(args)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// parseArgs разбирает JSON-строку аргументов tool-call в map.
// Пустая/битая строка → пустая map (не nil).
func parseArgs(s string) map[string]any {
	m := map[string]any{}
	if s == "" {
		return m
	}
	_ = json.Unmarshal([]byte(s), &m)
	return m
}

// toolCallAccumulator собирает tool-calls из стрим-дельт (OpenAI/GigaChat шлют
// имя/id в первом фрагменте, а аргументы — кусками по тому же index).
type toolCallAccumulator struct {
	order []int
	byIdx map[int]*accTool
}

type accTool struct {
	id   string
	name string
	args strings.Builder
}

func newToolCallAccumulator() *toolCallAccumulator {
	return &toolCallAccumulator{byIdx: map[int]*accTool{}}
}

func (a *toolCallAccumulator) add(idx int, id, name, argsFragment string) {
	t, ok := a.byIdx[idx]
	if !ok {
		t = &accTool{}
		a.byIdx[idx] = t
		a.order = append(a.order, idx)
	}
	if id != "" {
		t.id = id
	}
	if name != "" {
		t.name = name
	}
	if argsFragment != "" {
		t.args.WriteString(argsFragment)
	}
}

// finalize возвращает собранные tool-calls в порядке появления.
func (a *toolCallAccumulator) finalize(prefix string) []ToolCall {
	out := make([]ToolCall, 0, len(a.order))
	for i, idx := range a.order {
		t := a.byIdx[idx]
		if t.name == "" {
			continue
		}
		id := t.id
		if id == "" {
			id = toolCallID(prefix, i)
		}
		out = append(out, ToolCall{
			ID:        id,
			Name:      t.name,
			Arguments: parseArgs(t.args.String()),
		})
	}
	return out
}
