package connector

import (
	"errors"
	"fmt"
	"net/http"

	"maunium.net/go/mautrix/bridgev2"

	"github.com/beeper/reddit/pkg/redditchat"
)

var (
	ErrLoginInvalidCredentials = bridgev2.RespError{
		ErrCode:    "COM.BEEPER.REDDIT.INVALID_CREDENTIALS",
		Err:        "Reddit rejected that username or password. Please check them and try again.",
		StatusCode: http.StatusUnauthorized,
	}
	ErrLoginInvalidOTP = bridgev2.RespError{
		ErrCode:    "COM.BEEPER.REDDIT.INVALID_OTP",
		Err:        "That two-factor code wasn't accepted. Please try again.",
		StatusCode: http.StatusUnauthorized,
	}
	ErrLoginSSORequired = bridgev2.RespError{
		ErrCode:    "COM.BEEPER.REDDIT.SSO_REQUIRED",
		Err:        "This Reddit account signs in with Google or Apple, which this bridge can't use. Set a Reddit password on reddit.com, then try again.",
		StatusCode: http.StatusBadRequest,
	}
	ErrLoginVerificationBlocked = bridgev2.RespError{
		ErrCode:    "COM.BEEPER.REDDIT.VERIFICATION_BLOCKED",
		Err:        "Reddit blocked the sign-in with a verification check. Please wait a few minutes and try again.",
		StatusCode: http.StatusForbidden,
	}
	ErrLoginCaptchaFailed = bridgev2.RespError{
		ErrCode:    "COM.BEEPER.REDDIT.CAPTCHA_FAILED",
		Err:        "The CAPTCHA couldn't be completed. Please try again.",
		StatusCode: http.StatusBadRequest,
	}
	ErrLoginUnknown = bridgev2.RespError{
		ErrCode:    "M_UNKNOWN",
		Err:        "Internal error logging in to Reddit",
		StatusCode: http.StatusInternalServerError,
	}
)

// wrapRedditLoginError translates a redditchat error into one the client can act on.
// The original error is kept in the chain with %w — importantly, redditchat's status
// errors embed the raw response body, which must never be shown to a user.
func wrapRedditLoginError(err error) error {
	if err == nil {
		return nil
	}
	mapped := ErrLoginUnknown
	switch {
	case errors.Is(err, redditchat.ErrInvalidCredentials):
		mapped = ErrLoginInvalidCredentials
	case errors.Is(err, redditchat.ErrInvalidOTP):
		mapped = ErrLoginInvalidOTP
	case errors.Is(err, redditchat.ErrSSORequired):
		mapped = ErrLoginSSORequired
	case errors.Is(err, redditchat.ErrBrowserVerificationBlocked):
		mapped = ErrLoginVerificationBlocked
	case errors.Is(err, redditchat.ErrCaptchaRequired):
		mapped = ErrLoginCaptchaFailed
	}
	return fmt.Errorf("%w: %w", mapped, err)
}
