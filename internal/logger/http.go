package logger

import (
	"context"
	"errors"
)

// HTTPErrorSummary describes an HTTP failure without including its URL, which
// can contain credentials, message data, or other private query parameters.
func HTTPErrorSummary(err error) string {
	if errors.Is(err, context.Canceled) {
		return "request canceled"
	}
	var timeout interface{ Timeout() bool }
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &timeout) && timeout.Timeout()) {
		return "request timed out"
	}
	return "request failed"
}
