.PHONY: help run build test sqlc clean docker-build docker-up docker-down

help: ## Показать помощь
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-20s\033[0m %s\n", $$1, $$2}'

run: ## Запустить приложение
	go run cmd/bot/main.go

build: ## Собрать бинарник
	go build -o bin/telegram-bot-service cmd/bot/main.go

test: ## Запустить тесты
	go test -v ./...

sqlc: ## Сгенерировать код из SQL
	sqlc generate

clean: ## Удалить бинарники
	rm -rf bin/

docker-build: ## Собрать Docker образ
	docker build -t telegram-bot-service:latest .

docker-up: ## Запустить через docker-compose
	docker-compose up -d

docker-down: ## Остановить docker-compose
	docker-compose down

deps: ## Установить зависимости
	go mod download
	go install github.com/sqlc-dev/sqlc/cmd/sqlc@latest

lint: ## Запустить линтер
	golangci-lint run
