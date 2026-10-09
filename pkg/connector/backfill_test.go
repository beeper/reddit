package connector

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

// Message-history fixtures have no native reactions unless a test supplies
// a dedicated relations endpoint.
func historyTestClient(t *testing.T, handler http.HandlerFunc) *RedditClient {
	return testClient(t, func(w http.ResponseWriter, req *http.Request) {
		if strings.Contains(req.URL.Path, "/relations/") {
			_, _ = io.WriteString(w, `{"chunk":[]}`)
			return
		}
		handler(w, req)
	})
}

func historyPortal() *bridgev2.Portal {
	return &bridgev2.Portal{Portal: &database.Portal{PortalKey: networkid.PortalKey{ID: "!room:reddit.com", Receiver: "t2_a"}}}
}

func TestHistoryKeepsShortAndStateOnlyPagesUntilProviderTerminal(t *testing.T) {
	pages := map[string]string{
		"":   `{"start":"latest","end":"p1","chunk":[{"type":"m.room.message","event_id":"$new","sender":"@t2_b:reddit.com","origin_server_ts":3000,"content":{"msgtype":"m.text","body":"new"}}]}`,
		"p1": `{"start":"p1","end":"p2","chunk":[{"type":"m.room.member","state_key":"@t2_b:reddit.com","content":{"membership":"join"}}]}`,
		"p2": `{"start":"p2","end":"p3","chunk":[{"type":"m.room.message","event_id":"$older","sender":"@t2_b:reddit.com","origin_server_ts":2000,"content":{"msgtype":"m.text","body":"older"}},{"type":"m.room.message","event_id":"$oldest","sender":"@t2_b:reddit.com","origin_server_ts":1000,"content":{"msgtype":"m.text","body":"oldest"}}]}`,
		"p3": `{"start":"p3","chunk":[]}`,
	}
	calls := 0
	r := historyTestClient(t, func(w http.ResponseWriter, req *http.Request) {
		calls++
		if req.URL.Query().Get("dir") != "b" || req.URL.Query().Get("limit") != "2" {
			t.Error("wrong pagination request")
		}
		page, ok := pages[req.URL.Query().Get("from")]
		if !ok {
			t.Error("unexpected continuation")
			w.WriteHeader(400)
			return
		}
		_, _ = io.WriteString(w, page)
	})
	cursor := networkid.PaginationCursor("")
	counts := []int{1, 0, 2, 0}
	for i := range 4 {
		resp, err := r.FetchMessages(context.Background(), bridgev2.FetchMessagesParams{Portal: historyPortal(), Cursor: cursor, Count: 2})
		if err != nil {
			t.Fatal(err)
		}
		if len(resp.Messages) != counts[i] || resp.HasMore != (i < 3) {
			t.Fatalf("wrong page %d: %+v", i, resp)
		}
		if i == 2 && (resp.Messages[0].ID != "$oldest" || resp.Messages[1].ID != "$older") {
			t.Fatal("history is not chronological")
		}
		cursor = resp.Cursor
	}
	if calls != 4 {
		t.Fatalf("expected four provider pages, got %d", calls)
	}
}

func TestForwardHistoryFindsAnchorAcrossPages(t *testing.T) {
	calls := 0
	r := historyTestClient(t, func(w http.ResponseWriter, req *http.Request) {
		calls++
		if calls == 1 {
			_, _ = io.WriteString(w, `{"start":"latest","end":"p1","chunk":[{"type":"m.room.message","event_id":"$new","sender":"@t2_b:reddit.com","origin_server_ts":3000,"content":{"msgtype":"m.text","body":"new"}}]}`)
		} else {
			_, _ = io.WriteString(w, `{"start":"p1","end":"p2","chunk":[{"type":"m.room.message","event_id":"$middle","sender":"@t2_b:reddit.com","origin_server_ts":2000,"content":{"msgtype":"m.text","body":"middle"}},{"type":"m.room.message","event_id":"$anchor","sender":"@t2_b:reddit.com","origin_server_ts":1000,"content":{"msgtype":"m.text","body":"old"}}]}`)
		}
	})
	resp, err := r.FetchMessages(context.Background(), bridgev2.FetchMessagesParams{Portal: historyPortal(), Forward: true, Count: 2, AnchorMessage: &database.Message{ID: "$anchor"}})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || len(resp.Messages) != 2 || resp.Messages[0].ID != "$middle" || resp.Messages[1].ID != "$new" {
		t.Fatalf("incomplete catchup: %+v", resp)
	}
}

func TestHistoryRejectsStuckCursorAndWrongRoom(t *testing.T) {
	for i, body := range []string{
		`{"start":"p1","end":"p1","chunk":[]}`,
		`{"start":"wrong","end":"p2","chunk":[]}`,
		`{"start":"p1","chunk":[{"room_id":"!other:reddit.com","event_id":"$x","type":"m.room.message"}]}`,
		`{"start":"p1","chunk":[null]}`,
	} {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			r := historyTestClient(t, func(w http.ResponseWriter, req *http.Request) { _, _ = io.WriteString(w, body) })
			if _, err := r.FetchMessages(context.Background(), bridgev2.FetchMessagesParams{Portal: historyPortal(), Cursor: "p1"}); err == nil {
				t.Fatal("invalid history page was accepted")
			}
		})
	}
}

