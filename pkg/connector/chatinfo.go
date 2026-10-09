package connector

import (
	"context"
	"errors"
	"strings"

	"go.mau.fi/util/ptr"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

// GetChatInfo uses saved native state, or fetches it when provisioning gets a
// new room before the polling loop does. This read never advances the cursor.
func (r *RedditClient) GetChatInfo(ctx context.Context, portal *bridgev2.Portal) (*bridgev2.ChatInfo, error) {
	state, key, err := r.resolveRoomState(ctx, portalIDToRoomID(portal.ID), &mautrix.SyncJoinedRoom{}, &roomSnapshots{})
	if err != nil {
		return nil, err
	}
	if key != portal.PortalKey {
		return nil, errors.New("chat ownership does not match Reddit room state")
	}
	return r.chatInfoFromState(state), nil
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

func stripUserPrefix(name string) string {
	return strings.TrimPrefix(strings.TrimPrefix(name, "u/"), "/u/")
}
