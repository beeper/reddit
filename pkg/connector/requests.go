package connector

import (
	"context"
	"encoding/json"
	"errors"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/beeper/reddit/pkg/redditchat"
)

var _ bridgev2.MessageRequestAcceptingNetworkAPI = (*RedditClient)(nil)
var _ bridgev2.DeleteChatHandlingNetworkAPI = (*RedditClient)(nil)

// One full sync is shared by every room in a sync pass that needs it.
type roomSnapshots struct {
	full *redditchat.SyncResponse
}

// Reddit's full sync supplies missing room state during provisioning and
// native unhide. Retain only requested rooms and leave the poll cursor alone.
func (r *RedditClient) fetchRoomSnapshots(ctx context.Context, snapshots *roomSnapshots, rooms []id.RoomID) (*redditchat.SyncResponse, error) {
	if snapshots.full == nil {
		full, err := r.remote().Sync(ctx, "", 0)
		if err != nil {
			return nil, err
		}
		snapshots.full = full
	}
	full := snapshots.full
	state := &redditchat.SyncResponse{}
	state.Rooms.Join = make(map[id.RoomID]*mautrix.SyncJoinedRoom)
	state.Rooms.Invite = make(map[id.RoomID]*mautrix.SyncInvitedRoom)
	state.Rooms.Peek = make(map[id.RoomID]*mautrix.SyncJoinedRoom)
	for _, room := range rooms {
		if joined := full.Rooms.Join[room]; joined != nil {
			state.Rooms.Join[room] = joined
		} else if invited := full.Rooms.Invite[room]; invited != nil {
			state.Rooms.Invite[room] = invited
		}
		if peek := full.Rooms.Peek[room]; peek != nil {
			state.Rooms.Peek[room] = peek
		}
	}
	return state, nil
}

// Reddit's Ignore action hides a DM for this account; group requests are left.
// Bridgev2 owns local room cleanup after the native operation succeeds.
func (r *RedditClient) HandleMatrixDeleteChat(ctx context.Context, msg *bridgev2.MatrixDeleteChat) error {
	if msg == nil || msg.Portal == nil || msg.Portal.ID == "" || msg.Content == nil {
		return errors.New("chat deletion has no portal identity or content")
	}
	if msg.Portal.Receiver != "" && msg.Portal.Receiver != r.userLogin.ID {
		return errors.New("chat belongs to another login")
	}
	if msg.Content.DeleteForEveryone {
		return errors.New("reddit chats cannot be deleted for everyone")
	}
	roomID := portalIDToRoomID(msg.Portal.ID)
	state, key, err := r.resolveRoomState(ctx, roomID, &mautrix.SyncJoinedRoom{}, &roomSnapshots{})
	if err != nil {
		return err
	}
	if key != msg.Portal.PortalKey {
		return errors.New("chat ownership does not match Reddit room state")
	}
	switch state.Type {
	case "direct":
		return r.matrix().SetRoomAccountData(ctx, roomID, "com.reddit.hidden_chat", map[string]bool{"hidden": true})
	case "private_group":
		_, err = r.matrix().LeaveRoom(ctx, roomID)
		return err
	default:
		return errors.New("unsupported Reddit chat type for deletion")
	}
}

func (r *RedditClient) inviteSnapshot(invited *mautrix.SyncInvitedRoom) (*mautrix.SyncJoinedRoom, error) {
	if invited == nil {
		return nil, errors.New("missing Reddit invitation state")
	}
	joined := &mautrix.SyncJoinedRoom{State: mautrix.SyncEventsList{Events: append([]*event.Event(nil), invited.State.Events...)}}
	var hasType, ownInvite, isDirect bool
	for _, evt := range invited.State.Events {
		if evt == nil || evt.StateKey == nil {
			continue
		}
		if evt.Type.Type == "com.reddit.chat.type" {
			hasType = true
		}
		if evt.Type == event.StateMember && *evt.StateKey == string(userIDToMatrix(r.userID, "")) {
			data, err := json.Marshal(&evt.Content)
			if err != nil {
				return nil, err
			}
			var member event.MemberEventContent
			if err = json.Unmarshal(data, &member); err != nil {
				return nil, err
			}
			ownInvite, isDirect = member.Membership == event.MembershipInvite, member.IsDirect
		}
	}
	if !ownInvite {
		return nil, errors.New("reddit invitation does not identify the logged-in account")
	}
	// Native DM membership carries is_direct. If stripped invite state omits
	// com.reddit.chat.type, this is an explicit identity hint, not a member-count
	// guess. Unknown non-DM types remain unclassified and cannot be auto-joined.
	if !hasType && isDirect {
		key := ""
		joined.State.Events = append(joined.State.Events, &event.Event{Type: event.Type{Type: "com.reddit.chat.type", Class: event.StateEventType}, StateKey: &key, Content: event.Content{Raw: map[string]any{"type": "direct"}}})
	}
	return joined, nil
}

func (r *RedditClient) HandleMatrixAcceptMessageRequest(ctx context.Context, msg *bridgev2.MatrixAcceptMessageRequest) error {
	if msg == nil || msg.Portal == nil || msg.Portal.ID == "" {
		return errors.New("message request has no portal identity")
	}
	if msg.Portal.Receiver != "" && msg.Portal.Receiver != r.userLogin.ID {
		return errors.New("message request belongs to another login")
	}
	roomID := portalIDToRoomID(msg.Portal.ID)
	joined, err := r.matrix().JoinRoomByID(ctx, roomID)
	if err != nil {
		return err
	}
	if joined.RoomID != "" && joined.RoomID != roomID {
		return errors.New("reddit accepted a different conversation")
	}
	return nil
}

func (r *RedditClient) pendingRequestEvent(state *RoomState, key networkid.PortalKey) *simplevent.ChatResync {
	info := r.chatInfoFromState(state)
	return &simplevent.ChatResync{
		EventMeta: simplevent.EventMeta{Type: bridgev2.RemoteEventChatResync, PortalKey: key, CreatePortal: true},
		ChatInfo:  info,
		CheckNeedsBackfillFunc: func(_ context.Context, latest *database.Message) (bool, error) {
			// Populate an existing empty request room too. Subsequent resyncs
			// must not repeatedly fetch the same preview; older history has its
			// own SDK queue task and provider cursor.
			return info.CanBackfill && latest == nil, nil
		},
	}
}
