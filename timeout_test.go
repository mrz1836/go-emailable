package emailable_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-emailable"
	"github.com/mrz1836/go-emailable/internal/testutil"
)

// timeoutMarker is planted in transport and body errors: the SDK drops the
// original error, so it must never surface.
const timeoutMarker = "timeout-error-marker-61d0a7" // gitleaks:allow (synthetic test marker)

// errTimeoutMarker is a failure that is not a timeout, quoting the marker.
var errTimeoutMarker = errors.New("synthetic failure " + timeoutMarker)

// timeoutStallBody serves its Body's content and then stalls, as a dead
// connection does, until the request's context ends: wait blocks until then
// and returns the context's error. The Body counts reads and closes. Its own
// Block cannot stand in, because only Close ends it, and an http.Client
// Timeout ends the request's context without closing the body.
type timeoutStallBody struct {
	*testutil.Body

	wait func() error
}

// Read serves the content, then stalls.
func (b timeoutStallBody) Read(p []byte) (int, error) {
	n, err := b.Body.Read(p)
	if errors.Is(err, io.EOF) {
		return n, b.wait()
	}
	return n, err
}

// timeoutStalling returns a transport that answers with status and a body
// serving content that then stalls until the request's context ends. A
// status of 0 stalls before any response instead. The body must be made
// inside the bubble.
func timeoutStalling(status int, body *testutil.Body) testutil.RoundTripFunc {
	if status == 0 {
		return testutil.BlockUntilDone
	}
	return func(r *http.Request) (*http.Response, error) {
		ctx := r.Context()
		stall := timeoutStallBody{Body: body, wait: func() error {
			<-ctx.Done()
			return ctx.Err()
		}}
		return testutil.TransportWith(status, nil, -1, stall)(r)
	}
}

// timeoutTimed runs call and returns the time it took, which inside a bubble
// is the fake clock's exact elapsed time.
func timeoutTimed(call func() error) (time.Duration, error) {
	start := time.Now()
	err := call()
	return time.Since(start), err
}

// timeoutWhere names where a call stalls: before the response headers
// (status 0) or after them, in the body (status 200).
func timeoutWhere(status int) string {
	if status == 0 {
		return "awaiting headers"
	}
	return "mid-body"
}

// timeoutAssertTimedOut asserts that err is the SDK's own timeout for o:
// kind timeout, matching both ErrTimeout and context.DeadlineExceeded, after
// one attempt, its outcome unknown exactly for a paid operation.
func timeoutAssertTimedOut(t *testing.T, err error, o opCase, status int) {
	t.Helper()
	_ = assertCallError(t, err, o.want(wantCall{
		Kind: emailable.FailureTimeout, Status: status, Attempts: 1, OutcomeUnknown: o.paid,
	}), timeoutMarker)
	require.ErrorIs(t, err, emailable.ErrTimeout)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Equal(t, context.DeadlineExceeded, errors.Unwrap(callError(t, err)), "the cause is the context error alone")
}

// timeoutBoundsCase is one deadline set for TestTimeoutBoundsTheWholeCall.
type timeoutBoundsCase struct {
	name          string
	status        int           // 0 stalls before the response headers
	sdkTimeout    time.Duration // 0 keeps DefaultTimeout
	clientTimeout time.Duration // an injected http.Client's Timeout
	callerTimeout time.Duration // the caller's context deadline
	diagnostics   bool
	wantKind      emailable.FailureKind
	wantElapsed   time.Duration
}

// client returns a client with the case's options whose calls go through rt.
func (tc timeoutBoundsCase) client(t *testing.T, rt http.RoundTripper) *emailable.Client {
	t.Helper()
	opts := []emailable.Option{
		emailable.WithTestBaseURL(loopbackOrigin),
		emailable.WithHTTPClient(&http.Client{Transport: rt, Timeout: tc.clientTimeout}),
	}
	if tc.sdkTimeout > 0 {
		opts = append(opts, emailable.WithTimeout(tc.sdkTimeout))
	}
	if tc.diagnostics {
		opts = append(opts, emailable.WithProviderDiagnostics())
	}
	return newClient(t, opts...)
}

// context returns the caller's context for the case.
func (tc timeoutBoundsCase) context(parent context.Context) (context.Context, context.CancelFunc) {
	if tc.callerTimeout > 0 {
		return context.WithTimeout(parent, tc.callerTimeout)
	}
	return context.WithCancel(parent)
}

