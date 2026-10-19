package redditchat

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type pushTransport func(*http.Request) (*http.Response, error)

func (f pushTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func pushTestClient(t *testing.T, transport pushTransport) *Client {
	t.Helper()
	c, err := New(Credentials{UserID: "@t2_test:reddit.com", AccessToken: "secret-access-token"})
	if err != nil {
		t.Fatal(err)
	}
	c.SetHTTPClient(&http.Client{Transport: transport})
	return c
}

func TestRegisterFCMPushWire(t *testing.T) {
	calls := 0
	c := pushTestClient(t, func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Method != http.MethodPost || req.URL.String() != "https://gql-fed.reddit.com/" {
			t.Fatalf("wrong endpoint: %s %s", req.Method, req.URL)
		}
		if req.Header.Get("Authorization") != "Bearer secret-access-token" || req.Header.Get("X-Reddit-Device-Id") != "bridge-device" || req.Header.Get("Content-Type") != "application/json" || !strings.Contains(req.UserAgent(), "Reddit/Version 2026.41.0/") {
			t.Fatal("wrong registration headers")
		}
		var body struct {
			OperationName string                     `json:"operationName"`
			Variables     map[string]json.RawMessage `json:"variables"`
			Extensions    struct {
				PersistedQuery struct {
					Version int    `json:"version"`
					Hash    string `json:"sha256Hash"`
				} `json:"persistedQuery"`
			} `json:"extensions"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.OperationName != "RegisterMobilePushToken" || body.Extensions.PersistedQuery.Version != 1 || body.Extensions.PersistedQuery.Hash != "35313b81bfbb621c426b2f2ec276db3db476a5d8362a1ddbeec982a01dc74977" {
			t.Fatal("wrong persisted operation")
		}
		if len(body.Variables) != 6 || string(body.Variables["authTokens"]) != `["secret-access-token"]` || string(body.Variables["pushToken"]) != `"secret-push-token"` || string(body.Variables["deviceId"]) != `"bridge-device"` || string(body.Variables["timezoneName"]) != `"UTC"` || string(body.Variables["language"]) != `"en"` {
			t.Fatal("wrong registration variables")
		}
		var timestamp time.Time
		if err := json.Unmarshal(body.Variables["timestamp"], &timestamp); err != nil || time.Since(timestamp).Abs() > time.Minute {
			t.Fatalf("invalid registration timestamp: %v", err)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"data":{"registerMobilePushToken":{"ok":true}}}`))}, nil
	})
	if err := c.RegisterFCMPush(t.Context(), "secret-push-token", "bridge-device"); err != nil || calls != 1 {
		t.Fatalf("registration: %v (%d calls)", err, calls)
	}
}

func TestRegisterFCMPushFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		auth   bool
	}{
		{"unauthorized", 401, `secret-access-token`, true},
		{"forbidden", 403, `secret-push-token`, false},
		{"rate limited", 429, `{}`, false},
		{"empty", 200, ``, false},
		{"malformed", 200, `secret-access-token`, false},
		{"null data", 200, `{"data":null}`, false},
		{"missing result", 200, `{"data":{}}`, false},
		{"rejected", 200, `{"data":{"registerMobilePushToken":{"ok":false}}}`, false},
		{"partial error", 200, `{"data":{"registerMobilePushToken":{"ok":true}},"errors":[{"message":"secret-push-token"}]}`, false},
		{"graphql auth", 200, `{"errors":[{"message":"secret-access-token","extensions":{"code":"NOT_AUTHENTICATED"}}]}`, true},
		{"redirect", 307, ``, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			c := pushTestClient(t, func(req *http.Request) (*http.Response, error) {
				calls++
				if calls > 1 {
					t.Fatal("registration followed a redirect or retried")
				}
				return &http.Response{StatusCode: tc.status, Header: http.Header{"Location": {"https://other.example/"}}, Body: io.NopCloser(strings.NewReader(tc.body)), Request: req}, nil
			})
			err := c.RegisterFCMPush(t.Context(), "secret-push-token", "device")
			if err == nil || errors.Is(err, ErrPushAuthentication) != tc.auth {
				t.Fatalf("wrong error: %v", err)
			}
			if strings.Contains(err.Error(), "secret-") {
				t.Fatal("error exposed a credential")
			}
		})
	}
}

func TestRegisterFCMPushValidationAndCancellation(t *testing.T) {
	c := pushTestClient(t, func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid registration made a request")
		return nil, nil
	})
	for _, args := range [][2]string{{"", "device"}, {" \n", "device"}, {"token", ""}} {
		if err := c.RegisterFCMPush(t.Context(), args[0], args[1]); err == nil {
			t.Fatal("accepted invalid registration")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := c.RegisterFCMPush(ctx, "token", "device"); !errors.Is(err, context.Canceled) {
		t.Fatalf("wrong cancellation error: %v", err)
	}
	if err := (*Client)(nil).RegisterFCMPush(t.Context(), "token", "device"); !errors.Is(err, ErrPushAuthentication) {
		t.Fatalf("wrong missing-auth error: %v", err)
	}
	c.Matrix.Client.Transport = pushTransport(func(*http.Request) (*http.Response, error) {
		return nil, context.DeadlineExceeded
	})
	if err := c.RegisterFCMPush(t.Context(), "token", "device"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("transport error lost cause: %v", err)
	}
}
