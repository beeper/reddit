package connector

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"maunium.net/go/mautrix/bridgev2"

	"github.com/beeper/reddit/pkg/redditchat"
)

func TestWrapRedditLoginErrorMapsToClientErrors(t *testing.T) {
	statusErr := &redditchat.LoginHTTPError{Operation: "sign-in", StatusCode: http.StatusTooManyRequests}
	for _, tc := range []struct {
		name string
		err  error
		want bridgev2.RespError
	}{
		{"invalid credentials", fmt.Errorf("%w: %w", redditchat.ErrInvalidCredentials, statusErr), ErrLoginInvalidCredentials},
		{"invalid otp", redditchat.ErrInvalidOTP, ErrLoginInvalidOTP},
		{"sso", redditchat.ErrSSORequired, ErrLoginSSORequired},
		{"verification blocked", redditchat.ErrBrowserVerificationBlocked, ErrLoginVerificationBlocked},
		{"http status", statusErr, ErrLoginRejected},
		{"unknown", errors.New("dial tcp: connection refused"), ErrLoginUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := wrapRedditLoginError(tc.err)
			var respErr bridgev2.RespError
			if !errors.As(err, &respErr) || respErr.ErrCode != tc.want.ErrCode || respErr.StatusCode != tc.want.StatusCode {
				t.Fatalf("got %+v, want %s", respErr, tc.want.ErrCode)
			}
			if !errors.Is(err, tc.err) {
				t.Fatal("original error was dropped from the chain")
			}
		})
	}
}
