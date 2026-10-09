package redditchat

import (
	"net/http"
	"strings"
	"testing"
)

func TestLoginResponsesDoNotExposeSessionMaterial(t *testing.T) {
	const secret = "synthetic-login-secret-do-not-log"
	for _, tc := range []struct {
		name    string
		body    string
		wantErr bool
	}{
		{"success", `{"success":true,"session":"` + secret + `"}`, false},
		{"failure", `{"success":false,"error":"` + secret + `"}`, true},
		{"reason", `{"reason":"` + secret + `"}`, true},
		{"non_json", secret, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkLoginResponseBody([]byte(tc.body), ErrInvalidCredentials)
			if (err != nil) != tc.wantErr {
				t.Fatalf("unexpected rejection result: %v", err)
			}
			if err != nil && strings.Contains(err.Error(), secret) {
				t.Fatal("authentication response leaked into logs or client error")
			}
		})
	}
}

func TestLoginHTTPErrorDoesNotExposeResponseOrRedirect(t *testing.T) {
	const secret = "synthetic-redirect-secret-do-not-log"
	req, err := http.NewRequest(http.MethodPost, "https://www.reddit.com/login?token="+secret, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp := &http.Response{
		StatusCode: http.StatusForbidden,
		Status:     secret,
		Request:    req,
		Header: http.Header{
			"Location":     {"https://www.reddit.com/?token=" + secret},
			"Content-Type": {secret},
			"Cf-Ray":       {secret},
		},
	}
	result := redditStatusError(resp, []byte(secret))
	if strings.Contains(result.Error(), secret) {
		t.Fatal("authentication HTTP error leaked response or URL data")
	}
	if !strings.Contains(result.Error(), "status=403") {
		t.Fatal("HTTP status was lost")
	}
}
