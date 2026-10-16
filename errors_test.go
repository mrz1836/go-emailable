package emailable_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-emailable"
	"github.com/mrz1836/go-emailable/internal/testutil"
)

// errErrtJoined is joined with a CallError to prove that errors.Is sees through
// a join.
var errErrtJoined = errors.New("errt joined error")

// errErrtReadFailed is the error a failing error body returns. Its text must
// never surface.
var errErrtReadFailed = errors.New("errt read failed ERRTMARKER")

// errtNote is the text the provider messages of these tests carry. It must
// never appear in an error's text, only behind Diagnostics.
const errtNote = "errt note"

// errtKinds returns every documented failure kind, sorted.
func errtKinds() []emailable.FailureKind {
	return slices.Sorted(maps.Keys(kindSentinels()))
}

// errtSentinels returns the package's sentinels: every target of
// allSentinels but the context errors and the unrelated error.
func errtSentinels() []error {
	others := []error{context.Canceled, context.DeadlineExceeded, errUnrelated}
	return slices.DeleteFunc(allSentinels(), func(e error) bool { return slices.Contains(others, e) })
}

func TestErrorsSentinelMessages(t *testing.T) {
	want := map[error]string{
		emailable.ErrInvalidConfig:      "emailable: invalid configuration",
		emailable.ErrInvalidRequest:     "emailable: invalid request",
		emailable.ErrPending:            "emailable: verification pending",
		emailable.ErrUnauthorized:       "emailable: unauthorized",
		emailable.ErrPaymentRequired:    "emailable: payment required",
		emailable.ErrNotFound:           "emailable: not found",
		emailable.ErrRateLimited:        "emailable: rate limited",
		emailable.ErrRejected:           "emailable: rejected by provider",
		emailable.ErrUpstream:           "emailable: provider server error",
		emailable.ErrUnavailable:        "emailable: temporarily unavailable",
		emailable.ErrUnexpectedResponse: "emailable: unexpected response",
		emailable.ErrMalformedResponse:  "emailable: malformed response",
		emailable.ErrResponseTooLarge:   "emailable: response too large",
		emailable.ErrTransport:          "emailable: transport failure",
		emailable.ErrTimeout:            "emailable: timeout",
		emailable.ErrBudgetExhausted:    "emailable: budget exhausted",
		emailable.ErrOutcomeUnknown:     "emailable: outcome unknown",
	}
	sentinels := errtSentinels()
	require.Len(t, sentinels, len(want))
	seen := map[string]bool{}
	for _, s := range sentinels {
		msg := s.Error()
		assert.Equal(t, want[s], msg)
		assert.False(t, seen[msg], "duplicate message %q", msg)
		seen[msg] = true
		require.NoError(t, errors.Unwrap(s), "a sentinel wraps nothing")
	}
}

func TestErrorsValidationErrorText(t *testing.T) {
	tests := []struct {
		name string
		err  *emailable.ValidationError
		want string
	}{
		{
			name: "request field and reason",
			err:  &emailable.ValidationError{Err: emailable.ErrInvalidRequest, Field: "Email", Reason: reasonControlCharacter},
			want: "emailable: invalid request: Email: control_character",
		},
		{
			name: "config field and reason",
			err:  &emailable.ValidationError{Err: emailable.ErrInvalidConfig, Field: "apiKey", Reason: reasonRequired},
			want: "emailable: invalid configuration: apiKey: required",
		},
		{
			name: "indexed field",
			err:  &emailable.ValidationError{Err: emailable.ErrInvalidRequest, Field: "Emails[17]", Reason: "contains_comma"},
			want: "emailable: invalid request: Emails[17]: contains_comma",
		},
		{
			name: "no field",
			err:  &emailable.ValidationError{Err: emailable.ErrInvalidRequest, Reason: "too_large"},
			want: "emailable: invalid request: too_large",
		},
		{
			name: "no reason",
			err:  &emailable.ValidationError{Err: emailable.ErrInvalidRequest, Field: "Email"},
			want: "emailable: invalid request: Email",
		},
		{
			name: "sentinel only",
			err:  &emailable.ValidationError{Err: emailable.ErrInvalidConfig},
			want: "emailable: invalid configuration",
		},
		{
			name: "no sentinel",
			err:  &emailable.ValidationError{Field: "Timeout", Reason: reasonOutOfRange},
			want: "emailable: validation failed: Timeout: out_of_range",
		},
		{
			name: "zero value",
			err:  &emailable.ValidationError{},
			want: "emailable: validation failed",
		},
		{
			name: "nil receiver",
			err:  nil,
			want: "emailable: <nil ValidationError>",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.err.Error())
		})
	}
}

func TestErrorsValidationErrorUnwrap(t *testing.T) {
	var none *emailable.ValidationError
	require.NoError(t, none.Unwrap(), "a nil ValidationError unwraps to nil")

	ve := &emailable.ValidationError{Err: emailable.ErrInvalidConfig, Field: "Timeout", Reason: reasonOutOfRange}
	assert.Same(t, emailable.ErrInvalidConfig, ve.Unwrap())
	require.ErrorIs(t, ve, emailable.ErrInvalidConfig)
	require.NotErrorIs(t, ve, emailable.ErrInvalidRequest)

	wrapped := fmt.Errorf("setup: %w", ve)
	assert.Same(t, ve, validationError(t, wrapped))
	require.ErrorIs(t, wrapped, emailable.ErrInvalidConfig)

	noSentinel := &emailable.ValidationError{Field: "Email", Reason: reasonRequired}
	require.NoError(t, noSentinel.Unwrap(), "Err nil unwraps to nil")
	for _, target := range allSentinels() {
		require.NotErrorIs(t, noSentinel, target, "without Err it matches nothing")
	}
}

