package testutil

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// RequireErrorAs returns the first error in err's chain of type E, failing tb
// unless err is non-nil and has one.
func RequireErrorAs[E error](tb testing.TB, err error) E {
	tb.Helper()
	require.Error(tb, err)
	e, ok := errors.AsType[E](err)
	require.True(tb, ok, "want a %T, got %T", e, err)
	return e
}

// TimeoutError is a net.Error that reports a timeout. Its message is Msg, so
// a test can plant text in it that must never surface.
type TimeoutError struct {
	Msg string
}

// Error returns Msg, or a fixed message when Msg is empty.
func (e TimeoutError) Error() string {
	if e.Msg == "" {
		return "testutil: network timeout"
	}
	return e.Msg
}

// Timeout reports true.
func (TimeoutError) Timeout() bool { return true }

// Temporary reports true, as the net package does for its timeouts.
func (TimeoutError) Temporary() bool { return true }
