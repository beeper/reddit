package connector

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/beeper/reddit/pkg/redditchat"
)

// Catches sending a portal/Matrix ID instead of the native Reddit recipient.
func TestBlockUserRecipientAndNativeRejection(t *testing.T) {
	for _, accepted := range []bool{true, false} {
		r := testClient(t, func(w http.ResponseWriter, req *http.Request) {
			var body struct {
				Variables struct {
					Input struct {
						RedditorID string `json:"redditorId"`
					} `json:"input"`
				} `json:"variables"`
			}
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.Variables.Input.RedditorID != "t2_b" {
				t.Errorf("wrong target: %q", body.Variables.Input.RedditorID)
			}
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"updateRedditorBlockState": map[string]bool{"ok": accepted}}})
		})
		r.session, _ = redditchat.NewRedditSession(nil, r.meta.Credentials.Homeserver, "")
		r.session.CSRFToken = "test-csrf"
		r.blockedUsers = map[networkid.UserID]bool{}
		msg := &bridgev2.MatrixBlockUser{
			Portal:  &bridgev2.Portal{Portal: &database.Portal{OtherUserID: "t2_b"}},
			Content: &event.BeeperBlockUserEventContent{Block: true},
		}
		err := r.HandleMatrixBlockUser(t.Context(), msg)
		if (err == nil) != accepted || *r.userBlocked("t2_b") != accepted {
			t.Fatalf("native result not preserved: accepted=%v error=%v", accepted, err)
		}
	}
}

// Catches sharing another login's block list, setting group block flags, or
// clearing a known block when Reddit returns an invalid snapshot.
func TestBlockListNativeProjection(t *testing.T) {
	body := `{"data":{"identity":{"blockedRedditorsInfo":{"edges":[{"node":{"id":"t2_b"}}]}}}}`
	r := testClient(t, func(w http.ResponseWriter, req *http.Request) { io.WriteString(w, body) })
	r.session, _ = redditchat.NewRedditSession(nil, r.meta.Credentials.Homeserver, "")
	r.session.CSRFToken = "test-csrf"
	state := &RoomState{Type: "direct", Members: map[id.UserID]RoomMember{
		"@t2_a:reddit.com": {Membership: event.MembershipJoin},
		"@t2_b:reddit.com": {Membership: event.MembershipJoin},
	}}
	if info := r.chatInfoFromState(state); info.UserBlocked != nil {
		t.Fatal("unknown list must not clear saved portal state")
	}
	if _, err := r.refreshBlockedUsers(t.Context()); err != nil {
		t.Fatal(err)
	}
	if info := r.chatInfoFromState(state); info.UserBlocked == nil || !*info.UserBlocked {
		t.Fatal("native block missing from DM projection")
	}
	change := &blockListChange{client: r, userID: "t2_b"}
	other := testClient(t, func(http.ResponseWriter, *http.Request) {})
	if other.chatInfoFromState(state).UserBlocked != nil {
		t.Fatal("block state leaked across logins")
	}
	state.Type = "private_group"
	if r.chatInfoFromState(state).UserBlocked != nil {
		t.Fatal("group was marked blocked")
	}
	body = `{"data":{"identity":null}}`
	if _, err := r.refreshBlockedUsers(t.Context()); err == nil || !*r.userBlocked("t2_b") {
		t.Fatal("invalid native snapshot cleared block state")
	}
	body = `{"data":{"identity":{"blockedRedditorsInfo":{"edges":[]}}}}`
	if _, err := r.refreshBlockedUsers(t.Context()); err != nil || *r.userBlocked("t2_b") {
		t.Fatal("native unblock did not clear cached state")
	}
	if info, err := change.GetChatInfoChange(t.Context()); err != nil || *info.ChatInfo.UserBlocked {
		t.Fatal("block-list conversion used an outdated snapshot")
	}
}

func TestBlockUserNativeSupportLimits(t *testing.T) {
	r := testClient(t, func(http.ResponseWriter, *http.Request) { t.Error("unsupported action reached Reddit") })
	dm := &bridgev2.Portal{Portal: &database.Portal{RoomType: database.RoomTypeDM, OtherUserID: "t2_b"}}
	group := &bridgev2.Portal{Portal: &database.Portal{RoomType: database.RoomTypeGroupDM}}
	if r.GetCapabilities(t.Context(), dm).BlockUser {
		t.Fatal("blocking advertised without cookie session")
	}
	r.session, _ = redditchat.NewRedditSession(nil, r.meta.Credentials.Homeserver, "")
	r.session.CSRFToken = "test-csrf"
	if !r.GetCapabilities(t.Context(), dm).BlockUser || r.GetCapabilities(t.Context(), group).BlockUser {
		t.Fatal("blocking must be advertised for DMs only")
	}
	if err := r.HandleMatrixBlockUser(t.Context(), &bridgev2.MatrixBlockUser{
		Portal: dm, Content: &event.BeeperBlockUserEventContent{Block: true, ReportSpam: true},
	}); err == nil {
		t.Fatal("unsupported spam report silently ignored")
	}
}