func TestErrorsValidationErrorFromTheSDK(t *testing.T) {
	_, err := emailable.NewClient("")
	require.ErrorIs(t, err, emailable.ErrInvalidConfig)
	require.NotErrorIs(t, err, emailable.ErrInvalidRequest)
	assert.Equal(t, "emailable: invalid configuration: apiKey: required", err.Error())
	assert.NotErrorAs(t, err, new(*emailable.CallError), "a configuration failure is never a CallError")

	c, ct := newOfflineClient(t)
	_, err = c.VerifyEmail(t.Context(), emailable.VerifyEmailRequest{})
	require.ErrorIs(t, err, emailable.ErrInvalidRequest)
	require.NotErrorIs(t, err, emailable.ErrInvalidConfig)
	assert.Equal(t, "emailable: invalid request: Email: required", err.Error())
	assert.NotErrorAs(t, err, new(*emailable.CallError), "a validation failure is never a CallError")
	assert.Zero(t, ct.Attempts())
}

func TestErrorsCallErrorText(t *testing.T) {
	tests := []struct {
		name string
		err  *emailable.CallError
		want string
	}{
		{
			name: "op and kind",
			err:  &emailable.CallError{Op: emailable.OpVerifyEmail, Kind: emailable.FailureTransport},
			want: "emailable: verify_email: transport",
		},
		{
			name: "no op",
			err:  &emailable.CallError{Kind: emailable.FailureTimeout},
			want: "emailable: timeout",
		},
		{
			name: "no kind",
			err:  &emailable.CallError{Op: emailable.OpGetBatch},
			want: "emailable: get_batch: call_failed",
		},
		{
			name: "zero value",
			err:  &emailable.CallError{},
			want: "emailable: call_failed",
		},
		{
			name: "status code",
			err:  &emailable.CallError{Op: emailable.OpCreateBatch, Kind: emailable.FailureRejected, StatusCode: 422},
			want: "emailable: create_batch: rejected (HTTP 422)",
		},
		{
			name: "field without status",
			err: &emailable.CallError{
				Op: emailable.OpAccountInfo, Kind: emailable.FailureMalformedResponse, Field: "owner_email",
			},
			want: "emailable: account_info: malformed_response: owner_email",
		},
		{
			name: "status and field",
			err: &emailable.CallError{
				Op: emailable.OpVerifyEmail, Kind: emailable.FailureMalformedResponse, StatusCode: 200, Field: "score",
			},
			want: "emailable: verify_email: malformed_response (HTTP 200): score",
		},
		{
			name: "metadata never printed",
			err: &emailable.CallError{
				Op: emailable.OpGetBatch, Kind: emailable.FailureMalformedResponse, StatusCode: 200,
				Field: "emails[3].score", BatchID: testBatchID, Attempts: 1, OutcomeUnknown: true,
				RateLimit: emailable.RateLimit{Limit: new(int64(100))},
			},
			want: "emailable: get_batch: malformed_response (HTTP 200): emails[3].score",
		},
		{
			name: "no kind with status and field",
			err:  &emailable.CallError{StatusCode: 503, Field: "x"},
			want: "emailable: call_failed (HTTP 503): x",
		},
		{
			name: "negative status is printed",
			err:  &emailable.CallError{Kind: emailable.FailureUnexpectedResponse, StatusCode: -1},
			want: "emailable: unexpected_response (HTTP -1)",
		},
		{
			name: "callback operation",
			err:  &emailable.CallError{Op: emailable.OpBatchCallback, Kind: emailable.FailureResponseTooLarge},
			want: "emailable: batch_callback: response_too_large",
		},
		{
			name: "nil receiver",
			err:  nil,
			want: "emailable: <nil CallError>",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.err.Error())
		})
	}
}

// TestErrorsFakeMatchesItsKind builds a CallError from its exported fields
// alone, as a caller's fake does, and checks that it matches exactly the
// target of its kind, and ErrOutcomeUnknown only when OutcomeUnknown is set.
func TestErrorsFakeMatchesItsKind(t *testing.T) {
	kinds := errtKinds()
	require.Len(t, kinds, 16, "every documented kind has a target")
	for _, kind := range kinds {
		for _, unknown := range []bool{false, true} {
			t.Run(string(kind)+"/outcome unknown="+strconv.FormatBool(unknown), func(t *testing.T) {
				fake := &emailable.CallError{Kind: kind, Op: emailable.OpVerifyEmail, OutcomeUnknown: unknown}
				assertOnlySentinel(t, fake, kind, unknown)
				assertOnlySentinel(t, fmt.Errorf("wrapped: %w", fake), kind, unknown)
				assertOnlySentinel(t, errors.Join(errErrtJoined, fake), kind, unknown)
				want := wantMatches(kind, unknown)
				for _, target := range allSentinels() {
					assert.Equal(t, slices.Contains(want, target), fake.Is(target), "Is(%v)", target)
				}
				assert.False(t, fake.Is(nil), "nil is never matched")
				require.NoError(t, fake.Unwrap(), "a fake has no cause")
				assert.Equal(t, emailable.ProviderDiagnostics{}, fake.Diagnostics())
			})
		}
	}
}

