package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/botjoker/telegram-bot-service/internal/ai"
	"github.com/botjoker/telegram-bot-service/internal/storage"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	tele "gopkg.in/telebot.v3"
)

type MessageHandler struct {
	pool      *pgxpool.Pool
	queries   *storage.Queries
	botConfig storage.TelegramBot
	aiClient  ai.Provider
}

func NewMessageHandler(pool *pgxpool.Pool, queries *storage.Queries, config storage.TelegramBot) *MessageHandler {
	h := &MessageHandler{
		pool:      pool,
		queries:   queries,
		botConfig: config,
	}

	// Инициализируем AI клиент если включен
	if config.AiEnabled {
		h.aiClient = ai.NewProvider(config)
	}

	return h
}

// HandleStart обрабатывает команду /start
func (h *MessageHandler) HandleStart(c tele.Context) error {
	ctx := context.Background()

	// Логируем сообщение
	h.logMessage(ctx, c, false)

	// Отправляем welcome message
	msg := "Привет! Я ваш бизнес-ассистент."
	if h.botConfig.WelcomeMessage.Valid {
		msg = h.botConfig.WelcomeMessage.String
	}

	if err := c.Send(msg); err != nil {
		return err
	}

	// Логируем ответ
	h.logMessage(ctx, c, true)

	// Запускаем workflow если есть триггер на команду /start
	go h.executeWorkflowsForCommand(ctx, c, "/start")

	return nil
}

// HandleHelp обрабатывает команду /help
func (h *MessageHandler) HandleHelp(c tele.Context) error {
	ctx := context.Background()
	h.logMessage(ctx, c, false)

	helpText := "Доступные команды:\n/start - Начать\n/help - Помощь"
	if err := c.Send(helpText); err != nil {
		return err
	}

	h.logMessage(ctx, c, true)
	return nil
}

// HandleText обрабатывает любое текстовое сообщение
func (h *MessageHandler) HandleText(c tele.Context) error {
	ctx := context.Background()
	h.logMessage(ctx, c, false)

	userMessage := c.Text()

	// 1. Проверяем есть ли workflow с триггером на сообщения
	go h.executeWorkflowsForMessage(ctx, c, userMessage)

	// 2. Если AI включен - генерируем ответ
	if h.botConfig.AiEnabled && h.aiClient != nil {
		response, err := h.generateAIResponse(ctx, c, userMessage)
		if err != nil {
			log.Printf("AI error: %v", err)
			return c.Send("Извините, произошла ошибка при обработке запроса.")
		}

		if err := c.Send(response); err != nil {
			return err
		}

		h.logMessage(ctx, c, true)
	}

	return nil
}

// HandleCallback обрабатывает нажатия на inline кнопки
func (h *MessageHandler) HandleCallback(c tele.Context) error {
	ctx := context.Background()

	// Получаем данные callback
	data := c.Callback().Data

	// Обновляем контекст разговора
	h.updateConversationContext(ctx, c, map[string]interface{}{
		"last_callback": data,
	})

	// Подтверждаем callback
	return c.Respond(&tele.CallbackResponse{
		Text: "Принято",
	})
}

// executeWorkflowsForCommand выполняет workflow с триггером на команду
func (h *MessageHandler) executeWorkflowsForCommand(ctx context.Context, c tele.Context, command string) {
	// Загружаем workflows привязанные к этому боту
	workflows, err := h.queries.GetActiveWorkflowsByBot(ctx, h.botConfig.ID)
	if err != nil {
		log.Printf("Failed to load workflows for bot: %v", err)
		return
	}

	log.Printf("🔍 Найдено %d активных workflows для бота", len(workflows))

	for _, wf := range workflows {
		if wf.TriggerType == "command" {
			// Проверяем конфигурацию триггера
			var triggerConfig map[string]interface{}
			if wf.TriggerConfig != nil {
				if err := json.Unmarshal(wf.TriggerConfig, &triggerConfig); err == nil {
					if cmd, ok := triggerConfig["command"].(string); ok && cmd == command {
						log.Printf("▶️  Запускаем workflow %s для команды %s", wf.WorkflowName, command)
						// Простая реакция - отправляем описание workflow
						message := fmt.Sprintf("✅ Workflow '%s' запущен", wf.WorkflowName)
						if wf.Description.Valid {
							message = wf.Description.String
						}
						c.Send(message)
					}
				}
			}
		}
	}
}

