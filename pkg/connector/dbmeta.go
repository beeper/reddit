package connector

import (
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/id"

	"github.com/beeper/reddit/pkg/redditchat"
)

func (rc *RedditConnector) GetDBMetaTypes() database.MetaTypes {
	return database.MetaTypes{
		UserLogin: func() any { return &UserLoginMetadata{} },
		Portal:    func() any { return &PortalMetadata{} },
		Ghost:     nil,
		Message:   nil,
		Reaction:  func() any { return &ReactionMetadata{} },
	}
}

type ReactionMetadata struct {
	RemoteEventID id.EventID `json:"remote_event_id,omitempty"`
}

// UserLoginMetadata is persisted alongside each Reddit-connected user.
// It carries the Matrix credentials minted from Reddit, the serialized Reddit
// session cookies (so we can refresh the chat token without re-login), the
// chat token expiry, and the current Matrix sync cursor for the polling loop.
type UserLoginMetadata struct {
	Credentials      redditchat.Credentials `json:"credentials"`
	CookiesJSON      string                 `json:"cookies_json,omitempty"`
	ChatTokenExpiry  int64                  `json:"chat_token_expiry,omitempty"`
	NextBatch        string                 `json:"next_batch,omitempty"`
	Username         string                 `json:"username,omitempty"`
	RoomStateVersion int                    `json:"room_state_version,omitempty"`
	// Native per-account state outlives a deleted local portal. Persist it with
	// the sync cursor so unchanged invitations do not resurrect ignored DMs.
	HiddenRooms map[id.RoomID]bool `json:"hidden_rooms,omitempty"`
}

type PortalMetadata struct {
	State *RoomState `json:"state,omitempty"`
	// Preset remembers whether the room was created as "reddit_dm" or
	// "private_chat" so we can stamp the same preset on outgoing room creations
	// and avoid round-tripping the Reddit homeserver for state.
	Preset string `json:"preset,omitempty"`
}