// TestErrorsEveryKindHasOneTarget checks the documented mapping itself: each
// kind matches exactly one target with errors.Is, a timeout two (ErrTimeout
// and context.DeadlineExceeded), and no two kinds share their own target.
func TestErrorsEveryKindHasOneTarget(t *testing.T) {
	targets := slices.DeleteFunc(allSentinels(), func(e error) bool {
		return errors.Is(e, emailable.ErrOutcomeUnknown)
	})
	own := map[error]emailable.FailureKind{}
	for _, kind := range errtKinds() {
		fake := &emailable.CallError{Kind: kind}
		var matched []error
		for _, target := range targets {
			if errors.Is(fake, target) {
				matched = append(matched, target)
			}
		}
		want := 1
		if kind == emailable.FailureTimeout {
			want = 2
		}
		assert.Len(t, matched, want, "kind %s matches %v", kind, matched)
		target := kindSentinels()[kind]
		require.ErrorIs(t, fake, target)
		if other, dup := own[target]; dup {
			assert.Failf(t, "two kinds share a target", "%s and %s both match %v", other, kind, target)
		}
		own[target] = kind
	}
}

func TestErrorsContextKinds(t *testing.T) {
	timeout := &emailable.CallError{Kind: emailable.FailureTimeout}
	require.ErrorIs(t, timeout, emailable.ErrTimeout)
	require.ErrorIs(t, timeout, context.DeadlineExceeded, "an SDK timeout also matches the context error")
	require.NotErrorIs(t, timeout, context.Canceled)

	deadline := &emailable.CallError{Kind: emailable.FailureDeadlineExceeded}
	require.ErrorIs(t, deadline, context.DeadlineExceeded)
	require.NotErrorIs(t, deadline, emailable.ErrTimeout, "the caller's own deadline is not an SDK timeout")
	require.NotErrorIs(t, deadline, context.Canceled)

	canceled := &emailable.CallError{Kind: emailable.FailureCanceled}
	require.ErrorIs(t, canceled, context.Canceled)
	require.NotErrorIs(t, canceled, context.DeadlineExceeded)
	require.NotErrorIs(t, canceled, emailable.ErrTimeout)
}

func TestErrorsUnknownKindMatchesNothing(t *testing.T) {
	for _, kind := range []emailable.FailureKind{"", "call_failed", "Timeout", "PENDING", "made_up", "timeout "} {
		t.Run(strconv.Quote(string(kind)), func(t *testing.T) {
			ce := &emailable.CallError{Kind: kind, Op: emailable.OpAccountInfo, StatusCode: 500}
			for _, target := range allSentinels() {
				require.NotErrorIs(t, ce, target, "kind %q matched %v", kind, target)
			}
			ce.OutcomeUnknown = true
			for _, target := range allSentinels() {
				assert.Equal(t, errors.Is(target, emailable.ErrOutcomeUnknown), errors.Is(ce, target),
					"only ErrOutcomeUnknown, which is not a kind, matches: %v", target)
			}
		})
	}
}

func TestErrorsNilReceivers(t *testing.T) {
	var none *emailable.CallError
	assert.Equal(t, "emailable: <nil CallError>", none.Error())
	require.NoError(t, none.Unwrap())
	for _, target := range allSentinels() {
		assert.False(t, none.Is(target), "a nil CallError matches nothing: %v", target)
	}
	assert.False(t, none.Is(nil))
	assert.Equal(t, "(*emailable.CallError)(nil)", none.GoString())
	assert.Equal(t, "(*emailable.CallError)(nil)", fmt.Sprintf("%#v", none))
	assert.Equal(t, emailable.ProviderDiagnostics{}, none.Diagnostics())

	var noValidation *emailable.ValidationError
	assert.Equal(t, "emailable: <nil ValidationError>", noValidation.Error())
	require.NoError(t, noValidation.Unwrap())
}

