# SambaCRM AI-Agent Runtime

Рантайм AI-агентов SambaCRM (модуль `agents`, Phase 2). Один сервис обслуживает
агентов всех тенантов: ведёт диалоги в нескольких каналах, гоняет LLM-цикл с
вызовом инструментов, отвечает с опорой на базу знаний (RAG) и считает потребление
токенов.

> ⚠️ Это **не** старый telegram-бот с workflow-движком. Прежняя архитектура
> (`cmd/bot`, граф nodes+edges, таблицы `telegram_bots`/`telegram_workflows`, n8n)
> удалена. Единственный entrypoint — `cmd/agent`.

## Что делает

- 🌐 **Мультиканальность** — Telegram (webhook + `setWebhook`), VK (Callback API),
  Web-виджет (`/widget.js` + SSE). Каналы грузятся из `agent_channels`.
- 🔁 **Агентский цикл (ReAct)** — стриминг от LLM + вызов инструментов до
  `llm_max_iterations`, не граф сценариев (`internal/runtime/agent.go`).
- 🧠 **LLM per-agent** — провайдер/модель/ключ берутся из БД на каждого агента;
  ключи расшифровываются XChaCha20 (`AGENT_SECRETS_KEY`). Провайдеры:
  Anthropic, OpenAI, OpenRouter, DeepSeek, GigaChat, YandexGPT, T-Bank (t_pro).
- 📚 **RAG** — поиск делегирован Python-сервису `sambacrm-agent-rag`
  (`POST {RAG_URL}/v1/search`); ingest триггерит он же (`/v1/ingest`).
- 🛠 **Инструменты** — LLM решает «вызвать tool», сам tool исполняется на бэкенде
  (`POST {BACKEND_URL}/internal/agents/tools/exec/{name}`).
- 🙋 **Intake / lead capture**, 👤 **live-takeover** (перехват оператором),
  📈 **insights** и еженедельный **digest** (фоновые задачи Asynq).
- 💸 **Учёт токенов** — стоимость считается локально и постится в бэкенд
  (`/internal/agents/billing/track`).

## Технологии

- **Go 1.23**, `net/http` (ServeMux 1.22+ с path-параметрами)
- **pgx/v5** + **sqlc** (type-safe SQL; не редактировать сгенерированные файлы)
- **telebot** (Telegram), собственный VK Callback-клиент
- **asynq** + Redis (фоновые задачи)
- LLM-клиенты — собственные в `internal/llm/`

## Структура

```
cmd/agent/            # entrypoint (бинарь sambacrm-business-agent)
internal/
├── api/              # HTTP-сервер (webhooks, /internal/*, /chat/*, /widget.js)
├── runtime/          # агентский цикл (agent.go), сборка промпта
├── llm/              # клиенты провайдеров + подсчёт стоимости
├── agentstore/       # доступ к БД: каналы, диалоги, intake, takeover, insights, digest
├── tools/            # описание инструментов + remote-вызов на бэкенд
├── transports/       # telegram, vk
├── pii/              # редакция PII
├── queue/            # Asynq: insights:conversation, insights:weekly_digest
└── storage/          # sqlc-сгенерированный слой
```

## Запуск

```bash
make deps          # go mod download + sqlc
make sqlc          # регенерация из SQL (после правки *.sql)
make run           # go run ./cmd/agent
make build         # bin/sambacrm-business-agent
make test
make lint
```

## HTTP-эндпоинты (`internal/api/server.go`)

| Метод | Путь | Назначение |
|-------|------|-----------|
| GET | `/health` | liveness/readiness |
| POST | `/webhook/{transport}/{cid}` | входящие апдейты канала (Telegram и пр.) |
| POST | `/webhook/vk/{cid}` | VK Callback (синхронный ответ) |
| GET | `/widget.js` | JS веб-виджета |
| POST | `/chat/start`, `/chat/message`; GET `/chat/sse` | веб-чат (SSE, нужен Redis) |
| POST | `/internal/test` | playground (вызывает бэкенд) — internal-JWT |
| POST | `/internal/ingest` | триггер ingest знаний — internal-JWT |
| POST | `/internal/operator-message` | сообщение оператора в диалог — internal-JWT |

Входящий Telegram-webhook защищён per-channel `secret_token`
(`X-Telegram-Bot-Api-Secret-Token`), регистрируется через `setWebhook`.

## Переменные окружения

```
DATABASE_URL=postgresql://...            # общая business-БД
REDIS_HOST=, REDIS_PORT=, REDIS_USER=, REDIS_PASSWORD=
BACKEND_URL=http://sambacrm-backend:3005 # для tools/exec и billing/track
RAG_URL=http://sambacrm-agent-rag:8000   # без него RAG отключён (noopRAG)
AGENT_HTTP_ADDR=:8080
AGENT_PUBLIC_URL=https://agent.sambacrm.online   # нужен для Telegram setWebhook
INTERNAL_JWT_SECRET=...                   # межсервисный JWT (общий с backend/rag)
AGENT_SECRETS_KEY=...                      # XChaCha20-ключ расшифровки кред каналов/LLM
RAG_EMBEDDING_PROVIDER=bge_m3              # должен совпадать с провайдером индексации
INSIGHTS_PROVIDER=yandex_gpt               # дешёвый провайдер для аналитики
INSIGHTS_MODEL=yandexgpt-lite
GIGACHAT_SCOPE=GIGACHAT_API_PERS
```

LLM-ключи для агентов берутся **из БД** (`agent_credentials`), а не из ENV.

## Фоновые задачи (Asynq)

- `insights:conversation` — пост-анализ «остывшего» диалога (~каждые 5 мин).
- `insights:weekly_digest` — еженедельный дайджест по тенанту (понедельник, утро UTC).
  ⚠️ Дайджест пишется в БД; отправка email владельцу — TODO.

## Production

Деплоится Helm-чартом umbrella `infra/helm/sambacrm/` (сервис `agent`, образ
`botjoker/sambacrm-business-tg`, порт 8080, HPA 2-6, ingress `agent.sambacrm.online`
с `proxy-buffering: off` под SSE). Dockerfile собирает только `./cmd/agent`.
