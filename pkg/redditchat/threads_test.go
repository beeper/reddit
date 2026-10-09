package redditchat

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func threadTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	client, err := New(Credentials{Homeserver: srv.URL, AccessToken: "test", UserID: "@t2_a:reddit.com"})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestThreadHistoryUsesNativeSequencePagination(t *testing.T) {
	calls := 0
	client := threadTestClient(t, func(w http.ResponseWriter, req *http.Request) {
		calls++
		q := req.URL.Query()
		if req.URL.Path != "/_matrix/client/v3/rooms/!room:reddit.com/events" || q.Get("dir") != "b" {
			t.Error("wrong thread endpoint")
		}
		if q.Get("event_id") == "$root" {
			if q.Get("before") != "1" || q.Get("after") != "1" {
				t.Error("wrong root context request")
			}
			_, _ = io.WriteString(w, `{"events":[{"event_id":"$adjacent","seq_id":"25"},{"event_id":"$root","seq_id":"24"}]}`)
			return
		}
		if q.Get("parent") != "24" {
			t.Error("thread parent must be the native root sequence")
		}
		if !q.Has("seq") {
			if q.Get("before") != "1" || q.Has("after") {
				t.Error("wrong latest page request")
			}
			_, _ = io.WriteString(w, `{"last_seq_id":"24.1","events":[{"event_id":"$second","seq_id":"24.1","type":"m.room.message","content":{"msgtype":"m.text","body":"second"}}]}`)
		} else {
			if q.Get("seq") != "0" || q.Get("before") != "0" || q.Get("after") != "0" {
				t.Error("continuation must include the requested sequence")
			}
			_, _ = io.WriteString(w, `{"last_seq_id":"24.1","events":[{"event_id":"$first","seq_id":"24.0","type":"m.room.message","content":{"msgtype":"m.text","body":"first"}}]}`)
		}
	})
	page, err := client.ThreadMessages(context.Background(), "!room:reddit.com", "$root", "", 1)
	if err != nil || len(page.Chunk) != 1 || page.Chunk[0].ID != "$second" || page.End != "0" {
		t.Fatalf("wrong latest page: %+v, %v", page, err)
	}
	page, err = client.ThreadMessages(context.Background(), "!room:reddit.com", "$root", page.End, 1)
	if err != nil || len(page.Chunk) != 1 || page.Chunk[0].ID != "$first" || page.Start != "0" || page.End != "" || calls != 4 {
		t.Fatalf("wrong terminal page: %+v, %v (%d calls)", page, err, calls)
	}
}

func TestThreadHistoryRejectsWrongStream(t *testing.T) {
	for name, response := range map[string]string{
		"wrong root":       `{"events":[{"event_id":"$other","seq_id":"24"}]}`,
		"wrong root room":  `{"events":[{"event_id":"$root","room_id":"!other:reddit.com","seq_id":"24"}]}`,
		"duplicate root":   `{"events":[{"event_id":"$root","seq_id":"24"},{"event_id":"$root","seq_id":"25"}]}`,
		"nested root":      `{"events":[{"event_id":"$root","seq_id":"24.1"}]}`,
		"missing sequence": `{"events":[{"event_id":"$root"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			client := threadTestClient(t, func(w http.ResponseWriter, req *http.Request) { _, _ = io.WriteString(w, response) })
			if _, err := client.ThreadMessages(context.Background(), "!room:reddit.com", "$root", "", 10); err == nil {
				t.Fatal("invalid root accepted")
			}
		})
	}
	for _, child := range []string{
		`{"event_id":"$reply","seq_id":"25.0"}`,
		`{"event_id":"$reply","seq_id":"24.3"}`,
		`{"event_id":"$reply","seq_id":"24.0","room_id":"!other:reddit.com"}`,
		`{"event_id":"$reply","seq_id":"24.-1"}`,
		`null`,
	} {
		t.Run(child, func(t *testing.T) {
			client := threadTestClient(t, func(w http.ResponseWriter, req *http.Request) {
				if req.URL.Query().Has("event_id") {
					_, _ = io.WriteString(w, `{"events":[{"event_id":"$root","seq_id":"24"}]}`)
				} else {
					_, _ = fmt.Fprintf(w, `{"events":[%s]}`, child)
				}
			})
			if _, err := client.ThreadMessages(context.Background(), "!room:reddit.com", "$root", "2", 10); err == nil {
				t.Fatal("invalid child stream accepted")
			}
		})
	}
}

func TestThreadHistoryShortPageIsNotTerminal(t *testing.T) {
	client := threadTestClient(t, func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Query().Has("event_id") {
			_, _ = io.WriteString(w, `{"events":[{"event_id":"$root","seq_id":"0"}]}`)
		} else {
			_, _ = io.WriteString(w, `{"events":[{"event_id":"$reply","seq_id":"0.8"}]}`)
		}
	})
	page, err := client.ThreadMessages(context.Background(), "!room:reddit.com", "$root", "", 100)
	if err != nil || len(page.Chunk) != 1 || page.End != "7" {
		t.Fatalf("short page lost its continuation: %+v, %v", page, err)
	}
}
