package runtime

import (
	"fmt"
	"strings"

	"github.com/botjoker/sambacrm-business-tg/internal/llm"
)

// buildMessages собирает массив сообщений для LLM: system (persona + intake +
// RAG + few-shot + language) → история → текущее сообщение пользователя.
func (a *Agent) buildMessages(intake string, chunks []RAGChunk, fewShot []FewShotExample,
	history []llm.Message, userMsg string, attach []llm.Attachment, userLang string) []llm.Message {

	var sys strings.Builder
	sys.WriteString(a.cfg.Persona)
	sys.WriteString("\n\n")

	if intake != "" {
		sys.WriteString(intake)
		sys.WriteString("\n\n")
	}

	if len(chunks) > 0 {
		sys.WriteString("<retrieved_context>\n")
		for i, c := range chunks {
			sys.WriteString(fmt.Sprintf("[%d] %s\n%s\n\n", i+1, c.Source.Title, c.Content))
		}
		sys.WriteString("</retrieved_context>\n\n")
	}

	if len(fewShot) > 0 {
		sys.WriteString("<examples_from_owner>\n")
		for _, ex := range fewShot {
			sys.WriteString(fmt.Sprintf("User: %s\nAssistant: %s\n\n", ex.User, ex.Assistant))
		}
		sys.WriteString("</examples_from_owner>\n\n")
	}

	if userLang != "" && userLang != a.cfg.DefaultLang {
		sys.WriteString(fmt.Sprintf("<language>\nuser_language: %s\nОтвечай на этом языке, если не сказано иное.\n</language>\n", userLang))
	}

	msgs := make([]llm.Message, 0, len(history)+2)
	msgs = append(msgs, llm.Message{Role: llm.RoleSystem, Content: sys.String()})
	msgs = append(msgs, history...)
	msgs = append(msgs, llm.Message{Role: llm.RoleUser, Content: userMsg, Attachments: attach})
	return msgs
}

// renderIntakeBlock формирует <intake>-блок (см. AGENTS §4.5.1):
// собранные факты + недостающие required/optional поля + инструкция.
func renderIntakeBlock(fields []IntakeField, facts []Fact) string {
	if len(fields) == 0 {
		return ""
	}

	collected := make(map[string]Fact, len(facts))
	for _, f := range facts {
		collected[f.Key] = f
	}

	var b strings.Builder
	b.WriteString("<intake>\n")

	if len(collected) > 0 {
		b.WriteString("  <collected>\n")
		for _, f := range facts {
			verified := ""
			if f.Verified {
				verified = " (verified)"
			}
			b.WriteString(fmt.Sprintf("    - %s: %s%s\n", f.Key, f.Value, verified))
		}
		b.WriteString("  </collected>\n")
	}

	var missingRequired, missingOptional []IntakeField
	for _, fl := range fields {
		if _, ok := collected[fl.Key]; ok {
			continue
		}
		if fl.Required {
			missingRequired = append(missingRequired, fl)
		} else {
			missingOptional = append(missingOptional, fl)
		}
	}

	if len(missingRequired) > 0 {
		b.WriteString("  <missing required>\n")
		for _, fl := range missingRequired {
			b.WriteString(fmt.Sprintf("    - %s (%s)\n", fl.Key, fieldHint(fl)))
		}
		b.WriteString("  </missing>\n")
	}
	if len(missingOptional) > 0 {
		b.WriteString("  <missing optional>\n")
		for _, fl := range missingOptional {
			b.WriteString(fmt.Sprintf("    - %s (%s)\n", fl.Key, fieldHint(fl)))
		}
		b.WriteString("  </missing>\n")
	}

	b.WriteString("  <how_to_use>\n")
	b.WriteString("    Когда диалог естественно подойдёт, мягко выясни недостающие required-поля.\n")
	b.WriteString("    Не задавай больше 1–2 вопросов подряд. Не превращай в анкету.\n")
	b.WriteString("    Уже собранные факты НЕ переспрашивай.\n")
	b.WriteString("    Когда что-то узнаёшь — вызови record_intake_fact(field_key, value).\n")
	b.WriteString("  </how_to_use>\n")
	b.WriteString("</intake>")
	return b.String()
}

func fieldHint(f IntakeField) string {
	if f.WhyWeAsk != "" {
		return f.WhyWeAsk
	}
	if f.Label != "" {
		return f.Label
	}
	return f.Key
}