// executeWorkflowsForMessage выполняет workflow с триггером на сообщения
func (h *MessageHandler) executeWorkflowsForMessage(ctx context.Context, c tele.Context, message string) {
	// Загружаем workflows привязанные к этому боту
	workflows, err := h.queries.GetActiveWorkflowsByBot(ctx, h.botConfig.ID)
	if err != nil {
		log.Printf("Failed to load workflows for bot: %v", err)
		return
	}

	for _, wf := range workflows {
		if wf.TriggerType == "message" {
			// TODO: проверить pattern regex из trigger_config
			log.Printf("▶️  Workflow %s сработал на сообщение", wf.WorkflowName)
			// Пока просто логируем, обработку добавим позже
		}
	}
}

// generateAIResponse генерирует ответ через AI
func (h *MessageHandler) generateAIResponse(ctx context.Context, c tele.Context, userMessage string) (string, error) {
	// Получаем контекст разговора
	conv, err := h.getOrCreateConversation(ctx, c)
	if err != nil {
		return "", err
	}

	// Формируем промпт
	systemPrompt := "Ты - полезный бизнес-ассистент."
	if h.botConfig.AiSystemPrompt.Valid {
		systemPrompt = h.botConfig.AiSystemPrompt.String
	}

	// Если включен RAG - добавляем контекст
	var ragContext string
	// TODO: реализовать RAG поиск

	// Генерируем ответ
	response, err := h.aiClient.GenerateResponse(ctx, systemPrompt, userMessage, ragContext)
	if err != nil {
		return "", err
	}

	// Обновляем контекст разговора
	var contextData map[string]interface{}
	if err := json.Unmarshal(conv.Context, &contextData); err != nil {
		contextData = make(map[string]interface{})
	}
	contextData["last_user_message"] = userMessage
	contextData["last_ai_response"] = response

	newContext, _ := json.Marshal(contextData)
	if err := h.queries.UpdateConversation(ctx, storage.UpdateConversationParams{
		ID:      conv.ID,
		Context: newContext,
	}); err != nil {
		log.Printf("Failed to update conversation: %v", err)
	}

	return response, nil
}

// getOrCreateConversation получает или создает разговор
func (h *MessageHandler) getOrCreateConversation(ctx context.Context, c tele.Context) (storage.TelegramConversation, error) {
	chatID := c.Chat().ID

	conv, err := h.queries.GetConversation(ctx, storage.GetConversationParams{
		ProfileID: h.botConfig.ProfileID,
		ChatID:    int64(chatID),
	})

	if err != nil {
		// Создаем новый разговор
		emptyContext, _ := json.Marshal(map[string]interface{}{})
		conv, err = h.queries.CreateConversation(ctx, storage.CreateConversationParams{
			ProfileID:      h.botConfig.ProfileID,
			TelegramUserID: int64(c.Sender().ID),
			ChatID:         int64(chatID),
			Context:        emptyContext,
		})
		if err != nil {
			return storage.TelegramConversation{}, err
		}
	}

	return conv, nil
}

// updateConversationContext обновляет контекст разговора
func (h *MessageHandler) updateConversationContext(ctx context.Context, c tele.Context, updates map[string]interface{}) {
	conv, err := h.getOrCreateConversation(ctx, c)
	if err != nil {
		return
	}

	var contextData map[string]interface{}
	if err := json.Unmarshal(conv.Context, &contextData); err != nil {
		contextData = make(map[string]interface{})
	}

	// Обновляем поля
	for k, v := range updates {
		contextData[k] = v
	}

	newContext, _ := json.Marshal(contextData)
	h.queries.UpdateConversation(ctx, storage.UpdateConversationParams{
		ID:      conv.ID,
		Context: newContext,
	})
}

// logMessage логирует сообщение в БД
func (h *MessageHandler) logMessage(ctx context.Context, c tele.Context, isFromBot bool) {
	var text string
	if isFromBot {
		// Для ботовых сообщений текст уже отправлен
		text = ""
	} else {
		text = c.Text()
	}

	metadata, _ := json.Marshal(map[string]interface{}{
		"username": c.Sender().Username,
		"first_name": c.Sender().FirstName,
	})

	// Конвертируем string в pgtype.Text
	var messageText pgtype.Text
	messageText.Scan(text)

	h.queries.LogMessage(ctx, storage.LogMessageParams{
		ProfileID:      h.botConfig.ProfileID,
		TelegramUserID: int64(c.Sender().ID),
		ChatID:         int64(c.Chat().ID),
		MessageText:    messageText,
		IsFromBot:      isFromBot,
		Metadata:       metadata,
	})
}