// assertStalledBody asserts how the call treated the stalling body.
func (tc timeoutBoundsCase) assertStalledBody(t *testing.T, body *testutil.Body) {
	t.Helper()
	if tc.status == 0 {
		return
	}
	assert.Equal(t, 1, body.Closes(), "the body is closed exactly once")
	if tc.status != http.StatusOK && !tc.diagnostics {
		assert.Zero(t, body.Reads(), "an error body is never read without diagnostics")
	}
}

func timeoutBoundsCases() []timeoutBoundsCase {
	return []timeoutBoundsCase{
		{name: "default timeout awaiting headers", wantKind: emailable.FailureTimeout, wantElapsed: emailable.DefaultTimeout},
		{
			name: "custom timeout awaiting headers", sdkTimeout: 7 * time.Second,
			wantKind: emailable.FailureTimeout, wantElapsed: 7 * time.Second,
		},
		{
			name: "default timeout mid-body", status: http.StatusOK,
			wantKind: emailable.FailureTimeout, wantElapsed: emailable.DefaultTimeout,
		},
		{
			name: "custom timeout mid-body", status: http.StatusOK, sdkTimeout: 11 * time.Second,
			wantKind: emailable.FailureTimeout, wantElapsed: 11 * time.Second,
		},
		{
			name: "maximum timeout", sdkTimeout: 10 * time.Minute,
			wantKind: emailable.FailureTimeout, wantElapsed: 10 * time.Minute,
		},
		{
			name: "caller deadline awaiting headers", callerTimeout: 5 * time.Second,
			wantKind: emailable.FailureDeadlineExceeded, wantElapsed: 5 * time.Second,
		},
		{
			name: "caller deadline mid-body", status: http.StatusOK, callerTimeout: 5 * time.Second,
			wantKind: emailable.FailureDeadlineExceeded, wantElapsed: 5 * time.Second,
		},
		{
			name: "SDK timeout shorter than the caller deadline", sdkTimeout: 4 * time.Second, callerTimeout: 6 * time.Second,
			wantKind: emailable.FailureTimeout, wantElapsed: 4 * time.Second,
		},
		{
			name: "caller deadline shorter than the SDK timeout", sdkTimeout: 9 * time.Second, callerTimeout: 3 * time.Second,
			wantKind: emailable.FailureDeadlineExceeded, wantElapsed: 3 * time.Second,
		},
		{
			name: "injected client timeout awaiting headers", clientTimeout: 3 * time.Second,
			wantKind: emailable.FailureTimeout, wantElapsed: 3 * time.Second,
		},
		{
			name: "injected client timeout mid-body", status: http.StatusOK, clientTimeout: 3 * time.Second,
			wantKind: emailable.FailureTimeout, wantElapsed: 3 * time.Second,
		},
		{
			name: "SDK timeout shorter than the injected client's", sdkTimeout: 5 * time.Second, clientTimeout: 20 * time.Second,
			wantKind: emailable.FailureTimeout, wantElapsed: 5 * time.Second,
		},
		{
			name: "non-200 excerpt stalls until the timeout", status: http.StatusServiceUnavailable,
			sdkTimeout: 9 * time.Second, diagnostics: true,
			wantKind: emailable.FailureUnavailable, wantElapsed: 9 * time.Second,
		},
		{
			name: "non-200 excerpt stalls until the caller deadline", status: http.StatusTooManyRequests,
			callerTimeout: 2 * time.Second, diagnostics: true,
			wantKind: emailable.FailureRateLimited, wantElapsed: 2 * time.Second,
		},
		{
			name: "non-200 body never read without diagnostics", status: http.StatusServiceUnavailable,
			wantKind: emailable.FailureUnavailable,
		},
	}
}

