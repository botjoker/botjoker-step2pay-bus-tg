// Package runtime hosts the agent tool-use loop. Реализуется в 037+.
package runtime

import (
	"github.com/botjoker/sambacrm-business-tg/internal/storage"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Runtime — ядро agent-сервиса: tool-use loop, память, маршрутизация.
// На шаге 030 это пустая структура; реальная логика появляется в 037.
type Runtime struct {
	pool    *pgxpool.Pool
	queries *storage.Queries
}

// New создаёт Runtime. Дополнительные зависимости (llm-фабрика, rag-клиент,
// tool-реестр) будут инжектиться в последующих шагах.
func New(pool *pgxpool.Pool, queries *storage.Queries) *Runtime {
	return &Runtime{pool: pool, queries: queries}
}

// Pool возвращает пул соединений (используется api/queue слоями).
func (r *Runtime) Pool() *pgxpool.Pool { return r.pool }

// Queries возвращает sqlc-storage.
func (r *Runtime) Queries() *storage.Queries { return r.queries }
