package connector

import (
	"context"
	"errors"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/event"
)

func (r *RedditClient) convertReaction(ctx context.Context, key networkid.PortalKey, evt *event.Event) (*simplevent.Reaction, error) {
	content, ok := evt.Content.Parsed.(*event.ReactionEventContent)
	if !ok || content.RelatesTo.EventID == "" || content.RelatesTo.Type != event.RelAnnotation || content.RelatesTo.Key == "" {
		return nil, unbridgeable(errors.New("reaction is missing its target"))
	}
	emoji, shortcode, err := r.reactionEmoji(ctx, content.RelatesTo.Key)
	if err != nil {
		return nil, err
	}
	reaction := &simplevent.Reaction{
		EventMeta: simplevent.EventMeta{
			Type: bridgev2.RemoteEventReaction, PortalKey: key,
			Sender: r.makeSender(evt.Sender), Timestamp: time.UnixMilli(evt.Timestamp),
		},
		ReactionDBMeta: &ReactionMetadata{RemoteEventID: evt.ID},
		EmojiID:        networkid.EmojiID(content.RelatesTo.Key), Emoji: emoji,
		TargetMessage: makeMessageID(content.RelatesTo.EventID),
	}
	if shortcode != "" {
		reaction.ExtraContent = map[string]any{"com.beeper.reaction.shortcode": shortcode}
	}
	return reaction, nil
}
