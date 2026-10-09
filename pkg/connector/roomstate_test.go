package connector

import (
	"encoding/json"
	"net/http"
	"testing"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

func decodeRoom(t *testing.T, data string) *mautrix.SyncJoinedRoom {
	t.Helper()
	var joined mautrix.SyncJoinedRoom
	if err := json.Unmarshal([]byte(data), &joined); err != nil {
		t.Fatal(err)
	}
	return &joined
}

func TestRoomStateFromNativeTimelineSurvivesIncrementalSync(t *testing.T) {
	// Same field layout as the read-only live sync capture; synthetic identities.
	joined := decodeRoom(t, `{"summary":{"m.joined_member_count":2,"m.invited_member_count":0},"state":{"events":[]},"timeline":{"events":[
	{"type":"m.room.create","state_key":"","content":{"creator":"@t2_b:reddit.com","room_version":"9"}},
	{"type":"m.room.member","state_key":"@t2_b:reddit.com","content":{"membership":"join","displayname":"counterpart"}},
	{"type":"m.room.member","state_key":"@t2_a:reddit.com","content":{"membership":"invite","is_direct":true,"displayname":"owner"}},
	{"type":"com.reddit.chat.type","state_key":"","content":{"type":"direct","participants":["@t2_a:reddit.com","@t2_b:reddit.com"]}},
	{"type":"m.room.member","state_key":"@t2_a:reddit.com","content":{"membership":"join","displayname":"owner"}}
	]}}`)
	state, err := mergeRoomState(nil, joined)
	if err != nil {
		t.Fatal(err)
	}
	r := testClient(t, func(w http.ResponseWriter, req *http.Request) {
		t.Error("room hydration must not call unsupported endpoints")
	})
	info := r.chatInfoFromState(state)
	if *info.Type != database.RoomTypeDM || *info.Name != "counterpart" || info.Members.OtherUserID != "t2_b" || !info.Members.IsFull || len(info.Members.MemberMap) != 2 {
		t.Fatalf("incorrect native DM projection: %+v %+v", info, info.Members)
	}
	data, err := json.Marshal(&PortalMetadata{State: state})
	if err != nil {
		t.Fatal(err)
	}
	var restored PortalMetadata
	if err = json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	updated, err := mergeRoomState(restored.State, decodeRoom(t, `{"timeline":{"events":[{"type":"m.room.message","content":{"body":"next"}}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if updated.Type != "direct" || len(updated.Members) != 2 {
		t.Fatalf("incremental sync lost identity: %+v", updated)
	}
	firstKey := r.makePortalKey("!room:reddit.com", state.Type == "direct")
	if key := r.makePortalKey("!room:reddit.com", updated.Type == "direct"); key != firstKey {
		t.Fatal("incremental sync changed portal key")
	}
}

func TestRoomStateDoesNotMutatePreviousAndHonorsLatestState(t *testing.T) {
	previous := &RoomState{Type: "private_group", Members: map[id.UserID]RoomMember{"@t2_b:reddit.com": {Name: "counterpart", Membership: event.MembershipJoin}}}
	updated, err := mergeRoomState(previous, decodeRoom(t, `{"timeline":{"events":[{"type":"m.room.name","state_key":"","content":{"name":"old"}}]},"state_after":{"events":[{"type":"m.room.name","state_key":"","content":{"name":"new"}},{"type":"m.room.member","state_key":"@t2_b:reddit.com","content":{"membership":"leave"}}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(previous.Members) != 1 || previous.Name != nil {
		t.Fatal("merge mutated the prior snapshot")
	}
	if len(updated.Members) != 0 || *updated.Name != "new" {
		t.Fatalf("latest state ignored: %+v", updated)
	}
	r := testClient(t, func(w http.ResponseWriter, req *http.Request) {})
	r.main.Bridge.Config.SplitPortals = true
	if *r.chatInfoFromState(updated).Type != database.RoomTypeGroupDM {
		t.Fatal("split group was misclassified as a DM")
	}
}

func TestRoomStateRejectsUnknownAndChangedType(t *testing.T) {
	for _, data := range []string{
		`{"state":{"events":[{"type":"com.reddit.chat.type","state_key":"","content":{"type":"unknown-kind"}}]}}`,
		`{"state":{"events":[{"type":"com.reddit.chat.type","state_key":"","content":{"type":"private_group"}}]}}`,
	} {
		if _, err := mergeRoomState(&RoomState{Type: "direct"}, decodeRoom(t, data)); err == nil {
			t.Fatal("unknown or changed identity was accepted")
		}
	}
}
