package connector

import (
	"errors"
	"slices"
	"strings"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// Reddit sends a room-wide m.typing snapshot, while bridgev2 accepts one
// sender per event. Keep only the ephemeral native snapshot to emit stops for
// users missing from the next update. Nothing here owns delivery or persistence.
func (r *RedditClient) convertTyping(key networkid.PortalKey, evt *event.Event) ([]*simplevent.Typing, error) {
	if evt.RoomID != "" && makePortalID(evt.RoomID) != key.ID {
		return nil, errors.New("typing event belongs to another room")
	}
	if key.Receiver != "" && key.Receiver != r.userLogin.ID {
		return nil, errors.New("typing portal belongs to another login")
	}
	if err := evt.Content.ParseRaw(event.EphemeralEventTyping); err != nil && !errors.Is(err, event.ErrContentAlreadyParsed) {
		return nil, err
	}
	content, ok := evt.Content.Parsed.(*event.TypingEventContent)
	if !ok || content == nil {
		return nil, errors.New("invalid typing content")
	}
	users := make([]id.UserID, 0, len(content.UserIDs))
	for _, user := range content.UserIDs {
		localpart, server, err := user.Parse()
		if err != nil || server != "reddit.com" || !strings.HasPrefix(localpart, "t2_") || len(localpart) <= 3 {
			return nil, errors.New("typing event has an invalid user identity")
		}
		if networkid.UserLoginID(localpart) != r.userLogin.ID {
			users = append(users, user)
		}
	}
	slices.Sort(users)
	users = slices.Compact(users)
	r.typingMu.Lock()
	defer r.typingMu.Unlock()
	var out []*simplevent.Typing
	appendTyping := func(user id.UserID, timeout time.Duration) {
		out = append(out, &simplevent.Typing{
			EventMeta: simplevent.EventMeta{Type: bridgev2.RemoteEventTyping, PortalKey: key, Sender: r.makeSender(user)},
			Timeout:   timeout,
		})
	}
	for _, previous := range r.typing[key] {
		if !slices.Contains(users, previous) {
			appendTyping(previous, 0)
		}
	}
	for _, user := range users {
		// Native snapshots do not carry a timeout. Bound stale indicators if a
		// stop update is lost; repeated snapshots refresh the SDK's indicator.
		appendTyping(user, 30*time.Second)
	}
	if len(users) == 0 {
		delete(r.typing, key)
	} else {
		if r.typing == nil {
			r.typing = make(map[networkid.PortalKey][]id.UserID)
		}
		r.typing[key] = users
	}
	return out, nil
}