// TestTimeoutBoundsTheWholeCall checks, for VerifyEmail, which deadline ends
// a call that stalls before its headers or in its body, and exactly when: the
// SDK's timeout (WithTimeout), the caller's deadline, or an injected
// http.Client's Timeout, whichever comes first. An error status ends the call
// at once, unless diagnostics read its body, which the same deadlines bound.
func TestTimeoutBoundsTheWholeCall(t *testing.T) {
	for _, tc := range timeoutBoundsCases() {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				body := testutil.NewBody(`{"message"`)
				ct := &testutil.CountingTransport{Next: timeoutStalling(tc.status, body)}
				c := tc.client(t, ct)
				ctx, cancel := tc.context(t.Context())
				defer cancel()
				elapsed, err := timeoutTimed(func() error { return noValueWith(c.VerifyEmail(ctx, verifyRequest())) })
				// Only FailureTimeout matches ErrTimeout; it and
				// FailureDeadlineExceeded match context.DeadlineExceeded.
				_ = assertCallError(t, err, wantCall{
					Kind: tc.wantKind, Op: emailable.OpVerifyEmail, Status: tc.status, Attempts: 1,
					OutcomeUnknown: outcomeUnknownAfter(true, tc.wantKind),
				})
				assert.Equal(t, tc.wantElapsed, elapsed, "the call ends exactly at the first deadline")
				assert.Equal(t, 1, ct.Attempts(), "exactly one attempt")
				tc.assertStalledBody(t, body)
			})
		})
	}
}

// TestTimeoutAppliesToEveryOperation lets every method's attempt stall before
// its headers and in its body: WithTimeout ends it exactly on time, as a
// timeout, with the body closed once.
func TestTimeoutAppliesToEveryOperation(t *testing.T) {
	for _, o := range opCases() {
		for _, status := range []int{0, http.StatusOK} {
			t.Run(o.name+"/"+timeoutWhere(status), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					body := testutil.NewBody(`{"`)
					c := newTransportClient(t, timeoutStalling(status, body), emailable.WithTimeout(7*time.Second))
					elapsed, err := timeoutTimed(func() error { return o.call(t.Context(), c) })
					timeoutAssertTimedOut(t, err, o, status)
					assert.Equal(t, 7*time.Second, elapsed)
					if status != 0 {
						assert.Equal(t, 1, body.Closes(), "the body is closed exactly once")
					}
				})
			})
		}
	}
}

// TestTimeoutBoundsATricklingBody delivers each method's successful answer a
// few bytes at a time, one second apart. WithTimeout bounds the whole body
// read, not only the wait for headers: a body that ends in time succeeds,
// one that would end later times out exactly at WithTimeout.
func TestTimeoutBoundsATricklingBody(t *testing.T) {
	const sdkTimeout, gap = 7 * time.Second, time.Second
	for _, o := range opCases() {
		for _, inTime := range []bool{true, false} {
			name := o.name + "/past the timeout"
			if inTime {
				name = o.name + "/in time"
			}
			t.Run(name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					content := o.ok.Body
					step := 4 // bytes per read: far more reads than seconds in the timeout
					if inTime {
						step = (len(content) + 3) / 4 // four reads
					}
					parts := (len(content) + step - 1) / step
					body := testutil.NewBody(content)
					body.Step, body.Gap = step, gap
					rt := testutil.TransportWith(http.StatusOK, nil, int64(len(content)), body)
					c := newTransportClient(t, rt, emailable.WithTimeout(sdkTimeout))

					elapsed, err := timeoutTimed(func() error { return o.call(t.Context(), c) })

					assert.Equal(t, 1, body.Closes(), "the body is closed exactly once")
					if inTime {
						require.NoError(t, err)
						assert.Equal(t, time.Duration(parts)*gap, elapsed, "one gap per part")
						assert.Equal(t, int64(len(content)), body.BytesRead())
						return
					}
					require.Greater(t, time.Duration(parts)*gap, sdkTimeout, "the body would end after the timeout")
					timeoutAssertTimedOut(t, err, o, http.StatusOK)
					assert.Equal(t, sdkTimeout, elapsed, "the timeout cuts the body read")
					assert.Less(t, body.BytesRead(), int64(len(content)))
				})
			})
		}
	}
}

