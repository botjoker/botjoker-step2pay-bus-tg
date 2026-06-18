// Package telegram — Telegram-транспорт agent-рантайма. Входящие сообщения
// приходят webhook'ом в internal/api и передаются в Manager.HandleUpdate;
// telebot.Bot используется только для отправки/редактирования ответов.
package telegram

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"strconv"
	"strings"
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

// botRef — бот канала + его токен (токен нужен для сборки file-URL).
type botRef struct {
	bot   *tele.Bot
	token string
}

// Manager держит по одному telebot.Bot на канал (для отправки сообщений).
type Manager struct {
	runner Runner
	mu     sync.RWMutex
	bots   map[uuid.UUID]*botRef
}

func NewManager(runner Runner) *Manager {
	return &Manager{runner: runner, bots: make(map[uuid.UUID]*botRef)}
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
	m.bots[channelID] = &botRef{bot: bot, token: token}
	m.mu.Unlock()
	return nil
}

// Stop убирает бота канала.
func (m *Manager) Stop(channelID uuid.UUID) {
	m.mu.Lock()
	delete(m.bots, channelID)
	m.mu.Unlock()
}

func (m *Manager) get(channelID uuid.UUID) (*botRef, bool) {
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
// convID не нужен: Telegram адресует по channelID + externalUserID (chat id).
func (m *Manager) SendToConversation(_ context.Context, _ uuid.UUID, channelID uuid.UUID, externalUserID, text string) error {
	ref, ok := m.get(channelID)
	if !ok {
		return fmt.Errorf("telegram: channel %s not running", channelID)
	}
	chatID, err := strconv.ParseInt(externalUserID, 10, 64)
	if err != nil {
		return fmt.Errorf("telegram: bad external user id %q", externalUserID)
	}
	_, err = ref.bot.Send(&tele.Chat{ID: chatID}, text)
	return err
}

// WebhookSecret детерминированно выводит per-channel secret_token из
// INTERNAL_JWT_SECRET. Telegram присылает его в заголовке
// X-Telegram-Bot-Api-Secret-Token; runtime сверяет без обращения к БД.
func WebhookSecret(internalSecret string, channelID uuid.UUID) string {
	mac := hmac.New(sha256.New, []byte(internalSecret))
	mac.Write([]byte(channelID.String()))
	return hex.EncodeToString(mac.Sum(nil))[:32]
}

// SetWebhook регистрирует webhook канала в Telegram. base = AGENT_PUBLIC_URL.
func (m *Manager) SetWebhook(channelID uuid.UUID, base, internalSecret string) error {
	ref, ok := m.get(channelID)
	if !ok {
		return fmt.Errorf("telegram: no bot for channel %s", channelID)
	}
	url := strings.TrimRight(base, "/") + "/webhook/telegram/" + channelID.String()
	secret := WebhookSecret(internalSecret, channelID)

	form := neturl.Values{}
	form.Set("url", url)
	form.Set("secret_token", secret)
	form.Set("allowed_updates", `["message","callback_query"]`)

	resp, err := http.PostForm("https://api.telegram.org/bot"+ref.token+"/setWebhook", form)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("setWebhook %d: %s", resp.StatusCode, b)
	}
	return nil
}

// editInterval — частота edit message при стриминге (лимит TG ~30 edit/сек).
const editInterval = 700 * time.Millisecond
