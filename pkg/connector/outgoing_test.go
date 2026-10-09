package connector

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
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

func TestReactionRemovalUsesPersistedProviderIdentity(t *testing.T) {
	var removePath string
	r := testClient(t, func(w http.ResponseWriter, req *http.Request) {
		if strings.Contains(req.URL.Path, "/send/m.reaction/") {
			_, _ = io.WriteString(w, `{"event_id":"$reddit-reaction"}`)
		} else {
			removePath = req.URL.Path
			_, _ = io.WriteString(w, `{"event_id":"$reddit-redaction"}`)
		}
	})
	p := historyPortal()
	resp, err := r.HandleMatrixReaction(context.Background(), &bridgev2.MatrixReaction{
		MatrixEventBase: bridgev2.MatrixEventBase[*event.ReactionEventContent]{Portal: p, Event: &event.Event{ID: "$beeper-reaction"}},
		TargetMessage:   &database.Message{ID: "$reddit-target", Room: p.PortalKey},
		PreHandleResp:   &bridgev2.MatrixReactionPreResponse{EmojiID: "👍"},
	})
	if err != nil {
		t.Fatal(err)
	}
	serialized, err := json.Marshal(resp.Metadata)
	if err != nil {
		t.Fatal(err)
	}
	var restored ReactionMetadata
	if err = json.Unmarshal(serialized, &restored); err != nil {
		t.Fatal(err)
	}
	remove := &bridgev2.MatrixReactionRemove{MatrixEventBase: bridgev2.MatrixEventBase[*event.RedactionEventContent]{Portal: p, Event: &event.Event{ID: "$remove"}}, TargetReaction: &database.Reaction{Room: p.PortalKey, MXID: "$beeper-reaction", Metadata: &restored}}
	if err = r.HandleMatrixReactionRemove(context.Background(), remove); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(removePath, "/redact/$reddit-reaction/") || strings.Contains(removePath, "$beeper-reaction") {
		t.Fatalf("wrong deletion target: %s", removePath)
	}
	remove.TargetReaction.Metadata = &ReactionMetadata{}
	if err = r.HandleMatrixReactionRemove(context.Background(), remove); err == nil {
		t.Fatal("missing remote identity was silently substituted")
	}
}
