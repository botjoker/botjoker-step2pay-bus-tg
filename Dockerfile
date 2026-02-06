FROM golang:1.22-alpine AS builder

WORKDIR /app

# Установка зависимостей
RUN apk add --no-cache git

# Копируем go.mod и go.sum
COPY go.mod go.sum ./
RUN go mod download

# Копируем весь код
COPY . .

# Собираем приложение
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o telegram-bot-service ./cmd/bot

# Финальный образ
FROM alpine:latest

RUN apk --no-cache add ca-certificates tzdata

WORKDIR /root/

# Копируем бинарник
COPY --from=builder /app/telegram-bot-service .

# Запуск
CMD ["./telegram-bot-service"]
