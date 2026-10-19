package connector

import (
	"errors"
	"fmt"
	"net/http"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"

	"github.com/beeper/reddit/pkg/redditchat"
)

type redditFailure int

const (
	redditFailureTransient redditFailure = iota
	redditFailureRateLimited
	redditFailureUnauthorized
	redditFailureForbidden
	redditFailureRejected
)

func classifyRedditError(err error) redditFailure {
	redditErr, ok := redditchat.ExtractRedditError(err)
	switch {
	case ok && (redditErr.StatusCode == http.StatusTooManyRequests || redditErr.ErrCode == mautrix.MLimitExceeded.ErrCode):
		return redditFailureRateLimited
	case isUnauthorized(err):
		return redditFailureUnauthorized
	case ok && redditErr.StatusCode == http.StatusForbidden:
		return redditFailureForbidden
	case isPermanentHTTPError(err):
		return redditFailureRejected
	default:
		return redditFailureTransient
	}
}

// wrapRedditError maps a failed Reddit request to a message status without
// exposing provider error text.
func wrapRedditError(err error) error {
	if err == nil {
		return nil
	}
	status := bridgev2.WrapErrorInStatus(err)
	if isUnbridgeable(err) {
		return status.WithIsCertain(true).WithErrorReason(event.MessageStatusUnsupported).WithStatus(event.MessageStatusFail).WithMessage("reddit chat details are unavailable")
	}
	switch classifyRedditError(err) {
	case redditFailureRateLimited:
		return status.WithIsCertain(true).WithErrorReason(event.MessageStatusNetworkError).WithMessage("reddit is rate limiting requests, try again later")
	case redditFailureUnauthorized:
		return status.WithIsCertain(true).WithErrorReason(event.MessageStatusNetworkError).WithMessage("reddit session expired")
	case redditFailureForbidden:
		return status.WithIsCertain(true).WithErrorReason(event.MessageStatusNoPermission).WithStatus(event.MessageStatusFail).WithMessage("reddit doesn't allow this")
	case redditFailureRejected:
		return status.WithIsCertain(true).WithErrorReason(event.MessageStatusNetworkError).WithStatus(event.MessageStatusFail).WithMessage("reddit rejected this request")
	default:
		return status.WithIsCertain(false).WithErrorReason(event.MessageStatusNetworkError).WithMessage("couldn't reach reddit")
	}
}

func permanentFailure(reason event.MessageStatusReason, msg string) error {
	return bridgev2.WrapErrorInStatus(errors.New(msg)).WithIsCertain(true).WithErrorReason(reason).WithStatus(event.MessageStatusFail).WithMessage(msg)
}

// wrapRedditRespError maps a failed Reddit request to a provisioning error. The
// original error stays in the chain for logs; only the RespError is written.
func wrapRedditRespError(err error) error {
	var mapped bridgev2.RespError
	switch classifyRedditError(err) {
	case redditFailureRateLimited:
		mapped = bridgev2.RespError(mautrix.MLimitExceeded.WithMessage("Reddit is rate limiting requests. Please try again later."))
	case redditFailureUnauthorized:
		mapped = bridgev2.RespError(mautrix.MUnknown.WithMessage("Reddit session expired. Please try again shortly.").WithStatus(http.StatusBadGateway))
	case redditFailureForbidden:
		mapped = bridgev2.RespError(mautrix.MForbidden.WithMessage("Reddit doesn't allow this request."))
	case redditFailureRejected:
		mapped = bridgev2.RespError(mautrix.MUnknown.WithMessage("Reddit rejected the request.").WithStatus(http.StatusBadRequest))
	default:
		mapped = bridgev2.RespError(mautrix.MUnknown.WithMessage("Failed to reach Reddit. Please try again.").WithStatus(http.StatusBadGateway))
	}
	return fmt.Errorf("%w: %w", mapped, err)
}
