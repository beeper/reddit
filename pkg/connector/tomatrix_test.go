package connector

import (
	"context"
	"testing"

	"maunium.net/go/mautrix/event"
)

func TestIncomingThreadUsesBridgeMappingsAndPreservesSource(t *testing.T) {
	source := &event.MessageEventContent{MsgType: event.MsgText, Body: "reply", RelatesTo: (&event.RelatesTo{}).SetThread("$reddit-root", "$reddit-fallback")}
	evt := &event.Event{Content: event.Content{Parsed: source}}
	r := &RedditClient{}
	out, err := r.convertMessage(context.Background(), historyPortal(), nil, evt)
	if err != nil {
		t.Fatal(err)
	}
	if out.ThreadRoot == nil || *out.ThreadRoot != "$reddit-root" || out.ReplyTo != nil || out.Parts[0].Content.RelatesTo != nil {
		t.Fatal("provider relations leaked into Matrix content or fallback became an explicit reply")
	}
	if source.RelatesTo.GetThreadParent() != "$reddit-root" {
		t.Fatal("conversion changed original event")
	}
	source.RelatesTo.SetReplyTo("$reddit-reply")
	out, err = r.convertMessage(context.Background(), historyPortal(), nil, evt)
	if err != nil || out.ReplyTo == nil || out.ReplyTo.MessageID != "$reddit-reply" {
		t.Fatal("explicit in-thread reply was lost")
	}
}

func TestIncomingEditCannotBecomeSecondMessage(t *testing.T) {
	r := &RedditClient{}
	_, err := r.convertMessage(context.Background(), historyPortal(), nil, &event.Event{Content: event.Content{Parsed: &event.MessageEventContent{MsgType: event.MsgText, Body: "edited", RelatesTo: (&event.RelatesTo{}).SetReplace("$old")}}})
	if err == nil {
		t.Fatal("edit was converted into an ordinary new message")
	}
}
