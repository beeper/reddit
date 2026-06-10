package connector

import (
	"context"
	"errors"
	"fmt"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

var (
	_ bridgev2.EditHandlingNetworkAPI        = (*RedditClient)(nil)
	_ bridgev2.ReactionHandlingNetworkAPI    = (*RedditClient)(nil)
	_ bridgev2.RedactionHandlingNetworkAPI   = (*RedditClient)(nil)
	_ bridgev2.ReadReceiptHandlingNetworkAPI = (*RedditClient)(nil)
	_ bridgev2.TypingHandlingNetworkAPI      = (*RedditClient)(nil)
	_ bridgev2.RoomNameHandlingNetworkAPI    = (*RedditClient)(nil)
)

func (r *RedditClient) HandleMatrixMessage(ctx context.Context, msg *bridgev2.MatrixMessage) (*bridgev2.MatrixMessageResponse, error) {
	if !r.IsLoggedIn() {
		return nil, errors.New("not logged in to Reddit")
	}
	roomID := portalIDToRoomID(msg.Portal.ID)
	content := msg.Content
	if content == nil {
		return nil, errors.New("missing message content")
	}
	resp, err := r.rc.SendMessage(ctx, roomID, content)
	if err != nil {
		return nil, fmt.Errorf("send to reddit: %w", err)
	}
	return &bridgev2.MatrixMessageResponse{
		DB: &database.Message{
			ID:        makeMessageID(resp.EventID),
			MXID:      msg.Event.ID,
			SenderID:  r.userID,
			Timestamp: time.UnixMilli(msg.Event.Timestamp),
		},
	}, nil
}

func (r *RedditClient) HandleMatrixEdit(ctx context.Context, msg *bridgev2.MatrixEdit) error {
	if msg.EditTarget == nil {
		return errors.New("no edit target")
	}
	roomID := portalIDToRoomID(msg.Portal.ID)
	content := msg.Content
	if content == nil {
		return errors.New("missing edit content")
	}
	// Forward the m.replace relation as-is to Reddit's Matrix homeserver.
	if content.RelatesTo == nil {
		content.RelatesTo = &event.RelatesTo{
			Type:    event.RelReplace,
			EventID: messageIDToEventID(msg.EditTarget.ID),
		}
	}
	_, err := r.rc.SendMessage(ctx, roomID, content)
	return err
}

func (r *RedditClient) HandleMatrixMessageRemove(ctx context.Context, msg *bridgev2.MatrixMessageRemove) error {
	if msg.TargetMessage == nil {
		return errors.New("no redaction target")
	}
	roomID := portalIDToRoomID(msg.Portal.ID)
	reason := ""
	if msg.Content != nil {
		reason = msg.Content.Reason
	}
	_, err := r.matrix().RedactEvent(ctx, roomID, messageIDToEventID(msg.TargetMessage.ID), mautrix.ReqRedact{Reason: reason})
	return err
}

func (r *RedditClient) PreHandleMatrixReaction(ctx context.Context, msg *bridgev2.MatrixReaction) (bridgev2.MatrixReactionPreResponse, error) {
	emoji := ""
	if msg.Content != nil && msg.Content.RelatesTo.Key != "" {
		emoji = msg.Content.RelatesTo.Key
	}
	return bridgev2.MatrixReactionPreResponse{
		SenderID:     r.userID,
		EmojiID:      networkid.EmojiID(emoji),
		Emoji:        emoji,
		MaxReactions: 1,
	}, nil
}

func (r *RedditClient) HandleMatrixReaction(ctx context.Context, msg *bridgev2.MatrixReaction) (*database.Reaction, error) {
	if msg.TargetMessage == nil {
		return nil, errors.New("no reaction target")
	}
	roomID := portalIDToRoomID(msg.Portal.ID)
	emoji := string(msg.PreHandleResp.EmojiID)
	_, err := r.matrix().SendReaction(ctx, roomID, messageIDToEventID(msg.TargetMessage.ID), emoji)
	if err != nil {
		return nil, err
	}
	return &database.Reaction{}, nil
}

func (r *RedditClient) HandleMatrixReactionRemove(ctx context.Context, msg *bridgev2.MatrixReactionRemove) error {
	if msg.TargetReaction == nil {
		return errors.New("no reaction to remove")
	}
	roomID := portalIDToRoomID(msg.Portal.ID)
	_, err := r.matrix().RedactEvent(ctx, roomID, id.EventID(msg.TargetReaction.MXID), mautrix.ReqRedact{})
	return err
}

func (r *RedditClient) HandleMatrixReadReceipt(ctx context.Context, msg *bridgev2.MatrixReadReceipt) error {
	roomID := portalIDToRoomID(msg.Portal.ID)
	if msg.ExactMessage == nil {
		return nil
	}
	return r.rc.MarkRead(ctx, roomID, messageIDToEventID(msg.ExactMessage.ID))
}

func (r *RedditClient) HandleMatrixTyping(ctx context.Context, msg *bridgev2.MatrixTyping) error {
	roomID := portalIDToRoomID(msg.Portal.ID)
	return r.rc.SetTyping(ctx, roomID, msg.IsTyping, 30*time.Second)
}

func (r *RedditClient) HandleMatrixRoomName(ctx context.Context, msg *bridgev2.MatrixRoomName) (bool, error) {
	roomID := portalIDToRoomID(msg.Portal.ID)
	if msg.Content == nil {
		return false, nil
	}
	_, err := r.matrix().SendStateEvent(ctx, roomID, event.StateRoomName, "", msg.Content)
	if err != nil {
		return false, err
	}
	msg.Portal.Name = msg.Content.Name
	msg.Portal.NameSet = true
	return true, nil
}
