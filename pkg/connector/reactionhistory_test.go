package connector

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"testing"
)

func TestReactionHistoryPaginatesAndDoesNotReviveRemovedContent(t *testing.T) {
	calls := 0
	r := testClient(t, func(w http.ResponseWriter, req *http.Request) {
		calls++
		if req.URL.Path != "/_matrix/client/v1/rooms/!room:reddit.com/relations/$target/m.annotation/m.reaction" || req.URL.Query().Get("dir") != "b" {
			t.Fatal("wrong relations endpoint")
		}
		switch req.URL.Query().Get("from") {
		case "":
			_, _ = io.WriteString(w, `{"next_batch":"p1","chunk":[]}`)
		case "p1":
			_, _ = io.WriteString(w, `{"next_batch":"p2","chunk":[{"type":"m.reaction","event_id":"$removed","sender":"@t2_b:reddit.com","content":{"m.relates_to":{"event_id":"$target","rel_type":"m.annotation","key":"new-native-key"}},"unsigned":{"redacted_because":{"type":"m.room.redaction","event_id":"$remove","redacts":"$removed"}}}]}`)
		case "p2":
			_, _ = io.WriteString(w, `{"chunk":[{"type":"m.reaction","event_id":"$active","sender":"@t2_b:reddit.com","origin_server_ts":1234,"content":{"m.relates_to":{"event_id":"$target","rel_type":"m.annotation","key":"new-native-key"}}}]}`)
		default:
			t.Fatal("unexpected continuation")
		}
	})
	reactions, err := r.backfillReactions(context.Background(), historyPortal().PortalKey, "$target")
	if err != nil || calls != 3 || len(reactions) != 1 {
		t.Fatalf("incomplete or resurrected reactions: %+v %v, calls=%d", reactions, err, calls)
	}
	if reactions[0].DBMetadata.(*ReactionMetadata).RemoteEventID != "$active" || reactions[0].Timestamp.UnixMilli() != 1234 {
		t.Fatal("historical reaction lost its durable removal identity or timestamp")
	}
}

func TestReactionHistoryRejectsWrongIdentityAndCursorCycles(t *testing.T) {
	for i, body := range []string{
		`{"next_batch":"repeat","chunk":[]}`,
		`{"chunk":[null]}`,
		`{"chunk":[{"type":"m.reaction","event_id":"$r","room_id":"!wrong:reddit.com","sender":"@t2_b:reddit.com","content":{"m.relates_to":{"event_id":"$target","rel_type":"m.annotation","key":"key"}}}]}`,
		`{"chunk":[{"type":"m.reaction","event_id":"$r","sender":"@t2_b:reddit.com","content":{"m.relates_to":{"event_id":"$wrong","rel_type":"m.annotation","key":"key"}}}]}`,
		`{"chunk":[{"type":"m.reaction","event_id":"$r","sender":"@t2_b:foreign.example","content":{"m.relates_to":{"event_id":"$target","rel_type":"m.annotation","key":"key"}}}]}`,
		`{"chunk":[{"type":"m.reaction","event_id":"$r","sender":"@t2_b:reddit.com","content":{"m.relates_to":{"event_id":"$target","rel_type":"m.annotation","key":"key"}},"unsigned":{"redacted_because":{"type":"m.room.redaction","redacts":"$wrong"}}}]}`,
	} {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			r := testClient(t, func(w http.ResponseWriter, req *http.Request) { _, _ = io.WriteString(w, body) })
			if _, err := r.fetchReactionEvents(context.Background(), historyPortal().PortalKey, "$target"); err == nil {
				t.Fatal("unsafe reaction history was accepted")
			}
		})
	}
}