// TestTimeoutClosesABodyThatIgnoresContext uses bodies that never watch the
// request's context: one that blocks after its content until it is closed,
// and one whose next part is an hour away. The SDK closes the body when its
// timeout fires, which ends the read.
func TestTimeoutClosesABodyThatIgnoresContext(t *testing.T) {
	for _, o := range opCases() {
		for _, blocked := range []bool{true, false} {
			name := o.name + "/long gap"
			if blocked {
				name = o.name + "/blocks until closed"
			}
			t.Run(name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					var body *testutil.Body
					if blocked {
						body = testutil.NewBlockingBody(o.ok.Body[:len(o.ok.Body)/2])
					} else {
						body = testutil.NewBody(o.ok.Body)
						body.Step, body.Gap = 8, time.Hour
					}
					rt := testutil.TransportWith(http.StatusOK, nil, -1, body)
					c := newTransportClient(t, rt, emailable.WithTimeout(6*time.Second))
					elapsed, err := timeoutTimed(func() error { return o.call(t.Context(), c) })
					timeoutAssertTimedOut(t, err, o, http.StatusOK)
					assert.Equal(t, 6*time.Second, elapsed)
					assert.Equal(t, 1, body.Closes(), "the timeout closed the body, exactly once")
				})
			})
		}
	}
}

// TestTimeoutCallerDeadlineIsNotErrTimeout gives every method a caller
// deadline shorter and longer than WithTimeout, while the attempt stalls
// before its headers and in its body: the shorter one is the caller's
// deadline_exceeded, never ErrTimeout; the longer one leaves the SDK's
// timeout to fire first. Both match context.DeadlineExceeded.
func TestTimeoutCallerDeadlineIsNotErrTimeout(t *testing.T) {
	const sdkTimeout = 6 * time.Second
	for _, o := range opCases() {
		for _, status := range []int{0, http.StatusOK} {
			for _, caller := range []time.Duration{2 * time.Second, 9 * time.Second} {
				t.Run(fmt.Sprintf("%s/%s/caller %s", o.name, timeoutWhere(status), caller), func(t *testing.T) {
					synctest.Test(t, func(t *testing.T) {
						body := testutil.NewBody(`{`)
						c := newTransportClient(t, timeoutStalling(status, body), emailable.WithTimeout(sdkTimeout))
						ctx, cancel := context.WithTimeout(t.Context(), caller)
						defer cancel()
						elapsed, err := timeoutTimed(func() error { return o.call(ctx, c) })
						require.ErrorIs(t, err, context.DeadlineExceeded)
						if caller < sdkTimeout {
							_ = assertCallError(t, err, o.want(wantCall{
								Kind: emailable.FailureDeadlineExceeded, Status: status, Attempts: 1, OutcomeUnknown: o.paid,
							}))
							require.NotErrorIs(t, err, emailable.ErrTimeout, "the caller's deadline is not the SDK's timeout")
							assert.Equal(t, caller, elapsed)
							return
						}
						timeoutAssertTimedOut(t, err, o, status)
						assert.Equal(t, sdkTimeout, elapsed)
					})
				})
			}
		}
	}
}

// TestTimeoutInjectedClientTimeout gives every method an injected
// http.Client whose Timeout is shorter than WithTimeout: it ends the attempt,
// before its headers and in its body, as the SDK's own timeout would.
func TestTimeoutInjectedClientTimeout(t *testing.T) {
	for _, o := range opCases() {
		for _, status := range []int{0, http.StatusOK} {
			t.Run(o.name+"/"+timeoutWhere(status), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					body := testutil.NewBody(`{`)
					hc := &http.Client{Transport: timeoutStalling(status, body), Timeout: 3 * time.Second}
					c := newClient(t, emailable.WithTestBaseURL(loopbackOrigin), emailable.WithHTTPClient(hc))
					elapsed, err := timeoutTimed(func() error { return o.call(t.Context(), c) })
					timeoutAssertTimedOut(t, err, o, status)
					assert.Equal(t, 3*time.Second, elapsed)
					assert.Equal(t, 3*time.Second, hc.Timeout, "the caller's client is not modified")
				})
			})
		}
	}
}

