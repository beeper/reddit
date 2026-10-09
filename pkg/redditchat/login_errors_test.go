package redditchat

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLoginRejectionsMatchSentinels(t *testing.T) {
	submitPassword := func(s *RedditSession) error {
		_, err := s.SubmitPassword(context.Background(), "user", "pass", CaptchaResult{Token: "token"})
		return err
	}
	submitOTP := func(s *RedditSession) error {
		return s.SubmitOTP(context.Background(), "user", "pass", "123456", CaptchaResult{Token: "token"})
	}
	checkSSO := func(s *RedditSession) error {
		return s.checkOIDCRequired(context.Background(), "user")
	}
	for _, tc := range []struct {
		name   string
		status int
		body   string
		call   func(*RedditSession) error
		want   error
	}{
		{"password status", http.StatusUnauthorized, "", submitPassword, ErrInvalidCredentials},
		{"password body", http.StatusOK, `{"success":false}`, submitPassword, ErrInvalidCredentials},
		{"otp status", http.StatusBadRequest, "", submitOTP, ErrInvalidOTP},
		{"otp body", http.StatusOK, `{"success":false}`, submitOTP, ErrInvalidOTP},
		{"sso", http.StatusOK, `{"is_sso":true}`, checkSSO, ErrSSORequired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			session, err := NewRedditSession(nil, server.URL, "test-client")
			if err != nil {
				t.Fatal(err)
			}
			if err = tc.call(session); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}
