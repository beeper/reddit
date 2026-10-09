package connector

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// Reddit's messages endpoint excludes reactions. Fetch the separate relation
// stream to its explicit terminal cursor, including short and empty pages.
// Validate the complete result before applying any of it to an existing chat.
func (r *RedditClient) fetchReactionEvents(ctx context.Context, key networkid.PortalKey, target networkid.MessageID) ([]*event.Event, error) {
	if target == "" || (key.Receiver != "" && key.Receiver != r.userLogin.ID) {
		return nil, errors.New("invalid reaction history target or owner")
	}
	var events []*event.Event
	cursor := ""
	seen := map[string]bool{}
	for {
		page, err := r.remote().Reactions(ctx, portalIDToRoomID(key.ID), messageIDToEventID(target), cursor)
		if isPermanentHTTPError(err) {
			return nil, unbridgeable(fmt.Errorf("fetch Reddit reactions: %w", err))
		} else if err != nil {
			return nil, fmt.Errorf("fetch Reddit reactions: %w", err)
		}
		for _, evt := range page.Chunk {
			if evt == nil || evt.Type != event.EventReaction || evt.ID == "" || (evt.RoomID != "" && makePortalID(evt.RoomID) != key.ID) {
				return nil, unbridgeable(errors.New("reaction history has an invalid event identity"))
			}
			local, server, err := evt.Sender.Parse()
			if err != nil || server != "reddit.com" || !strings.HasPrefix(local, "t2_") {
				return nil, unbridgeable(errors.New("reaction history has an invalid sender"))
			}
			if err = evt.Content.ParseRaw(event.EventReaction); err != nil && !errors.Is(err, event.ErrContentAlreadyParsed) {
				return nil, unbridgeable(err)
			}
			relation := evt.Content.AsReaction().RelatesTo
			if relation.Type != event.RelAnnotation || makeMessageID(relation.EventID) != target || relation.Key == "" {
				return nil, unbridgeable(errors.New("reaction history belongs to another message"))
			}
			if redaction := evt.Unsigned.RedactedBecause; redaction != nil {
				if redaction.Type != event.EventRedaction || redaction.Redacts != evt.ID || (redaction.RoomID != "" && makePortalID(redaction.RoomID) != key.ID) {
					return nil, unbridgeable(errors.New("reaction history has an invalid removal identity"))
				}
			}
			events = append(events, evt)
		}
		if page.NextBatch == "" {
			return events, nil
		}
		if page.NextBatch == cursor || seen[page.NextBatch] {
			return nil, unbridgeable(errors.New("reddit reaction history cursor did not advance"))
		}
		seen[page.NextBatch] = true
		cursor = page.NextBatch
	}
}

func (r *RedditClient) backfillReactions(ctx context.Context, key networkid.PortalKey, target networkid.MessageID) ([]*bridgev2.BackfillReaction, error) {
	events, err := r.fetchReactionEvents(ctx, key, target)
	if err != nil {
		return nil, err
	}
	var reactions []*bridgev2.BackfillReaction
	seen := map[string]id.EventID{}
	for _, evt := range events {
		if evt.Unsigned.RedactedBecause != nil {
			continue
		}
		key := evt.Content.AsReaction().RelatesTo.Key
		identity := string(evt.Sender) + "\x00" + key
		if previous := seen[identity]; previous != "" {
			if previous != evt.ID {
				return nil, unbridgeable(errors.New("reaction history has conflicting active identities"))
			}
			continue
		}
		seen[identity] = evt.ID
		emoji, shortcode, err := r.reactionEmoji(ctx, key)
		if err != nil {
			return nil, err
		}
		reaction := &bridgev2.BackfillReaction{Sender: r.makeSender(evt.Sender), EmojiID: networkid.EmojiID(key), Emoji: emoji, Timestamp: time.UnixMilli(evt.Timestamp), DBMetadata: &ReactionMetadata{RemoteEventID: evt.ID}}
		if shortcode != "" {
			reaction.ExtraContent = map[string]any{"com.beeper.reaction.shortcode": shortcode}
		}
		reactions = append(reactions, reaction)
	}
	return reactions, nil
}
