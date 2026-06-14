// Package api — internal HTTP server agent-рантайма: web SSE-чат, admin
// playground, internal webhooks, ingest-trigger. Маршрутизация на stdlib
// net/http (Go 1.22 ServeMux patterns), без внешних роутеров. Глубокое
// исполнение агента инкапсулировано в Engine (internal/agentstore).
package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/botjoker/sambacrm-business-tg/internal/runtime"
	"github.com/botjoker/sambacrm-business-tg/internal/storage"
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

	srv *http.Server
}

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

	// Webhooks от транспортов (полная реализация — Phase 6).
	mux.HandleFunc("POST /webhook/telegram/{cid}", s.handleWebhookStub)
	mux.HandleFunc("POST /webhook/vk/{cid}", s.handleWebhookStub)
	mux.HandleFunc("POST /webhook/max/{cid}", s.handleWebhookStub)
	mux.HandleFunc("POST /webhook/avito/{cid}", s.handleWebhookStub)

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
	w.Header().Set("Content-Type", "application/javascript")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("// SambaCRM agent widget — bundle реализуется в шаге 075\n"))
}

func (s *Server) handleWebhookStub(w http.ResponseWriter, r *http.Request) {
	slog.Info("webhook received (stub)", "path", r.URL.Path, "cid", r.PathValue("cid"))
	writeJSON(w, http.StatusOK, map[string]string{"status": "accepted"})
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
