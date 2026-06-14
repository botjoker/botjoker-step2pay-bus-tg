package agentstore

import (
	"context"
	"fmt"
	"strings"

	"github.com/botjoker/sambacrm-business-tg/internal/llm"
	"github.com/botjoker/sambacrm-business-tg/internal/storage"
	"github.com/google/uuid"
)

const summarizeThreshold = 30 // сообщений

const summarizePrompt = `Ты — суммаризатор диалога. Сделай 2-3 предложения о ключевых моментах:
кто пишет, какая тема/проблема, какие факты уже выяснены, что осталось.
Пиши на русском. Только summary, без преамбулы.

История:
%s

Summary:`

// MaybeSummarize генерирует summary, если диалог превысил порог. summarizer —
// дешёвый провайдер на ПЛАТФОРМЕННЫХ ключах (не тенантских), чтобы инфра-задача
// не тратила тенантский бюджет. Вызывается из wiring после ответа (03D).
func (m *DBMemory) MaybeSummarize(ctx context.Context, convID uuid.UUID, summarizer llm.LLMProvider, model string) error {
	count, err := m.q.CountConversationMessages(ctx, toUUID(convID))
	if err != nil {
		return err
	}
	if count <= summarizeThreshold {
		return nil
	}

	raw, err := m.q.LastMessages(ctx, storage.LastMessagesParams{
		ConversationID: toUUID(convID),
		Limit:          summarizeThreshold,
	})
	if err != nil {
		return err
	}

	var b strings.Builder
	for i := len(raw) - 1; i >= 0; i-- {
		r := raw[i]
		b.WriteString(r.Role)
		b.WriteString(": ")
		b.WriteString(fromText(r.Content))
		b.WriteString("\n")
	}

	res, err := summarizer.Complete(ctx, llm.CompleteRequest{
		Model:     model,
		Messages:  []llm.Message{{Role: llm.RoleUser, Content: fmt.Sprintf(summarizePrompt, b.String())}},
		MaxTokens: 256,
	})
	if err != nil {
		return err
	}

	return m.q.UpdateConversationSummary(ctx, storage.UpdateConversationSummaryParams{
		ID:      toUUID(convID),
		Summary: toText(strings.TrimSpace(res.Content)),
	})
}