func TestErrorsGoString(t *testing.T) {
	ce := &emailable.CallError{
		Kind: emailable.FailureRateLimited, Op: emailable.OpGetBatch, StatusCode: 429, OutcomeUnknown: true,
		BatchID: testBatchID, Attempts: 1, Field: "emails[2].state",
		RateLimit: emailable.RateLimit{Limit: new(int64(100)), Remaining: new(int64(0)), Reset: new(int64(math.MaxInt64))},
	}
	want := `&emailable.CallError{Kind:"rate_limited", Op:"get_batch", StatusCode:429, OutcomeUnknown:true, ` +
		`BatchID:"` + testBatchID + `", Attempts:1, Field:"emails[2].state", ` +
		`RateLimit:emailable.RateLimit{Limit:100, Remaining:0, Reset:9223372036854775807}}`
	assert.Equal(t, want, ce.GoString())
	assert.Equal(t, want, fmt.Sprintf("%#v", ce), "%#v uses GoString")
	assert.NotContains(t, ce.GoString(), "private", "only exported fields")

	zero := `&emailable.CallError{Kind:"", Op:"", StatusCode:0, OutcomeUnknown:false, BatchID:"", Attempts:0, ` +
		`Field:"", RateLimit:emailable.RateLimit{Limit:nil, Remaining:nil, Reset:nil}}`
	assert.Equal(t, zero, (&emailable.CallError{}).GoString())

	partial := &emailable.CallError{Kind: emailable.FailureUpstream, RateLimit: emailable.RateLimit{Remaining: new(int64(7))}}
	assert.Contains(t, partial.GoString(), "RateLimit:emailable.RateLimit{Limit:nil, Remaining:7, Reset:nil}}")

	sdk := callError(t, errtCanceledVerify(t))
	assert.Equal(t, `&emailable.CallError{Kind:"canceled", Op:"verify_email", StatusCode:0, OutcomeUnknown:false, `+
		`BatchID:"", Attempts:0, Field:"", RateLimit:emailable.RateLimit{Limit:nil, Remaining:nil, Reset:nil}}`,
		fmt.Sprintf("%#v", sdk))
}

// errtCanceledVerify returns the error of a VerifyEmail whose context is
// already canceled.
func errtCanceledVerify(t *testing.T) error {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	c, ct := newOfflineClient(t)
	_, err := c.VerifyEmail(ctx, verifyRequest())
	assert.Zero(t, ct.Attempts())
	return err
}

func TestErrorsFromTheSDKKeepAFixedCause(t *testing.T) {
	err := errtCanceledVerify(t)
	ce := assertCallError(t, err, wantCall{Kind: emailable.FailureCanceled, Op: emailable.OpVerifyEmail})
	assert.Same(t, context.Canceled, ce.Unwrap(), "the cause is the context error itself")
	assert.Equal(t, emailable.ProviderDiagnostics{}, ce.Diagnostics())

	// A copy made from the exported fields alone loses the cause, as a fake
	// has none, but still matches the same targets.
	fake := &emailable.CallError{Kind: ce.Kind, Op: ce.Op, OutcomeUnknown: ce.OutcomeUnknown}
	require.NoError(t, fake.Unwrap())
	assertOnlySentinel(t, fake, ce.Kind, ce.OutcomeUnknown)
}

func TestErrorsValuesAreSafeTokens(t *testing.T) {
	kinds := []emailable.FailureKind{
		emailable.FailurePending, emailable.FailureUnauthorized, emailable.FailurePaymentRequired,
		emailable.FailureNotFound, emailable.FailureRateLimited, emailable.FailureRejected, emailable.FailureUpstream,
		emailable.FailureUnavailable, emailable.FailureUnexpectedResponse, emailable.FailureMalformedResponse,
		emailable.FailureResponseTooLarge, emailable.FailureTransport, emailable.FailureTimeout,
		emailable.FailureCanceled, emailable.FailureDeadlineExceeded, emailable.FailureBudgetExhausted,
	}
	assert.ElementsMatch(t, errtKinds(), kinds, "the documented mapping covers every kind constant")
	pattern := regexp.MustCompile(`^[a-z_]{1,32}$`)
	for _, k := range kinds {
		assert.Regexp(t, pattern, string(k))
	}

	ops := map[emailable.Operation]string{
		emailable.OpVerifyEmail:   "verify_email",
		emailable.OpCreateBatch:   "create_batch",
		emailable.OpGetBatch:      "get_batch",
		emailable.OpAccountInfo:   "account_info",
		emailable.OpBatchCallback: "batch_callback",
	}
	for op, want := range ops {
		assert.Equal(t, want, string(op))
		assert.Regexp(t, pattern, string(op))
	}
}

// errtHeader returns a header holding name and value pairs exactly as given,
// keys not canonicalized, as an injected transport might set them.
func errtHeader(pairs ...string) http.Header {
	h := http.Header{}
	for i := 0; i+1 < len(pairs); i += 2 {
		h[pairs[i]] = append(h[pairs[i]], pairs[i+1])
	}
	return h
}

// errtRateLimited makes one AccountInfo call answered in memory with HTTP 429
// and header, and returns the rate limits of its CallError.
func errtRateLimited(t *testing.T, header http.Header) emailable.RateLimit {
	t.Helper()
	rt := testutil.TransportWith(http.StatusTooManyRequests, header, 0, io.NopCloser(strings.NewReader("")))
	_, err := newTransportClient(t, rt).AccountInfo(t.Context(), emailable.AccountInfoRequest{})
	ce := assertCallError(t, err, wantCall{
		Kind: emailable.FailureRateLimited, Op: emailable.OpAccountInfo, Status: http.StatusTooManyRequests, Attempts: 1,
	})
	return ce.RateLimit
}

