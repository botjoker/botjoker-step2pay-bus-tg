package storage

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
)

// ActivePDConsentTemplate возвращает действующую tenant-specific редакцию
// согласия. Этот небольшой runtime-query намеренно не меняет sqlc-артефакты.
func (q *Queries) ActivePDConsentTemplate(ctx context.Context, profileID pgtype.UUID) (AgentConsentTemplate, error) {
	row := q.db.QueryRow(ctx, `
		SELECT id, profile_id, consent_type, version, title, body_md,
		       is_active, created_at, updated_at
		FROM agent_consent_templates
		WHERE profile_id = $1 AND consent_type = 'pd_processing' AND is_active = true
		ORDER BY updated_at DESC
		LIMIT 1`, profileID)
	var result AgentConsentTemplate
	err := row.Scan(
		&result.ID, &result.ProfileID, &result.ConsentType, &result.Version,
		&result.Title, &result.BodyMd, &result.IsActive,
		&result.CreatedAt, &result.UpdatedAt,
	)
	return result, err
}

type RecordPDConsentParams struct {
	ProfileID      pgtype.UUID
	ConversationID pgtype.UUID
	TemplateID     pgtype.UUID
	Version        string
	TextSnapshot   string
	UserAgent      string
}

// RecordPDConsent идемпотентно фиксирует явное согласие пользователя.
func (q *Queries) RecordPDConsent(ctx context.Context, arg RecordPDConsentParams) error {
	_, err := q.db.Exec(ctx, `
		INSERT INTO agent_consents
			(profile_id, conversation_id, template_id, consent_type,
			 consent_version, consent_text_snapshot, granted, granted_at, user_agent)
		SELECT $1, $2, $3, 'pd_processing', $4, $5, true, NOW(), NULLIF($6, '')
		WHERE NOT EXISTS (
			SELECT 1 FROM agent_consents
			WHERE conversation_id = $2
			  AND consent_type = 'pd_processing'
			  AND consent_version = $4
			  AND granted = true
			  AND revoked_at IS NULL
		)`,
		arg.ProfileID, arg.ConversationID, arg.TemplateID,
		arg.Version, arg.TextSnapshot, arg.UserAgent,
	)
	return err
}
