package connector

import (
	"context"
	"errors"
	"strings"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
)

// convertMessage turns a Reddit-side m.room.message or m.sticker event into a bridgev2
// ConvertedMessage. Event IDs belong to different servers: relation targets
// must be resolved by bridgev2 rather than forwarding Reddit IDs to Beeper.
func (r *RedditClient) convertMessage(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, src *event.Event) (*bridgev2.ConvertedMessage, error) {
	if src == nil {
		return nil, errors.New("nil source event")
	}
	if src.Unsigned.RedactedBecause != nil {
		if portal == nil {
			return nil, errors.New("deleted message requires a portal")
		}
		if err := validateTombstone(portal.PortalKey, src); err != nil {
			zerolog.Ctx(ctx).Warn().Err(err).Stringer("event_id", src.ID).Msg("Reddit deletion proof failed validation, hiding deleted message")
		}
		return &bridgev2.ConvertedMessage{Parts: []*bridgev2.ConvertedMessagePart{{
			ID: "", Type: event.EventMessage, Content: &event.MessageEventContent{}, DontBridge: true,
		}}}, nil
	}
	if err := src.Content.ParseRaw(src.Type); err != nil && !errors.Is(err, event.ErrContentAlreadyParsed) {
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
	partType := event.EventMessage
	switch {
	case src.Type == event.EventSticker, content.MsgType == event.MsgImage:
		if err := r.receiveMedia(ctx, portal, intent, &content); err != nil {
			return nil, err
		}
		if src.Type == event.EventSticker {
			partType = event.EventSticker
			if key, ok := strings.CutPrefix(string(original.URL), redditStickerPrefix); ok {
				content.Info.BridgedSticker = &event.BridgedSticker{Network: "reddit", ID: key, PackURL: stickerPackURL}
			}
		}
	case content.MsgType == event.MsgText, content.MsgType == event.MsgNotice, content.MsgType == event.MsgEmote:
		if portal != nil {
			r.convertMentions(portal, &content)
		}
	default:
		return nil, bridgev2.ErrUnsupportedMessageType
	}

	out := &bridgev2.ConvertedMessage{
		Parts: []*bridgev2.ConvertedMessagePart{
			{
				ID:      networkid.PartID(""),
				Type:    partType,
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
