package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/botjoker/sambacrm-business-tg/internal/llm"
	"github.com/botjoker/sambacrm-business-tg/internal/safety"
	"github.com/google/uuid"
)

const intakeCollectionNotice = "Отлично! Оставьте контакт, чтобы менеджер мог связаться с вами."

// Agent — исполняемый экземпляр агента: конфигурация + LLM-провайдер + набор
// инжектируемых зависимостей. Создаётся через NewAgent (factory.go).
type Agent struct {
	cfg      AgentConfig
	provider llm.LLMProvider

	tools     ToolRegistry
	rag       RAGClient
	pii       PIIClient
	intake    IntakeStore
	billing   BillingTracker
	memory    Memory
	takeover  TakeoverGate
	recorder  MessageRecorder
	fewShot   FewShotStore
	extractor FactExtractor
	logger    *slog.Logger
}

// Run — главный entrypoint. Возвращает канал StreamEvent для транспорта.
func (a *Agent) Run(ctx context.Context, req RunRequest) (<-chan llm.StreamEvent, error) {
	out := make(chan llm.StreamEvent, 32)
	go func() {
		defer close(out)
		a.run(ctx, req, out)
	}()
	return out, nil
}

func (a *Agent) run(ctx context.Context, req RunRequest, out chan<- llm.StreamEvent) {
	convID := req.ConversationID

	// 0. Hard-cap по тарифу.
	if hit, err := a.billing.IsHardCapHit(ctx, a.cfg.ProfileID); err == nil && hit {
		a.emitText(out, "Лимит сообщений по тарифу исчерпан. Свяжитесь с владельцем чата.")
		return
	}

	// 0a. Live takeover — если оператор активен, рантайм молчит.
	if active, err := a.takeover.IsActive(ctx, convID); err == nil && active {
		a.logger.Info("takeover active, runtime silent", "conversation", convID)
		return
	}

	// 1. PII-редакция входящего.
	redactedUser, redactionLog, err := a.pii.Redact(ctx, req.UserMessage)
	if err != nil {
		// Контакты и другие PII нельзя передавать модели даже при отказе
		// редактора. Ошибка безопаснее пропущенного сообщения.
		a.logger.Error("pii redaction failed", "conversation", convID, "err", err)
		a.emitText(out, "Не удалось безопасно обработать сообщение. Попробуйте ещё раз.")
		return
	}

	// 1a. Prompt-injection эвристика. Не блокируем сообщение — лишь помечаем,
	// чтобы усилить sandwich-defense в system-промпте и оставить след в логах.
	injection := safety.Detect(req.UserMessage)
	if injection.Suspicious {
		a.logger.Warn("prompt injection suspected",
			"conversation", convID,
			"score", injection.Score,
			"patterns", injection.Patterns,
		)
	}

	// 2. Сохранить user-message.
	userMsgID, err := a.recorder.Record(ctx, RecordedMessage{
		ConversationID:   convID,
		ProfileID:        a.cfg.ProfileID,
		Role:             "user",
		Content:          redactedUser,
		ContentOriginal:  req.UserMessage,
		RedactionApplied: redactionLog != nil,
		RedactionLog:     redactionLog,
	})
	if err != nil {
		a.logger.Warn("record user message failed", "err", err)
	}

	// 2a. «Заведомый захват» контактов: если PII-редактор нашёл телефон/email —
	// сразу пишем факт в поле опросника с соответствующим field_type. Работает
	// даже если основной LLM в этом же цикле не вызовет record_intake_fact.
	// Best-effort: ошибка не валит обработку сообщения.
	if redactionLog != nil {
		if err := a.intake.CaptureFromRedaction(ctx, ContactCaptureRequest{
			ProfileID:       a.cfg.ProfileID,
			AgentID:         a.cfg.AgentID,
			ConversationID:  convID,
			SourceMessageID: userMsgID,
			RedactionLog:    redactionLog,
		}); err != nil {
			a.logger.Warn("capture from redaction failed", "err", err)
		}
	}

	// 2b. Inline fact extractor: короткий прицельный вызов дешёвой модели по
	// последним сообщениям — вытаскивает то, что LLM в основном цикле мог
	// пропустить. Skip внутри, если все обязательные поля уже собраны.
	// До шага 5, чтобы новые факты попали в <intake>-блок этого же цикла.
	if err := a.extractor.ExtractInline(ctx, ExtractInlineRequest{
		AgentID:        a.cfg.AgentID,
		ConversationID: convID,
		ProfileID:      a.cfg.ProfileID,
		ModelOverride:  a.cfg.ExtractorModel,
	}); err != nil {
		a.logger.Debug("inline extractor failed", "err", err)
	}

	// 3. Определение языка.
	userLang := a.detectLanguage(req.UserMessage)

	// 4. RAG (если включён).
	var chunks []RAGChunk
	if a.cfg.RagEnabled {
		// Дешёвые модели (DeepSeek/gpt-4o-mini) хуже удерживают внимание на длинном
		// контексте — top-3 даёт лучшее качество, чем top-10 с шумом.
		topK := a.cfg.RagTopK
		if topK <= 0 {
			topK = 3
		}
		chunks, _ = a.rag.Search(ctx, RAGSearchRequest{
			ProfileID: a.cfg.ProfileID,
			AgentID:   a.cfg.AgentID,
			Query:     redactedUser,
			TopK:      topK,
			MinScore:  a.cfg.RagMinScore,
		})
	}

	// 5. Опросник: схема + собранные факты + cross-conversation memory.
	intakeSchema, _ := a.intake.LoadSchema(ctx, a.cfg.AgentID)
	facts, _ := a.intake.LoadFacts(ctx, convID)
	prevFacts, _ := a.intake.LoadPreviousFacts(ctx, a.cfg.AgentID, convID)
	intakeCompleted, _ := a.intake.IsCompleted(ctx, convID)
	intakeBlock := renderIntakeBlock(intakeSchema, facts, prevFacts, intakeCompleted)

	// 6. Few-shot примеры.
	fewShot, _ := a.fewShot.Load(ctx, a.cfg.AgentID)

	// 7. История диалога.
	history := req.HistoryOverride
	if history != nil {
		// Playground присылает историю из браузера, поэтому пользовательские
		// реплики редактируем так же, как сообщения из обычного диалога.
		safeHistory := make([]llm.Message, 0, len(history))
		for _, msg := range history {
			if msg.Role == llm.RoleUser {
				redacted, _, err := a.pii.Redact(ctx, msg.Content)
				if err != nil {
					a.logger.Warn("pii redaction failed for playground history", "err", err)
					continue
				}
				msg.Content = redacted
			}
			safeHistory = append(safeHistory, msg)
		}
		history = safeHistory
	} else {
		history, _ = a.memory.Load(ctx, convID)
	}

	// 8. Tool-use схемы (нужны до сборки промпта — определяют, инъектить ли
	//    перечень платных документов).
	tools, _ := a.tools.SchemasFor(ctx, a.cfg.AgentID)
	if intakeCompleted {
		tools = withoutTool(tools, "request_form")
	}

	// 9. Сборка messages. Вложения пробрасываем только если у агента включено зрение.
	attach := req.Attachments
	if !a.cfg.VisionEnabled {
		attach = nil
	}
	// Перечень платных документов — только если tool sell_document реально включён.
	var sellableDocs []SellableDoc
	if hasTool(tools, "sell_document") {
		sellableDocs = a.cfg.SellableDocs
	}
	msgs := a.buildMessages(intakeBlock, chunks, fewShot, history, redactedUser, attach, userLang, sellableDocs, injection.Suspicious, redactionLog != nil)

	maxIter := a.cfg.MaxIterations
	if maxIter <= 0 {
		// Дешёвые модели склонны к «зомби-циклам» (повторные одинаковые tool-calls).
		// 3 итерации достаточно для сценария: think → tool → reply. Более сложные
		// цепочки — через explicit MaxIterations в конфиге агента.
		maxIter = 3
	}

	for iter := 0; iter < maxIter; iter++ {
		start := time.Now()
		stream, err := a.provider.Stream(ctx, llm.StreamRequest{
			Model:       a.cfg.LLMModel,
			Messages:    msgs,
			Tools:       tools,
			Temperature: a.cfg.Temperature,
			MaxTokens:   a.cfg.MaxTokens,
		})
		if err != nil {
			a.emitError(out, err)
			return
		}

		assistantText, toolCalls, tokensIn, tokensOut := drainStream(stream, out)
		latencyMs := time.Since(start).Milliseconds()
		formRequested := hasToolCall(toolCalls, "request_form")
		if formRequested {
			// Сохраняем в истории тот же короткий текст, который показывает интерфейс,
			// вместо произвольной подводки модели перед вызовом защищённой формы.
			assistantText = intakeCollectionNotice
		}

		// Записать assistant-message.
		assistantMsgID, _ := a.recorder.Record(ctx, RecordedMessage{
			ConversationID:   convID,
			ProfileID:        a.cfg.ProfileID,
			Role:             "assistant",
			Content:          assistantText,
			ToolCalls:        toolCalls,
			TokensIn:         tokensIn,
			TokensOut:        tokensOut,
			LatencyMs:        latencyMs,
			LLMModel:         a.cfg.LLMModel,
			LLMProvider:      a.provider.Name(),
			ResponseLanguage: userLang,
		})

		// Биллинг-тик.
		_ = a.billing.Track(ctx, BillingDelta{
			ProfileID: a.cfg.ProfileID,
			Messages:  1,
			TokensIn:  int64(tokensIn),
			TokensOut: int64(tokensOut),
		})

		// Нет tool-calls → финальный ответ.
		if len(toolCalls) == 0 {
			out <- llm.StreamEvent{Type: llm.EventDone, TokensIn: tokensIn, TokensOut: tokensOut}
			return
		}

		// Выполнить tool-calls и добавить результаты в контекст.
		results := a.execTools(ctx, convID, assistantMsgID, toolCalls, out)
		if formRequested {
			// Форма является финальным действием хода. Дополнительная итерация модели
			// создавала второе сообщение после уже показанной формы.
			out <- llm.StreamEvent{Type: llm.EventDone, TokensIn: tokensIn, TokensOut: tokensOut}
			return
		}
		msgs = appendAssistantWithTools(msgs, assistantText, toolCalls)
		msgs = appendToolResults(msgs, results)
	}

	a.emitError(out, fmt.Errorf("max iterations (%d) exceeded", maxIter))
}

