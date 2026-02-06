package queue

import (
	"fmt"
	"os"

	"github.com/hibiken/asynq"
)

// NewAsynqClient создает клиента Asynq для создания задач
func NewAsynqClient() (*asynq.Client, error) {
	redisAddr := getRedisAddr()
	redisPassword := getRedisPassword()
	redisUser := os.Getenv("REDIS_USER")

	client := asynq.NewClient(asynq.RedisClientOpt{
		Addr:     redisAddr,
		Username: redisUser,
		Password: redisPassword,
		DB:       0,
	})

	return client, nil
}

// NewAsynqServer создает сервер Asynq для обработки задач
func NewAsynqServer() (*asynq.Server, error) {
	redisAddr := getRedisAddr()
	redisPassword := getRedisPassword()
	redisUser := os.Getenv("REDIS_USER")

	srv := asynq.NewServer(
		asynq.RedisClientOpt{
			Addr:     redisAddr,
			Username: redisUser,
			Password: redisPassword,
			DB:       0,
		},
		asynq.Config{
			Concurrency: 10,
		},
	)

	return srv, nil
}

// getRedisAddr возвращает адрес Redis
func getRedisAddr() string {
	host := os.Getenv("REDIS_HOST")
	if host == "" {
		host = "localhost"
	}

	port := os.Getenv("REDIS_PORT")
	if port == "" {
		port = "6379"
	}

	return fmt.Sprintf("%s:%s", host, port)
}

// getRedisPassword возвращает пароль Redis (из файла или env)
func getRedisPassword() string {
	// Пробуем прочитать из Docker secret
	passwordFile := os.Getenv("REDIS_PASSWORD_FILE")
	if passwordFile != "" {
		data, err := os.ReadFile(passwordFile)
		if err == nil {
			return string(data)
		}
	}

	// Берем из env переменной
	return os.Getenv("REDIS_PASSWORD")
}
