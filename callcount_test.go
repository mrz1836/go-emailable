package emailable_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-emailable"
	"github.com/mrz1836/go-emailable/internal/testutil"
)

// errCallCountTransport is a synthetic transport or body read failure.
var errCallCountTransport = errors.New("callcount synthetic transport failure")

// Limits the call-count client runs with: every operation is capped at
// callCountLimit bytes, and every attempt at callCountTimeout.
const (
	callCountLimit   = 1 << 10
	callCountTimeout = 5 * time.Second
)

// callCountFailure is one way an attempted call can fail after its request
// reached the transport.
type callCountFailure struct {
	name string
	// step answers the attempt; cancel cancels the caller's context. It is
	// made inside the bubble.
	step     func(cancel context.CancelFunc) testutil.Step
	kind     func(op emailable.Operation) emailable.FailureKind
	status   int
	deadline time.Duration // the caller's context deadline; 0 for none
}

// callCountKind returns a kind function that ignores the operation.
func callCountKind(kind emailable.FailureKind) func(emailable.Operation) emailable.FailureKind {
	return func(emailable.Operation) emailable.FailureKind { return kind }
}

// callCountStatus is the failure of an HTTP status other than 200, whose
// kind the status decides. A 3xx carries a Location that must never be
// followed.
func callCountStatus(code int) callCountFailure {
	return callCountFailure{
		name: "HTTP " + strconv.Itoa(code),
		step: func(context.CancelFunc) testutil.Step {
			step := testutil.Step{Status: code, Body: `{"message":"no"}`}
			if code >= 300 && code < 400 {
				step.Header = http.Header{"Content-Type": {mediaJSON}, "Location": {loopbackOrigin + "/elsewhere"}}
			}
			return step
		},
		kind:   func(op emailable.Operation) emailable.FailureKind { return statusKind(op, code) },
		status: code,
	}
}

// callCountBody is the failure of an HTTP 200 answer with body.
func callCountBody(name, body string, kind emailable.FailureKind) callCountFailure {
	return callCountFailure{
		name:   name,
		step:   func(context.CancelFunc) testutil.Step { return testutil.Step{Body: body} },
		kind:   callCountKind(kind),
		status: http.StatusOK,
	}
}

// callCountRespond is a failure answered by respond, with kind and status.
func callCountRespond(name string, kind emailable.FailureKind, status int,
	respond func(r *http.Request) (*http.Response, error),
) callCountFailure {
	return callCountFailure{
		name:   name,
		step:   func(context.CancelFunc) testutil.Step { return testutil.Step{Respond: respond} },
		kind:   callCountKind(kind),
		status: status,
	}
}

