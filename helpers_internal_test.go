package emailable

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-emailable/internal/testutil"
)

// Literals the internal tests repeat.
const (
	jsonNull       = "null"
	internalKey    = "test_internalmarker42" // gitleaks:allow (synthetic test marker)
	internalOrigin = "https://127.0.0.1"
	internalEmail  = "jane.doe@example.com"
)

// newInternalClient returns a client built with internalKey and opts, failing
// tb when NewClient fails.
func newInternalClient(tb testing.TB, opts ...Option) *Client {
	tb.Helper()
	c, err := NewClient(internalKey, opts...)
	require.NoError(tb, err)
	return c
}

// newInternalTransportClient returns a client whose calls go through rt, in
// memory, with opts applied after the transport options.
func newInternalTransportClient(tb testing.TB, rt http.RoundTripper, opts ...Option) *Client {
	tb.Helper()
	all := append([]Option{WithTestBaseURL(internalOrigin), WithHTTPClient(&http.Client{Transport: rt})}, opts...)
	return newInternalClient(tb, all...)
}

// asCallError returns the *CallError in err's chain, failing tb when there is
// none.
func asCallError(tb testing.TB, err error) *CallError {
	tb.Helper()
	return testutil.RequireErrorAs[*CallError](tb, err)
}

// requireValidation asserts that err is the *ValidationError for field and
// reason that matches sentinel, ErrInvalidConfig or ErrInvalidRequest, with
// the fixed error text, which never quotes a value.
func requireValidation(tb testing.TB, err, sentinel error, field, reason string) {
	tb.Helper()
	ve := testutil.RequireErrorAs[*ValidationError](tb, err)
	require.ErrorIs(tb, err, sentinel)
	require.Equal(tb, field, ve.Field)
	require.Equal(tb, reason, ve.Reason)
	require.Equal(tb, sentinel.Error()+": "+field+": "+reason, err.Error())
}

// requireCause asserts that derr is a decoding failure whose cause matches
// cause.
func requireCause(tb testing.TB, derr *decodeError, cause error, msgAndArgs ...any) {
	tb.Helper()
	require.NotNil(tb, derr, msgAndArgs...)
	require.ErrorIs(tb, derr.cause, cause, msgAndArgs...)
}

// assertDecodeError asserts that derr is a decoding failure of kind at field.
func assertDecodeError(tb testing.TB, derr *decodeError, kind FailureKind, field string) {
	tb.Helper()
	require.NotNil(tb, derr, "want a decoding failure at %s", field)
	assert.Equal(tb, kind, derr.kind, "kind at %s", field)
	assert.Equal(tb, field, derr.field)
}
