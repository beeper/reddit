package redditchat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

type sequencedEvents struct {
	Events []json.RawMessage `json:"events"`
}

func parseSequencedEvent(raw json.RawMessage) (*event.Event, string, error) {
	var sequence struct {
		ID string `json:"seq_id"`
	}
	var evt event.Event
	if err := json.Unmarshal(raw, &sequence); err != nil {
		return nil, "", err
	}
	if err := json.Unmarshal(raw, &evt); err != nil {
		return nil, "", err
	}
	return &evt, sequence.ID, nil
}

// ThreadMessages follows the sequenced /events contract used by Reddit Chat.
// The ordinary Matrix /relations endpoint returns no thread replies. Resolve
// the stable event ID to a native sequence, then paginate its child stream.
func (c *Client) ThreadMessages(ctx context.Context, room id.RoomID, root id.EventID, from string, limit int) (*mautrix.RespMessages, error) {
	if room == "" || root == "" {
		return nil, errors.New("thread history requires a room and root")
	}
	if limit <= 0 {
		limit = 100
	}
	limit = min(limit, 100)
	query := map[string]string{"event_id": string(root), "dir": "b", "before": "1", "after": "1"}
	var contextEvents sequencedEvents
	_, err := c.Matrix.MakeRequest(ctx, http.MethodGet, c.Matrix.BuildURLWithQuery(mautrix.ClientURLPath{"v3", "rooms", room, "events"}, query), nil, &contextEvents)
	if err != nil {
		return nil, err
	}
	parent := ""
	for _, raw := range contextEvents.Events {
		evt, sequence, err := parseSequencedEvent(raw)
		if err != nil {
			return nil, err
		}
		if evt.ID != root {
			continue
		}
		if parent != "" || (evt.RoomID != "" && evt.RoomID != room) {
			return nil, errors.New("thread root has conflicting identity")
		}
		if _, err = strconv.ParseUint(sequence, 10, 64); err != nil {
			return nil, errors.New("thread root has an invalid native sequence")
		}
		parent = sequence
	}
	if parent == "" {
		return nil, errors.New("thread root was not returned by Reddit")
	}
	query = map[string]string{"parent": parent, "dir": "b", "before": strconv.Itoa(limit)}
	var boundary uint64
	if from != "" {
		boundary, err = strconv.ParseUint(from, 10, 64)
		if err != nil {
			return nil, errors.New("invalid thread history cursor")
		}
		query["seq"], query["before"], query["after"] = from, strconv.Itoa(limit-1), "0"
	}
	var page sequencedEvents
	_, err = c.Matrix.MakeRequest(ctx, http.MethodGet, c.Matrix.BuildURLWithQuery(mautrix.ClientURLPath{"v3", "rooms", room, "events"}, query), nil, &page)
	if err != nil {
		return nil, err
	}
	result := &mautrix.RespMessages{Start: from}
	var oldest uint64
	for i, raw := range page.Events {
		evt, sequence, err := parseSequencedEvent(raw)
		if err != nil {
			return nil, err
		}
		child, ok := strings.CutPrefix(sequence, parent+".")
		seq, parseErr := strconv.ParseUint(child, 10, 64)
		if !ok || parseErr != nil || (from != "" && seq > boundary) || (evt.RoomID != "" && evt.RoomID != room) {
			return nil, errors.New("thread history event is outside the requested stream")
		}
		if i == 0 || seq < oldest {
			oldest = seq
		}
		result.Chunk = append(result.Chunk, evt)
	}
	// Reddit's native client continues at the preceding sequence, including on
	// short pages. Sequence zero is the explicit beginning of the child stream.
	if len(page.Events) > 0 && oldest > 0 {
		result.End = strconv.FormatUint(oldest-1, 10)
	}
	return result, nil
}
