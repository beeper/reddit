package connector

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

func validateNativeEvent(key networkid.PortalKey, evt *event.Event) error {
	if evt == nil || evt.ID == "" || (evt.RoomID != "" && makePortalID(evt.RoomID) != key.ID) {
		return errors.New("reddit event has an invalid room or event identity")
	}
	local, server, err := evt.Sender.Parse()
	if err != nil || server != "reddit.com" || !strings.HasPrefix(local, "t2_") || len(local) <= 3 {
		return errors.New("reddit event has an invalid sender")
	}
	return nil
}

func redactionTarget(evt *event.Event) (id.EventID, error) {
	if evt == nil || evt.Type != event.EventRedaction {
		return "", errors.New("expected a Reddit redaction")
	}
	if err := evt.Content.ParseRaw(event.EventRedaction); err != nil && !errors.Is(err, event.ErrContentAlreadyParsed) {
		return "", err
	}
	target := evt.Redacts
	if content, ok := evt.Content.Parsed.(*event.RedactionEventContent); ok && content.Redacts != "" {
		if target != "" && target != content.Redacts {
			return "", errors.New("reddit redaction has conflicting targets")
		}
		target = content.Redacts
	}
	if target == "" {
		return "", errors.New("reddit redaction has no target")
	}
	return target, nil
}

func validateTombstone(key networkid.PortalKey, target *event.Event) error {
	if err := validateNativeEvent(key, target); err != nil {
		return err
	}
	removal := target.Unsigned.RedactedBecause
	if err := validateNativeEvent(key, removal); err != nil {
		return fmt.Errorf("invalid Reddit deletion proof: %w", err)
	}
	removedID, err := redactionTarget(removal)
	if err != nil || removedID != target.ID {
		return errors.New("reddit deletion proof belongs to another event")
	}
	if target.Type != event.EventMessage && target.Type != event.EventReaction {
		return errors.New("unsupported deleted Reddit event type")
	}
	return nil
}

// Reddit redactions identify an event, while bridgev2 reaction removals identify
// a message, sender and emoji. Resolve that native identity through Reddit's
// event endpoint; the framework owns the corresponding local lookup/removal.
func (r *RedditClient) convertRedaction(ctx context.Context, key networkid.PortalKey, removal *event.Event) (bridgev2.RemoteEvent, error) {
	if err := validateNativeEvent(key, removal); err != nil {
		return nil, unbridgeable(err)
	}
	targetID, err := redactionTarget(removal)
	if err != nil {
		return nil, unbridgeable(err)
	}
	target, err := r.remote().GetEvent(ctx, portalIDToRoomID(key.ID), targetID)
	if isPermanentHTTPError(err) {
		return nil, unbridgeable(fmt.Errorf("resolve deleted Reddit event: %w", err))
	} else if err != nil {
		return nil, fmt.Errorf("resolve deleted Reddit event: %w", err)
	}
	if target == nil || target.ID != targetID {
		return nil, unbridgeable(errors.New("reddit returned another deletion target"))
	}
	if err = validateTombstone(key, target); err != nil {
		return nil, unbridgeable(err)
	}
	if proof := target.Unsigned.RedactedBecause; proof.ID != removal.ID || proof.Sender != removal.Sender || proof.Timestamp != removal.Timestamp {
		return nil, unbridgeable(errors.New("reddit returned a different deletion proof"))
	}
	removed, err := r.convertDeletedEvent(key, target)
	return removed, unbridgeable(err)
}

func (r *RedditClient) convertDeletedEvent(key networkid.PortalKey, target *event.Event) (bridgev2.RemoteEvent, error) {
	if key.Receiver != "" && key.Receiver != r.userLogin.ID {
		return nil, errors.New("deletion portal belongs to another login")
	}
	if err := validateTombstone(key, target); err != nil {
		return nil, err
	}
	removal := target.Unsigned.RedactedBecause
	meta := simplevent.EventMeta{PortalKey: key, Sender: r.makeSender(removal.Sender), Timestamp: time.UnixMilli(removal.Timestamp)}
	if target.Type == event.EventMessage {
		meta.Type = bridgev2.RemoteEventMessageRemove
		return &simplevent.MessageRemove{EventMeta: meta, TargetMessage: makeMessageID(target.ID)}, nil
	}
	if err := target.Content.ParseRaw(event.EventReaction); err != nil && !errors.Is(err, event.ErrContentAlreadyParsed) {
		return nil, err
	}
	relation := target.Content.AsReaction().RelatesTo
	if relation.Type != event.RelAnnotation || relation.EventID == "" || relation.Key == "" {
		return nil, errors.New("deleted Reddit reaction has no target or emoji")
	}
	meta.Type = bridgev2.RemoteEventReactionRemove
	// Reaction removal identifies the original reactor, even for a moderator's
	// redaction. The SDK selects the intent and local reaction to remove.
	meta.Sender = r.makeSender(target.Sender)
	return &simplevent.Reaction{EventMeta: meta, TargetMessage: makeMessageID(relation.EventID), EmojiID: networkid.EmojiID(relation.Key)}, nil
}
