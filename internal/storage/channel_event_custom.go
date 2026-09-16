package storage

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

// RecordInboundChannelEvent atomically deduplicates an inbound provider event
// and stores a compact reference in the conversation context. The fallback
// keeps older installations without communication_events operational.
func (q *Queries) RecordInboundChannelEvent(
	ctx context.Context,
	conversationID pgtype.UUID,
	providerEventID string,
	providerMetadata []byte,
) (bool, error) {
	row := q.db.QueryRow(ctx, `
WITH conversation AS (
  SELECT id, profile_id, channel_id
  FROM agent_conversations
  WHERE id = $1
), inserted AS (
  INSERT INTO communication_events (profile_id, event_type, conversation_id, payload)
  SELECT conversation.profile_id, 'channel.inbound', conversation.id,
         jsonb_build_object(
           'type', 'channel.inbound',
           'channel_id', conversation.channel_id,
           'provider_event_id', $2::text,
           'provider_metadata_reference', $3::jsonb
         )
  FROM conversation
  ON CONFLICT DO NOTHING
  RETURNING conversation_id
), updated AS (
  UPDATE agent_conversations conversation
  SET context = COALESCE(conversation.context, '{}'::jsonb) || jsonb_build_object(
        'marketing_channel_event', jsonb_build_object(
          'external_event_id', $2,
          'provider_metadata_reference', $3::jsonb
        )
      ),
      last_message_at = NOW()
  WHERE conversation.id = $1
    AND EXISTS (SELECT 1 FROM inserted)
  RETURNING conversation.id
)
SELECT EXISTS (SELECT 1 FROM updated)`, conversationID, providerEventID, providerMetadata)
	var inserted bool
	if err := row.Scan(&inserted); err == nil {
		return inserted, nil
	} else if pgErr, ok := err.(*pgconn.PgError); !ok || pgErr.Code != "42P01" {
		return false, err
	}

	contextPatch, err := channelEventContextPatch(providerEventID, providerMetadata)
	if err != nil {
		return false, err
	}
	command, err := q.db.Exec(ctx, `
UPDATE agent_conversations
SET context = COALESCE(context, '{}'::jsonb) || $2::jsonb,
    last_message_at = NOW()
WHERE id = $1
  AND COALESCE(context->'marketing_channel_event'->>'external_event_id', '') <> $3`, conversationID, contextPatch, providerEventID)
	if err != nil {
		return false, err
	}
	return command.RowsAffected() > 0, nil
}

func channelEventContextPatch(providerEventID string, providerMetadata []byte) ([]byte, error) {
	var metadata any
	if err := json.Unmarshal(providerMetadata, &metadata); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"marketing_channel_event": map[string]any{
			"external_event_id":           providerEventID,
			"provider_metadata_reference": metadata,
		},
	})
}
