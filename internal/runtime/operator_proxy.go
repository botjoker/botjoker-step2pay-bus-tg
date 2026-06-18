package runtime

import (
	"context"
	"fmt"
	"sync"

	"github.com/google/uuid"
)

// OperatorMessage — событие от backend, когда оператор написал в чат (live takeover).
type OperatorMessage struct {
	ConversationID    uuid.UUID
	OperatorAccountID uuid.UUID
	Text              string
	Mode              string // 'silent' | 'open'
}

// TransportSender — отправка сообщения в конкретный канал. Реализуется каждым
// транспортом (telegram/vk/web). convID нужен web-транспорту (доставка по SSE-каналу
// диалога); telegram/vk адресуют по channelID+externalUserID и convID игнорируют.
type TransportSender interface {
	SendToConversation(ctx context.Context, convID, channelID uuid.UUID, externalUserID, text string) error
}

// ConvRoute — куда слать: тип канала + идентификаторы получателя.
type ConvRoute struct {
	ProfileID      uuid.UUID
	ChannelType    string
	ChannelID      uuid.UUID
	ExternalUserID string
}

// ConvResolver — резолвит conversation в маршрут доставки.
type ConvResolver interface {
	Resolve(ctx context.Context, convID uuid.UUID) (ConvRoute, error)
}

// OperatorProxy пересылает операторские сообщения в нужный транспорт.
// Запись в историю (role=operator) делает backend (он владелец БД agent_messages),
// поэтому прокси сам в историю НЕ пишет — иначе сообщение дублировалось бы.
type OperatorProxy struct {
	mu         sync.RWMutex
	transports map[string]TransportSender
	resolver   ConvResolver
}

// NewOperatorProxy создаёт прокси. resolver обязателен для реальной работы.
func NewOperatorProxy(resolver ConvResolver) *OperatorProxy {
	return &OperatorProxy{
		transports: map[string]TransportSender{},
		resolver:   resolver,
	}
}

// Register регистрирует транспорт по типу канала ('telegram', 'vk', 'web', ...).
func (p *OperatorProxy) Register(channelType string, sender TransportSender) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.transports[channelType] = sender
}

// Handle доставляет операторское сообщение в транспорт и записывает в историю.
func (p *OperatorProxy) Handle(ctx context.Context, op OperatorMessage) error {
	if p.resolver == nil {
		return fmt.Errorf("operator proxy: no resolver")
	}
	route, err := p.resolver.Resolve(ctx, op.ConversationID)
	if err != nil {
		return err
	}

	p.mu.RLock()
	sender := p.transports[route.ChannelType]
	p.mu.RUnlock()
	if sender == nil {
		return fmt.Errorf("operator proxy: no transport for %q", route.ChannelType)
	}

	text := op.Text
	if op.Mode == "open" {
		text = "Оператор: " + op.Text
	}
	return sender.SendToConversation(ctx, op.ConversationID, route.ChannelID, route.ExternalUserID, text)
}