// timeoutNetErrors are errors a transport or a body may fail with, and
// whether each reports a network timeout.
func timeoutNetErrors() []struct {
	name    string
	err     error
	timeout bool
} {
	marked := testutil.TimeoutError{Msg: "i/o timeout " + timeoutMarker}
	return []struct {
		name    string
		err     error
		timeout bool
	}{
		{name: "net.Error timeout", err: marked, timeout: true},
		{name: "net.OpError timeout", err: &net.OpError{Op: "read", Net: "tcp", Err: marked}, timeout: true},
		{name: "os.ErrDeadlineExceeded", err: os.ErrDeadlineExceeded, timeout: true},
		{name: "wrapped net.Error timeout", err: fmt.Errorf("transport: %w", marked), timeout: true},
		{
			name: "deeply wrapped net.Error timeout", timeout: true,
			err: fmt.Errorf("proxy: %w", &net.OpError{Op: "dial", Net: "tcp", Err: fmt.Errorf("conn: %w", marked)}),
		},
		{name: "joined with a net.Error timeout", err: errors.Join(errTimeoutMarker, marked), timeout: true},
		{name: "wrapped os.ErrDeadlineExceeded", err: fmt.Errorf("read: %w", os.ErrDeadlineExceeded), timeout: true},
		{name: "net.OpError without timeout", err: &net.OpError{Op: "read", Net: "tcp", Err: errTimeoutMarker}},
		{name: "plain error", err: errTimeoutMarker},
	}
}

// timeoutFailWith returns a transport that fails with err: from the round
// trip itself when status is 0, else from the body of a status answer.
func timeoutFailWith(err error, status int) testutil.RoundTripFunc {
	return func(r *http.Request) (*http.Response, error) {
		testutil.Drain(r)
		if status == 0 {
			return nil, err
		}
		body := testutil.NewBody(`{"`)
		body.End = err
		return testutil.TransportWith(status, nil, -1, body)(r)
	}
}

// TestTimeoutNetworkTimeoutError fails every method's attempt, from the
// transport and from the body, with errors that do and do not report a
// network timeout anywhere in their tree, wrapped or joined: the first are a
// timeout, the others a transport failure, and the marker in their text never
// surfaces. Nothing waits for a deadline.
func TestTimeoutNetworkTimeoutError(t *testing.T) {
	for _, o := range opCases() {
		for _, ne := range timeoutNetErrors() {
			for _, status := range []int{0, http.StatusOK} {
				t.Run(o.name+"/"+ne.name+"/"+timeoutWhere(status), func(t *testing.T) {
					synctest.Test(t, func(t *testing.T) {
						c := newTransportClient(t, timeoutFailWith(ne.err, status))
						elapsed, err := timeoutTimed(func() error { return o.call(t.Context(), c) })
						assert.Zero(t, elapsed, "a reported failure ends the call at once")
						kind := emailable.FailureTransport
						if ne.timeout {
							kind = emailable.FailureTimeout
						}
						_ = assertCallError(t, err, o.want(wantCall{
							Kind: kind, Status: status, Attempts: 1, OutcomeUnknown: o.paid,
						}), timeoutMarker)
						assert.Equal(t, ne.timeout, errors.Is(err, context.DeadlineExceeded))
					})
				})
			}
		}
	}
}

// TestTimeoutStalledBody feeds every method a body that keeps answering
// (0, nil): after 100 such reads in a row the call fails at once as a
// transport failure, instead of spinning until the timeout. 99 empty reads
// before each part still succeed; 100 do not.
func TestTimeoutStalledBody(t *testing.T) {
	for _, o := range opCases() {
		t.Run(o.name+"/stalls after its content", func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				body := testutil.NewBody(`{"`)
				body.Stall = true
				c := newTransportClient(t, testutil.TransportWith(http.StatusOK, nil, -1, body))
				elapsed, err := timeoutTimed(func() error { return o.call(t.Context(), c) })
				_ = assertCallError(t, err, o.want(wantCall{
					Kind: emailable.FailureTransport, Status: http.StatusOK, Attempts: 1, OutcomeUnknown: o.paid,
				}))
				assert.Zero(t, elapsed, "a stalled body fails at once, not at the timeout")
				assert.Equal(t, 1+100, body.Reads(), "the content, then 100 empty reads")
				assert.Equal(t, 1, body.Closes())
			})
		})
		for _, empties := range []int{99, 100} {
			t.Run(fmt.Sprintf("%s/%d empty reads before each part", o.name, empties), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					body := testutil.NewBody(o.ok.Body)
					body.Step, body.Empties = 8, empties
					c := newTransportClient(t, testutil.TransportWith(http.StatusOK, nil, int64(len(o.ok.Body)), body))
					err := o.call(t.Context(), c)
					assert.Equal(t, 1, body.Closes())
					if empties < 100 {
						require.NoError(t, err)
						return
					}
					_ = assertCallError(t, err, o.want(wantCall{
						Kind: emailable.FailureTransport, Status: http.StatusOK, Attempts: 1, OutcomeUnknown: o.paid,
					}))
					assert.Zero(t, body.BytesRead(), "it stalled before the first part")
				})
			})
		}
	}
}