func TestErrorsRateLimitHeaderNames(t *testing.T) {
	full := rateLimit(100, 99, 60)
	tests := []struct {
		name   string
		header http.Header
		want   emailable.RateLimit
	}{
		{
			name:   "canonical keys",
			header: errtHeader("Ratelimit-Limit", "100", "Ratelimit-Remaining", "99", "Ratelimit-Reset", "60"),
			want:   full,
		},
		{
			name:   "lowercase keys",
			header: errtHeader("ratelimit-limit", "100", "ratelimit-remaining", "99", "ratelimit-reset", "60"),
			want:   full,
		},
		{
			name:   "uppercase keys",
			header: errtHeader("RATELIMIT-LIMIT", "100", "RATELIMIT-REMAINING", "99", "RATELIMIT-RESET", "60"),
			want:   full,
		},
		{
			name:   "mixed-case keys",
			header: errtHeader("RateLimit-Limit", "100", "rateLimit-Remaining", "99", "RateLimit-RESET", "60"),
			want:   full,
		},
		{name: "absent", header: errtHeader()},
		{
			name:   "only remaining",
			header: errtHeader("Ratelimit-Remaining", "3"),
			want:   emailable.RateLimit{Remaining: new(int64(3))},
		},
		{
			name: "two keys differing in case",
			header: errtHeader("Ratelimit-Limit", "100", "ratelimit-limit", "100",
				"Ratelimit-Remaining", "99", "Ratelimit-Reset", "60"),
			want: emailable.RateLimit{Remaining: new(int64(99)), Reset: new(int64(60))},
		},
		{
			name:   "repeated header",
			header: errtHeader("Ratelimit-Reset", "60", "Ratelimit-Reset", "60", "Ratelimit-Limit", "100"),
			want:   emailable.RateLimit{Limit: new(int64(100))},
		},
		{
			name:   "similar names ignored",
			header: errtHeader("X-Ratelimit-Limit", "100", "Ratelimit-Limit-Extra", "100", "Ratelimit", "limit=100"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, errtRateLimited(t, tt.header))
		})
	}
}

func TestErrorsRateLimitHeaderValues(t *testing.T) {
	tests := []struct {
		name   string
		values []string // the values of ratelimit-limit; nil when absent
		want   *int64
	}{
		{name: "absent"},
		{name: "zero", values: []string{"0"}, want: new(int64(0))},
		{name: "small", values: []string{"250"}, want: new(int64(250))},
		{name: "leading zeros", values: []string{"007"}, want: new(int64(7))},
		{name: "19 digits", values: []string{"9223372036854775807"}, want: new(int64(math.MaxInt64))},
		{name: "19 digits with leading zeros", values: []string{"0000000000000000042"}, want: new(int64(42))},
		{name: "19 digits over int64", values: []string{"9999999999999999999"}},
		{name: "just over int64", values: []string{"9223372036854775808"}},
		{name: "20 digits", values: []string{"00000000000000000001"}},
		{name: "empty", values: []string{""}},
		{name: "no values", values: []string{}},
		{name: "repeated", values: []string{"5", "5"}},
		{name: "non-digit", values: []string{"12a"}},
		{name: "letters", values: []string{"abc"}},
		{name: "fraction", values: []string{"1.5"}},
		{name: "exponent", values: []string{"1e3"}},
		{name: "hex", values: []string{"0x10"}},
		{name: "leading plus", values: []string{"+5"}},
		{name: "leading minus", values: []string{"-5"}},
		{name: "leading space", values: []string{" 5"}},
		{name: "trailing space", values: []string{"5 "}},
		{name: "list", values: []string{"5, 6"}},
		{name: "fullwidth digit", values: []string{"\uff15"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			header := errtHeader("Ratelimit-Remaining", "1", "Ratelimit-Reset", "60")
			if tt.values != nil {
				header["Ratelimit-Limit"] = tt.values
			}
			got := errtRateLimited(t, header)
			assert.Equal(t, tt.want, got.Limit)
			assert.Equal(t, new(int64(1)), got.Remaining, "the other headers are read on their own")
			assert.Equal(t, new(int64(60)), got.Reset)
		})
	}
}

// TestErrorsRateLimitOnEveryErrorResponse proves that every operation keeps
// the rate-limit headers of an error response, whatever its status.
func TestErrorsRateLimitOnEveryErrorResponse(t *testing.T) {
	header := errtHeader("Ratelimit-Limit", "100", "Ratelimit-Remaining", "0", "Ratelimit-Reset", "30")
	for _, o := range opCases() {
		for _, code := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable, http.StatusForbidden} {
			t.Run(o.name+"/"+strconv.Itoa(code), func(t *testing.T) {
				rt := testutil.TransportWith(code, header, 0, io.NopCloser(strings.NewReader("")))
				err := o.call(t.Context(), newTransportClient(t, rt))
				kind := statusKind(o.op, code)
				ce := assertCallError(t, err, o.want(wantCall{
					Kind: kind, Status: code, Attempts: 1, OutcomeUnknown: outcomeUnknownAfter(o.paid, kind),
				}))
				assert.Equal(t, rateLimit(100, 0, 30), ce.RateLimit)
			})
		}
	}
}

func TestErrorsRateLimitOverTLS(t *testing.T) {
	for _, proto := range testutil.Protocols() {
		t.Run(proto.Name(), func(t *testing.T) {
			c, _ := newServerClientProto(t, proto, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("RateLimit-Limit", "120")
				w.Header().Set("ratelimit-remaining", "0")
				w.Header().Set("RATELIMIT-RESET", "15")
				w.WriteHeader(http.StatusTooManyRequests)
			})
			_, err := c.AccountInfo(t.Context(), emailable.AccountInfoRequest{})
			ce := assertCallError(t, err, wantCall{
				Kind: emailable.FailureRateLimited, Op: emailable.OpAccountInfo, Status: http.StatusTooManyRequests,
				Attempts: 1,
			})
			assert.Equal(t, rateLimit(120, 0, 15), ce.RateLimit)
		})
	}
}

