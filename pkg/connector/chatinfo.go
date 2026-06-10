package connector

import (
	"context"
	"strings"

	"go.mau.fi/util/ptr"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// GetChatInfo fetches room metadata from the Reddit Matrix homeserver. It is
// called by bridgev2 whenever a portal needs (re)synced state — typically
// after a ChatResync event.
func (r *RedditClient) GetChatInfo(ctx context.Context, portal *bridgev2.Portal) (*bridgev2.ChatInfo, error) {
	roomID := portalIDToRoomID(portal.ID)
	preset := ""
	if meta, ok := portal.Metadata.(*PortalMetadata); ok && meta != nil {
		preset = meta.Preset
	}
	isDM := preset == redditDMPreset || portal.Receiver != ""
	return r.fetchChatInfo(ctx, roomID, isDM, mautrix.SyncJoinedRoom{})
}

// GetUserInfo fetches profile info for a ghost (a Reddit user puppeted into
// the bridge homeserver).
func (r *RedditClient) GetUserInfo(ctx context.Context, ghost *bridgev2.Ghost) (*bridgev2.UserInfo, error) {
	mxid := userIDToMatrix(ghost.ID, "")
	resp, err := r.matrix().GetProfile(ctx, mxid)
	if err != nil {
		return nil, err
	}
	name := resp.DisplayName
	if name == "" {
		name = string(ghost.ID)
	}
	formatted := r.main.Config.FormatDisplayname(DisplaynameParams{
		Username: stripUserPrefix(name),
		UserID:   string(ghost.ID),
	})
	info := &bridgev2.UserInfo{Name: ptr.Ptr(formatted)}
	if !resp.AvatarURL.IsEmpty() {
		mxc := resp.AvatarURL.CUString()
		info.Avatar = &bridgev2.Avatar{
			ID:  networkid.AvatarID(mxc),
			MXC: mxc,
		}
	}
	return info, nil
}

func (r *RedditClient) fetchChatInfo(ctx context.Context, roomID id.RoomID, isDM bool, sync mautrix.SyncJoinedRoom) (*bridgev2.ChatInfo, error) {
	info := &bridgev2.ChatInfo{CanBackfill: true}
	if isDM {
		info.Type = ptr.Ptr(database.RoomTypeDM)
	} else {
		info.Type = ptr.Ptr(database.RoomTypeDefault)
	}

	if name := r.readRoomName(ctx, roomID, sync); name != "" {
		info.Name = ptr.Ptr(name)
	}
	if avatar := r.readRoomAvatar(ctx, roomID, sync); avatar != nil {
		info.Avatar = avatar
	}

	members, err := r.readRoomMembers(ctx, roomID, sync)
	if err == nil {
		info.Members = members
	}
	return info, nil
}

func (r *RedditClient) readRoomName(ctx context.Context, roomID id.RoomID, sync mautrix.SyncJoinedRoom) string {
	if name := findStateString(sync.State.Events, event.StateRoomName, "name"); name != "" {
		return name
	}
	var content event.RoomNameEventContent
	err := r.matrix().StateEvent(ctx, roomID, event.StateRoomName, "", &content)
	if err == nil {
		return content.Name
	}
	return ""
}

func (r *RedditClient) readRoomAvatar(ctx context.Context, roomID id.RoomID, sync mautrix.SyncJoinedRoom) *bridgev2.Avatar {
	url := findStateString(sync.State.Events, event.StateRoomAvatar, "url")
	if url == "" {
		var content event.RoomAvatarEventContent
		if err := r.matrix().StateEvent(ctx, roomID, event.StateRoomAvatar, "", &content); err == nil {
			url = string(content.URL)
		}
	}
	if url == "" {
		return nil
	}
	return &bridgev2.Avatar{
		ID:  networkid.AvatarID(url),
		MXC: id.ContentURIString(url),
	}
}

func (r *RedditClient) readRoomMembers(ctx context.Context, roomID id.RoomID, sync mautrix.SyncJoinedRoom) (*bridgev2.ChatMemberList, error) {
	resp, err := r.matrix().JoinedMembers(ctx, roomID)
	if err != nil {
		return nil, err
	}
	memberMap := bridgev2.ChatMemberMap{}
	for mxid := range resp.Joined {
		uid := makeUserID(mxid)
		memberMap.Set(bridgev2.ChatMember{
			EventSender: bridgev2.EventSender{
				IsFromMe:    networkid.UserLoginID(uid) == r.userLogin.ID,
				Sender:      uid,
				SenderLogin: networkid.UserLoginID(uid),
			},
			Membership: event.MembershipJoin,
		})
	}
	return &bridgev2.ChatMemberList{
		IsFull:           true,
		TotalMemberCount: len(memberMap),
		MemberMap:        memberMap,
	}, nil
}

func findStateString(state []*event.Event, eventType event.Type, field string) string {
	for _, e := range state {
		if e == nil || e.Type != eventType {
			continue
		}
		if v, ok := e.Content.Raw[field].(string); ok {
			return v
		}
	}
	return ""
}

func stripUserPrefix(name string) string {
	return strings.TrimPrefix(strings.TrimPrefix(name, "u/"), "/u/")
}