// TestTimeoutEndlessBody feeds every method a body that never ends: the read
// stops one byte past the operation's limit, and fails as
// response_too_large. A body declared larger than the limit is never read.
func TestTimeoutEndlessBody(t *testing.T) {
	for _, o := range opCases() {
		defaultLimit := int64(1 << 20)
		if o.op == emailable.OpGetBatch {
			defaultLimit = emailable.DefaultMaxResponseBytes
		}
		tests := []struct {
			name     string
			opts     []emailable.Option
			declared int64
			wantRead int64
		}{
			{name: "1 KiB limit", opts: []emailable.Option{emailable.WithMaxResponseBytes(1 << 10)}, declared: -1, wantRead: 1<<10 + 1},
			{name: "default limit", declared: -1, wantRead: defaultLimit + 1},
			{name: "declared over the limit", opts: []emailable.Option{emailable.WithMaxResponseBytes(1 << 10)}, declared: 1<<10 + 1},
			{name: "declared huge", declared: 1 << 62},
		}
		for _, tt := range tests {
			t.Run(o.name+"/"+tt.name, func(t *testing.T) {
				body := &testutil.EndlessBody{}
				c := newTransportClient(t, testutil.TransportWith(http.StatusOK, nil, tt.declared, body), tt.opts...)
				err := o.call(t.Context(), c)
				_ = assertCallError(t, err, o.want(wantCall{
					Kind: emailable.FailureResponseTooLarge, Status: http.StatusOK, Attempts: 1, OutcomeUnknown: o.paid,
				}))
				assert.Equal(t, tt.wantRead, body.BytesRead(), "at most one byte past the limit is read")
				assert.Equal(t, 1, body.Closes())
			})
		}
	}
}

// timeoutSentFailure is a failure after the request reached the transport.
type timeoutSentFailure struct {
	name   string
	status int
	kind   emailable.FailureKind
	// respond answers the attempt; cancel cancels the caller's context.
	respond func(cancel context.CancelFunc) testutil.RoundTripFunc
}

func timeoutSentFailures() []timeoutSentFailure {
	return []timeoutSentFailure{
		{
			name: "timeout awaiting headers", kind: emailable.FailureTimeout,
			respond: func(context.CancelFunc) testutil.RoundTripFunc { return testutil.BlockUntilDone },
		},
		{
			name: "timeout mid-body", kind: emailable.FailureTimeout, status: http.StatusOK,
			respond: func(context.CancelFunc) testutil.RoundTripFunc {
				return func(r *http.Request) (*http.Response, error) {
					return testutil.TransportWith(http.StatusOK, nil, -1, testutil.NewBlockingBody(`{"`))(r)
				}
			},
		},
		{
			name: "network timeout", kind: emailable.FailureTimeout,
			respond: func(context.CancelFunc) testutil.RoundTripFunc {
				return func(r *http.Request) (*http.Response, error) {
					testutil.Drain(r)
					return nil, testutil.TimeoutError{Msg: timeoutMarker}
				}
			},
		},
		{
			name: "transport failure", kind: emailable.FailureTransport,
			respond: func(context.CancelFunc) testutil.RoundTripFunc {
				return func(r *http.Request) (*http.Response, error) {
					testutil.Drain(r)
					return nil, errTimeoutMarker
				}
			},
		},
		{
			name: "canceled awaiting headers", kind: emailable.FailureCanceled,
			respond: func(cancel context.CancelFunc) testutil.RoundTripFunc {
				return func(r *http.Request) (*http.Response, error) {
					cancel()
					return testutil.BlockUntilDone(r)
				}
			},
		},
		{
			name: "canceled mid-body", kind: emailable.FailureCanceled, status: http.StatusOK,
			respond: func(cancel context.CancelFunc) testutil.RoundTripFunc {
				return func(r *http.Request) (*http.Response, error) {
					time.AfterFunc(time.Second, cancel)
					return testutil.TransportWith(http.StatusOK, nil, -1, testutil.NewBlockingBody(`{"`))(r)
				}
			},
		},
	}
}