// errtDiagnosed makes one call of o answered in memory with code and body,
// which is declared with length cl, and returns the body and the CallError,
// which must carry the kind of code.
func errtDiagnosed(t *testing.T, o opCase, code int, body *testutil.Body, cl int64,
	opts ...emailable.Option,
) (*emailable.CallError, *testutil.Body) {
	t.Helper()
	c := newTransportClient(t, testutil.TransportWith(code, nil, cl, body), opts...)
	err := o.call(t.Context(), c)
	kind := statusKind(o.op, code)
	ce := assertCallError(t, err, o.want(wantCall{
		Kind: kind, Status: code, Attempts: 1, OutcomeUnknown: outcomeUnknownAfter(o.paid, kind),
	}), errtNote, "ERRTMARKER")
	assert.Equal(t, 1, body.Closes(), "the body is closed exactly once")
	return ce, body
}

func TestErrorsDiagnosticsOffByDefault(t *testing.T) {
	content := messageBody(errtNote + " " + testKey)
	for _, o := range operations() {
		for _, code := range []int{249, 302, 400, 401, 402, 403, 404, 429, 500, 503} {
			for _, cl := range []int64{-1, int64(len(content))} {
				name := o.name + "/" + strconv.Itoa(code) + "/length " + strconv.FormatInt(cl, 10)
				t.Run(name, func(t *testing.T) {
					ce, body := errtDiagnosed(t, o, code, testutil.NewBody(content), cl)
					assert.Empty(t, ce.Diagnostics().Message)
					assert.Zero(t, body.Reads(), "without diagnostics an error body is never read")
				})
			}
		}
	}
}

func TestErrorsDiagnosticsMessage(t *testing.T) {
	escapedKey := testutil.Esc(`~u0074`) + testKey[1:]
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "plain", body: messageBody(errtNote), want: errtNote},
		{name: "echoes the address", body: messageBody(errtNote + " for " + testEmail), want: errtNote + " for " + testEmail},
		{
			name: "key redacted", body: messageBody(errtNote + " " + testKey + " and " + testKey),
			want: errtNote + " [redacted] and [redacted]",
		},
		{
			name: "escaped key redacted after decoding", body: `{"message":"` + errtNote + ` ` + escapedKey + `"}`,
			want: errtNote + " [redacted]",
		},
		{
			name: "escapes decoded", body: testutil.Esc(`{"message":"errt ~u006eote ~"q~" ~u00e9 ~ud83d~ude00"}`),
			want: "errt note \"q\" é \U0001F600",
		},
		{name: "other members ignored", body: `{"error":"x","message":"` + errtNote + `","code":7}`, want: errtNote},
		{name: "nested message ignored", body: `{"error":{"message":"x"},"message":"` + errtNote + `"}`, want: errtNote},
		{name: "empty message", body: `{"message":""}`},
		{name: "absent", body: `{"error":"` + errtNote + `"}`},
		{name: "null", body: `{"message":null}`},
		{name: "number", body: `{"message":42}`},
		{name: "boolean", body: `{"message":true}`},
		{name: "array", body: `{"message":["` + errtNote + `"]}`},
		{name: "object", body: `{"message":{"text":"` + errtNote + `"}}`},
		{name: "case variant only", body: `{"Message":"` + errtNote + `"}`},
		{name: "duplicate", body: `{"message":"` + errtNote + `","message":"` + errtNote + `"}`},
		{name: "case-folded duplicate", body: `{"message":"` + errtNote + `","MESSAGE":"other"}`},
		{name: "escaped duplicate", body: testutil.Esc(`{"message":"errt note","~u006dessage":"other"}`)},
		{name: "not JSON", body: errtNote},
		{name: "HTML", body: `<html><body>` + errtNote + `</body></html>`},
		{name: "top-level array", body: `[{"message":"` + errtNote + `"}]`},
		{name: "top-level string", body: `"` + errtNote + `"`},
		{name: "trailing data", body: messageBody(errtNote) + ` x`},
		{name: "invalid UTF-8", body: "{\"message\":\"errt note \xff\"}"},
		{name: "empty body", body: ``},
	}
	verify := opCaseNamed("VerifyEmail")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ce, body := errtDiagnosed(t, verify, http.StatusBadRequest, testutil.NewBody(tt.body), int64(len(tt.body)),
				emailable.WithProviderDiagnostics())
			// The body never changes the kind and never fails the call.
			assert.Equal(t, tt.want, ce.Diagnostics().Message)
			assert.Equal(t, int64(len(tt.body)), body.BytesRead())
		})
	}
}

func TestErrorsDiagnosticsOnEveryOperation(t *testing.T) {
	content := messageBody(errtNote + " " + testKey)
	for _, o := range opCases() {
		for _, code := range []int{http.StatusPaymentRequired, http.StatusServiceUnavailable, http.StatusFound} {
			t.Run(o.name+"/"+strconv.Itoa(code), func(t *testing.T) {
				ce, _ := errtDiagnosed(t, o, code, testutil.NewBody(content), -1, emailable.WithProviderDiagnostics())
				assert.Equal(t, errtNote+" [redacted]", ce.Diagnostics().Message)
			})
		}
	}
}

