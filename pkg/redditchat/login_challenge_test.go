package redditchat

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPrepareLoginHandlesVerificationPageVariants(t *testing.T) {
	for _, tc := range []struct {
		name, tokenName, heading string
	}{
		{"current_without_heading", "jsc_token", ""},
		{"legacy", "token", "Please wait for verification"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Method != http.MethodGet || r.URL.Path != "/login/" {
					t.Errorf("unexpected login request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if r.URL.Query().Get("js_challenge") == "1" {
					q := r.URL.Query()
					if q.Get(tc.tokenName) != "test-challenge-token" || q.Get("solution") != "seedseed" || q.Get("jsc_orig_r") != "/login/" {
						t.Error("verification form was not preserved")
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					http.SetCookie(w, &http.Cookie{Name: "csrf_token", Value: "test-csrf", Path: "/"})
					_, _ = w.Write([]byte("<html>Login form</html>"))
					return
				}
				_, _ = fmt.Fprintf(w, `<html>%s<script>await(async e=>e+e)("seed")</script><form action="/login/"><input type="hidden" name="solution" value=""><input type="hidden" name="js_challenge" value="1"><input type="hidden" name="%s" value="test-challenge-token"><input type="hidden" name="jsc_orig_r" value="/login/"></form></html>`, tc.heading, tc.tokenName)
			}))
			defer server.Close()
			session, err := NewRedditSession(nil, server.URL, "test-client")
			if err != nil {
				t.Fatal(err)
			}
			_, err = session.prepareLogin(context.Background())
			if err != nil {
				t.Fatalf("preparing login failed: %v", err)
			}
			if requests != 2 || session.CSRFToken != "test-csrf" {
				t.Fatalf("login preparation incomplete: requests=%d csrf_present=%v", requests, session.CSRFToken != "")
			}
		})
	}
}
