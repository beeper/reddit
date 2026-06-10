package connector

import (
	"maunium.net/go/mautrix/bridgev2/database"

	"prohibition/redditchat"
)

func (rc *RedditConnector) GetDBMetaTypes() database.MetaTypes {
	return database.MetaTypes{
		UserLogin: func() any { return &UserLoginMetadata{} },
		Portal:    func() any { return &PortalMetadata{} },
		Ghost:     nil,
		Message:   nil,
		Reaction:  nil,
	}
}

// UserLoginMetadata is persisted alongside each Reddit-connected user.
// It carries the Matrix credentials minted from Reddit, the serialized Reddit
// session cookies (so we can refresh the chat token without re-login), the
// chat token expiry, and the current Matrix sync cursor for the polling loop.
type UserLoginMetadata struct {
	Credentials     redditchat.Credentials `json:"credentials"`
	CookiesJSON     string                 `json:"cookies_json,omitempty"`
	ChatTokenExpiry int64                  `json:"chat_token_expiry,omitempty"`
	NextBatch       string                 `json:"next_batch,omitempty"`
	Username        string                 `json:"username,omitempty"`
}

type PortalMetadata struct {
	// Preset remembers whether the room was created as "reddit_dm" or
	// "private_chat" so we can stamp the same preset on outgoing room creations
	// and avoid round-tripping the Reddit homeserver for state.
	Preset string `json:"preset,omitempty"`
}