func TestErrorsDiagnosticsCallbackURLRedacted(t *testing.T) {
	content := messageBody("bad url " + testHook + " key " + testKey)
	c := newTransportClient(t, testutil.StaticTransport(http.StatusBadRequest, content), emailable.WithProviderDiagnostics())
	_, err := c.CreateBatch(t.Context(), emailable.CreateBatchRequest{Emails: []string{testEmail}, CallbackURL: testHook})
	ce := assertCallError(t, err, wantCall{
		Kind: emailable.FailureRejected, Op: emailable.OpCreateBatch, Status: http.StatusBadRequest, Attempts: 1,
	}, "bad url")
	assert.Equal(t, "bad url [redacted] key [redacted]", ce.Diagnostics().Message)
}

// TestErrorsDiagnosticsCallbackTokenRedacted proves that a callback URL's
// query values of 8 bytes or more count as the call's secrets on their own,
// as a provider echoing only the token might print them; shorter ones would
// match ordinary text, so they do not.
func TestErrorsDiagnosticsCallbackTokenRedacted(t *testing.T) {
	const hook = "https://hooks.example.com/emailable?token=hookmarker93c1d7&v=2"
	content := messageBody("token hookmarker93c1d7 v 2")
	c := newTransportClient(t, testutil.StaticTransport(http.StatusBadRequest, content), emailable.WithProviderDiagnostics())
	_, err := c.CreateBatch(t.Context(), emailable.CreateBatchRequest{Emails: []string{testEmail}, CallbackURL: hook})
	ce := assertCallError(t, err, wantCall{
		Kind: emailable.FailureRejected, Op: emailable.OpCreateBatch, Status: http.StatusBadRequest, Attempts: 1,
	})
	assert.Equal(t, "token [redacted] v 2", ce.Diagnostics().Message)
}

// TestErrorsDiagnosticsRedactQueryEscapedSecrets proves that the API key and
// the call's secrets are scrubbed in the form they take in a query string
// too, as a provider echoing its request might print them.
func TestErrorsDiagnosticsRedactQueryEscapedSecrets(t *testing.T) {
	const key = "test_errt+key/marker=9" // gitleaks:allow (synthetic test marker)
	escapedKey, escapedHook := url.QueryEscape(key), url.QueryEscape(testHook)
	require.NotEqual(t, key, escapedKey)
	require.NotEqual(t, testHook, escapedHook)
	message := messageBody("key " + key + " " + escapedKey + " url " + testHook + " " + escapedHook)
	s := testutil.NewScript(testutil.Step{Status: http.StatusBadRequest, Body: message})
	c, err := emailable.NewClient(key, emailable.WithTestBaseURL(loopbackOrigin),
		emailable.WithHTTPClient(&http.Client{Transport: s}), emailable.WithProviderDiagnostics())
	require.NoError(t, err)
	_, err = c.CreateBatch(t.Context(), emailable.CreateBatchRequest{Emails: []string{testEmail}, CallbackURL: testHook})
	ce := assertCallError(t, err, wantCall{
		Kind: emailable.FailureRejected, Op: emailable.OpCreateBatch, Status: http.StatusBadRequest, Attempts: 1,
	}, key, escapedKey, escapedHook)
	assert.Equal(t, "key [redacted] [redacted] url [redacted] [redacted]", ce.Diagnostics().Message)
}

func TestErrorsDiagnosticsCutTo1KiB(t *testing.T) {
	const limit = 1 << 10
	tests := []struct {
		name    string
		message string
		want    string
	}{
		{name: "short", message: "abc", want: "abc"},
		{name: "exactly 1 KiB", message: strings.Repeat("a", limit), want: strings.Repeat("a", limit)},
		{name: "1 KiB plus one", message: strings.Repeat("a", limit+1), want: strings.Repeat("a", limit)},
		{name: "far over", message: strings.Repeat("a", 10*limit), want: strings.Repeat("a", limit)},
		{
			name: "two-byte rune across the cut", message: strings.Repeat("a", limit-1) + "éb",
			want: strings.Repeat("a", limit-1),
		},
		{
			name: "two-byte rune ending at the cut", message: strings.Repeat("a", limit-2) + "éb",
			want: strings.Repeat("a", limit-2) + "é",
		},
		{
			name: "three-byte rune across the cut", message: strings.Repeat("a", limit-2) + "€b",
			want: strings.Repeat("a", limit-2),
		},
		{
			name: "four-byte rune across the cut", message: strings.Repeat("a", limit-1) + "\U0001F600",
			want: strings.Repeat("a", limit-1),
		},
		{
			name: "only multi-byte runes", message: strings.Repeat("€", 400),
			want: strings.Repeat("€", limit/3),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ce, _ := errtDiagnosed(t, opCaseNamed("AccountInfo"), http.StatusInternalServerError,
				testutil.NewBody(messageBody(tt.message)), -1, emailable.WithProviderDiagnostics())
			got := ce.Diagnostics().Message
			assert.Equal(t, tt.want, got)
			assert.LessOrEqual(t, len(got), limit)
		})
	}
}

