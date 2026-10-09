package connector

import (
	"bytes"
	"context"
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

type directMediaTestMatrix struct {
	bridgev2.MatrixConnector
	ids []networkid.MediaID
}

func (m *directMediaTestMatrix) GenerateContentURI(_ context.Context, mediaID networkid.MediaID) (id.ContentURIString, error) {
	m.ids = append(m.ids, bytes.Clone(mediaID))
	return "mxc://proxy.example/opaque", nil
}

func TestDirectMediaKeepsStableAccountIdentityWithoutFetching(t *testing.T) {
	matrix := &directMediaTestMatrix{}
	main := &RedditConnector{Bridge: &bridgev2.Bridge{Matrix: matrix}}
	main.SetUseDirectMedia()
	for _, owner := range []networkid.UserLoginID{"t2_a", "t2_b"} {
		r := &RedditClient{main: main, userLogin: &bridgev2.UserLogin{UserLogin: &database.UserLogin{ID: owner}}}
		original := &event.FileInfo{ThumbnailURL: "mxc://reddit.com/thumb"}
		content := &event.MessageEventContent{URL: "mxc://reddit.com/image", Info: original}
		// No native client or Matrix intent: message conversion must do no IO.
		if err := r.receiveMedia(context.Background(), nil, nil, content); err != nil {
			t.Fatal(err)
		}
		if content.URL != "mxc://proxy.example/opaque" || content.Info.ThumbnailURL != content.URL || original.ThumbnailURL != "mxc://reddit.com/thumb" {
			t.Fatal("direct media failed or mutated source metadata")
		}
		for _, foreign := range []id.ContentURIString{"https://cdn.example/image?expires=1", "mxc://other.example/image"} {
			if _, err := r.directMediaURI(context.Background(), foreign); err == nil {
				t.Fatal("accepted a transient or foreign media URL")
			}
		}
	}
	if len(matrix.ids) != 4 || string(matrix.ids[0]) != "1\x00t2_a\x00image" || string(matrix.ids[2]) != "1\x00t2_b\x00image" {
		t.Fatal("native identity was lost or shared across accounts")
	}
}
