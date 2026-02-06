package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/botjoker/sambacrm-business-tg/internal/bot"
	"github.com/botjoker/sambacrm-business-tg/internal/storage"
	"github.com/botjoker/sambacrm-business-tg/pkg/utils"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
)

func main() {
	// Загрузка переменных окружения
	if err := godotenv.Load(); err != nil {
		log.Println("Warning: .env file not found")
	}

	ctx := context.Background()

	// Подключение к БД
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal("DATABASE_URL is required")
	}

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		log.Fatalf("Unable to connect to database: %v", err)
	}
	defer pool.Close()

	// Подключение к Redis
	redisClient, err := utils.NewRedisClient()
	if err != nil {
		log.Fatalf("Unable to connect to Redis: %v", err)
	}
	defer redisClient.Close()

	log.Println("✅ Подключено к PostgreSQL и Redis")

	// Создаем storage
	queries := storage.New(pool)

	// Создаем Bot Manager
	manager := bot.NewManager(pool, queries)

	// Загружаем и запускаем всех активных ботов
	if err := manager.LoadAndStartBots(ctx); err != nil {
		log.Fatalf("Failed to start bots: %v", err)
	}

	log.Println("✅ Telegram Bot Service запущен")
	log.Printf("📊 Запущено ботов: %d", manager.ActiveBotsCount())

	// Graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("🛑 Остановка сервиса...")
	manager.StopAll()
	log.Println("✅ Сервис остановлен")
}
