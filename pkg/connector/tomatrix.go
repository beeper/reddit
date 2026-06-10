package connector

import (
	"context"
	"errors"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
)

// convertMessage turns a Reddit-side m.room.message event into a bridgev2
// ConvertedMessage. Because Reddit chat is itself Matrix-backed, the content
// shape on both sides is the same Matrix event format; we mostly forward it.
// Media URIs that point at Reddit's homeserver are still served over Matrix
// so consumers (Beeper homeserver) can fetch them via the standard media API.
func (r *RedditClient) convertMessage(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, src *event.Event) (*bridgev2.ConvertedMessage, error) {
	if src == nil {
		return nil, errors.New("nil source event")
	}
	content, ok := src.Content.Parsed.(*event.MessageEventContent)
	if !ok {
		// Fall back to raw content.
		content = &event.MessageEventContent{}
		_ = src.Content.ParseRaw(event.EventMessage)
		if parsed, ok := src.Content.Parsed.(*event.MessageEventContent); ok {
			content = parsed
		}
	}

	out := &bridgev2.ConvertedMessage{
		Parts: []*bridgev2.ConvertedMessagePart{
			{
				ID:      networkid.PartID(""),
				Type:    event.EventMessage,
				Content: content,
			},
		},
	}

	// Translate m.relates_to into bridgev2's reply field so threading works
	// across the bridge.
	if content.RelatesTo != nil {
		if content.RelatesTo.InReplyTo != nil && content.RelatesTo.InReplyTo.EventID != "" {
			out.ReplyTo = &networkid.MessageOptionalPartID{
				MessageID: makeMessageID(content.RelatesTo.InReplyTo.EventID),
			}
		}
	}
	return out, nil
}
