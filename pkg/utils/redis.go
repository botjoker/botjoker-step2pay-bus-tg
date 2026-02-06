package utils

import (
	"context"
	"fmt"
	"os"

	"github.com/redis/go-redis/v9"
)

// NewRedisClient создает подключение к Redis с учетом env переменных и secrets
func NewRedisClient() (*redis.Client, error) {
	host := os.Getenv("REDIS_HOST")
	if host == "" {
		host = "localhost"
	}

	port := os.Getenv("REDIS_PORT")
	if port == "" {
		port = "6379"
	}

	user := os.Getenv("REDIS_USER")
	if user == "" {
		user = "default"
	}

	// Пробуем прочитать пароль из файла секрета (Docker secrets)
	password := ""
	passwordFile := os.Getenv("REDIS_PASSWORD_FILE")
	if passwordFile != "" {
		data, err := os.ReadFile(passwordFile)
		if err == nil {
			password = string(data)
		}
	}

	// Если не нашли в файле, берем из env переменной
	if password == "" {
		password = os.Getenv("REDIS_PASSWORD")
	}

	client := redis.NewClient(&redis.Options{
		Addr:     fmt.Sprintf("%s:%s", host, port),
		Username: user,
		Password: password,
		DB:       0,
	})

	// Проверяем подключение
	ctx := context.Background()
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("failed to connect to Redis: %w", err)
	}

	return client, nil
}
