package ai

import (
	"context"
	"fmt"
	"os"

	"github.com/botjoker/sambacrm-business-tg/internal/storage"
	"github.com/sashabaranov/go-openai"
)

// Provider - интерфейс для AI провайдеров
type Provider interface {
	GenerateResponse(ctx context.Context, systemPrompt, userMessage, ragContext string) (string, error)
}

// NewProvider создает AI провайдера на основе конфигурации бота
func NewProvider(config storage.TelegramBot) Provider {
	provider := "openai"
	if config.AiProvider.Valid {
		provider = config.AiProvider.String
	}

	switch provider {
	case "openai":
		return NewOpenAIProvider(config)
	case "anthropic":
		// TODO: реализовать Anthropic
		return NewOpenAIProvider(config) // fallback
	default:
		return NewOpenAIProvider(config)
	}
}

// OpenAIProvider реализует Provider для OpenAI
type OpenAIProvider struct {
	client      *openai.Client
	model       string
	temperature float32
	maxTokens   int
}

func NewOpenAIProvider(config storage.TelegramBot) *OpenAIProvider {
	// API ключ из env переменной (credentials будем подключать позже)
	apiKey := os.Getenv("OPENAI_API_KEY")

	client := openai.NewClient(apiKey)

	model := "gpt-4"
	if config.AiModel.Valid {
		model = config.AiModel.String
	}

	temperature := float32(0.7)
	if config.AiTemperature.Valid {
		// TODO: конвертировать pgtype.Numeric в float32
		temperature = 0.7
	}

	maxTokens := 2000
	if config.AiMaxTokens.Valid {
		maxTokens = int(config.AiMaxTokens.Int32)
	}

	return &OpenAIProvider{
		client:      client,
		model:       model,
		temperature: temperature,
		maxTokens:   maxTokens,
	}
}

func (p *OpenAIProvider) GenerateResponse(ctx context.Context, systemPrompt, userMessage, ragContext string) (string, error) {
	messages := []openai.ChatCompletionMessage{
		{
			Role:    openai.ChatMessageRoleSystem,
			Content: systemPrompt,
		},
	}

	// Добавляем RAG контекст если есть
	if ragContext != "" {
		messages = append(messages, openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleSystem,
			Content: fmt.Sprintf("Дополнительный контекст из базы знаний:\n%s", ragContext),
		})
	}

	messages = append(messages, openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleUser,
		Content: userMessage,
	})

	resp, err := p.client.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model:       p.model,
		Messages:    messages,
		Temperature: p.temperature,
		MaxTokens:   p.maxTokens,
	})

	if err != nil {
		return "", fmt.Errorf("openai error: %w", err)
	}

	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("no response from OpenAI")
	}

	return resp.Choices[0].Message.Content, nil
}
