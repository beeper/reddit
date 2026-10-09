package connector

import (
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/event"
)

func outgoingTestMessage() *bridgev2.MatrixMessage {
	p := historyPortal()
	return &bridgev2.MatrixMessage{MatrixEventBase: bridgev2.MatrixEventBase[*event.MessageEventContent]{
		Portal: p, Event: &event.Event{ID: "$beeper-event", Timestamp: 1234},
		Content: &event.MessageEventContent{MsgType: event.MsgText, Body: "test", RelatesTo: (&event.RelatesTo{}).SetReplyTo("$beeper-target")},
	}, ReplyTo: &database.Message{ID: "$reddit-target", Room: p.PortalKey}}
}

func TestOutgoingThreadTranslationAndMissingMapping(t *testing.T) {
	msg := outgoingTestMessage()
	msg.ThreadRoot = &database.Message{ID: "$reddit-root", Room: msg.Portal.PortalKey}
	msg.Content.RelatesTo = (&event.RelatesTo{}).SetThread("$beeper-root", "$beeper-target")
	content, err := outgoingContent(msg)
	if err != nil || content.RelatesTo.GetThreadParent() != "$reddit-root" || content.RelatesTo.GetReplyTo() != "$reddit-target" || !content.RelatesTo.IsFallingBack {
		t.Fatalf("thread/reply conversion failed: %v", err)
	}
	msg.ThreadRoot = nil
	if _, err = outgoingContent(msg); err == nil {
		t.Fatal("unmapped thread was flattened")
	}
}

func TestReplyToThreadMessageKeepsNativeRoot(t *testing.T) {
	msg := outgoingTestMessage()
	msg.ReplyTo.ThreadRoot = "$existing-root"
	content, err := outgoingContent(msg)
	if err != nil {
		t.Fatal(err)
	}
	if content.RelatesTo.GetThreadParent() != "$existing-root" || content.RelatesTo.GetReplyTo() != "$reddit-target" || !content.RelatesTo.IsFallingBack {
		t.Fatal("reply created a nested thread instead of retaining the native root")
	}
}
