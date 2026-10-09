package connector

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// Reddit's native sync contains m.read and m.read.private with thread_id and
// ts. Bridgev2's receipt interface represents public room receipts, so private
// or thread-specific receipts must not be widened to that interface.
func (r *RedditClient) convertReadReceipts(key networkid.PortalKey, evt *event.Event) ([]*simplevent.Receipt, error) {
	if evt.RoomID != "" && makePortalID(evt.RoomID) != key.ID {
		return nil, errors.New("read receipt belongs to another room")
	}
	if key.Receiver != "" && key.Receiver != r.userLogin.ID {
		return nil, errors.New("read receipt portal belongs to another login")
	}
	if err := evt.Content.ParseRaw(event.EphemeralEventReceipt); err != nil && !errors.Is(err, event.ErrContentAlreadyParsed) {
		return nil, err
	}
	content, ok := evt.Content.Parsed.(*event.ReceiptEventContent)
	if !ok || content == nil {
		return nil, errors.New("invalid read receipt content")
	}
	bySender := make(map[id.UserID]*simplevent.Receipt)
	for target, receipts := range *content {
		for sender, receipt := range receipts[event.ReceiptTypeRead] {
			if receipt.ThreadID != "" && receipt.ThreadID != event.ReadReceiptThreadMain {
				continue
			}
			localpart, server, err := sender.Parse()
			if err != nil || server != "reddit.com" || !strings.HasPrefix(localpart, "t2_") || target == "" {
				return nil, errors.New("read receipt has an invalid sender or target identity")
			}
			converted := bySender[sender]
			if converted == nil {
				converted = &simplevent.Receipt{EventMeta: simplevent.EventMeta{
					Type: bridgev2.RemoteEventReadReceipt, PortalKey: key, Sender: r.makeSender(sender),
				}}
				bySender[sender] = converted
			}
			converted.Targets = append(converted.Targets, makeMessageID(target))
			if receipt.Timestamp.After(converted.Timestamp) {
				converted.Timestamp = receipt.Timestamp
			}
			// ReadUpTo remains zero: the receipt timestamp is when the user read
			// something, not the creation time of the last message they read.
		}
	}
	senders := make([]id.UserID, 0, len(bySender))
	for sender := range bySender {
		senders = append(senders, sender)
	}
	slices.Sort(senders)
	out := make([]*simplevent.Receipt, 0, len(senders))
	for _, sender := range senders {
		receipt := bySender[sender]
		slices.Sort(receipt.Targets)
		out = append(out, receipt)
	}
	return out, nil
}

func (r *RedditClient) handleEphemeralEvents(ctx context.Context, key networkid.PortalKey, events []*event.Event) {
	for _, evt := range events {
		if evt == nil {
			continue
		}
		if evt.Type == event.EphemeralEventTyping {
			typings, err := r.convertTyping(key, evt)
			if err != nil {
				zerolog.Ctx(ctx).Err(err).Stringer("portal_key", key).Msg("Failed to convert Reddit typing event, skipping")
				continue
			}
			for _, typing := range typings {
				r.userLogin.QueueRemoteEvent(typing)
			}
			continue
		}
		if evt.Type != event.EphemeralEventReceipt {
			continue
		}
		receipts, err := r.convertReadReceipts(key, evt)
		if err != nil {
			zerolog.Ctx(ctx).Err(err).Stringer("portal_key", key).Msg("Failed to convert Reddit read receipt, skipping")
			continue
		}
		for _, receipt := range receipts {
			if receipt.Timestamp.IsZero() {
				receipt.Timestamp = time.Now()
			}
			r.userLogin.QueueRemoteEvent(receipt)
		}
	}
}