// callCountFailures returns every failure class: each status class, HTTP 249
// included, then transport failures, timeouts, cancellations, and answers
// that cannot be read.
func callCountFailures() []callCountFailure {
	codes := []int{
		101, 204, 249, 299, 300, 301, 302, 303, 304, 307, 308, 400, 401, 402, 403, 404, 409, 422, 429, 499,
		500, 501, 502, 503, 504, 599,
	}
	failures := make([]callCountFailure, 0, len(codes)+20)
	for _, code := range codes {
		failures = append(failures, callCountStatus(code))
	}
	return append(failures,
		callCountRespond("transport failure", emailable.FailureTransport, 0, func(r *http.Request) (*http.Response, error) {
			testutil.Drain(r)
			return nil, errCallCountTransport
		}),
		callCountRespond("network timeout", emailable.FailureTimeout, 0, func(r *http.Request) (*http.Response, error) {
			testutil.Drain(r)
			return nil, testutil.TimeoutError{}
		}),
		callCountRespond("SDK timeout awaiting headers", emailable.FailureTimeout, 0, testutil.BlockUntilDone),
		callCountRespond("SDK timeout mid-body", emailable.FailureTimeout, http.StatusOK,
			func(r *http.Request) (*http.Response, error) {
				return testutil.TransportWith(http.StatusOK, nil, -1, testutil.NewBlockingBody(`{"email`))(r)
			}),
		callCountFailure{
			name: "canceled awaiting headers",
			step: func(cancel context.CancelFunc) testutil.Step {
				return testutil.Step{Respond: func(r *http.Request) (*http.Response, error) {
					cancel()
					return testutil.BlockUntilDone(r)
				}}
			},
			kind: callCountKind(emailable.FailureCanceled),
		},
		callCountFailure{
			name: "canceled mid-body",
			step: func(cancel context.CancelFunc) testutil.Step {
				return testutil.Step{Respond: func(r *http.Request) (*http.Response, error) {
					cancel()
					return testutil.TransportWith(http.StatusOK, nil, -1, testutil.NewBlockingBody(`{"email`))(r)
				}}
			},
			kind: callCountKind(emailable.FailureCanceled), status: http.StatusOK,
		},
		callCountFailure{
			name:     "caller deadline awaiting headers",
			step:     func(context.CancelFunc) testutil.Step { return testutil.Step{Respond: testutil.BlockUntilDone} },
			kind:     callCountKind(emailable.FailureDeadlineExceeded),
			deadline: 2 * time.Second,
		},
		callCountBody("malformed JSON", `{"email":`, emailable.FailureMalformedResponse),
		callCountBody("invalid UTF-8", "{\"message\":\"\xff\"}", emailable.FailureMalformedResponse),
		callCountBody("repeated key", `{"message":"a","Message":"b"}`, emailable.FailureMalformedResponse),
		callCountBody("data after the object", `{} {}`, emailable.FailureMalformedResponse),
		callCountBody("not an object", `[]`, emailable.FailureUnexpectedResponse),
		callCountRespond("declared too large", emailable.FailureResponseTooLarge, http.StatusOK,
			func(r *http.Request) (*http.Response, error) {
				return testutil.TransportWith(http.StatusOK, nil, callCountLimit+1, io.NopCloser(strings.NewReader("{}")))(r)
			}),
		callCountRespond("endless body", emailable.FailureResponseTooLarge, http.StatusOK,
			func(r *http.Request) (*http.Response, error) {
				return testutil.TransportWith(http.StatusOK, nil, -1, &testutil.EndlessBody{})(r)
			}),
		callCountRespond("undecoded encoding", emailable.FailureUnexpectedResponse, http.StatusOK,
			func(r *http.Request) (*http.Response, error) {
				header := http.Header{"Content-Type": {mediaJSON}, "Content-Encoding": {"gzip"}}
				return testutil.TransportWith(http.StatusOK, header, 2, io.NopCloser(strings.NewReader("{}")))(r)
			}),
		callCountRespond("undecoded encoding under a lower-case name", emailable.FailureUnexpectedResponse, http.StatusOK,
			func(r *http.Request) (*http.Response, error) {
				header := http.Header{"Content-Type": {mediaJSON}, "content-encoding": {"identity, br"}}
				return testutil.TransportWith(http.StatusOK, header, 2, io.NopCloser(strings.NewReader("{}")))(r)
			}),
		callCountRespond("truncated body", emailable.FailureTransport, http.StatusOK,
			func(r *http.Request) (*http.Response, error) {
				return testutil.TransportWith(http.StatusOK, nil, 100, io.NopCloser(strings.NewReader(`{"a":1}`)))(r)
			}),
		callCountRespond("stalled body", emailable.FailureTransport, http.StatusOK,
			func(r *http.Request) (*http.Response, error) {
				body := testutil.NewBody(`{"email"`)
				body.Stall = true
				return testutil.TransportWith(http.StatusOK, nil, -1, body)(r)
			}),
		callCountRespond("body read failure", emailable.FailureTransport, http.StatusOK,
			func(r *http.Request) (*http.Response, error) {
				body := testutil.NewBody(`{"email"`)
				body.End = errCallCountTransport
				return testutil.TransportWith(http.StatusOK, nil, -1, body)(r)
			}),
	)
}

// callCountClient returns a client whose calls go, in memory, through s, with
// the call-count limits.
func callCountClient(t *testing.T, s *testutil.Script) *emailable.Client {
	t.Helper()
	return newTransportClient(t, s, emailable.WithTimeout(callCountTimeout), emailable.WithMaxResponseBytes(callCountLimit))
}

