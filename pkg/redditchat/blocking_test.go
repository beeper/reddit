package redditchat

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func blockingSession(t *testing.T, handler http.HandlerFunc) *RedditSession {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	session, err := NewRedditSession(srv.Client(), srv.URL, "test-agent")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(srv.URL)
	session.Client.Jar.SetCookies(u, []*http.Cookie{{Name: "csrf_token", Value: "test-csrf"}, {Name: "reddit_session", Value: "test-session"}})
	return session
}

// Catches accidentally using Matrix auth/IDs or the legacy GraphQL shape.
func TestSetUserBlockedNativeWire(t *testing.T) {
	for _, blocked := range []bool{true, false} {
		t.Run(map[bool]string{true: "block", false: "unblock"}[blocked], func(t *testing.T) {
			s := blockingSession(t, func(w http.ResponseWriter, req *http.Request) {
				if req.Method != http.MethodPost || req.URL.Path != "/svc/shreddit/graphql" {
					t.Errorf("wrong endpoint: %s %s", req.Method, req.URL.Path)
				}
				for header, want := range map[string]string{"Content-Type": "application/json", "Accept": "application/json", "User-Agent": "test-agent", "Authorization": ""} {
					if got := req.Header.Get(header); got != want {
						t.Errorf("%s = %q, want %q", header, got, want)
					}
				}
				if req.Header.Get("Referer") != "http://"+req.Host+"/chat/" || req.Header.Get("Origin") != "http://"+req.Host {
					t.Error("missing Reddit origin/referer")
				}
				cookie, err := req.Cookie("reddit_session")
				if err != nil || cookie.Value != "test-session" {
					t.Error("missing session cookie")
				}
				var got map[string]any
				if err = json.NewDecoder(req.Body).Decode(&got); err != nil {
					t.Fatal(err)
				}
				state := "NONE"
				if blocked {
					state = "BLOCKED"
				}
				want := map[string]any{"operation": "ChatUpdateRedditorBlockState", "csrf_token": "test-csrf", "variables": map[string]any{"input": map[string]any{"redditorId": "t2_b", "blockState": state}}}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("request = %#v, want %#v", got, want)
				}
				io.WriteString(w, `{"data":{"updateRedditorBlockState":{"ok":true}},"errors":[],"operation":"ChatUpdateRedditorBlockState"}`)
			})
			if err := s.SetUserBlocked(t.Context(), "t2_b", blocked); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// Catches HTTP-200 GraphQL failures being acknowledged as successful blocks.
func TestSetUserBlockedRejectsFailures(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"http", "private-server-body", 429},
		{"graphql", `{"data":{"updateRedditorBlockState":{"ok":true}},"errors":[{"message":"private-server-body"}]}`, 200},
		{"rejected", `{"data":{"updateRedditorBlockState":{"ok":false}}}`, 200},
		{"missing result", `{"data":{}}`, 200},
		{"null data", `{"data":null}`, 200},
		{"missing data", `{}`, 200},
		{"malformed", `private-server-body`, 200},
		{"wrong type", `{"data":{"updateRedditorBlockState":{"ok":"private-server-body"}}}`, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := blockingSession(t, func(w http.ResponseWriter, req *http.Request) {
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			})
			err := s.SetUserBlocked(t.Context(), "t2_b", true)
			if err == nil || strings.Contains(err.Error(), "private-server-body") {
				t.Fatalf("failure not safely propagated: %v", err)
			}
		})
	}
	s := blockingSession(t, func(http.ResponseWriter, *http.Request) { t.Error("invalid input reached Reddit") })
	for _, user := range []string{"", "name", "@t2_b:reddit.com", "t2_b/path"} {
		if err := s.SetUserBlocked(t.Context(), user, true); err == nil {
			t.Errorf("invalid user accepted: %s", user)
		}
	}
	var missing *RedditSession
	if err := missing.SetUserBlocked(t.Context(), "t2_b", true); err != ErrSessionUnavailable {
		t.Fatalf("missing session: %v", err)
	}
}

// Catches malformed snapshots being interpreted as an authoritative unblock-all.
func TestGetBlockedUsersNativeResponse(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       []string
	}{
		{"populated", `{"data":{"identity":{"blockedRedditorsInfo":{"edges":[{"node":{"id":"t2_b","name":"counterpart"}},{"node":{"id":"t2_c"}}]}}},"errors":[]}`, []string{"t2_b", "t2_c"}},
		{"empty", `{"data":{"identity":{"blockedRedditorsInfo":{"edges":[]}}}}`, []string{}},
		{"null list", `{"data":{"identity":{"blockedRedditorsInfo":null}}}`, nil},
		{"missing edges", `{"data":{"identity":{"blockedRedditorsInfo":{}}}}`, nil},
		{"null edges", `{"data":{"identity":{"blockedRedditorsInfo":{"edges":null}}}}`, nil},
		{"missing ID", `{"data":{"identity":{"blockedRedditorsInfo":{"edges":[{"node":{}}]}}}}`, nil},
		{"invalid ID", `{"data":{"identity":{"blockedRedditorsInfo":{"edges":[{"node":{"id":"username"}}]}}}}`, nil},
		{"partial error", `{"data":{"identity":{"blockedRedditorsInfo":{"edges":[]}}},"errors":[{}]}`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := blockingSession(t, func(w http.ResponseWriter, req *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if body["operation"] != "ChatBlockedRedditors" || !reflect.DeepEqual(body["variables"], map[string]any{}) {
					t.Errorf("incorrect list request: %#v", body)
				}
				io.WriteString(w, tc.body)
			})
			got, err := s.GetBlockedUsers(t.Context())
			if (err != nil) != (tc.want == nil) || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("list = %v, %v; want %v", got, err, tc.want)
			}
		})
	}
}
