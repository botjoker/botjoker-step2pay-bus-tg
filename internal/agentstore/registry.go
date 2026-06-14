package agentstore

import (
	"context"

	"github.com/botjoker/sambacrm-business-tg/internal/llm"
	"github.com/botjoker/sambacrm-business-tg/internal/runtime"
	"github.com/botjoker/sambacrm-business-tg/internal/storage"
	"github.com/botjoker/sambacrm-business-tg/internal/tools"
	"github.com/google/uuid"
)

// DBToolRegistry реализует runtime.ToolRegistry: схемы из enabled-tools тенанта,
// локальные tools — в БД напрямую, остальные — через backend tool-exec.
type DBToolRegistry struct {
	q      *storage.Queries
	remote *tools.RemoteExecutor
}

func NewToolRegistry(q *storage.Queries, remote *tools.RemoteExecutor) *DBToolRegistry {
	return &DBToolRegistry{q: q, remote: remote}
}

var _ runtime.ToolRegistry = (*DBToolRegistry)(nil)

// SchemasFor возвращает JSON Schema только для включённых у агента инструментов.
func (r *DBToolRegistry) SchemasFor(ctx context.Context, agentID uuid.UUID) ([]llm.ToolDef, error) {
	enabled, err := r.q.ListEnabledTools(ctx, toUUID(agentID))
	if err != nil {
		return nil, err
	}
	out := make([]llm.ToolDef, 0, len(enabled))
	for _, t := range enabled {
		if def, ok := tools.Definitions[t.ToolName]; ok {
			out = append(out, def)
		}
	}
	return out, nil
}

// Execute диспатчит: локальный tool → в БД; иначе → backend.
func (r *DBToolRegistry) Execute(ctx context.Context, ec runtime.ToolExecCtx, name string, args map[string]any) (map[string]any, error) {
	if tools.LocalToolNames[name] {
		return r.execLocal(ctx, ec, name, args)
	}
	if r.remote == nil {
		return map[string]any{"error": "remote tools not configured"}, nil
	}
	return r.remote.Exec(ctx, ec, name, args)
}
