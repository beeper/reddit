package connector

import (
	"errors"
	"net/http"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
)

// unbridgeableError marks native data that can never be bridged, so skipping it
// is safe. Any other failure, such as a network error, must be retried.
type unbridgeableError struct{ error }

func (e unbridgeableError) Unwrap() error { return e.error }

func unbridgeable(err error) error {
	if err == nil {
		return nil
	}
	return unbridgeableError{err}
}

func isUnbridgeable(err error) bool {
	return errors.As(err, new(unbridgeableError)) ||
		errors.Is(err, bridgev2.ErrUnsupportedMessageType) ||
		errors.Is(err, bridgev2.ErrUnsupportedMediaType) ||
		errors.Is(err, bridgev2.ErrMediaTooLarge)
}

// The server refused this specific request, so repeating it cannot succeed.
// Beyond not-found, require a Matrix errcode: Reddit's edge also returns
// temporary HTML blocks. Expired sessions, timeouts and rate limits retry.
func isPermanentHTTPError(err error) bool {
	var httpErr mautrix.HTTPError
	if !errors.As(err, &httpErr) || httpErr.Response == nil || isUnauthorized(err) {
		return false
	}
	code := httpErr.Response.StatusCode
	if isGoneStatus(code) {
		return true
	}
	return httpErr.RespError != nil && code >= 400 && code < 500 && code != http.StatusRequestTimeout && code != http.StatusTooManyRequests
}

func isGoneStatus(code int) bool {
	return code == http.StatusNotFound || code == http.StatusGone
}
