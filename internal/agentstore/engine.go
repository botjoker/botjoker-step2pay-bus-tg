package agentstore

import (
	"context"
	"errors"
	"log/slog"

	"github.com/botjoker/sambacrm-business-tg/internal/api"
	"github.com/botjoker/sambacrm-business-tg/internal/llm"
	"github.com/botjoker/sambacrm-business-tg/internal/runtime"
	"github.com/botjoker/sambacrm-business-tg/internal/storage"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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
	q        *storage.Queries
	sink     EventSink
	factory  ProviderFactory
	ingest   IngestTrigger
	opProxy  *runtime.OperatorProxy
	deps     []runtime.AgentOption // общие зависимости (recorder/memory/intake/tools/...)

	secretsKey string // AGENT_SECRETS_KEY для расшифровки токенов каналов
}

// SetSecretsKey задаёт AGENT_SECRETS_KEY для расшифровки токенов каналов.
func (e *Engine) SetSecretsKey(key string) { e.secretsKey = key }

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
	return agent.Run(ctx, runtime.RunRequest{ConversationID: convID, UserMessage: text, Attachments: attachments})
}

// ChannelInfo — канал транспорта с расшифрованным токеном.
type ChannelInfo struct {
	ChannelID uuid.UUID
	Token     string
}

// VKChannelInfo — VK-канал с токеном/секретом/группой.
type VKChannelInfo struct {
	ChannelID   uuid.UUID
	AccessToken string
	SecretKey   string
	GroupID     int64
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
		out = append(out, info)
	}
	return out, nil
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
func (e *Engine) OperatorMessage(ctx context.Context, conversationID, operatorAccountID uuid.UUID, text, mode string) error {
	if e.opProxy == nil {
		return errors.New("operator proxy not configured")
	}
	return e.opProxy.Handle(ctx, runtime.OperatorMessage{
		ConversationID:    conversationID,
		OperatorAccountID: operatorAccountID,
		Text:              text,
		Mode:              mode,
	})
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
