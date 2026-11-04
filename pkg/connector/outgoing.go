package connector

import (
	"errors"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// Local bridges supply the network transaction ID through bridgev2. For an
// appservice event, the Matrix event ID is the stable native request key.
func (r *RedditClient) outgoingTransaction(portal *bridgev2.Portal, evt *event.Event, input networkid.RawTransactionID) (string, error) {
	if portal == nil || portal.ID == "" || evt == nil || evt.ID == "" {
		return "", errors.New("outgoing event has no portal or event identity")
	}
	if portal.Receiver != "" && portal.Receiver != r.userLogin.ID {
		return "", errors.New("outgoing portal belongs to another login")
	}
	if input != "" {
		return string(input), nil
	}
	return string(evt.ID), nil
}

func mappedTarget(portal *bridgev2.Portal, target *database.Message) (id.EventID, error) {
	if target == nil || target.ID == "" {
		return "", errors.New("message target has no Reddit mapping")
	}
	if target.Room != portal.PortalKey {
		return "", errors.New("message target belongs to another portal")
	}
	return messageIDToEventID(target.ID), nil
}

func outgoingContent(msg *bridgev2.MatrixMessage) (*event.MessageEventContent, error) {
	if msg.Content == nil {
		return nil, errors.New("missing message content")
	}
	// Copy the content before replacing relations; the SDK still owns the source.
	content := *msg.Content
	// Reddit's web chat displays plain text. Forwarding Matrix HTML makes the
	// native bubble blank, so retain the plain body as the visible fallback.
	content.Format = ""
	content.FormattedBody = ""
	content.RelatesTo = nil
	content.NewContent = nil
	if msg.Content.RelatesTo.GetReplaceID() != "" {
		return nil, bridgev2.ErrEditsNotSupported
	}
	var root id.EventID
	if msg.ThreadRoot != nil {
		mappedRoot, err := mappedTarget(msg.Portal, msg.ThreadRoot)
		if err != nil {
			return nil, err
		}
		root = mappedRoot
		if msg.ThreadRoot.ThreadRoot != "" {
			root = messageIDToEventID(msg.ThreadRoot.ThreadRoot)
		}
	} else if msg.Content.RelatesTo.GetThreadParent() != "" {
		return nil, errors.New("thread root has not been mapped to Reddit")
	} else if msg.Content.RelatesTo.GetReplyTo() != "" {
		return nil, errors.New("reply target has not been mapped to Reddit")
	}
	if root != "" {
		content.RelatesTo = (&event.RelatesTo{}).SetThread(root, root)
	}
	return &content, nil
}
