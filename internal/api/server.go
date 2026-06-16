// Package api — internal HTTP server agent-рантайма: web SSE-чат, admin
// playground, internal webhooks, ingest-trigger. Маршрутизация на stdlib
// net/http (Go 1.22 ServeMux patterns), без внешних роутеров. Глубокое
// исполнение агента инкапсулировано в Engine (internal/agentstore).
package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/botjoker/sambacrm-business-tg/internal/runtime"
	"github.com/botjoker/sambacrm-business-tg/internal/storage"
	"github.com/botjoker/sambacrm-business-tg/internal/transports/telegram"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

// Server — internal HTTP API.
type Server struct {
	runtime *runtime.Runtime
	pool    *pgxpool.Pool
	queries *storage.Queries

	engine         Engine
	sse            *SSEHub
	internalSecret string // INTERNAL_JWT_SECRET (HS256)

	webhooks  map[string]WebhookFunc // transport ("telegram"/...) → async-обработчик
	vkWebhook VKWebhookFunc          // VK требует синхронный plain-text ответ

	srv *http.Server
}

// WebhookFunc обрабатывает сырой payload вебхука транспорта (async, ответ 200/JSON).
type WebhookFunc func(ctx context.Context, channelID uuid.UUID, body []byte) error

// VKWebhookFunc — синхронный обработчик VK Callback API (возвращает текст-ответ).
type VKWebhookFunc func(ctx context.Context, channelID uuid.UUID, body []byte) (string, error)

// NewServer создаёт сервер. rdb может быть nil (тогда SSE недоступен).
func NewServer(rt *runtime.Runtime, pool *pgxpool.Pool, q *storage.Queries) *Server {
	return &Server{
		runtime:        rt,
		pool:           pool,
		queries:        q,
		internalSecret: os.Getenv("INTERNAL_JWT_SECRET"),
	}
}

// SetEngine подключает реализацию Engine (из agentstore, при сборке сервиса).
func (s *Server) SetEngine(e Engine) { s.engine = e }

// SetWebhookHandler регистрирует async-обработчик вебхуков для транспорта.
func (s *Server) SetWebhookHandler(transport string, fn WebhookFunc) {
	if s.webhooks == nil {
		s.webhooks = map[string]WebhookFunc{}
	}
	s.webhooks[transport] = fn
}

// SetVKWebhookHandler регистрирует синхронный VK Callback-обработчик.
func (s *Server) SetVKWebhookHandler(fn VKWebhookFunc) { s.vkWebhook = fn }

// SetRedis подключает Redis для SSE.
func (s *Server) SetRedis(rdb *redis.Client) { s.sse = NewSSEHub(rdb) }

func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", s.handleHealth)

	// Public chat (виджет / client) — CORS.
	mux.HandleFunc("POST /chat/start", s.handleChatStart)
	mux.HandleFunc("GET /chat/sse", s.handleSSE)
	mux.HandleFunc("POST /chat/message", s.handleChatMessage)

	// Internal (internal-JWT).
	mux.HandleFunc("POST /internal/test", s.internalJWTRequired(s.handleInternalTest))
	mux.HandleFunc("POST /internal/ingest", s.internalJWTRequired(s.handleInternalIngest))
	mux.HandleFunc("POST /internal/operator-message", s.internalJWTRequired(s.handleOperatorMessage))
	mux.HandleFunc("POST /internal/auth/refresh", s.handleAuthRefresh)

	// VK Callback — синхронный plain-text ответ (более специфичный маршрут).
	mux.HandleFunc("POST /webhook/vk/{cid}", s.handleVKWebhook)
	// Остальные транспорты: общий async-маршрут.
	mux.HandleFunc("POST /webhook/{transport}/{cid}", s.handleWebhook)

	mux.HandleFunc("GET /widget.js", s.handleWidgetJS)

	return recoverMiddleware(corsMiddleware(mux))
}

// Start блокирующе слушает addr (запускать в горутине).
func (s *Server) Start(addr string) {
	s.srv = &http.Server{
		Addr:              addr,
		Handler:           s.handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	slog.Info("agent api listening", "addr", addr)
	if err := s.srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("api server error", "err", err)
	}
}

// Shutdown корректно гасит сервер.
func (s *Server) Shutdown(ctx context.Context) {
	if s.srv != nil {
		_ = s.srv.Shutdown(ctx)
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleWidgetJS(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(widgetJS)
}

func (s *Server) handleWebhook(w http.ResponseWriter, r *http.Request) {
	transport := r.PathValue("transport")
	cid, err := uuid.Parse(r.PathValue("cid"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad channel id")
		return
	}
	// Telegram: сверяем per-channel secret_token (изоляция тенантов, F02).
	if transport == "telegram" {
		got := r.Header.Get("X-Telegram-Bot-Api-Secret-Token")
		want := telegram.WebhookSecret(s.internalSecret, cid)
		if got != want {
			writeError(w, http.StatusForbidden, "invalid secret token")
			return
		}
	}
	fn := s.webhooks[transport]
	if fn == nil {
		// транспорт не подключён — принимаем и игнорируем (200, чтобы транспорт не ретраил).
		writeJSON(w, http.StatusOK, map[string]string{"status": "ignored"})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "read body")
		return
	}
	go func() {
		if err := fn(context.Background(), cid, body); err != nil {
			slog.Error("webhook handler", "transport", transport, "err", err)
		}
	}()
	writeJSON(w, http.StatusOK, map[string]string{"status": "accepted"})
}

// handleVKWebhook — синхронный: VK ждёт plain-text "ok" или confirmation-код.
func (s *Server) handleVKWebhook(w http.ResponseWriter, r *http.Request) {
	cid, err := uuid.Parse(r.PathValue("cid"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad channel id")
		return
	}
	if s.vkWebhook == nil {
		_, _ = w.Write([]byte("ok"))
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "read body")
		return
	}
	resp, err := s.vkWebhook(r.Context(), cid, body)
	if err != nil {
		slog.Error("vk webhook", "err", err)
		_, _ = w.Write([]byte("ok"))
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write([]byte(resp))
}

// --- helpers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
