package connector

import (
	"context"
	"errors"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
)

// convertMessage turns a Reddit-side m.room.message event into a bridgev2
// ConvertedMessage. Event IDs belong to different servers: relation targets
// must be resolved by bridgev2 rather than forwarding Reddit IDs to Beeper.
func (r *RedditClient) convertMessage(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, src *event.Event) (*bridgev2.ConvertedMessage, error) {
	if src == nil {
		return nil, errors.New("nil source event")
	}
	if err := src.Content.ParseRaw(event.EventMessage); err != nil && !errors.Is(err, event.ErrContentAlreadyParsed) {
		return nil, unbridgeable(err)
	}
	original, ok := src.Content.Parsed.(*event.MessageEventContent)
	if !ok || original == nil {
		return nil, unbridgeable(errors.New("reddit message has invalid content"))
	}
	if original.RelatesTo.GetReplaceID() != "" {
		return nil, unbridgeable(errors.New("reddit edit requires reconciliation; refusing to create a duplicate message"))
	}
	content := *original
	content.RemoveReplyFallback()
	content.RelatesTo = nil
	content.NewContent = nil
	switch content.MsgType {
	case event.MsgImage:
		if err := r.receiveMedia(ctx, portal, intent, &content); err != nil {
			return nil, err
		}
	case event.MsgText, event.MsgNotice, event.MsgEmote:
	default:
		return nil, bridgev2.ErrUnsupportedMessageType
	}

	out := &bridgev2.ConvertedMessage{
		Parts: []*bridgev2.ConvertedMessagePart{
			{
				ID:      networkid.PartID(""),
				Type:    event.EventMessage,
				Content: &content,
			},
		},
	}

	// Translate m.relates_to into bridgev2's reply field so threading works
	// across the bridge.
	if target := original.RelatesTo.GetNonFallbackReplyTo(); target != "" {
		out.ReplyTo = &networkid.MessageOptionalPartID{MessageID: makeMessageID(target)}
	}
	if root := original.RelatesTo.GetThreadParent(); root != "" {
		mappedRoot := makeMessageID(root)
		out.ThreadRoot = &mappedRoot
	}
	return out, nil
}
