package connector

import (
	"slices"
	"testing"

	"maunium.net/go/mautrix/id"
)

func TestLinkMentions(t *testing.T) {
	members := map[id.UserID]RoomMember{
		"@t2_a:reddit.com": {Name: "Some_User-1"},
		"@t2_b:reddit.com": {Name: "other"},
		"@t2_c:reddit.com": {},
	}
	beeperMXID := func(mxid id.UserID) id.UserID { return "@ghost_" + id.UserID(makeUserID(mxid)) + ":beeper.local" }
	for _, tc := range []struct {
		name, body, want string
		mentioned        []id.UserID
	}{
		{"start", "@some_user-1 hi", `<a href="https://matrix.to/#/@ghost_t2_a:beeper.local">@some_user-1</a> hi`, []id.UserID{"@ghost_t2_a:beeper.local"}},
		{"two", "hey @other & @Some_User-1!", `hey <a href="https://matrix.to/#/@ghost_t2_b:beeper.local">@other</a> &amp; <a href="https://matrix.to/#/@ghost_t2_a:beeper.local">@Some_User-1</a>!`, []id.UserID{"@ghost_t2_b:beeper.local", "@ghost_t2_a:beeper.local"}},
		{"not a member", "@stranger <b>", "@stranger &lt;b&gt;", nil},
		{"email and url", "a@other.com reddit.com/@other", "a@other.com reddit.com/@other", nil},
		{"newline", "line\n@other", `line<br><a href="https://matrix.to/#/@ghost_t2_b:beeper.local">@other</a>`, []id.UserID{"@ghost_t2_b:beeper.local"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			formatted, mentioned := linkMentions(tc.body, members, beeperMXID)
			if formatted != tc.want || !slices.Equal(mentioned, tc.mentioned) {
				t.Fatalf("linkMentions(%q) = %q, %v", tc.body, formatted, mentioned)
			}
		})
	}
}