// TestTimeoutOutcomeUnknownOnlyForPaidOps fails every method after its
// request reached the transport, by timeout, transport failure or
// cancellation: the outcome is unknown, and the error matches
// ErrOutcomeUnknown, exactly for the paid ones (VerifyEmail,
// WaitForVerification and CreateBatch), never for GetBatch or AccountInfo.
func TestTimeoutOutcomeUnknownOnlyForPaidOps(t *testing.T) {
	for _, o := range opCases() {
		for _, f := range timeoutSentFailures() {
			t.Run(o.name+"/"+f.name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					ctx, cancel := context.WithCancel(t.Context())
					defer cancel()
					c := newTransportClient(t, f.respond(cancel), emailable.WithTimeout(4*time.Second))
					err := o.call(ctx, c)
					ce := assertCallError(t, err, o.want(wantCall{
						Kind: f.kind, Status: f.status, Attempts: 1, OutcomeUnknown: o.paid,
					}), timeoutMarker)
					assert.Equal(t, o.paid, ce.OutcomeUnknown)
					assert.Equal(t, o.paid, errors.Is(err, emailable.ErrOutcomeUnknown))
				})
			})
		}
	}
}

// TestTimeoutDeadlineCarriedToTransport checks that the request every method
// hands the transport carries the call's deadline: WithTimeout from the
// start of the call, or the caller's deadline when it is earlier.
func TestTimeoutDeadlineCarriedToTransport(t *testing.T) {
	tests := []struct {
		name   string
		caller time.Duration // 0 for none
		want   time.Duration
	}{
		{name: "SDK timeout", want: 13 * time.Second},
		{name: "caller deadline earlier", caller: 4 * time.Second, want: 4 * time.Second},
		{name: "caller deadline later", caller: time.Minute, want: 13 * time.Second},
	}
	for _, o := range opCases() {
		for _, tt := range tests {
			t.Run(o.name+"/"+tt.name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					var left time.Duration
					var hasDeadline bool
					s := testutil.NewScript(testutil.Step{Respond: func(r *http.Request) (*http.Response, error) {
						var deadline time.Time
						deadline, hasDeadline = r.Context().Deadline()
						left = time.Until(deadline)
						return testutil.Reply(r, o.ok)
					}})
					c := newTransportClient(t, s, emailable.WithTimeout(13*time.Second))
					ctx := t.Context()
					if tt.caller > 0 {
						var cancel context.CancelFunc
						ctx, cancel = context.WithTimeout(ctx, tt.caller)
						defer cancel()
					}
					require.NoError(t, o.call(ctx, c))
					require.True(t, hasDeadline, "every attempt carries a deadline")
					assert.Equal(t, tt.want, left)
				})
			})
		}
	}
}

// TestTimeoutCallerCancel cancels the caller's context while every method's
// attempt waits for headers and while it reads the body, at once and after a
// while: the call ends exactly then, canceled, after one attempt.
func TestTimeoutCallerCancel(t *testing.T) {
	for _, o := range opCases() {
		for _, status := range []int{0, http.StatusOK} {
			for _, after := range []time.Duration{0, 2 * time.Second} {
				t.Run(fmt.Sprintf("%s/%s/after %s", o.name, timeoutWhere(status), after), func(t *testing.T) {
					synctest.Test(t, func(t *testing.T) {
						body := testutil.NewBody(`{`)
						ct := &testutil.CountingTransport{Next: timeoutStalling(status, body)}
						c := newTransportClient(t, ct)
						ctx, cancel := context.WithCancel(t.Context())
						defer cancel()

						type outcome struct {
							elapsed time.Duration
							err     error
						}
						done := make(chan outcome, 1)
						go func() {
							elapsed, err := timeoutTimed(func() error { return o.call(ctx, c) })
							done <- outcome{elapsed: elapsed, err: err}
						}()
						if after > 0 {
							<-time.After(after)
						}
						synctest.Wait()
						require.Empty(t, done, "the call must still be waiting")
						cancel()
						out := <-done

						_ = assertCallError(t, out.err, o.want(wantCall{
							Kind: emailable.FailureCanceled, Status: status, Attempts: 1, OutcomeUnknown: o.paid,
						}))
						assert.Equal(t, after, out.elapsed, "the call ends when the caller cancels")
						assert.Equal(t, 1, ct.Attempts())
						if status != 0 {
							assert.Equal(t, 1, body.Closes())
						}
					})
				})
			}
		}
	}
}
