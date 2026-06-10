package connector

import (
	"fmt"
	"strings"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/id"
)

// Reddit Matrix MXIDs look like @t2_abc123:reddit.com. We use the localpart
// (without the leading @) as the bridge-side UserID/UserLoginID so the bridge's
// own homeserver isn't leaked into Reddit-flavored IDs.

func makeUserID(matrixUserID id.UserID) networkid.UserID {
	localpart := strings.TrimPrefix(matrixUserID.String(), "@")
	if idx := strings.Index(localpart, ":"); idx >= 0 {
		localpart = localpart[:idx]
	}
	return networkid.UserID(localpart)
}

func makeUserLoginID(matrixUserID id.UserID) networkid.UserLoginID {
	return networkid.UserLoginID(makeUserID(matrixUserID))
}

func userIDToMatrix(userID networkid.UserID, homeserver string) id.UserID {
	return id.UserID(fmt.Sprintf("@%s:%s", userID, matrixServerName(homeserver)))
}

func matrixServerName(homeserverURL string) string {
	// "https://matrix.redditspace.com" → "reddit.com" (Reddit's MXIDs use
	// reddit.com as the server name, not the homeserver hostname).
	const def = "reddit.com"
	if homeserverURL == "" {
		return def
	}
	return def
}

func makePortalID(roomID id.RoomID) networkid.PortalID {
	return networkid.PortalID(roomID)
}

func portalIDToRoomID(portalID networkid.PortalID) id.RoomID {
	return id.RoomID(portalID)
}

func makeMessageID(eventID id.EventID) networkid.MessageID {
	return networkid.MessageID(eventID)
}

func messageIDToEventID(messageID networkid.MessageID) id.EventID {
	return id.EventID(messageID)
}

// makePortalKey decides whether a portal is split per user-login (DMs) or
// shared (groups). Reddit-Matrix exposes is_direct on the create event but the
// connector resolves this lazily from PortalMetadata or from sync state.
func (r *RedditClient) makePortalKey(roomID id.RoomID, isDM bool) networkid.PortalKey {
	key := networkid.PortalKey{ID: makePortalID(roomID)}
	if isDM || r.main.Bridge.Config.SplitPortals {
		key.Receiver = r.userLogin.ID
	}
	return key
}

func (r *RedditClient) makeSender(matrixUserID id.UserID) bridgev2.EventSender {
	uid := makeUserID(matrixUserID)
	return bridgev2.EventSender{
		IsFromMe:    networkid.UserLoginID(uid) == r.userLogin.ID,
		Sender:      uid,
		SenderLogin: networkid.UserLoginID(uid),
	}
}
