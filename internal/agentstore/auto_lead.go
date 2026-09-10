package agentstore

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/botjoker/sambacrm-business-tg/internal/storage"
	"github.com/jackc/pgx/v5"
)

type autoLeadMetadata struct {
	Name          string
	Phone         string
	Email         string
	ContactExtra  string
	Summary       string
	Sentiment     string
	PrimaryIntent string
}

// maybeCreateAutoLead creates or links a lead when the agent setting is enabled
// and either a phone fact exists or post-analysis detected purchase intent.
// Contact values stored by the protected form are the source of truth; model
// extraction is only a fallback for ordinary conversation data.
func maybeCreateAutoLead(
	ctx context.Context,
	q *storage.Queries,
	conv storage.AgentConversation,
	meta autoLeadMetadata,
) error {
	auto, err := q.GetAgentAutoCreateLead(ctx, conv.AgentID)
	if err != nil || !auto {
		return err
	}
	facts, err := q.ListConversationFacts(ctx, conv.ID)
	if err != nil {
		return err
	}
	fieldTypes := map[string]string{}
	fields, err := q.ListIntakeFields(ctx, conv.AgentID)
	if err != nil {
		return err
	}
	for _, field := range fields {
		fieldTypes[field.FieldKey] = strings.ToLower(strings.TrimSpace(field.FieldType))
	}
	data := map[string]json.RawMessage{}
	name := strings.TrimSpace(meta.Name)
	phone := strings.TrimSpace(meta.Phone)
	email := strings.TrimSpace(meta.Email)
	contactExtra := strings.TrimSpace(meta.ContactExtra)
	for _, fact := range facts {
		if len(fact.FieldValue) > 0 {
			data[fact.FieldKey] = json.RawMessage(fact.FieldValue)
		}
		value := strings.TrimSpace(jsonbToString(fact.FieldValue))
		switch {
		case fact.FieldKey == "name" || fieldTypes[fact.FieldKey] == "name":
			if name == "" {
				name = value
			}
		case fact.FieldKey == "phone" || fieldTypes[fact.FieldKey] == "phone":
			if phone == "" {
				phone = value
			}
		case fact.FieldKey == "email" || fieldTypes[fact.FieldKey] == "email":
			if email == "" {
				email = value
			}
		case fact.FieldKey == "vk" || fieldTypes[fact.FieldKey] == "vk":
			if contactExtra == "" {
				contactExtra = value
			}
		}
	}
	marketingContext, err := channelMarketingContext(ctx, q, conv)
	if err != nil {
		return err
	}
	data["marketing"] = marketingContext
	if conv.LeadID.Valid {
		if err := q.MergeLeadMarketingContext(ctx, conv.ProfileID, conv.LeadID, marketingContext); err != nil {
			return err
		}
		if contactExtra == "" {
			return nil
		}
		return q.SetLeadContactExtraIfEmpty(ctx, conv.ProfileID, conv.LeadID, toText(contactExtra))
	}
	if normalized, normalizeErr := normalizePhone(phone); normalizeErr == nil {
		phone = normalized
	}
	if phone == "" && meta.PrimaryIntent != "purchase_intent" {
		return nil
	}

	if phone != "" {
		existing, findErr := q.FindOpenLeadByPhone(ctx, storage.FindOpenLeadByPhoneParams{
			ProfileID:    conv.ProfileID,
			ContactPhone: toText(phone),
		})
		if findErr == nil && existing.Valid {
			if updateErr := q.MergeLeadMarketingContext(ctx, conv.ProfileID, existing, marketingContext); updateErr != nil {
				return updateErr
			}
			if contactExtra != "" {
				if updateErr := q.SetLeadContactExtraIfEmpty(ctx, conv.ProfileID, existing, toText(contactExtra)); updateErr != nil {
					return updateErr
				}
			}
			return q.SetConversationLead(ctx, storage.SetConversationLeadParams{
				ID: conv.ID, LeadID: existing,
			})
		}
		if findErr != nil && !errors.Is(findErr, pgx.ErrNoRows) {
			return findErr
		}
	}

	stageID, err := q.GetLeadStartStage(ctx, conv.ProfileID)
	if err != nil {
		return err
	}
	if !stageID.Valid {
		return errors.New("auto-create lead: start stage is missing")
	}
	dataJSON, err := json.Marshal(data)
	if err != nil {
		return err
	}
	leadID, err := q.InsertLeadAutoWithExtra(ctx, storage.InsertLeadAutoWithExtraParams{
		ProfileID:            conv.ProfileID,
		StageID:              stageID,
		Title:                toText(name),
		ContactName:          toText(name),
		ContactPhone:         toText(phone),
		ContactEmail:         toText(email),
		ContactExtra:         toText(contactExtra),
		SourceAgentID:        conv.AgentID,
		SourceConversationID: conv.ID,
		Data:                 dataJSON,
		Summary:              toText(meta.Summary),
		Sentiment:            toText(meta.Sentiment),
		PrimaryIntent:        toText(meta.PrimaryIntent),
	})
	if err != nil {
		return err
	}
	return q.SetConversationLead(ctx, storage.SetConversationLeadParams{ID: conv.ID, LeadID: leadID})
}

func channelMarketingContext(ctx context.Context, q *storage.Queries, conv storage.AgentConversation) (json.RawMessage, error) {
	channel, err := q.GetChannel(ctx, conv.ChannelID)
	if err != nil {
		return nil, err
	}
	result := map[string]any{
		"contact_channel":    normalizeContactChannel(channel.ChannelType),
		"channel_account_id": fromUUID(channel.ID).String(),
		"conversation_id":    fromUUID(conv.ID).String(),
		"external_user_id":   conv.ExternalUserID,
	}
	if conv.ExternalChatID.Valid {
		result["external_chat_id"] = conv.ExternalChatID.String
	}
	var conversationContext map[string]any
	if len(conv.Context) > 0 && json.Unmarshal(conv.Context, &conversationContext) == nil {
		if event, ok := conversationContext["marketing_channel_event"].(map[string]any); ok {
			if externalEventID, ok := event["external_event_id"].(string); ok && externalEventID != "" {
				result["external_event_id"] = externalEventID
			}
			if reference, ok := event["provider_metadata_reference"]; ok {
				result["provider_metadata_reference"] = reference
			}
		}
	}
	return json.Marshal(result)
}

func normalizeContactChannel(channelType string) string {
	normalized := strings.ToLower(strings.TrimSpace(channelType))
	switch normalized {
	case "web":
		return "samba_widget"
	case "telegram", "vk", "max", "whatsapp", "email":
		return normalized
	default:
		return "other"
	}
}