// TestCallCountOneAttemptPerFailure proves that every failure an attempted
// call can meet ends it after exactly one request: the script holds two
// successes after the failing answer, so a retry, a follow-up or a resend
// would succeed and be counted. An hour later still nothing else was sent.
// WaitForVerification repeats an HTTP 249 by design; its counts are in
// TestCallCountWaitForVerification.
func TestCallCountOneAttemptPerFailure(t *testing.T) {
	for _, o := range opCases() {
		for _, f := range callCountFailures() {
			if o.helper && f.status == emailable.StatusPending {
				continue
			}
			t.Run(o.name+"/"+f.name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					ctx, cancel := context.WithCancel(t.Context())
					defer cancel()
					callCtx := ctx
					if f.deadline > 0 {
						var stop context.CancelFunc
						callCtx, stop = context.WithTimeout(ctx, f.deadline)
						defer stop()
					}
					s := testutil.NewScript(f.step(cancel), o.ok, o.ok)
					c := callCountClient(t, s)

					err := o.call(callCtx, c)

					kind := f.kind(o.op)
					ce := assertCallError(t, err, o.want(wantCall{
						Kind: kind, Status: f.status, Attempts: 1, OutcomeUnknown: outcomeUnknownAfter(o.paid, kind),
					}))
					assert.Equal(t, 1, ce.Attempts, "Attempts counts the one request")
					<-time.After(time.Hour)
					synctest.Wait()
					assert.Equal(t, []string{o.path}, s.Paths(), "exactly one request, and nothing after it")
				})
			})
		}
	}
}

// callCountInvalid is a call whose request fails local validation.
type callCountInvalid struct {
	call          func(ctx context.Context, c *emailable.Client) error
	field, reason string
}

// callCountInvalidRequests returns an invalid request for each method that
// takes one with fields.
func callCountInvalidRequests() map[string]callCountInvalid {
	return map[string]callCountInvalid{
		"VerifyEmail": {
			call: func(ctx context.Context, c *emailable.Client) error {
				return noValueWith(c.VerifyEmail(ctx, emailable.VerifyEmailRequest{}))
			},
			field: "Email", reason: reasonRequired,
		},
		"CreateBatch": {
			call: func(ctx context.Context, c *emailable.Client) error {
				return noValueWith(c.CreateBatch(ctx, emailable.CreateBatchRequest{}))
			},
			field: "Emails", reason: reasonRequired,
		},
		"GetBatch": {
			call: func(ctx context.Context, c *emailable.Client) error {
				return noValueWith(c.GetBatch(ctx, emailable.GetBatchRequest{}))
			},
			field: "BatchID", reason: reasonRequired,
		},
		"WaitForVerification": {
			call: func(ctx context.Context, c *emailable.Client) error {
				return noValueWith(c.WaitForVerification(ctx, emailable.WaitForVerificationRequest{Verify: verifyRequest()}))
			},
			field: "MaxAttempts", reason: reasonRequired,
		},
	}
}

// TestCallCountNothingSent proves that a call that fails before sending
// makes no transport call and reports no attempt: an invalid request, a nil
// context, a Client not built by NewClient, and a context already canceled or
// expired.
func TestCallCountNothingSent(t *testing.T) {
	for _, o := range opCases() {
		t.Run(o.name, func(t *testing.T) {
			c, ct := newOfflineClient(t)
			if inv, ok := callCountInvalidRequests()[o.name]; ok {
				assertValidation(t, inv.call(t.Context(), c), emailable.ErrInvalidRequest, inv.field, inv.reason)
			}
			t.Run("nil context", func(t *testing.T) {
				var nilCtx context.Context
				assertValidation(t, o.call(nilCtx, c), emailable.ErrInvalidRequest, "ctx", reasonNil)
			})
			assertValidation(t, o.call(t.Context(), &emailable.Client{}), emailable.ErrInvalidConfig, "Client", reasonNotInitialized)
			assertValidation(t, o.call(t.Context(), nil), emailable.ErrInvalidConfig, "Client", reasonNotInitialized)

			canceled, cancel := context.WithCancel(t.Context())
			cancel()
			ce := assertCallError(t, o.call(canceled, c), o.want(wantCall{Kind: emailable.FailureCanceled}))
			assert.Zero(t, ce.Attempts, "nothing was sent")
			expired, stop := context.WithTimeout(t.Context(), -time.Second)
			defer stop()
			ce = assertCallError(t, o.call(expired, c), o.want(wantCall{Kind: emailable.FailureDeadlineExceeded}))
			assert.Zero(t, ce.Attempts, "nothing was sent")

			assert.Zero(t, ct.Attempts(), "no transport call")
		})
	}
}