func TestBackwardHistorySeeksOldestMappingBeforeReturningPage(t *testing.T) {
	calls := 0
	r := historyTestClient(t, func(w http.ResponseWriter, req *http.Request) {
		calls++
		switch req.URL.Query().Get("from") {
		case "":
			_, _ = io.WriteString(w, `{"end":"p1","chunk":[{"type":"m.reaction","event_id":"$new-reaction"},{"type":"m.room.message","event_id":"$new","sender":"@t2_b:reddit.com","origin_server_ts":3000,"content":{"msgtype":"m.text","body":"new"}}]}`)
		case "p1":
			_, _ = io.WriteString(w, `{"start":"p1","end":"p2","chunk":[{"type":"m.room.message","event_id":"$anchor","sender":"@t2_b:reddit.com","origin_server_ts":2000,"content":{"msgtype":"m.text","body":"known"}},{"type":"m.room.message","event_id":"$old","sender":"@t2_b:reddit.com","origin_server_ts":1000,"content":{"msgtype":"m.text","body":"old"}}]}`)
		default:
			t.Fatal("unexpected continuation")
		}
	})
	resp, err := r.FetchMessages(context.Background(), bridgev2.FetchMessagesParams{Portal: historyPortal(), Count: 2, AnchorMessage: &database.Message{ID: "$anchor"}})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || len(resp.Messages) != 1 || resp.Messages[0].ID != "$old" || resp.Cursor != "p2" || !resp.HasMore {
		t.Fatalf("wrong backward page: %+v", resp)
	}
}

func TestHistoryDoesNotSilentlyPassMissingAnchor(t *testing.T) {
	for _, forward := range []bool{false, true} {
		t.Run(fmt.Sprint(forward), func(t *testing.T) {
			r := historyTestClient(t, func(w http.ResponseWriter, req *http.Request) { _, _ = io.WriteString(w, `{"chunk":[]}`) })
			_, err := r.FetchMessages(context.Background(), bridgev2.FetchMessagesParams{Portal: historyPortal(), Forward: forward, AnchorMessage: &database.Message{ID: "$absent"}})
			if err == nil {
				t.Fatal("missing anchor silently treated as complete history")
			}
		})
	}
}

func TestThreadHistoryExcludesRootAndPreservesRelations(t *testing.T) {
	r := historyTestClient(t, func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Query().Get("event_id") == "$root" {
			_, _ = io.WriteString(w, `{"events":[{"event_id":"$root","seq_id":"24"}]}`)
		} else {
			_, _ = io.WriteString(w, `{"events":[{"type":"m.room.message","event_id":"$reply","seq_id":"24.0","sender":"@t2_b:reddit.com","origin_server_ts":3000,"content":{"msgtype":"m.text","body":"reply","m.relates_to":{"rel_type":"m.thread","event_id":"$root","is_falling_back":true,"m.in_reply_to":{"event_id":"$root"}}}}]}`)
		}
	})
	resp, err := r.FetchMessages(context.Background(), bridgev2.FetchMessagesParams{
		Portal: historyPortal(), Forward: true, ThreadRoot: "$root",
		AnchorMessage: &database.Message{ID: "$root"}, Count: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Messages) != 1 || resp.Messages[0].ID != "$reply" || resp.Messages[0].ThreadRoot == nil || *resp.Messages[0].ThreadRoot != "$root" || resp.HasMore {
		t.Fatalf("thread history lost its root relation or terminal boundary: %+v", resp)
	}
}

func TestThreadHistoryCatchesUpToExactReplyAnchor(t *testing.T) {
	pages := 0
	r := historyTestClient(t, func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Query().Has("event_id") {
			_, _ = io.WriteString(w, `{"events":[{"event_id":"$root","seq_id":"24"}]}`)
			return
		}
		pages++
		if !req.URL.Query().Has("seq") {
			_, _ = io.WriteString(w, `{"events":[{"type":"m.room.message","event_id":"$reply","seq_id":"24.1","sender":"@t2_b:reddit.com","origin_server_ts":3000,"content":{"msgtype":"m.text","body":"reply","m.relates_to":{"rel_type":"m.thread","event_id":"$root"}}}]}`)
		} else {
			_, _ = io.WriteString(w, `{"events":[{"event_id":"$anchor","seq_id":"24.0"}]}`)
		}
	})
	resp, err := r.FetchMessages(context.Background(), bridgev2.FetchMessagesParams{
		Portal: historyPortal(), Forward: true, ThreadRoot: "$root",
		AnchorMessage: &database.Message{ID: "$anchor"}, Count: 1,
	})
	if err != nil || pages != 2 || len(resp.Messages) != 1 || resp.Messages[0].ID != "$reply" {
		t.Fatalf("thread catchup failed: %+v, %v (%d pages)", resp, err, pages)
	}
}

func TestThreadHistoryRejectsWrongRelationAndReceiver(t *testing.T) {
	r := historyTestClient(t, func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Query().Has("event_id") {
			_, _ = io.WriteString(w, `{"events":[{"event_id":"$root","seq_id":"24"}]}`)
		} else {
			_, _ = io.WriteString(w, `{"events":[{"type":"m.room.message","event_id":"$reply","seq_id":"24.0","sender":"@t2_b:reddit.com","content":{"msgtype":"m.text","body":"reply","m.relates_to":{"rel_type":"m.thread","event_id":"$other"}}}]}`)
		}
	})
	params := bridgev2.FetchMessagesParams{Portal: historyPortal(), ThreadRoot: "$root"}
	if _, err := r.FetchMessages(context.Background(), params); err == nil {
		t.Fatal("accepted a reply to another thread")
	}
	params.Portal.Receiver = "t2_other"
	if _, err := r.FetchMessages(context.Background(), params); err == nil {
		t.Fatal("accepted another login's history")
	}
}
