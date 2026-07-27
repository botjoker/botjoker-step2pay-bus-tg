package agentstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/botjoker/sambacrm-business-tg/internal/api"
	"github.com/botjoker/sambacrm-business-tg/internal/llm"
	"github.com/botjoker/sambacrm-business-tg/internal/runtime"
	"github.com/botjoker/sambacrm-business-tg/internal/storage"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

var _ api.Engine = (*Engine)(nil)

// EventSink — публикатор событий диалога (реализуется api.SSEHub).
type EventSink interface {
	PublishEvent(ctx context.Context, convID uuid.UUID, evType, text, tool, errStr string) error
}

// ProviderFactory строит LLM-провайдера и конфигурацию агента по agentID
// (включая выбор модели и расшифровку tenant-креденшелов). Реализуется в
// cmd/agent (зависит от AGENT_SECRETS_KEY и таблицы credentials).
type ProviderFactory func(ctx context.Context, agentID uuid.UUID) (llm.LLMProvider, runtime.AgentConfig, error)

// IngestTrigger ставит/запускает индексацию источника знаний.
type IngestTrigger func(ctx context.Context, sourceID, profileID uuid.UUID) error

// Engine — sqlc/runtime-backed реализация api.Engine.
type Engine struct {
	q       *storage.Queries
	sink    EventSink
	factory ProviderFactory
	ingest  IngestTrigger
	opProxy *runtime.OperatorProxy
	deps    []runtime.AgentOption // общие зависимости (recorder/memory/intake/tools/...)

	secretsKey string                                      // AGENT_SECRETS_KEY для расшифровки токенов каналов
	postTurn   func(ctx context.Context, convID uuid.UUID) // дебаунс-триггер insights после хода
	formLink   func(convID uuid.UUID) string
}

// SetSecretsKey задаёт AGENT_SECRETS_KEY для расшифровки токенов каналов.
func (e *Engine) SetSecretsKey(key string) { e.secretsKey = key }

// SetPostTurn регистрирует колбэк, вызываемый после завершения хода реального
// диалога (не playground). Используется для дебаунс-постановки insights-задачи.
func (e *Engine) SetPostTurn(fn func(ctx context.Context, convID uuid.UUID)) { e.postTurn = fn }

// SetIntakeFormLink подключает channel-neutral генератор защищённой ссылки.
func (e *Engine) SetIntakeFormLink(fn func(convID uuid.UUID) string) { e.formLink = fn }

// firePostTurn безопасно вызывает postTurn-колбэк, если он задан.
func (e *Engine) firePostTurn(ctx context.Context, convID uuid.UUID) {
	if e.postTurn != nil {
		e.postTurn(ctx, convID)
	}
}

// recordGreeting вставляет приветствие агента первым assistant-сообщением нового
// диалога (если приветствие задано). Best-effort — ошибка не валит создание диалога.
func (e *Engine) recordGreeting(ctx context.Context, profileID, agentID, convID pgtype.UUID) {
	greeting, err := e.q.GetAgentGreeting(ctx, agentID)
	if err != nil || !greeting.Valid || strings.TrimSpace(greeting.String) == "" {
		return
	}
	if err := e.q.InsertGreetingMessage(ctx, storage.InsertGreetingMessageParams{
		ProfileID:      profileID,
		ConversationID: convID,
		Content:        greeting,
	}); err != nil {
		slog.Warn("greeting insert failed", "conversation", fromUUID(convID), "err", err)
	}
}

// NewEngine собирает движок. deps — общие AgentOption (recorder, memory, intake,
// takeover, billing, tools, pii, rag), которые применяются к каждому агенту.
func NewEngine(q *storage.Queries, sink EventSink, factory ProviderFactory, ingest IngestTrigger, opProxy *runtime.OperatorProxy, deps ...runtime.AgentOption) *Engine {
	return &Engine{q: q, sink: sink, factory: factory, ingest: ingest, opProxy: opProxy, deps: deps}
}

