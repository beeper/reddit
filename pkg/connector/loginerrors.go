package connector

import (
	"errors"
	"fmt"
	"net/http"

	"maunium.net/go/mautrix/bridgev2"

	"github.com/beeper/reddit/pkg/redditchat"
)

var (
	ErrLoginMissingCredentials = bridgev2.RespError{
		ErrCode:    "FI.MAU.REDDIT.MISSING_CREDENTIALS",
		Err:        "Username and password are required.",
		StatusCode: http.StatusBadRequest,
	}
	ErrLoginMissingOTP = bridgev2.RespError{
		ErrCode:    "FI.MAU.REDDIT.MISSING_OTP",
		Err:        "Enter the 6-digit code from your authenticator app.",
		StatusCode: http.StatusBadRequest,
	}
	ErrLoginVerificationFailed = bridgev2.RespError{
		ErrCode:    "FI.MAU.REDDIT.VERIFICATION_FAILED",
		Err:        "Reddit verification did not complete. Please try again.",
		StatusCode: http.StatusBadRequest,
	}
	ErrLoginInvalidCredentials = bridgev2.RespError{
		ErrCode:    "FI.MAU.REDDIT.INVALID_CREDENTIALS",
		Err:        "Reddit rejected that username or password. Please check them and try again.",
		StatusCode: http.StatusUnauthorized,
	}
	ErrLoginInvalidOTP = bridgev2.RespError{
		ErrCode:    "FI.MAU.REDDIT.INVALID_OTP",
		Err:        "That two-factor code wasn't accepted. Please try again.",
		StatusCode: http.StatusUnauthorized,
	}
	ErrLoginSSORequired = bridgev2.RespError{
		ErrCode:    "FI.MAU.REDDIT.SSO_REQUIRED",
		Err:        "This Reddit account signs in with Google or Apple, which this bridge can't use. Set a Reddit password on reddit.com, then try again.",
		StatusCode: http.StatusBadRequest,
	}
	ErrLoginVerificationBlocked = bridgev2.RespError{
		ErrCode:    "FI.MAU.REDDIT.VERIFICATION_BLOCKED",
		Err:        "Reddit blocked the sign-in with a verification check. Please wait a few minutes and try again.",
		StatusCode: http.StatusForbidden,
	}
	ErrLoginRejected = bridgev2.RespError{
		ErrCode:    "FI.MAU.REDDIT.LOGIN_REJECTED",
		StatusCode: http.StatusBadRequest,
	}
	ErrLoginUnknown = bridgev2.RespError{
		ErrCode:    "M_UNKNOWN",
		Err:        "Internal error logging in to Reddit",
		StatusCode: http.StatusInternalServerError,
	}
)

// The original error stays in the chain for logs; only the mapped RespError is
// written to the client.
func wrapRedditLoginError(err error) error {
	var mapped bridgev2.RespError
	var statusErr *redditchat.LoginHTTPError
	switch {
	case errors.Is(err, redditchat.ErrInvalidCredentials):
		mapped = ErrLoginInvalidCredentials
	case errors.Is(err, redditchat.ErrInvalidOTP):
		mapped = ErrLoginInvalidOTP
	case errors.Is(err, redditchat.ErrSSORequired):
		mapped = ErrLoginSSORequired
	case errors.Is(err, redditchat.ErrBrowserVerificationBlocked):
		mapped = ErrLoginVerificationBlocked
	case errors.As(err, &statusErr):
		mapped = ErrLoginRejected.WithMessage("Reddit rejected the %s request (HTTP %d).", statusErr.Operation, statusErr.StatusCode)
	default:
		mapped = ErrLoginUnknown
	}
	return fmt.Errorf("%w: %w", mapped, err)
}
