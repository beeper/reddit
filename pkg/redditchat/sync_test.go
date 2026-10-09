package redditchat

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"maunium.net/go/mautrix/event"
)

func TestSyncDecodesRedditPreviewWithoutJoining(t *testing.T) {
	var response SyncResponse
	err := json.Unmarshal([]byte(`{"next_batch":"next","rooms":{"join":{"!joined:reddit.com":{}},"invite":{"!pending:reddit.com":{"invite_state":{"events":[]}}},"peek":{"!pending:reddit.com":{"account_data":{"events":[{"type":"com.reddit.hidden_chat","content":{"hidden":true}}]}}}}}`), &response)
	if err != nil {
		t.Fatal(err)
	}
	if response.NextBatch != "next" || len(response.Rooms.Join) != 1 || len(response.Rooms.Invite) != 1 || len(response.Rooms.Peek) != 1 {
		t.Fatalf("native sync sections were lost or combined: %+v", response)
	}
	if response.Rooms.Join["!pending:reddit.com"] != nil || len(response.Rooms.Peek["!pending:reddit.com"].AccountData.Events) != 1 {
		t.Fatal("preview became a join or lost account data")
	}
}

// Exercise the actual SDK request and JSON decoder: Reddit suppresses
// reactions even for filter={}, without returning an HTTP error.
func TestSyncReceivesReactionsWithoutInlineFilter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/_matrix/client/v3/sync" || req.URL.Query().Get("since") != "previous" {
			t.Error("sync lost its endpoint or durable cursor")
		}
		if req.URL.Query().Has("filter") {
			_, _ = io.WriteString(w, `{"next_batch":"next","rooms":{"join":{"!room:reddit.com":{"timeline":{"events":[]}}}}}`)
			return
		}
		_, _ = io.WriteString(w, `{"next_batch":"next","rooms":{"join":{"!room:reddit.com":{"timeline":{"events":[{"type":"m.reaction","event_id":"$reaction","sender":"@t2_b:reddit.com","content":{"m.relates_to":{"event_id":"$message","rel_type":"m.annotation","key":"jvuspmbga7081.gif"}}},{"type":"m.room.redaction","event_id":"$removal","sender":"@t2_b:reddit.com","redacts":"$reaction","content":{}}]}}}}}`)
	}))
	defer srv.Close()
	client, err := New(Credentials{Homeserver: srv.URL, AccessToken: "test", UserID: "@t2_a:reddit.com"})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Sync(context.Background(), "previous", 0)
	if err != nil {
		t.Fatal(err)
	}
	room := resp.Rooms.Join["!room:reddit.com"]
	if room == nil || len(room.Timeline.Events) != 2 || resp.NextBatch != "next" {
		t.Fatal("successful sync silently lost reaction events")
	}
	reaction, removal := room.Timeline.Events[0], room.Timeline.Events[1]
	if reaction.Type != event.EventReaction || removal.Type != event.EventRedaction || removal.Redacts != reaction.ID {
		t.Fatal("decoded event types or removal identity cannot reach the connector")
	}
	if err = reaction.Content.ParseRaw(reaction.Type); err != nil || reaction.Content.AsReaction().RelatesTo.Key != "jvuspmbga7081.gif" {
		t.Fatal("reaction relation was lost during decoding", err)
	}
}
