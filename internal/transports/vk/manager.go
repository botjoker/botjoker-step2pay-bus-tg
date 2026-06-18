// Package vk — ВКонтакте-транспорт через Callback API. Реализован на чистом
// HTTP к VK API (messages.send), без vksdk. VK не поддерживает edit message —
// ответ отправляется одним сообщением.
package vk

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/botjoker/sambacrm-business-tg/internal/llm"
	"github.com/botjoker/sambacrm-business-tg/internal/mdfmt"
	"github.com/botjoker/sambacrm-business-tg/internal/runtime"
	"github.com/google/uuid"
)

const vkAPIVersion = "5.199"

// Runner — исполнение агента (реализуется agentstore.Engine, тот же, что у TG).
type Runner interface {
	StartChannelConversation(ctx context.Context, channelID uuid.UUID, externalUserID, externalChatID string) (convID, agentID, profileID uuid.UUID, err error)
	RunConversation(ctx context.Context, convID uuid.UUID, text string, attachments []llm.Attachment) (<-chan llm.StreamEvent, error)
}

// Channel — параметры VK-канала.
type Channel struct {
	ChannelID    uuid.UUID
	AccessToken  string
	SecretKey    string
	Confirmation string // код подтверждения Callback API
	GroupID      int64
}

// Manager управляет VK-каналами.
type Manager struct {
	runner   Runner
	client   *http.Client
	apiBase  string // переопределяется в тестах
	mu       sync.RWMutex
	channels map[uuid.UUID]Channel
}

func NewManager(runner Runner) *Manager {
	return &Manager{
		runner:   runner,
		client:   &http.Client{Timeout: 30 * time.Second},
		apiBase:  "https://api.vk.com/method",
		channels: make(map[uuid.UUID]Channel),
	}
}

// Start регистрирует VK-канал.
func (m *Manager) Start(ch Channel) error {
	if ch.AccessToken == "" {
		return fmt.Errorf("vk: empty access token for channel %s", ch.ChannelID)
	}
	m.mu.Lock()
	m.channels[ch.ChannelID] = ch
	m.mu.Unlock()
	return nil
}

func (m *Manager) Stop(channelID uuid.UUID) {
	m.mu.Lock()
	delete(m.channels, channelID)
	m.mu.Unlock()
}

func (m *Manager) get(channelID uuid.UUID) (Channel, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c, ok := m.channels[channelID]
	return c, ok
}

func (m *Manager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.channels)
}

// SendToConversation реализует runtime.TransportSender (для live takeover).
// convID не нужен: VK адресует по channelID + externalUserID (peer id).
func (m *Manager) SendToConversation(ctx context.Context, _ uuid.UUID, channelID uuid.UUID, externalUserID, text string, att *runtime.Attachment) error {
	ch, ok := m.get(channelID)
	if !ok {
		return fmt.Errorf("vk: channel %s not running", channelID)
	}
	peerID, err := strconv.ParseInt(externalUserID, 10, 64)
	if err != nil {
		return fmt.Errorf("vk: bad peer id %q", externalUserID)
	}
	// VK не принимает внешние файлы без upload-сервера — отдаём документ ссылкой
	// в тексте (F1-1). Загрузку в docs.save можно добавить позже при необходимости.
	if att != nil && att.URL != "" {
		if text != "" {
			text += "\n\n"
		}
		text += att.URL
	}
	return m.sendMessage(ctx, ch, peerID, text)
}

// sendMessage отправляет сообщение через messages.send.
func (m *Manager) sendMessage(ctx context.Context, ch Channel, peerID int64, text string) error {
	form := url.Values{
		"access_token": {ch.AccessToken},
		"v":            {vkAPIVersion},
		"peer_id":      {strconv.FormatInt(peerID, 10)},
		"message":      {clampVK(mdfmt.ToPlain(text))},
		"random_id":    {strconv.FormatInt(time.Now().UnixNano()&0x7fffffff, 10)},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.apiBase+"/messages.send",
		nil)
	if err != nil {
		return err
	}
	req.URL.RawQuery = form.Encode()
	resp, err := m.client.Do(req)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

// clampVK обрезает текст до лимита VK (4096 символов).
func clampVK(s string) string {
	const limit = 4096
	if len(s) <= limit {
		return s
	}
	return s[:limit]
}
