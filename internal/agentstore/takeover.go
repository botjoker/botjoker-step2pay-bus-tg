package agentstore

import (
	"context"
	"errors"

	"github.com/botjoker/sambacrm-business-tg/internal/runtime"
	"github.com/botjoker/sambacrm-business-tg/internal/storage"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// DBTakeover реализует runtime.TakeoverGate: активен ли оператор в диалоге.
type DBTakeover struct {
	q *storage.Queries
}

func NewTakeover(q *storage.Queries) *DBTakeover { return &DBTakeover{q: q} }

var _ runtime.TakeoverGate = (*DBTakeover)(nil)

// IsActive — true, если есть takeover с ended_at IS NULL.
func (t *DBTakeover) IsActive(ctx context.Context, convID uuid.UUID) (bool, error) {
	_, err := t.q.GetActiveTakeover(ctx, toUUID(convID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
