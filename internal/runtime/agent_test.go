package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/botjoker/sambacrm-business-tg/internal/llm"
	"github.com/google/uuid"
)

// fakeProvider отдаёт заранее заготовленные стримы (по одному на итерацию).
type fakeProvider struct {
	scripts [][]llm.StreamEvent
	calls   int
}

func (f *fakeProvider) Name() string         { return "fake" }
func (f *fakeProvider) SupportsVision() bool  { return false }
func (f *fakeProvider) SupportsTools() bool   { return true }
func (f *fakeProvider) Complete(context.Context, llm.CompleteRequest) (*llm.CompleteResult, error) {
	return nil, nil
}
func (f *fakeProvider) Stream(_ context.Context, _ llm.StreamRequest) (<-chan llm.StreamEvent, error) {
	idx := f.calls
	if idx >= len(f.scripts) {
		idx = len(f.scripts) - 1
	}
	f.calls++
	ch := make(chan llm.StreamEvent, 8)
	go func() {
		defer close(ch)
		for _, ev := range f.scripts[idx] {
			ch <- ev
		}
	}()
	return ch, nil
}

// fakeTools — реестр с одним инструментом, Execute фиксирует вызовы.
type fakeTools struct {
	executed []string
}

func (t *fakeTools) SchemasFor(context.Context, uuid.UUID) ([]llm.ToolDef, error) {
	return []llm.ToolDef{{Name: "get_time", Description: "time", Schema: map[string]any{"type": "object"}}}, nil
}
func (t *fakeTools) Execute(_ context.Context, _ ToolExecCtx, name string, _ map[string]any) (map[string]any, error) {
	t.executed = append(t.executed, name)
	return map[string]any{"result": "12:00"}, nil
}

func collect(ch <-chan llm.StreamEvent) (text string, done bool, errs int) {
	var b strings.Builder
	for ev := range ch {
		switch ev.Type {
		case llm.EventText:
			b.WriteString(ev.Text)
		case llm.EventDone:
			done = true
		case llm.EventError:
			errs++
		}
	}
	return b.String(), done, errs
}

func baseCfg() AgentConfig {
	return AgentConfig{
		AgentID:       uuid.New(),
		ProfileID:     uuid.New(),
		Persona:       "Ты ассистент.",
		LLMModel:      "fake-model",
		MaxIterations: 4,
	}
}

func TestAgent_BasicFlow(t *testing.T) {
	prov := &fakeProvider{scripts: [][]llm.StreamEvent{
		{
			{Type: llm.EventText, Text: "Привет!"},
			{Type: llm.EventDone, TokensIn: 5, TokensOut: 2},
		},
	}}
	a := NewAgent(baseCfg(), prov)

	ch, err := a.Run(context.Background(), RunRequest{
		ConversationID: uuid.New(),
		UserMessage:    "привет",
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	text, done, errs := collect(ch)
	if text != "Привет!" {
		t.Errorf("text = %q", text)
	}
	if !done {
		t.Error("no done event")
	}
	if errs != 0 {
		t.Errorf("errors = %d", errs)
	}
	if prov.calls != 1 {
		t.Errorf("provider calls = %d, want 1", prov.calls)
	}
}

func TestAgent_ToolLoop(t *testing.T) {
	tc := llm.ToolCall{ID: "c1", Name: "get_time", Arguments: map[string]any{}}
	prov := &fakeProvider{scripts: [][]llm.StreamEvent{
		// итерация 1: запросить инструмент
		{
			{Type: llm.EventToolCall, ToolCall: &tc},
			{Type: llm.EventDone, TokensIn: 10, TokensOut: 3},
		},
		// итерация 2: финальный ответ
		{
			{Type: llm.EventText, Text: "Сейчас 12:00"},
			{Type: llm.EventDone, TokensIn: 12, TokensOut: 4},
		},
	}}
	tools := &fakeTools{}
	a := NewAgent(baseCfg(), prov, WithToolRegistry(tools))

	ch, _ := a.Run(context.Background(), RunRequest{
		ConversationID: uuid.New(),
		UserMessage:    "сколько времени?",
	})
	text, done, errs := collect(ch)

	if text != "Сейчас 12:00" {
		t.Errorf("text = %q", text)
	}
	if !done {
		t.Error("no done event")
	}
	if errs != 0 {
		t.Errorf("errors = %d", errs)
	}
	if len(tools.executed) != 1 || tools.executed[0] != "get_time" {
		t.Errorf("executed = %v", tools.executed)
	}
	if prov.calls != 2 {
		t.Errorf("provider calls = %d, want 2 (loop)", prov.calls)
	}
}

func TestAgent_HardCapBlocks(t *testing.T) {
	prov := &fakeProvider{scripts: [][]llm.StreamEvent{{{Type: llm.EventDone}}}}
	a := NewAgent(baseCfg(), prov, WithBilling(cappedBilling{}))

	ch, _ := a.Run(context.Background(), RunRequest{ConversationID: uuid.New(), UserMessage: "hi"})
	text, done, _ := collect(ch)
	if !strings.Contains(text, "Лимит") {
		t.Errorf("expected cap message, got %q", text)
	}
	if !done {
		t.Error("no done event")
	}
	if prov.calls != 0 {
		t.Errorf("provider must not be called on hard-cap, calls=%d", prov.calls)
	}
}

type cappedBilling struct{}

func (cappedBilling) IsHardCapHit(context.Context, uuid.UUID) (bool, error) { return true, nil }
func (cappedBilling) Track(context.Context, BillingDelta) error             { return nil }