// TestErrorsDiagnosticsRedactBeforeCut proves that a key straddling the 1 KiB
// cut is redacted before the message is cut, so no prefix of it survives.
func TestErrorsDiagnosticsRedactBeforeCut(t *testing.T) {
	const limit = 1 << 10
	for _, lead := range []int{limit - 12, limit - 4, limit - 1} {
		t.Run(strconv.Itoa(lead), func(t *testing.T) {
			message := strings.Repeat("a", lead) + testKey + " tail"
			ce, _ := errtDiagnosed(t, opCaseNamed("AccountInfo"), http.StatusInternalServerError,
				testutil.NewBody(messageBody(message)), -1, emailable.WithProviderDiagnostics())
			got := ce.Diagnostics().Message
			assert.Len(t, got, limit)
			want := (strings.Repeat("a", lead) + "[redacted] tail")[:limit]
			assert.Equal(t, want, got)
			assert.NotContains(t, got, testKey[:min(len(testKey), limit-lead)], "no part of the key survives")
		})
	}
}

func TestErrorsDiagnosticsBodyBound(t *testing.T) {
	const limit = 64 << 10
	content := messageBody(errtNote)
	tests := []struct {
		name string
		size int
		cl   int64 // the declared length, -1 for unknown
		want string
		read int64 // bytes read from the body
	}{
		{name: "exactly 64 KiB declared", size: limit, cl: limit, want: errtNote, read: limit},
		{name: "exactly 64 KiB unknown length", size: limit, cl: -1, want: errtNote, read: limit},
		{name: "64 KiB plus one declared", size: limit + 1, cl: limit + 1, read: 0},
		{name: "64 KiB plus one unknown length", size: limit + 1, cl: -1, read: limit + 1},
		{name: "1 MiB unknown length", size: 1 << 20, cl: -1, read: limit + 1},
		{name: "declared over, sent within", size: 100, cl: limit + 1, read: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ce, body := errtDiagnosed(t, opCaseNamed("VerifyEmail"), http.StatusServiceUnavailable,
				testutil.NewBody(padded(t, content, tt.size)), tt.cl, emailable.WithProviderDiagnostics())
			// An oversized excerpt never changes the kind.
			assert.Equal(t, tt.want, ce.Diagnostics().Message)
			assert.Equal(t, tt.read, body.BytesRead())
		})
	}
}

func TestErrorsDiagnosticsUnreadableBody(t *testing.T) {
	content := messageBody(errtNote)
	tests := []struct {
		name   string
		header http.Header
		body   func() *testutil.Body
		read   bool // whether any byte is read
	}{
		{
			name:   "gzip encoded",
			header: http.Header{"Content-Type": {mediaJSON}, "Content-Encoding": {"gzip"}},
			body:   func() *testutil.Body { return testutil.NewBody(content) },
		},
		{
			name:   "br encoded",
			header: http.Header{"Content-Type": {mediaJSON}, "Content-Encoding": {"br"}},
			body:   func() *testutil.Body { return testutil.NewBody(content) },
		},
		{
			name:   "encoded under a lowercase header name",
			header: errtHeader("Content-Type", mediaJSON, "content-encoding", "gzip"),
			body:   func() *testutil.Body { return testutil.NewBody(content) },
		},
		{
			name: "read failure",
			body: func() *testutil.Body {
				b := testutil.NewBody(content)
				b.End = errErrtReadFailed
				return b
			},
			read: true,
		},
		{
			name: "stalled body",
			body: func() *testutil.Body {
				b := testutil.NewBody(content)
				b.Stall = true
				return b
			},
			read: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := tt.body()
			c := newTransportClient(t, testutil.TransportWith(http.StatusUnauthorized, tt.header, -1, body),
				emailable.WithProviderDiagnostics())
			_, err := c.AccountInfo(t.Context(), emailable.AccountInfoRequest{})
			ce := assertCallError(t, err, wantCall{
				Kind: emailable.FailureUnauthorized, Op: emailable.OpAccountInfo, Status: http.StatusUnauthorized,
				Attempts: 1,
			}, errtNote, "ERRTMARKER")
			// An unreadable error body yields no message and never fails the call.
			assert.Empty(t, ce.Diagnostics().Message)
			assert.Equal(t, tt.read, body.BytesRead() > 0)
			assert.Equal(t, 1, body.Closes())
		})
	}
}

func TestErrorsDiagnosticsNeverPrinted(t *testing.T) {
	marker := "errtdiagmarker"
	c := newTransportClient(t, testutil.StaticTransport(http.StatusBadRequest, messageBody(marker+" "+testEmail)),
		emailable.WithProviderDiagnostics())
	_, err := c.VerifyEmail(t.Context(), verifyRequest())
	ce := assertCallError(t, err, wantCall{
		Kind: emailable.FailureRejected, Op: emailable.OpVerifyEmail, Status: http.StatusBadRequest, Attempts: 1,
	}, marker)
	assert.Equal(t, marker+" "+testEmail, ce.Diagnostics().Message, "the message is reachable only here")
	testutil.AssertShapesNeverPrint(t, ce, marker, testEmail)
}
