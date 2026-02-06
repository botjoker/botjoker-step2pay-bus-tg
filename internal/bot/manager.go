package bot

import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/botjoker/sambacrm-business-tg/internal/storage"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	tele "gopkg.in/telebot.v3"
)

// Manager управляет множеством ботов
type Manager struct {
	pool    *pgxpool.Pool
	queries *storage.Queries
	bots    map[uuid.UUID]*BotInstance // key = bot_id
	mu      sync.RWMutex
}

// BotInstance - один запущенный бот
type BotInstance struct {
	BotID     uuid.UUID
	ProfileID uuid.UUID
	Bot       *tele.Bot
	Config    storage.TelegramBot
	Handler   *MessageHandler
	cancel    context.CancelFunc
}

func NewManager(pool *pgxpool.Pool, queries *storage.Queries) *Manager {
	return &Manager{
		pool:    pool,
		queries: queries,
		bots:    make(map[uuid.UUID]*BotInstance),
	}
}

// LoadAndStartBots загружает всех активных ботов из БД и запускает их
func (m *Manager) LoadAndStartBots(ctx context.Context) error {
	bots, err := m.queries.GetAllActiveBots(ctx)
	if err != nil {
		return fmt.Errorf("failed to load bots: %w", err)
	}

	for _, botConfig := range bots {
		if err := m.StartBot(ctx, botConfig); err != nil {
			username := ""
			if botConfig.BotUsername.Valid {
				username = botConfig.BotUsername.String
			}
			log.Printf("❌ Не удалось запустить бота (@%s): %v", username, err)
			continue
		}
		username := ""
		if botConfig.BotUsername.Valid {
			username = botConfig.BotUsername.String
		}
		log.Printf("✅ Запущен бот @%s", username)
	}

	return nil
}

// StartBot запускает одного бота
func (m *Manager) StartBot(parentCtx context.Context, config storage.TelegramBot) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Конвертируем UUID из pgtype.UUID в uuid.UUID
	var botID uuid.UUID
	copy(botID[:], config.ID.Bytes[:])

	// Проверяем что бот еще не запущен
	if _, exists := m.bots[botID]; exists {
		return fmt.Errorf("bot %s already running", botID)
	}

	// Создаем Telegram бота
	pref := tele.Settings{
		Token: config.BotToken,
		Poller: &tele.LongPoller{
			Timeout: 10,
		},
	}

	bot, err := tele.NewBot(pref)
	if err != nil {
		return fmt.Errorf("failed to create bot: %w", err)
	}

	// Создаем контекст для этого бота
	ctx, cancel := context.WithCancel(parentCtx)

	// Конвертируем ProfileID из pgtype.UUID в uuid.UUID
	var profileID uuid.UUID
	copy(profileID[:], config.ProfileID.Bytes[:])

	// Создаем handler для сообщений
	handler := NewMessageHandler(m.pool, m.queries, config)

	instance := &BotInstance{
		BotID:     botID,
		ProfileID: profileID,
		Bot:       bot,
		Config:    config,
		Handler:   handler,
		cancel:    cancel,
	}

	// Регистрируем обработчики
	instance.registerHandlers()
	log.Printf("📝 Обработчики зарегистрированы для бота %s", botID)

	// Запускаем в отдельной горутине
	go func() {
		log.Printf("🚀 Запуск Long Polling для бота %s...", botID)
		bot.Start()
		log.Printf("⏸️  Long Polling остановлен для бота %s", botID)
		<-ctx.Done()
		bot.Stop()
	}()

	m.bots[botID] = instance

	return nil
}

// registerHandlers регистрирует обработчики сообщений
func (b *BotInstance) registerHandlers() {
	// Команды
	b.Bot.Handle("/start", b.Handler.HandleStart)
	b.Bot.Handle("/help", b.Handler.HandleHelp)

	// Любой текст
	b.Bot.Handle(tele.OnText, b.Handler.HandleText)

	// Callback от inline кнопок
	b.Bot.Handle(tele.OnCallback, b.Handler.HandleCallback)
}

// StopBot останавливает конкретного бота
func (m *Manager) StopBot(botID uuid.UUID) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if instance, exists := m.bots[botID]; exists {
		instance.cancel()
		delete(m.bots, botID)
		log.Printf("🛑 Остановлен бот %s", botID)
	}
}

// StopAll останавливает всех ботов
func (m *Manager) StopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for botID, instance := range m.bots {
		instance.cancel()
		log.Printf("🛑 Остановлен бот %s", botID)
	}

	m.bots = make(map[uuid.UUID]*BotInstance)
}

// ActiveBotsCount возвращает количество активных ботов
func (m *Manager) ActiveBotsCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.bots)
}

// GetBot возвращает инстанс бота по bot_id
func (m *Manager) GetBot(botID uuid.UUID) (*BotInstance, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	instance, exists := m.bots[botID]
	return instance, exists
}
