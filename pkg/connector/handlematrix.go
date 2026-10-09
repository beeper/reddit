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
	txn, err := r.outgoingTransaction(msg.Portal, msg.Event, msg.InputTransactionID)
	if err != nil {
		return nil, err
	}
	content, err := outgoingContent(msg)
	if err != nil {
		return nil, err
	}
	// Beeper represents GIF attachments as m.video + fi.mau.gif. Reddit
	// accepts the original GIF bytes as an image, not an ordinary video.
	if content.GetCapMsgType() == event.CapMsgGIF && content.Info.MimeType == "image/gif" {
		content.MsgType = event.MsgImage
		copyMediaInfo(content)
		content.Info.MauGIF = false
	}
	switch content.MsgType {
	case event.MsgImage:
		if err = r.sendMedia(ctx, content); err != nil {
			return nil, err
		}
	case event.MsgText, event.MsgNotice, event.MsgEmote:
	default:
		return nil, bridgev2.ErrUnsupportedMessageType
	}
	resp, err := r.remote().SendMessage(ctx, portalIDToRoomID(msg.Portal.ID), content, mautrix.ReqSendEvent{TransactionID: txn})
	if err != nil {
		return nil, fmt.Errorf("send to reddit: %w", err)
	}
	if resp.EventID == "" {
		return nil, errors.New("reddit returned no message identity")
	}
	return &bridgev2.MatrixMessageResponse{
		DB: &database.Message{
			ID:         makeMessageID(resp.EventID),
			SendTxnID:  msg.InputTransactionID,
			SenderID:   r.userID,
			Timestamp:  time.UnixMilli(msg.Event.Timestamp),
			ThreadRoot: makeMessageID(content.RelatesTo.GetThreadParent()),
		},
	}, nil
}

func (r *RedditClient) HandleMatrixEdit(ctx context.Context, msg *bridgev2.MatrixEdit) error {
	// The observed Reddit Chat menu has no edit action. Do not claim support or
	// turn an attempted edit into a second message while that contract is unknown.
	return bridgev2.ErrEditsNotSupported
}

func (r *RedditClient) HandleMatrixMessageRemove(ctx context.Context, msg *bridgev2.MatrixMessageRemove) error {
	txn, err := r.outgoingTransaction(msg.Portal, msg.Event, msg.InputTransactionID)
	if err != nil {
		return err
	}
	target, err := mappedTarget(msg.Portal, msg.TargetMessage)
	if err != nil {
		return err
	}
	roomID := portalIDToRoomID(msg.Portal.ID)
	reason := ""
	if msg.Content != nil {
		reason = msg.Content.Reason
	}
	_, err = r.matrix().RedactEvent(ctx, roomID, target, mautrix.ReqRedact{Reason: reason, TxnID: txn})
	return err
}

func (r *RedditClient) PreHandleMatrixReaction(ctx context.Context, msg *bridgev2.MatrixReaction) (bridgev2.MatrixReactionPreResponse, error) {
	emoji := ""
	if msg.Content != nil && msg.Content.RelatesTo.Key != "" {
		emoji = msg.Content.RelatesTo.Key
	}
	key, err := r.resolveOutgoingReaction(ctx, emoji)
	if err != nil {
		return bridgev2.MatrixReactionPreResponse{}, err
	}
	return bridgev2.MatrixReactionPreResponse{
		SenderID: r.userID,
		EmojiID:  networkid.EmojiID(key),
		Emoji:    emoji,
	}, nil
}

func (r *RedditClient) HandleMatrixReaction(ctx context.Context, msg *bridgev2.MatrixReaction) (*database.Reaction, error) {
	txn, err := r.outgoingTransaction(msg.Portal, msg.Event, msg.InputTransactionID)
	if err != nil {
		return nil, err
	}
	target, err := mappedTarget(msg.Portal, msg.TargetMessage)
	if err != nil {
		return nil, err
	}
	if msg.PreHandleResp == nil || msg.PreHandleResp.EmojiID == "" {
		return nil, errors.New("reaction has no emoji")
	}
	roomID := portalIDToRoomID(msg.Portal.ID)
	emoji := string(msg.PreHandleResp.EmojiID)
	resp, err := r.matrix().SendMessageEvent(ctx, roomID, event.EventReaction, &event.ReactionEventContent{RelatesTo: event.RelatesTo{Type: event.RelAnnotation, EventID: target, Key: emoji}}, mautrix.ReqSendEvent{TransactionID: txn})
	if err != nil {
		return nil, err
	}
	if resp.EventID == "" {
		return nil, errors.New("reddit returned no reaction identity")
	}
	return &database.Reaction{Metadata: &ReactionMetadata{RemoteEventID: resp.EventID}}, nil
}

func (r *RedditClient) HandleMatrixReactionRemove(ctx context.Context, msg *bridgev2.MatrixReactionRemove) error {
	txn, err := r.outgoingTransaction(msg.Portal, msg.Event, msg.InputTransactionID)
	if err != nil {
		return err
	}
	if msg.TargetReaction == nil || msg.TargetReaction.Room != msg.Portal.PortalKey {
		return errors.New("no reaction to remove")
	}
	meta, ok := msg.TargetReaction.Metadata.(*ReactionMetadata)
	if !ok || meta.RemoteEventID == "" {
		return errors.New("reaction has no Reddit event mapping")
	}
	roomID := portalIDToRoomID(msg.Portal.ID)
	_, err = r.matrix().RedactEvent(ctx, roomID, meta.RemoteEventID, mautrix.ReqRedact{TxnID: txn})
	return err
}

func (r *RedditClient) HandleMatrixReadReceipt(ctx context.Context, msg *bridgev2.MatrixReadReceipt) error {
	roomID := portalIDToRoomID(msg.Portal.ID)
	if msg.ExactMessage == nil {
		return nil
	}
	return r.remote().MarkRead(ctx, roomID, messageIDToEventID(msg.ExactMessage.ID))
}

func (r *RedditClient) HandleMatrixTyping(ctx context.Context, msg *bridgev2.MatrixTyping) error {
	// Reddit requires joined membership for typing. Composing a reply must
	// not accept the request; bridgev2 clears this flag after explicit acceptance.
	if msg.Portal.MessageRequest {
		return nil
	}
	roomID := portalIDToRoomID(msg.Portal.ID)
	return r.remote().SetTyping(ctx, roomID, msg.IsTyping, 30*time.Second)
}

func (r *RedditClient) HandleMatrixRoomName(ctx context.Context, msg *bridgev2.MatrixRoomName) (bool, error) {
	if msg.Portal.RoomType == database.RoomTypeDM {
		return false, bridgev2.ErrRoomMetadataNotAllowed
	}
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
