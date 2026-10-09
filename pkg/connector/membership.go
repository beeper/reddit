package connector

import (
	"context"
	"errors"
	"strings"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
)

var _ bridgev2.MembershipHandlingNetworkAPI = (*RedditClient)(nil)

func (r *RedditClient) HandleMatrixMembership(ctx context.Context, msg *bridgev2.MatrixMembershipChange) (*bridgev2.MatrixMembershipResult, error) {
	if msg == nil || msg.Portal == nil || msg.Portal.ID == "" {
		return nil, errors.New("membership change has no portal identity")
	}
	if msg.Type.IsSelf && msg.OrigSender != nil {
		return nil, bridgev2.ErrMembershipNotSupported
	}
	if msg.Portal.Receiver != "" && msg.Portal.Receiver != r.userLogin.ID {
		return nil, errors.New("chat belongs to another login")
	}
	if !r.IsLoggedIn() {
		return nil, bridgev2.ErrNotLoggedIn
	}
	switch msg.Type {
	case bridgev2.ProfileChange:
		return nil, nil
	case bridgev2.Leave, bridgev2.RejectInvite:
		// This also preserves Reddit's hide-instead-of-leave semantics for DMs.
		return nil, r.HandleMatrixDeleteChat(ctx, &bridgev2.MatrixDeleteChat{
			Portal: msg.Portal, Content: &event.BeeperChatDeleteEventContent{},
		})
	case bridgev2.AcceptInvite:
		return nil, r.HandleMatrixAcceptMessageRequest(ctx, &bridgev2.MatrixAcceptMessageRequest{Portal: msg.Portal})
	case bridgev2.Invite, bridgev2.Kick, bridgev2.RevokeInvite:
	default:
		return nil, bridgev2.ErrMembershipNotSupported
	}
	roomID := portalIDToRoomID(msg.Portal.ID)
	state, key, err := r.resolveRoomState(ctx, roomID, &mautrix.SyncJoinedRoom{}, &roomSnapshots{})
	if err != nil {
		return nil, err
	}
	if key != msg.Portal.PortalKey || state.Type != "private_group" {
		return nil, bridgev2.ErrMembershipNotSupported
	}
	var targetID networkid.UserID
	switch target := msg.Target.(type) {
	case *bridgev2.Ghost:
		targetID = target.ID
	case *bridgev2.UserLogin:
		targetID = networkid.UserID(target.ID)
	}
	if !strings.HasPrefix(string(targetID), "t2_") || strings.Trim(string(targetID)[3:], "0123456789abcdefghijklmnopqrstuvwxyz") != "" || len(targetID) <= 3 {
		return nil, errors.New("membership target is not a Reddit account ID")
	}
	if targetID == r.userID {
		return nil, errors.New("use accept or leave for your own membership")
	}
	targetMXID := userIDToMatrix(targetID, "")
	if msg.Type == bridgev2.Invite {
		err = r.remote().Invite(ctx, roomID, targetMXID)
	} else {
		_, err = r.matrix().KickUser(ctx, roomID, &mautrix.ReqKickUser{UserID: targetMXID})
	}
	return nil, err
}
