FROM golang:1.23-alpine AS builder

# Build arguments для переменных окружения
ARG DATABASE_URL
ARG REDIS_HOST
ARG REDIS_PORT
ARG REDIS_USER
ARG REDIS_PASSWORD

WORKDIR /app

# Установка зависимостей
RUN apk add --no-cache git

# Копируем go.mod и go.sum
COPY go.mod go.sum ./
RUN go mod download

# Копируем весь код
COPY . .

# Собираем оба entrypoint'а в один образ (сервис НЕ переименовываем):
#   cmd/bot   → telegram-бот (legacy, дефолтный CMD)
#   cmd/agent → AI-agent runtime (запускается через command override в k8s)
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o sambacrm-business-tg ./cmd/bot
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o sambacrm-business-agent ./cmd/agent

# Финальный образ
FROM alpine:latest

# Устанавливаем runtime зависимости
RUN apk --no-cache add ca-certificates tzdata

WORKDIR /root/

# Копируем бинарник
COPY --from=builder /app/sambacrm-business-tg .

# Переменные окружения (можно переопределить при запуске)
ENV DATABASE_URL=${DATABASE_URL}
ENV REDIS_HOST=${REDIS_HOST}
ENV REDIS_PORT=${REDIS_PORT}
ENV REDIS_USER=${REDIS_USER}
ENV REDIS_PASSWORD=${REDIS_PASSWORD}

# Запуск
CMD ["./sambacrm-business-tg"]
