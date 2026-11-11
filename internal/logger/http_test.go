package logger

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestHTTPErrorSummaryDoesNotExposeWrappedData(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{fmt.Errorf("private-url: %w", context.Canceled), "request canceled"},
		{fmt.Errorf("private-url: %w", context.DeadlineExceeded), "request timed out"},
		{errors.New("private-url: connection refused"), "request failed"},
	} {
		got := HTTPErrorSummary(tc.err)
		if got != tc.want || strings.Contains(got, "private-url") {
			t.Fatalf("unexpected safe summary %q, want %q", got, tc.want)
		}
	}
}
