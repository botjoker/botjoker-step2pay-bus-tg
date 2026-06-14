package agentstore

import (
	"context"

	"github.com/botjoker/sambacrm-business-tg/internal/runtime"
	"github.com/botjoker/sambacrm-business-tg/internal/storage"
	"github.com/google/uuid"
)

// DBConvResolver реализует runtime.ConvResolver: conversation → маршрут доставки.
type DBConvResolver struct {
	q *storage.Queries
}

func NewConvResolver(q *storage.Queries) *DBConvResolver { return &DBConvResolver{q: q} }

var _ runtime.ConvResolver = (*DBConvResolver)(nil)

func (r *DBConvResolver) Resolve(ctx context.Context, convID uuid.UUID) (runtime.ConvRoute, error) {
	conv, err := r.q.GetAgentConversation(ctx, toUUID(convID))
	if err != nil {
		return runtime.ConvRoute{}, err
	}
	ch, err := r.q.GetChannel(ctx, conv.ChannelID)
	if err != nil {
		return runtime.ConvRoute{}, err
	}
	return runtime.ConvRoute{
		ProfileID:      fromUUID(conv.ProfileID),
		ChannelType:    ch.ChannelType,
		ChannelID:      fromUUID(ch.ID),
		ExternalUserID: conv.ExternalUserID,
	}, nil
}
