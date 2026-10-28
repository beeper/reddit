package connector

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/bridgeconfig"
	"maunium.net/go/mautrix/bridgev2/database"

	"github.com/beeper/reddit/pkg/redditchat"
)

func testClient(t *testing.T, handler http.HandlerFunc) *RedditClient {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	creds := redditchat.Credentials{Homeserver: srv.URL, UserID: "@t2_a:reddit.com", AccessToken: "test-token"}
	rc, err := redditchat.New(creds)
	if err != nil {
		t.Fatal(err)
	}
	main := &RedditConnector{Bridge: &bridgev2.Bridge{Config: &bridgeconfig.BridgeConfig{}}}
	main.Config.DisplaynameTemplate = "{{ .Username }}"
	if err = main.Config.PostProcess(); err != nil {
		t.Fatal(err)
	}
	return &RedditClient{main: main, rc: rc, meta: &UserLoginMetadata{Credentials: creds}, userID: "t2_a", userLogin: &bridgev2.UserLogin{UserLogin: &database.UserLogin{ID: "t2_a"}}}
}

func TestResolveIdentifierRequiresUnambiguousExactMatch(t *testing.T) {
	for _, tc := range []struct {
		name, query, response, wantID string
	}{
		{"fuzzy only", "intended", `{"results":[{"user_id":"@t2_b:reddit.com","display_name":"someone_else"}]}`, ""},
		{"ambiguous", "intended", `{"results":[{"user_id":"@t2_b:reddit.com","display_name":"intended"},{"user_id":"@t2_c:reddit.com","display_name":"intended"}]}`, ""},
		{"exact after fuzzy", " /u/Intended ", `{"results":[{"user_id":"@t2_b:reddit.com","display_name":"intended_other"},{"user_id":"@t2_c:reddit.com","display_name":"u/intended"}]}`, "t2_c"},
		{"duplicate same identity", "intended", `{"results":[{"user_id":"@t2_b:reddit.com","display_name":"intended"},{"user_id":"@t2_b:reddit.com","display_name":"intended"}]}`, "t2_b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := testClient(t, func(w http.ResponseWriter, req *http.Request) {
				if req.URL.Path != "/_matrix/client/v3/user_directory/search" {
					t.Errorf("unexpected request: %s", req.URL.Path)
				}
				_, _ = io.WriteString(w, tc.response)
			})
			result, err := r.ResolveIdentifier(context.Background(), tc.query, false)
			if tc.wantID == "" {
				if err == nil || result != nil {
					t.Fatal("ambiguous or fuzzy identifier was accepted")
				}
			} else if err != nil || string(result.UserID) != tc.wantID {
				t.Fatalf("exact lookup = %v, %v", result, err)
			}
		})
	}
}