// TestCallCountWaitForVerification counts WaitForVerification's requests: one
// per HTTP 249 until a result, the budget or another outcome ends it, and
// never one more.
func TestCallCountWaitForVerification(t *testing.T) {
	answers := func(n int, last ...testutil.Step) []testutil.Step {
		steps := make([]testutil.Step, 0, n+len(last)+2)
		for range n {
			steps = append(steps, testutil.Step{Status: emailable.StatusPending, Body: pending})
		}
		steps = append(steps, last...)
		// Spare answers: a request past the expected count is recorded.
		return append(steps, testutil.Step{Body: verifyOK}, testutil.Step{Body: verifyOK})
	}
	result := testutil.Step{Body: verifyOK}
	tests := []struct {
		name      string
		steps     []testutil.Step
		req       emailable.WaitForVerificationRequest
		wantKind  emailable.FailureKind // "" for success
		wantCalls int
	}{
		{name: "a result at once", steps: answers(0, result), req: waitRequest(0, 30, time.Minute), wantCalls: 1},
		{name: "one 249, then a result", steps: answers(1, result), req: waitRequest(0, 30, time.Minute), wantCalls: 2},
		{name: "four 249s, then a result", steps: answers(4, result), req: waitRequest(0, 30, time.Minute), wantCalls: 5},
		{
			name: "29 249s, then a result", steps: answers(29, result), req: waitRequest(time.Second, 30, time.Minute),
			wantCalls: 30,
		},
		{
			name: "MaxAttempts 2", steps: answers(5), req: waitRequest(0, 2, time.Minute),
			wantKind: emailable.FailureBudgetExhausted, wantCalls: 2,
		},
		{
			name: "MaxAttempts 5", steps: answers(8), req: waitRequest(0, 5, time.Minute),
			wantKind: emailable.FailureBudgetExhausted, wantCalls: 5,
		},
		{
			name: "MaxAttempts 30", steps: answers(33), req: waitRequest(time.Second, 30, time.Minute),
			wantKind: emailable.FailureBudgetExhausted, wantCalls: 30,
		},
		{
			name: "MaxWait 9s", steps: answers(8), req: waitRequest(0, 30, 9*time.Second),
			wantKind: emailable.FailureBudgetExhausted, wantCalls: 5,
		},
		{
			name: "MaxPendingWait", steps: answers(12), req: waitRequest(30*time.Second, 30, emailable.MaxPendingWait),
			wantKind: emailable.FailureBudgetExhausted, wantCalls: 8,
		},
		{
			name: "a 503 after two 249s", steps: answers(2, testutil.Step{Status: http.StatusServiceUnavailable}),
			req: waitRequest(0, 30, time.Minute), wantKind: emailable.FailureUnavailable, wantCalls: 3,
		},
		{
			name: "a transport failure after three 249s", steps: answers(3, testutil.Step{Err: errCallCountTransport}),
			req: waitRequest(0, 30, time.Minute), wantKind: emailable.FailureTransport, wantCalls: 4,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s := testutil.NewScript(tt.steps...)
				c := newTransportClient(t, s)
				v, err := c.WaitForVerification(t.Context(), tt.req)
				if tt.wantKind == "" {
					require.NoError(t, err)
					require.NotNil(t, v)
				} else {
					require.Nil(t, v)
					ce := callError(t, err)
					assert.Equal(t, tt.wantKind, ce.Kind)
					assert.Equal(t, tt.wantCalls, ce.Attempts, "Attempts counts every request")
				}
				<-time.After(time.Hour)
				synctest.Wait()
				assert.Equal(t, tt.wantCalls, s.Calls(), "exactly this many requests, and none after")
			})
		})
	}
}

// TestCallCountOnSuccess checks the success path: one request per call, and
// a second identical call on the same Client makes a second request,
// identical to the first: nothing is cached or deduplicated.
func TestCallCountOnSuccess(t *testing.T) {
	for _, o := range opCases() {
		t.Run(o.name, func(t *testing.T) {
			s := testutil.NewScript(o.ok, o.ok, o.ok)
			c := newTransportClient(t, s)
			require.NoError(t, o.call(t.Context(), c))
			assert.Equal(t, []string{o.path}, s.Paths(), "one request")
			require.NoError(t, o.call(t.Context(), c))
			assert.Equal(t, []string{o.path, o.path}, s.Paths(), "a second call makes a second request")
			reqs := s.Requests()
			require.Len(t, reqs, 2)
			for _, r := range reqs {
				assertWireRequest(t, r, o.method, o.path)
				assertNoReplay(t, r)
			}
			assert.Equal(t, reqs[0].URL, reqs[1].URL, "the same request twice")
			assert.Equal(t, reqs[0].Body, reqs[1].Body)
		})
	}
}
