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
// транспортом в Phase 6 (telegram/vk/web).
type TransportSender interface {
	SendToConversation(ctx context.Context, channelID uuid.UUID, externalUserID, text string) error
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

// OperatorProxy пересылает операторские сообщения в нужный транспорт и пишет их
// в историю (role=operator).
type OperatorProxy struct {
	mu         sync.RWMutex
	transports map[string]TransportSender
	resolver   ConvResolver
	recorder   MessageRecorder
}

// NewOperatorProxy создаёт прокси. recorder/resolver обязательны для реальной работы.
func NewOperatorProxy(resolver ConvResolver, recorder MessageRecorder) *OperatorProxy {
	return &OperatorProxy{
		transports: map[string]TransportSender{},
		resolver:   resolver,
		recorder:   recorder,
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
	if err := sender.SendToConversation(ctx, route.ChannelID, route.ExternalUserID, text); err != nil {
		return err
	}

	if p.recorder != nil {
		_, _ = p.recorder.Record(ctx, RecordedMessage{
			ConversationID:    op.ConversationID,
			ProfileID:         route.ProfileID,
			Role:              "operator",
			Content:           op.Text,
			OperatorAccountID: op.OperatorAccountID,
		})
	}
	return nil
}
