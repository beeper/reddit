package connector

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"
)

func deletionFixture(t *testing.T) *event.Event {
	t.Helper()
	var evt event.Event
	err := json.Unmarshal([]byte(`{"type":"m.room.message","event_id":"$deleted","room_id":"!room:reddit.com","sender":"@t2_b:reddit.com","origin_server_ts":1000,"content":{"msgtype":"m.text","body":"retained deleted content"},"unsigned":{"redacted_because":{"type":"m.room.redaction","event_id":"$removal","room_id":"!room:reddit.com","sender":"@t2_b:reddit.com","origin_server_ts":2000,"redacts":"$deleted","content":{}}}}`), &evt)
	if err != nil {
		t.Fatal(err)
	}
	return &evt
}

func TestDeletedContentNeverConvertsToTextOrMedia(t *testing.T) {
	r := testClient(t, func(w http.ResponseWriter, req *http.Request) { t.Error("deleted content must not fetch media") })
	evt := deletionFixture(t)
	for _, content := range []event.Content{evt.Content, {Raw: map[string]any{}}, {Parsed: &event.MessageEventContent{MsgType: event.MsgImage, Body: "deleted image", URL: "mxc://reddit.com/removed"}}} {
		evt.Content = content
		msg, err := r.convertMessage(context.Background(), historyPortal(), nil, evt)
		if err != nil || len(msg.Parts) != 1 || !msg.Parts[0].DontBridge || msg.Parts[0].Content.Body != "" {
			t.Fatal("retained deleted content was exposed", err)
		}
	}
}

func TestDeletionRejectsWrongRoomOwnerAndProof(t *testing.T) {
	for _, tc := range []string{"room", "owner", "proof", "foreign sender", "missing proof", "conflicting targets"} {
		t.Run(tc, func(t *testing.T) {
			r := testClient(t, func(w http.ResponseWriter, req *http.Request) { t.Error("unexpected request") })
			key := historyPortal().PortalKey
			evt := deletionFixture(t)
			switch tc {
			case "room":
				evt.RoomID = "!other:reddit.com"
			case "owner":
				key.Receiver = "t2_other"
			case "proof":
				evt.Unsigned.RedactedBecause.Redacts = "$other"
			case "foreign sender":
				evt.Sender = "@t2_b:foreign.example"
			case "missing proof":
				evt.Unsigned.RedactedBecause = nil
			case "conflicting targets":
				evt.Unsigned.RedactedBecause.Content = event.Content{Parsed: &event.RedactionEventContent{Redacts: "$other"}}
			}
			if _, err := r.convertDeletedEvent(key, evt); err == nil {
				t.Fatal("unsafe deletion accepted")
			}
		})
	}
}

func TestRedactionResolvesItsNativeTarget(t *testing.T) {
	target := deletionFixture(t)
	r := testClient(t, func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/_matrix/client/v3/rooms/!room:reddit.com/event/$deleted" {
			t.Error("wrong native target", req.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(target)
	})
	removal, err := r.convertRedaction(context.Background(), historyPortal().PortalKey, target.Unsigned.RedactedBecause)
	if err != nil || removal.(bridgev2.RemoteMessageRemove).GetTargetMessage() != "$deleted" {
		t.Fatal(removal, err)
	}
	unrelated := *target.Unsigned.RedactedBecause
	unrelated.ID = "$another-removal"
	if _, err = r.convertRedaction(context.Background(), historyPortal().PortalKey, &unrelated); err == nil {
		t.Fatal("different deletion proof accepted")
	}
}
