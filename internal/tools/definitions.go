// Package tools — определения инструментов агента + remote-диспетчер к backend.
// Локальные tools и реестр на sqlc — в internal/agentstore (registry.go).
package tools

import "github.com/botjoker/sambacrm-business-tg/internal/llm"

// obj — хелпер для краткости JSON Schema.
func obj(props map[string]any, required ...string) map[string]any {
	m := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		m["required"] = required
	}
	return m
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
func intP(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}
func num(desc string) map[string]any { return map[string]any{"type": "number", "description": desc} }

// LocalToolNames — инструменты, выполняемые рантаймом (без HTTP к backend).
var LocalToolNames = map[string]bool{
	"record_intake_fact":     true,
	"confirm_intake_fact":    true,
	"get_intake_status":      true,
	"get_current_time":       true,
	"request_human_handover": true,
	"schedule_followup":      true,
	"cite_source":            true,
}

// Definitions — JSON Schema всех инструментов (для tool-calling LLM).
var Definitions = map[string]llm.ToolDef{
	// --- CRM (remote, через backend tool-exec) ---
	"search_customers": {
		Name:        "search_customers",
		Description: "Поиск клиентов по имени/email/телефону в CRM тенанта.",
		Schema: obj(map[string]any{
			"query": str("Поисковая строка: имя, email или телефон"),
			"limit": intP("Максимум результатов (1-50)"),
		}, "query"),
	},
	"create_customer": {
		Name:        "create_customer",
		Description: "Создать нового клиента в CRM.",
		Schema: obj(map[string]any{
			"name":  str("Имя клиента"),
			"phone": str("Телефон"),
			"email": str("Email"),
		}, "name"),
	},
	"find_appointment_slots": {
		Name:        "find_appointment_slots",
		Description: "Найти свободные слоты для записи на услугу/к специалисту.",
		Schema: obj(map[string]any{
			"service_id":    str("ID услуги"),
			"specialist_id": str("ID специалиста (опц.)"),
			"date_from":     str("Дата с (YYYY-MM-DD)"),
			"date_to":       str("Дата по (YYYY-MM-DD)"),
		}, "date_from"),
	},
	"book_appointment": {
		Name:        "book_appointment",
		Description: "Записать клиента на конкретный слот.",
		Schema: obj(map[string]any{
			"customer_id": str("ID клиента"),
			"slot":        str("Время слота (RFC3339)"),
			"service_id":  str("ID услуги"),
		}, "slot"),
	},
	"send_email_template": {
		Name:        "send_email_template",
		Description: "Отправить клиенту письмо по шаблону.",
		Schema: obj(map[string]any{
			"template_key": str("Ключ шаблона"),
			"customer_id":  str("ID клиента"),
		}, "template_key"),
	},
	"send_payment_link": {
		Name:        "send_payment_link",
		Description: "Сгенерировать и отправить клиенту ссылку на оплату.",
		Schema: obj(map[string]any{
			"amount":      num("Сумма"),
			"description": str("Назначение платежа"),
			"customer_id": str("ID клиента"),
		}, "amount"),
	},
	"sell_document": {
		Name: "sell_document",
		Description: "Оформить платный документ: создаёт счёт на оплату, после оплаты клиент " +
			"автоматически получает готовый PDF. Вызывай ТОЛЬКО когда клиент согласился купить " +
			"конкретный документ и собраны нужные данные (см. перечень доступных документов и " +
			"требуемые поля в системном промпте). Содержимое документа не сочиняется — это " +
			"подстановка собранных данных в выверенный шаблон тенанта.",
		Schema: obj(map[string]any{
			"template_id": str("ID шаблона документа из перечня доступных платных документов"),
			"variables": map[string]any{
				"type":                 "object",
				"description":          "Собранные значения для подстановки в шаблон (по ключам из описания документа). Можно опустить — недостающие подтянутся из фактов диалога.",
				"additionalProperties": true,
			},
		}, "template_id"),
	},
	"escalate_to_human": {
		Name:        "escalate_to_human",
		Description: "Эскалировать диалог на живого оператора, когда вопрос вне компетенции.",
		Schema: obj(map[string]any{
			"reason": str("Причина эскалации"),
		}, "reason"),
	},
	"request_form": {
		Name:        "request_form",
		Description: "Запросить у пользователя структурированную форму (например, заявку).",
		Schema: obj(map[string]any{
			"form_key": str("Ключ формы"),
		}, "form_key"),
	},
	"cite_source": {
		Name:        "cite_source",
		Description: "Указать источник из базы знаний, на котором основан ответ.",
		Schema: obj(map[string]any{
			"source_id": str("ID источника знаний"),
			"quote":     str("Цитата"),
		}, "source_id"),
	},
	"analyze_image": {
		Name:        "analyze_image",
		Description: "Проанализировать изображение (по media_id или image_url) согласно инструкции.",
		Schema: obj(map[string]any{
			"media_id":    str("ID медиа в системе (опц.)"),
			"image_url":   str("Прямой URL изображения (опц.)"),
			"instruction": str("Что нужно понять/прочитать на изображении"),
		}, "instruction"),
	},

	// --- Локальные (runtime) ---
	"record_intake_fact": {
		Name:        "record_intake_fact",
		Description: "Записать выясненный факт опросника. Вызывай, когда узнал значение поля из intake.",
		Schema: obj(map[string]any{
			"field_key":      str("Ключ поля из intake.missing или intake.collected"),
			"value":          map[string]any{"description": "Значение в соответствии с типом поля"},
			"source_excerpt": str("Точная цитата пользователя"),
			"confidence":     map[string]any{"type": "number", "minimum": 0, "maximum": 1},
		}, "field_key", "value"),
	},
	"confirm_intake_fact": {
		Name:        "confirm_intake_fact",
		Description: "Пометить факт как подтверждённый пользователем (is_verified=true).",
		Schema: obj(map[string]any{
			"field_key": str("Ключ поля"),
		}, "field_key"),
	},
	"get_intake_status": {
		Name:        "get_intake_status",
		Description: "Получить текущий статус опросника: собранные и недостающие поля.",
		Schema:      obj(map[string]any{}),
	},
	"get_current_time": {
		Name:        "get_current_time",
		Description: "Текущие дата и время (для расчётов «завтра», «через час» и т.п.).",
		Schema:      obj(map[string]any{}),
	},
	"request_human_handover": {
		Name:        "request_human_handover",
		Description: "Передать диалог человеку: помечает диалог как эскалированный.",
		Schema: obj(map[string]any{
			"reason": str("Кратко зачем нужен человек"),
		}),
	},
	"schedule_followup": {
		Name:        "schedule_followup",
		Description: "Запланировать сообщение пользователю на будущее время (напоминание/follow-up).",
		Schema: obj(map[string]any{
			"when":            str("Когда написать, в формате RFC3339 (напр. 2026-06-20T10:00:00Z)"),
			"message_to_user": str("Что напомнить/написать"),
		}, "when"),
	},
}