func hasToolCall(calls []llm.ToolCall, name string) bool {
	for i := range calls {
		if calls[i].Name == name {
			return true
		}
	}
	return false
}

// execTools выполняет все tool-calls последовательно, эмитит tool_call события,
// возвращает сообщения с результатами (role=tool) для следующей итерации.
func (a *Agent) execTools(ctx context.Context, convID, msgID uuid.UUID, calls []llm.ToolCall, out chan<- llm.StreamEvent) []llm.Message {
	results := make([]llm.Message, 0, len(calls))
	for i := range calls {
		tc := calls[i]
		out <- llm.StreamEvent{Type: llm.EventToolCall, ToolCall: &tc}

		res, err := a.tools.Execute(ctx, ToolExecCtx{
			ProfileID:      a.cfg.ProfileID,
			AgentID:        a.cfg.AgentID,
			ConversationID: convID,
			MessageID:      msgID,
		}, tc.Name, tc.Arguments)
		if err != nil {
			res = map[string]any{"error": err.Error()}
		}

		results = append(results, llm.Message{
			Role:       llm.RoleTool,
			ToolCallID: tc.ID,
			Name:       tc.Name,
			Content:    marshalToolResult(res),
		})
	}
	return results
}

func (a *Agent) emitText(out chan<- llm.StreamEvent, t string) {
	out <- llm.StreamEvent{Type: llm.EventText, Text: t}
	out <- llm.StreamEvent{Type: llm.EventDone}
}

func (a *Agent) emitError(out chan<- llm.StreamEvent, err error) {
	a.logger.Error("agent run error", "err", err)
	out <- llm.StreamEvent{Type: llm.EventError, Error: err}
}

// drainStream потребляет события LLM, реэмитит текст наружу, накапливает
// tool_calls и токены. Возвращает (текст, tool_calls, tokensIn, tokensOut).
func drainStream(stream <-chan llm.StreamEvent, out chan<- llm.StreamEvent) (string, []llm.ToolCall, int, int) {
	var text string
	var calls []llm.ToolCall
	var ti, to int

	for ev := range stream {
		switch ev.Type {
		case llm.EventText:
			text += ev.Text
			out <- ev
		case llm.EventToolCall:
			if ev.ToolCall != nil {
				calls = append(calls, *ev.ToolCall)
			}
		case llm.EventDone:
			ti, to = ev.TokensIn, ev.TokensOut
		case llm.EventError:
			out <- ev
		}
	}
	return text, calls, ti, to
}
