package storage

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
)

// InsertLeadAutoWithExtra keeps the generated sqlc files untouched while the
// lead contract includes the later contact_extra column.
type InsertLeadAutoWithExtraParams struct {
	ProfileID            pgtype.UUID
	StageID              pgtype.UUID
	Title                pgtype.Text
	ContactName          pgtype.Text
	ContactPhone         pgtype.Text
	ContactEmail         pgtype.Text
	ContactExtra         pgtype.Text
	SourceAgentID        pgtype.UUID
	SourceConversationID pgtype.UUID
	Data                 []byte
	Summary              pgtype.Text
	Sentiment            pgtype.Text
	PrimaryIntent        pgtype.Text
}

func (q *Queries) InsertLeadAutoWithExtra(ctx context.Context, arg InsertLeadAutoWithExtraParams) (pgtype.UUID, error) {
	row := q.db.QueryRow(ctx, `
INSERT INTO leads
  (profile_id, stage_id, title, contact_name, contact_phone, contact_email, contact_extra,
   source, source_agent_id, source_conversation_id, data, summary, sentiment,
   primary_intent, sort_order)
VALUES ($1, $2, $3, $4, $5, $6, $7, 'agent', $8, $9, $10, $11, $12, $13,
   COALESCE((SELECT MAX(sort_order) + 1 FROM leads
             WHERE profile_id = $1 AND stage_id = $2 AND is_deleted = false), 0))
RETURNING id`,
		arg.ProfileID, arg.StageID, arg.Title, arg.ContactName, arg.ContactPhone,
		arg.ContactEmail, arg.ContactExtra, arg.SourceAgentID, arg.SourceConversationID,
		arg.Data, arg.Summary, arg.Sentiment, arg.PrimaryIntent,
	)
	var id pgtype.UUID
	err := row.Scan(&id)
	return id, err
}

func (q *Queries) SetLeadContactExtraIfEmpty(ctx context.Context, profileID, leadID pgtype.UUID, value pgtype.Text) error {
	_, err := q.db.Exec(ctx, `
UPDATE leads
SET contact_extra = $3, updated_at = NOW()
WHERE id = $1 AND profile_id = $2 AND is_deleted = false
  AND NULLIF(BTRIM(contact_extra), '') IS NULL`, leadID, profileID, value)
	return err
}
