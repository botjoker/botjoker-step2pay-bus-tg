package storage

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
)

func (q *Queries) CountUserMessages(ctx context.Context, conversationID pgtype.UUID) (int64, error) {
	row := q.db.QueryRow(ctx, `
SELECT COUNT(*)
FROM agent_messages
WHERE conversation_id = $1 AND role = 'user'`, conversationID)
	var count int64
	err := row.Scan(&count)
	return count, err
}
