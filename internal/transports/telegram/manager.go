// Package telegram — Telegram-транспорт agent-рантайма. Входящие сообщения
// приходят webhook'ом в internal/api и передаются в Manager.HandleUpdate;
// telebot.Bot используется только для отправки/редактирования ответов.
package telegram

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/botjoker/sambacrm-business-tg/internal/llm"
	"github.com/google/uuid"
	tele "gopkg.in/telebot.v3"
)

// Runner — исполнение агента (реализуется agentstore.Engine).
type Runner interface {
	StartChannelConversation(ctx context.Context, channelID uuid.UUID, externalUserID, externalChatID string) (convID, agentID, profileID uuid.UUID, err error)
	RunConversation(ctx context.Context, convID uuid.UUID, text string, attachments []llm.Attachment) (<-chan llm.StreamEvent, error)
}

// Manager держит по одному telebot.Bot на канал (для отправки сообщений).
type Manager struct {
	runner Runner
	mu     sync.RWMutex
	bots   map[uuid.UUID]*tele.Bot
}

func NewManager(runner Runner) *Manager {
	return &Manager{runner: runner, bots: make(map[uuid.UUID]*tele.Bot)}
}

// Start регистрирует бота канала (token уже расшифрован).
func (m *Manager) Start(channelID uuid.UUID, token string) error {
	if token == "" {
		return fmt.Errorf("telegram: empty token for channel %s", channelID)
	}
	bot, err := tele.NewBot(tele.Settings{
		Token:  token,
		Poller: nil, // входящие — через webhook, поллер не нужен
	})
	if err != nil {
		return fmt.Errorf("telegram: new bot: %w", err)
	}
	m.mu.Lock()
	m.bots[channelID] = bot
	m.mu.Unlock()
	return nil
}

// Stop убирает бота канала.
func (m *Manager) Stop(channelID uuid.UUID) {
	m.mu.Lock()
	delete(m.bots, channelID)
	m.mu.Unlock()
}

func (m *Manager) get(channelID uuid.UUID) (*tele.Bot, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	b, ok := m.bots[channelID]
	return b, ok
}

// Count — число активных ботов.
func (m *Manager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.bots)
}

// SendToConversation реализует runtime.TransportSender (для live takeover).
func (m *Manager) SendToConversation(_ context.Context, channelID uuid.UUID, externalUserID, text string) error {
	bot, ok := m.get(channelID)
	if !ok {
		return fmt.Errorf("telegram: channel %s not running", channelID)
	}
	chatID, err := strconv.ParseInt(externalUserID, 10, 64)
	if err != nil {
		return fmt.Errorf("telegram: bad external user id %q", externalUserID)
	}
	_, err = bot.Send(&tele.Chat{ID: chatID}, text)
	return err
}

// editInterval — частота edit message при стриминге (лимит TG ~30 edit/сек).
const editInterval = 700 * time.Millisecond