// StartConversation резолвит канал по web_slug и возвращает/создаёт диалог.
func (e *Engine) StartConversation(ctx context.Context, webSlug, externalUserID string) (api.StartResult, error) {
	ch, err := e.q.GetWebChannelBySlug(ctx, toText(webSlug))
	if err != nil {
		return api.StartResult{}, err
	}

	conv, err := e.q.GetAgentConversationByExternal(ctx, storage.GetAgentConversationByExternalParams{
		AgentID:        ch.AgentID,
		ChannelID:      ch.ID,
		ExternalUserID: externalUserID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		conv, err = e.q.CreateAgentConversation(ctx, storage.CreateAgentConversationParams{
			ProfileID:      ch.ProfileID,
			AgentID:        ch.AgentID,
			ChannelID:      ch.ID,
			ExternalUserID: externalUserID,
		})
		if err == nil {
			e.recordGreeting(ctx, conv.ProfileID, conv.AgentID, conv.ID)
		}
	}
	if err != nil {
		return api.StartResult{}, err
	}

	return api.StartResult{
		ConversationID: fromUUID(conv.ID),
		ProfileID:      fromUUID(conv.ProfileID),
		AgentID:        fromUUID(conv.AgentID),
	}, nil
}

// HandleMessage запускает агента для диалога и публикует события в SSE.
func (e *Engine) HandleMessage(ctx context.Context, conversationID uuid.UUID, text string) error {
	conv, err := e.q.GetAgentConversation(ctx, toUUID(conversationID))
	if err != nil {
		return err
	}
	agent, err := e.buildAgent(ctx, fromUUID(conv.AgentID))
	if err != nil {
		return err
	}
	events, err := agent.Run(ctx, runtime.RunRequest{ConversationID: conversationID, UserMessage: text})
	if err != nil {
		return err
	}
	for ev := range events {
		e.publish(ctx, conversationID, ev)
	}
	e.firePostTurn(ctx, conversationID)
	return nil
}

// IntakeForm строится только из tenant-owned схемы и фактов текущего диалога.
func (e *Engine) IntakeForm(ctx context.Context, conversationID uuid.UUID) ([]api.IntakeFormField, error) {
	conv, err := e.q.GetAgentConversation(ctx, toUUID(conversationID))
	if err != nil {
		return nil, err
	}
	fields, err := e.q.ListIntakeFields(ctx, conv.AgentID)
	if err != nil {
		return nil, err
	}
	facts, err := e.q.ListConversationFacts(ctx, conv.ID)
	if err != nil {
		return nil, err
	}
	have := make(map[string]bool, len(facts))
	for _, fact := range facts {
		have[fact.FieldKey] = true
	}
	out := make([]api.IntakeFormField, 0, len(fields))
	for _, field := range fields {
		if have[field.FieldKey] {
			continue
		}
		out = append(out, api.IntakeFormField{
			Key:      field.FieldKey,
			Label:    field.FieldLabel,
			Type:     field.FieldType,
			Required: field.IsRequired,
			Why:      fromText(field.WhyWeAsk),
			Options:  parseEnumOptions(field.FieldOptions),
		})
	}
	return out, nil
}

// ConsentDocument возвращает tenant-specific редакцию или безопасный fallback.
func (e *Engine) ConsentDocument(ctx context.Context, conversationID uuid.UUID) (api.ConsentDocument, error) {
	conv, err := e.q.GetAgentConversation(ctx, toUUID(conversationID))
	if err != nil {
		return api.ConsentDocument{}, err
	}
	template, err := e.q.ActivePDConsentTemplate(ctx, conv.ProfileID)
	if err == nil {
		return api.ConsentDocument{
			Title:      template.Title,
			Body:       template.BodyMd,
			Version:    template.Version,
			TemplateID: fromUUID(template.ID),
		}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return api.ConsentDocument{}, err
	}
	return api.ConsentDocument{
		Title:   "Согласие на обработку персональных данных",
		Version: "pd-processing-v1",
		Body: "Я свободно, своей волей и в своём интересе даю согласие на обработку предоставленных мной персональных данных для обработки обращения, обратной связи и оказания запрошенных услуг.\n\n" +
			"Обработка может включать сбор, запись, систематизацию, хранение, уточнение, использование и удаление данных. Согласие действует до достижения целей обработки или его отзыва. Отозвать согласие можно, обратившись к владельцу сервиса.",
	}, nil
}

// SubmitIntake валидирует весь payload и фиксирует явное согласие. Контактные
// значения сохраняются в БД и не превращаются в user-message или LLM prompt.
func (e *Engine) SubmitIntake(ctx context.Context, conversationID uuid.UUID, submission api.IntakeSubmission) error {
	values := submission.Values
	if !submission.ConsentGranted {
		return &api.IntakeValidationError{Message: "подтвердите согласие на обработку персональных данных"}
	}
	if len(values) > 100 {
		return &api.IntakeValidationError{Message: "слишком много полей"}
	}
	conv, err := e.q.GetAgentConversation(ctx, toUUID(conversationID))
	if err != nil {
		return err
	}
	fields, err := e.q.ListIntakeFields(ctx, conv.AgentID)
	if err != nil {
		return err
	}
	existingFacts, err := e.q.ListConversationFacts(ctx, conv.ID)
	if err != nil {
		return err
	}
	have := make(map[string]bool, len(existingFacts))
	for _, fact := range existingFacts {
		have[fact.FieldKey] = true
	}
	byKey := make(map[string]storage.AgentIntakeField, len(fields))
	for _, field := range fields {
		byKey[field.FieldKey] = field
	}
	type normalizedValue struct {
		field storage.AgentIntakeField
		value string
	}
	normalized := make([]normalizedValue, 0, len(values))
	for key, raw := range values {
		field, ok := byKey[key]
		if !ok {
			return &api.IntakeValidationError{Message: fmt.Sprintf("неизвестное поле: %s", key)}
		}
		if have[key] {
			continue // устаревшая/повторно открытая форма не перезаписывает факт
		}
		rawValue := valueToString(raw)
		if strings.TrimSpace(rawValue) == "" && !field.IsRequired {
			continue
		}
		value, validationErr := validateAndNormalize(rawValue, &field)
		if validationErr != nil {
			return &api.IntakeValidationError{Message: fmt.Sprintf("%s: %s", field.FieldLabel, validationErr)}
		}
		normalized = append(normalized, normalizedValue{field: field, value: value})
	}
	for _, field := range fields {
		if !field.IsRequired || have[field.FieldKey] {
			continue
		}
		if _, submitted := values[field.FieldKey]; submitted {
			continue
		}
		return &api.IntakeValidationError{Message: fmt.Sprintf("заполните обязательное поле: %s", field.FieldLabel)}
	}
	for _, item := range normalized {
		valueJSON, marshalErr := json.Marshal(item.value)
		if marshalErr != nil {
			return marshalErr
		}
		if err := e.q.DeactivateActiveFact(ctx, storage.DeactivateActiveFactParams{
			ConversationID: conv.ID,
			FieldKey:       item.field.FieldKey,
		}); err != nil {
			return err
		}
		if _, err := e.q.InsertConversationFact(ctx, storage.InsertConversationFactParams{
			ProfileID:       conv.ProfileID,
			ConversationID:  conv.ID,
			IntakeFieldID:   item.field.ID,
			FieldKey:        item.field.FieldKey,
			FieldValue:      valueJSON,
			Confidence:      toNumeric(1),
			SourceMessageID: pgtype.UUID{},
			SourceExcerpt:   toText("secure form"),
		}); err != nil {
			return err
		}
		if err := e.q.VerifyFact(ctx, storage.VerifyFactParams{
			ConversationID: conv.ID,
			FieldKey:       item.field.FieldKey,
		}); err != nil {
			return err
		}
	}
	consent, err := e.ConsentDocument(ctx, conversationID)
	if err != nil {
		return err
	}
	if err := e.q.RecordPDConsent(ctx, storage.RecordPDConsentParams{
		ProfileID:      conv.ProfileID,
		ConversationID: conv.ID,
		TemplateID:     toUUIDOrNull(consent.TemplateID),
		Version:        consent.Version,
		TextSnapshot:   consent.Body,
		UserAgent:      submission.UserAgent,
	}); err != nil {
		return err
	}
	return nil
}

// Test — playground: возвращает канал событий напрямую (api стримит его в SSE).
func (e *Engine) Test(ctx context.Context, agentID uuid.UUID, message string) (<-chan llm.StreamEvent, error) {
	agent, err := e.buildAgent(ctx, agentID)
	if err != nil {
		return nil, err
	}
	// playground: эфемерный диалог без записи транспорта.
	return agent.Run(ctx, runtime.RunRequest{ConversationID: uuid.New(), UserMessage: message})
}

// StartChannelConversation резолвит канал по id и возвращает/создаёт диалог
// для внешнего пользователя транспорта (Telegram/VK/...).
func (e *Engine) StartChannelConversation(ctx context.Context, channelID uuid.UUID, externalUserID, externalChatID string) (convID, agentID, profileID uuid.UUID, err error) {
	ch, err := e.q.GetChannel(ctx, toUUID(channelID))
	if err != nil {
		return uuid.Nil, uuid.Nil, uuid.Nil, err
	}
	conv, err := e.q.GetAgentConversationByExternal(ctx, storage.GetAgentConversationByExternalParams{
		AgentID:        ch.AgentID,
		ChannelID:      ch.ID,
		ExternalUserID: externalUserID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		conv, err = e.q.CreateAgentConversation(ctx, storage.CreateAgentConversationParams{
			ProfileID:      ch.ProfileID,
			AgentID:        ch.AgentID,
			ChannelID:      ch.ID,
			ExternalUserID: externalUserID,
			ExternalChatID: toText(externalChatID),
		})
		if err == nil {
			e.recordGreeting(ctx, conv.ProfileID, conv.AgentID, conv.ID)
		}
	}
	if err != nil {
		return uuid.Nil, uuid.Nil, uuid.Nil, err
	}
	return fromUUID(conv.ID), fromUUID(conv.AgentID), fromUUID(conv.ProfileID), nil
}

// RunConversation строит агента для диалога и возвращает поток событий
// (транспорт сам стримит его пользователю).
func (e *Engine) RunConversation(ctx context.Context, convID uuid.UUID, text string, attachments []llm.Attachment) (<-chan llm.StreamEvent, error) {
	conv, err := e.q.GetAgentConversation(ctx, toUUID(convID))
	if err != nil {
		return nil, err
	}
	agent, err := e.buildAgent(ctx, fromUUID(conv.AgentID))
	if err != nil {
		return nil, err
	}
	raw, err := agent.Run(ctx, runtime.RunRequest{ConversationID: convID, UserMessage: text, Attachments: attachments})
	if err != nil {
		return nil, err
	}
	// Прокладка: добавляем ссылку к request_form для TG/VK и по завершении хода
	// дёргаем postTurn-триггер. Сама ссылка генерируется без участия LLM.
	// Контекст хода может отмениться после стрима — для триггера берём фоновый.
	out := make(chan llm.StreamEvent, 32)
	go func() {
		defer close(out)
		for ev := range raw {
			if ev.Type == llm.EventToolCall && ev.ToolCall != nil && ev.ToolCall.Name == "request_form" && e.formLink != nil {
				// Не мутируем исходный tool-call: он дальше попадёт в контекст LLM.
				// Защищённая ссылка предназначена только транспорту.
				transportCall := *ev.ToolCall
				transportCall.Arguments = make(map[string]any, len(ev.ToolCall.Arguments)+1)
				for key, value := range ev.ToolCall.Arguments {
					transportCall.Arguments[key] = value
				}
				transportCall.Arguments["secure_form_url"] = e.formLink(convID)
				ev.ToolCall = &transportCall
			}
			out <- ev
		}
		if e.postTurn != nil {
			e.firePostTurn(context.Background(), convID)
		}
	}()
	return out, nil
}

// ChannelInfo — канал транспорта с расшифрованным токеном.
type ChannelInfo struct {
	ChannelID uuid.UUID
	Token     string
}

// VKChannelInfo — VK-канал с токеном/секретом/группой.
type VKChannelInfo struct {
	ChannelID    uuid.UUID
	AccessToken  string
	SecretKey    string
	GroupID      int64
	Confirmation string // код подтверждения VK Callback (из БД, не ENV)
}

// ListVKChannels возвращает активные VK-каналы с расшифрованными токенами.
func (e *Engine) ListVKChannels(ctx context.Context) ([]VKChannelInfo, error) {
	rows, err := e.q.ListActiveChannelsByType(ctx, "vk")
	if err != nil {
		return nil, err
	}
	out := make([]VKChannelInfo, 0, len(rows))
	for _, ch := range rows {
		accessToken, err := decryptSecret(fromText(ch.VkAccessToken), e.secretsKey)
		if err != nil {
			slog.Warn("vk channel: cannot decrypt access token, skipping",
				"channel", fromUUID(ch.ID), "err", err)
			continue
		}
		secretKey, err := decryptSecret(fromText(ch.VkSecretKey), e.secretsKey)
		if err != nil {
			slog.Warn("vk channel: cannot decrypt secret key, skipping",
				"channel", fromUUID(ch.ID), "err", err)
			continue
		}
		info := VKChannelInfo{
			ChannelID:   fromUUID(ch.ID),
			AccessToken: accessToken,
			SecretKey:   secretKey,
		}
		if ch.VkGroupID.Valid {
			info.GroupID = ch.VkGroupID.Int64
		}
		info.Confirmation = fromText(ch.VkConfirmation)
		out = append(out, info)
	}
	return out, nil
}

// GetVKChannel возвращает один VK-канал по id с расшифрованными токенами.
// Возвращает (nil, nil), если канал удалён/неактивен/не VK. Используется
// hot-reload'ом каналов из backend'а (POST /internal/agents/channels/reload).
func (e *Engine) GetVKChannel(ctx context.Context, channelID uuid.UUID) (*VKChannelInfo, error) {
	ch, err := e.q.GetChannel(ctx, toUUID(channelID))
	if err != nil {
		return nil, err
	}
	if !ch.IsActive || ch.ChannelType != "vk" {
		return nil, nil
	}
	accessToken, err := decryptSecret(fromText(ch.VkAccessToken), e.secretsKey)
	if err != nil {
		return nil, err
	}
	secretKey, err := decryptSecret(fromText(ch.VkSecretKey), e.secretsKey)
	if err != nil {
		return nil, err
	}
	info := &VKChannelInfo{
		ChannelID:    channelID,
		AccessToken:  accessToken,
		SecretKey:    secretKey,
		Confirmation: fromText(ch.VkConfirmation),
	}
	if ch.VkGroupID.Valid {
		info.GroupID = ch.VkGroupID.Int64
	}
	return info, nil
}

// GetTelegramChannel возвращает один Telegram-канал по id с расшифрованным токеном.
// Возвращает (nil, nil), если канал удалён/неактивен/не telegram.
func (e *Engine) GetTelegramChannel(ctx context.Context, channelID uuid.UUID) (*ChannelInfo, error) {
	ch, err := e.q.GetChannel(ctx, toUUID(channelID))
	if err != nil {
		return nil, err
	}
	if !ch.IsActive || ch.ChannelType != "telegram" {
		return nil, nil
	}
	tok, err := decryptSecret(fromText(ch.TgBotToken), e.secretsKey)
	if err != nil {
		return nil, err
	}
	return &ChannelInfo{ChannelID: channelID, Token: tok}, nil
}

// ListTelegramChannels возвращает активные Telegram-каналы с расшифрованными токенами.
func (e *Engine) ListTelegramChannels(ctx context.Context) ([]ChannelInfo, error) {
	rows, err := e.q.ListActiveChannelsByType(ctx, "telegram")
	if err != nil {
		return nil, err
	}
	out := make([]ChannelInfo, 0, len(rows))
	for _, ch := range rows {
		tok, err := decryptSecret(fromText(ch.TgBotToken), e.secretsKey)
		if err != nil {
			slog.Warn("telegram channel: cannot decrypt token, skipping",
				"channel", fromUUID(ch.ID), "err", err)
			continue // НЕ поднимаем бот с битым токеном
		}
		out = append(out, ChannelInfo{ChannelID: fromUUID(ch.ID), Token: tok})
	}
	return out, nil
}

// TriggerIngest проксирует к инжектированному триггеру.
func (e *Engine) TriggerIngest(ctx context.Context, sourceID, profileID uuid.UUID) error {
	if e.ingest == nil {
		return errors.New("ingest not configured")
	}
	return e.ingest(ctx, sourceID, profileID)
}

// OperatorMessage пересылает сообщение оператора через OperatorProxy.
// att != nil → к сообщению прикрепляется файл-вложение (документ, F1-1).
func (e *Engine) OperatorMessage(ctx context.Context, conversationID, operatorAccountID uuid.UUID, text, mode string, att *runtime.Attachment) error {
	if e.opProxy == nil {
		return errors.New("operator proxy not configured")
	}
	return e.opProxy.Handle(ctx, runtime.OperatorMessage{
		ConversationID:    conversationID,
		OperatorAccountID: operatorAccountID,
		Text:              text,
		Mode:              mode,
		Attachment:        att,
	})
}

// History возвращает видимую историю диалога для веб-виджета.
func (e *Engine) History(ctx context.Context, conversationID uuid.UUID, limit int) ([]api.ChatHistoryMessage, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := e.q.ChatHistory(ctx, storage.ChatHistoryParams{
		ConversationID: toUUID(conversationID),
		Limit:          int32(limit),
	})
	if err != nil {
		return nil, err
	}
	out := make([]api.ChatHistoryMessage, 0, len(rows))
	for _, r := range rows {
		out = append(out, api.ChatHistoryMessage{Role: r.Role, Content: fromText(r.Content)})
	}
	return out, nil
}

func (e *Engine) buildAgent(ctx context.Context, agentID uuid.UUID) (*runtime.Agent, error) {
	provider, cfg, err := e.factory(ctx, agentID)
	if err != nil {
		return nil, err
	}
	return runtime.NewAgent(cfg, provider, e.deps...), nil
}

func (e *Engine) publish(ctx context.Context, convID uuid.UUID, ev llm.StreamEvent) {
	if e.sink == nil {
		return
	}
	tool, errStr := "", ""
	if ev.ToolCall != nil {
		tool = ev.ToolCall.Name
	}
	if ev.Error != nil {
		errStr = ev.Error.Error()
	}
	_ = e.sink.PublishEvent(ctx, convID, ev.Type, ev.Text, tool, errStr)
}
