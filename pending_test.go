package emailable_test

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-emailable"
	"github.com/mrz1836/go-emailable/internal/testutil"
)

// Every WaitForVerification test that sends anything runs in a synctest
// bubble over an in-memory transport, so its clock is exact and no real time
// passes. A bubble also fails its test when a goroutine started inside it is
// still blocked once the test function returns: none of these tests can pass
// while the helper leaves a goroutine, or a request, behind.

// errPendingTransport is a synthetic transport failure.
var errPendingTransport = errors.New("pending synthetic transport failure")

// pendingSpares is how many spare successes follow a script's steps: a
// request the helper must not make then succeeds and is counted, instead of
// failing on an exhausted script.
const pendingSpares = 3

// pendingAnswer is the provider's HTTP 249: still verifying.
func pendingAnswer() testutil.Step {
	return testutil.Step{Status: emailable.StatusPending, Body: pending}
}

// pendingSuccess is a successful verify answer.
func pendingSuccess() testutil.Step { return testutil.Step{Body: verifyOK} }

// pendingBlock is a step that never answers: it waits for the request's
// context to end.
func pendingBlock() testutil.Step { return testutil.Step{Respond: testutil.BlockUntilDone} }

// pendingAfter is a step that answers as step does once d has passed, or
// fails with the request context's error when that ends first.
func pendingAfter(d time.Duration, step testutil.Step) testutil.Step {
	return testutil.Step{Respond: func(r *http.Request) (*http.Response, error) {
		select {
		case <-time.After(d):
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
		return testutil.Reply(r, step)
	}}
}

// pendingRepeat returns n copies of step.
func pendingRepeat(n int, step testutil.Step) []testutil.Step {
	steps := make([]testutil.Step, n)
	for i := range steps {
		steps[i] = step
	}
	return steps
}

// pendingSteps concatenates step lists and appends pendingSpares successes.
func pendingSteps(lists ...[]testutil.Step) []testutil.Step {
	n := pendingSpares
	for _, l := range lists {
		n += len(l)
	}
	out := make([]testutil.Step, 0, n)
	for _, l := range lists {
		out = append(out, l...)
	}
	return append(out, pendingRepeat(pendingSpares, pendingSuccess())...)
}

// pendingEffective is the pause WaitForVerification makes for interval.
func pendingEffective(interval time.Duration) time.Duration {
	if interval == 0 {
		return emailable.DefaultPendingInterval
	}
	return interval
}

// pendingSchedule returns the offsets of n attempts made every step from 0.
func pendingSchedule(step time.Duration, n int) []time.Duration {
	at := make([]time.Duration, n)
	for i := range at {
		at[i] = time.Duration(i) * step
	}
	return at
}

// pendingRateLimit is the ratelimit-limit every scripted answer carries.
const pendingRateLimit = 100

// pendingVerified asserts that v is the scripted successful verification.
func pendingVerified(t *testing.T, v *emailable.Verification) {
	t.Helper()
	require.NotNil(t, v)
	assert.Equal(t, &emailable.Verification{
		Email: testEmail, State: emailable.StateDeliverable, Reason: emailable.ReasonAcceptedEmail,
	}, v)
}

// pendingHarness is a client whose calls go, in memory, through a Script,
// with the offset from the harness's start at which each request reached the
// transport, on the bubble's fake clock.
type pendingHarness struct {
	c      *emailable.Client
	script *testutil.Script
	start  time.Time

	mu sync.Mutex
	at []time.Duration
}

// pendingStart returns a harness answering with steps, for a client built
// with opts. It must be called inside the bubble, just before the call.
func pendingStart(t *testing.T, opts []emailable.Option, steps ...testutil.Step) *pendingHarness {
	t.Helper()
	h := &pendingHarness{script: testutil.NewScript(steps...), start: time.Now()}
	clock := testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		h.mu.Lock()
		h.at = append(h.at, time.Since(h.start))
		h.mu.Unlock()
		return h.script.RoundTrip(r)
	})
	h.c = newTransportClient(t, clock, opts...)
	return h
}

// pendingRun is the outcome of one WaitForVerification call in a bubble.
type pendingRun struct {
	v       *emailable.Verification
	err     error
	elapsed time.Duration
	calls   int
	at      []time.Duration
	query   string // the raw query of the first request, "" when none was sent
}

// wait calls WaitForVerification and measures the fake clock's elapsed time.
// It then lets an hour pass and asserts that nothing was requested after the
// call returned, that every request kept the wire policy and could not be
// replayed, and that every attempt carried a byte-identical query.
func (h *pendingHarness) wait(ctx context.Context, t *testing.T, req emailable.WaitForVerificationRequest) pendingRun {
	t.Helper()
	start := time.Now()
	v, err := h.c.WaitForVerification(ctx, req)
	run := pendingRun{v: v, err: err, elapsed: time.Since(start), calls: h.script.Calls()}
	if err != nil {
		assert.Nil(t, v, "nothing alongside an error")
	}
	<-time.After(time.Hour)
	synctest.Wait()
	assert.Equal(t, run.calls, h.script.Calls(), "no request after WaitForVerification returned")
	h.mu.Lock()
	run.at = slices.Clone(h.at)
	h.mu.Unlock()
	for i, r := range h.script.Requests() {
		assertWireRequest(t, r, http.MethodGet, pathVerify)
		assertNoReplay(t, r)
		if i == 0 {
			run.query = rawQuery(t, r)
			continue
		}
		assert.Equal(t, run.query, rawQuery(t, r), "attempt %d repeats the first request byte for byte", i+1)
	}
	return run
}

func TestWaitForVerificationConstants(t *testing.T) {
	assert.Equal(t, 2*time.Second, emailable.DefaultPendingInterval)
	assert.Equal(t, 4*time.Minute, emailable.MaxPendingWait)
	assert.Equal(t, 249, emailable.StatusPending)
}

// pendingVerifyCase is a verify request WaitForVerification repeats, and the
// raw query every attempt must carry.
type pendingVerifyCase struct {
	name  string
	req   emailable.VerifyEmailRequest
	query string
}

func pendingVerifyCases() []pendingVerifyCase {
	no, yes := false, true
	return []pendingVerifyCase{
		{name: "minimal", req: verifyRequest(), query: "email=jane.doe%40example.com"},
		{
			name: "every option",
			req: emailable.VerifyEmailRequest{
				Email: testEmail, SMTP: &no, AcceptAll: &yes, ProviderTimeout: 10 * time.Second,
				CaptchaResponse: testCaptcha,
			},
			query: "accept_all=true&captcha_response=" + testCaptcha + "&email=jane.doe%40example.com&smtp=false&timeout=10",
		},
	}
}

// TestWaitForVerificationSucceeds answers n HTTP 249s and then a result: the
// helper makes n+1 requests, each one interval after the last 249, every one
// byte-identical to the first, and returns the result as soon as it arrives.
func TestWaitForVerificationSucceeds(t *testing.T) {
	tests := []struct {
		pendings int
		interval time.Duration
	}{
		{pendings: 0},
		{pendings: 1},
		{pendings: 3, interval: time.Second},
		{pendings: 2, interval: 7 * time.Second},
		{pendings: 7, interval: 30 * time.Second},
		{pendings: 29},
	}
	for _, vc := range pendingVerifyCases() {
		for _, tt := range tests {
			name := vc.name + "/" + strconv.Itoa(tt.pendings) + " pending, interval " + tt.interval.String()
			t.Run(name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					h := pendingStart(t, nil, pendingSteps(pendingRepeat(tt.pendings, pendingAnswer()))...)
					run := h.wait(t.Context(), t, emailable.WaitForVerificationRequest{
						Verify: vc.req, Interval: tt.interval, MaxAttempts: 30, MaxWait: emailable.MaxPendingWait,
					})
					require.NoError(t, run.err)
					pendingVerified(t, run.v)
					eff := pendingEffective(tt.interval)
					assert.Equal(t, tt.pendings+1, run.calls, "one request per 249, then the one that succeeds")
					assert.Equal(t, pendingSchedule(eff, tt.pendings+1), run.at, "each attempt one interval after the last 249")
					assert.Equal(t, time.Duration(tt.pendings)*eff, run.elapsed, "returns as soon as an attempt succeeds")
					assert.Equal(t, vc.query, run.query, "the request is encoded once, as documented")
				})
			})
		}
	}
}

// TestWaitForVerificationBudgetAttempts answers HTTP 249 to every request:
// after MaxAttempts requests the helper stops at once, without a final pause,
// with budget_exhausted carrying the last status and the number of requests.
func TestWaitForVerificationBudgetAttempts(t *testing.T) {
	tests := []struct {
		attempts int
		interval time.Duration
	}{
		{attempts: 2},
		{attempts: 3, interval: time.Second},
		{attempts: 10, interval: 8 * time.Second},
		{attempts: 30},
		{attempts: 30, interval: time.Second},
	}
	for _, tt := range tests {
		t.Run(strconv.Itoa(tt.attempts)+" attempts, interval "+tt.interval.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := pendingStart(t, nil, pendingSteps(pendingRepeat(tt.attempts+pendingSpares, pendingAnswer()))...)
				run := h.wait(t.Context(), t, waitRequest(tt.interval, tt.attempts, emailable.MaxPendingWait))
				_ = assertCallError(t, run.err, wantCall{
					Kind: emailable.FailureBudgetExhausted, Op: emailable.OpVerifyEmail, Status: emailable.StatusPending,
					Attempts: tt.attempts,
				})
				require.NotErrorIs(t, run.err, emailable.ErrPending, "budget_exhausted is not pending")
				eff := pendingEffective(tt.interval)
				assert.Equal(t, tt.attempts, run.calls, "exactly MaxAttempts requests")
				assert.Equal(t, pendingSchedule(eff, tt.attempts), run.at)
				assert.Equal(t, time.Duration(tt.attempts-1)*eff, run.elapsed, "no pause after the last attempt")
			})
		})
	}
}

// TestWaitForVerificationBudgetTime answers HTTP 249 to every request and
// checks the time budget: an attempt starts only while the elapsed time plus
// the interval is less than MaxWait, so at the exact boundary
// (elapsed+Interval == MaxWait) no further attempt is made.
func TestWaitForVerificationBudgetTime(t *testing.T) {
	tests := []struct {
		name     string
		maxWait  time.Duration
		interval time.Duration
		want     int
	}{
		{name: "exact boundary", maxWait: 5 * time.Second, interval: time.Second, want: 5},
		{name: "one nanosecond past the boundary", maxWait: 5*time.Second + 1, interval: time.Second, want: 6},
		{name: "just short of the boundary", maxWait: 4999 * time.Millisecond, interval: time.Second, want: 5},
		{name: "MaxWait just above the interval", maxWait: time.Second + 1, interval: time.Second, want: 2},
		{name: "MaxWait just above the default interval", maxWait: 2*time.Second + 1, want: 2},
		{name: "MaxWait just above a 30s interval", maxWait: 30*time.Second + 1, interval: 30 * time.Second, want: 2},
		{name: "default interval at the boundary", maxWait: 10 * time.Second, want: 5},
		{name: "default interval between boundaries", maxWait: 7 * time.Second, want: 4},
		{name: "MaxPendingWait at the boundary", maxWait: emailable.MaxPendingWait, interval: 30 * time.Second, want: 8},
		{name: "MaxPendingWait-1ns", maxWait: emailable.MaxPendingWait - 1, interval: 30 * time.Second, want: 8},
		{name: "210s at the boundary", maxWait: 210 * time.Second, interval: 30 * time.Second, want: 7},
		{name: "210s+1ns allows an eighth attempt", maxWait: 210*time.Second + 1, interval: 30 * time.Second, want: 8},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := pendingStart(t, nil, pendingSteps(pendingRepeat(tt.want+pendingSpares, pendingAnswer()))...)
				run := h.wait(t.Context(), t, waitRequest(tt.interval, 30, tt.maxWait))
				_ = assertCallError(t, run.err, wantCall{
					Kind: emailable.FailureBudgetExhausted, Op: emailable.OpVerifyEmail, Status: emailable.StatusPending,
					Attempts: tt.want,
				})
				require.NotErrorIs(t, run.err, context.DeadlineExceeded, "MaxWait is not a context deadline")
				eff := pendingEffective(tt.interval)
				assert.Equal(t, tt.want, run.calls)
				assert.Equal(t, pendingSchedule(eff, tt.want), run.at)
				assert.Equal(t, time.Duration(tt.want-1)*eff, run.elapsed, "returns as soon as no attempt can start")
				assert.LessOrEqual(t, run.elapsed, tt.maxWait)
			})
		})
	}
}

// TestWaitForVerificationSlowAttemptsShiftTheClock lets attempts take time
// before their 249 arrives: the budget counts that time too, so fewer
// attempts fit, and each pause starts only when its 249 has arrived.
func TestWaitForVerificationSlowAttemptsShiftTheClock(t *testing.T) {
	slow := func(d time.Duration) testutil.Step { return pendingAfter(d, pendingAnswer()) }
	tests := []struct {
		name        string
		steps       []testutil.Step
		wantAt      []time.Duration
		wantElapsed time.Duration
	}{
		{
			name:        "every 249 at once",
			steps:       pendingRepeat(8, pendingAnswer()),
			wantAt:      pendingSchedule(2*time.Second, 5),
			wantElapsed: 8 * time.Second,
		},
		{
			name:        "a slow first 249",
			steps:       []testutil.Step{slow(7 * time.Second), pendingAnswer(), pendingAnswer()},
			wantAt:      []time.Duration{0, 9 * time.Second},
			wantElapsed: 9 * time.Second,
		},
		{
			name:        "a slow first 249 that uses up the budget",
			steps:       []testutil.Step{slow(8 * time.Second), pendingAnswer()},
			wantAt:      []time.Duration{0},
			wantElapsed: 8 * time.Second,
		},
		{
			name:        "every 249 takes a second",
			steps:       pendingRepeat(6, slow(time.Second)),
			wantAt:      pendingSchedule(3*time.Second, 4),
			wantElapsed: 10 * time.Second,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := pendingStart(t, nil, pendingSteps(tt.steps)...)
				run := h.wait(t.Context(), t, waitRequest(2*time.Second, 30, 10*time.Second))
				_ = assertCallError(t, run.err, wantCall{
					Kind: emailable.FailureBudgetExhausted, Op: emailable.OpVerifyEmail, Status: emailable.StatusPending,
					Attempts: len(tt.wantAt),
				})
				assert.Equal(t, tt.wantAt, run.at)
				assert.Equal(t, tt.wantElapsed, run.elapsed)
			})
		})
	}
}

// TestWaitForVerificationNeverCutsAnAttemptShort lets the last attempt run
// past MaxWait: MaxWait only decides whether an attempt starts, so the one in
// flight completes and its outcome is returned. The worst case is an attempt
// that starts just before MaxWait and runs until the SDK's timeout: about
// MaxWait plus WithTimeout.
func TestWaitForVerificationNeverCutsAnAttemptShort(t *testing.T) {
	const maxWait, sdkTimeout = 5 * time.Second, 10 * time.Second
	tests := []struct {
		name        string
		steps       []testutil.Step
		want        wantCall // the zero value for success
		wantAt      []time.Duration
		wantElapsed time.Duration
	}{
		{
			name:        "a result that arrives after MaxWait",
			steps:       []testutil.Step{pendingAnswer(), pendingAnswer(), pendingAfter(8*time.Second, pendingSuccess())},
			wantAt:      []time.Duration{0, time.Second, 2 * time.Second},
			wantElapsed: 10 * time.Second,
		},
		{
			name:  "a 249 that arrives after MaxWait",
			steps: []testutil.Step{pendingAnswer(), pendingAfter(9*time.Second, pendingAnswer())},
			want: wantCall{
				Kind: emailable.FailureBudgetExhausted, Op: emailable.OpVerifyEmail, Status: emailable.StatusPending,
				Attempts: 2,
			},
			wantAt:      []time.Duration{0, time.Second},
			wantElapsed: 10 * time.Second,
		},
		{
			name:  "an attempt that starts just before MaxWait and times out",
			steps: []testutil.Step{pendingAfter(3500*time.Millisecond, pendingAnswer()), pendingBlock()},
			want: wantCall{
				Kind: emailable.FailureTimeout, Op: emailable.OpVerifyEmail, Attempts: 2, OutcomeUnknown: true,
			},
			wantAt:      []time.Duration{0, 4500 * time.Millisecond},
			wantElapsed: 4500*time.Millisecond + sdkTimeout,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := pendingStart(t, []emailable.Option{emailable.WithTimeout(sdkTimeout)}, pendingSteps(tt.steps)...)
				run := h.wait(t.Context(), t, waitRequest(time.Second, 30, maxWait))
				if tt.want == (wantCall{}) {
					require.NoError(t, run.err)
					pendingVerified(t, run.v)
				} else {
					_ = assertCallError(t, run.err, tt.want)
				}
				assert.Equal(t, tt.wantAt, run.at)
				assert.Equal(t, tt.wantElapsed, run.elapsed)
				assert.Greater(t, run.elapsed, maxWait, "the attempt in flight outlived MaxWait")
				assert.LessOrEqual(t, run.elapsed, maxWait+sdkTimeout, "the worst case is MaxWait plus one timeout")
			})
		})
	}
}

// TestWaitForVerificationInterval checks the pause after each 249 on the fake
// clock: 0 means DefaultPendingInterval, and any other value from 1s to 30s
// is used as it is.
func TestWaitForVerificationInterval(t *testing.T) {
	for _, interval := range []time.Duration{0, time.Second, 1500 * time.Millisecond, 7 * time.Second, 30 * time.Second} {
		t.Run(interval.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := pendingStart(t, nil, pendingSteps(pendingRepeat(3, pendingAnswer()))...)
				run := h.wait(t.Context(), t, waitRequest(interval, 30, emailable.MaxPendingWait))
				require.NoError(t, run.err)
				eff := pendingEffective(interval)
				assert.Equal(t, pendingSchedule(eff, 4), run.at)
				assert.Equal(t, 3*eff, run.elapsed)
			})
		})
	}
}

// pendingBodies records the response bodies a pendingTrickling step hands
// out.
type pendingBodies struct {
	mu     sync.Mutex
	bodies []*testutil.Body
}

// step returns a step answering HTTP 249 with a body that delivers the
// provider's message 16 bytes at a time, gap before each part.
func (b *pendingBodies) step(gap time.Duration) testutil.Step {
	return testutil.Step{Respond: func(r *http.Request) (*http.Response, error) {
		body := testutil.NewBody(pending)
		body.Step, body.Gap = 16, gap
		b.mu.Lock()
		b.bodies = append(b.bodies, body)
		b.mu.Unlock()
		return testutil.Response(r, emailable.StatusPending, body, int64(len(pending))), nil
	}}
}

// list returns the bodies handed out so far.
func (b *pendingBodies) list() []*testutil.Body {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.bodies)
}

// TestWaitForVerificationPauseStartsAfterTheAnswer checks that each pause
// starts when the 249 answer has ended: after its headers when the body is
// not read, and after its body when diagnostics read it.
func TestWaitForVerificationPauseStartsAfterTheAnswer(t *testing.T) {
	const interval, gap = 2 * time.Second, 500 * time.Millisecond
	bodyTime := time.Duration((len(pending)+15)/16) * gap // one gap per 16-byte part
	tests := []struct {
		name     string
		opts     []emailable.Option
		headers  time.Duration // how long each 249's headers take
		wantBody time.Duration // how long reading each 249's body takes
	}{
		{name: "slow headers", headers: 3 * time.Second},
		{name: "body not read without diagnostics"},
		{name: "body read with diagnostics", opts: []emailable.Option{emailable.WithProviderDiagnostics()}, wantBody: bodyTime},
		{
			name: "slow headers and body read with diagnostics", headers: time.Second,
			opts: []emailable.Option{emailable.WithProviderDiagnostics()}, wantBody: bodyTime,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				bodies := &pendingBodies{}
				answer := bodies.step(gap)
				if tt.headers > 0 {
					answer = pendingAfter(tt.headers, answer)
				}
				h := pendingStart(t, tt.opts, pendingSteps(pendingRepeat(3, answer))...)
				run := h.wait(t.Context(), t, waitRequest(interval, 30, emailable.MaxPendingWait))
				require.NoError(t, run.err)
				each := tt.headers + tt.wantBody + interval
				assert.Equal(t, pendingSchedule(each, 4), run.at, "each pause starts when its 249 has ended")
				assert.Equal(t, 3*each, run.elapsed)
				list := bodies.list()
				require.Len(t, list, 3)
				for _, b := range list {
					assert.Equal(t, 1, b.Closes(), "every 249 body is closed exactly once")
					if tt.wantBody == 0 {
						assert.Zero(t, b.Reads(), "without diagnostics a 249 body is never read")
					} else {
						assert.Equal(t, int64(len(pending)), b.BytesRead(), "diagnostics read the whole 249 body")
					}
				}
			})
		})
	}
}

// TestWaitForVerificationCallerContext ends the caller's context before the
// first attempt, during a pause and during an attempt. A context that ends
// during a pause stops the helper at once, without waiting for its timer,
// with canceled or deadline_exceeded, StatusCode 249 and nothing in flight;
// one that ends during an attempt leaves that attempt's outcome unknown.
func TestWaitForVerificationCallerContext(t *testing.T) {
	answers := pendingRepeat(6, pendingAnswer())
	block := func(n int) []testutil.Step {
		return append(pendingRepeat(n, pendingAnswer()), pendingBlock())
	}
	tests := []struct {
		name        string
		ctx         contextFactory
		steps       []testutil.Step
		want        wantCall
		wantElapsed time.Duration
	}{
		{
			name: "canceled before the first attempt", ctx: alreadyCanceled, steps: answers,
			want: wantCall{Kind: emailable.FailureCanceled},
		},
		{
			name: "deadline passed before the first attempt", ctx: expiresAfter(-time.Second), steps: answers,
			want: wantCall{Kind: emailable.FailureDeadlineExceeded},
		},
		{
			name: "canceled during the first pause", ctx: canceledAfter(500 * time.Millisecond), steps: answers,
			want:        wantCall{Kind: emailable.FailureCanceled, Status: emailable.StatusPending, Attempts: 1},
			wantElapsed: 500 * time.Millisecond,
		},
		{
			name: "canceled during the third pause", ctx: canceledAfter(5 * time.Second), steps: answers,
			want:        wantCall{Kind: emailable.FailureCanceled, Status: emailable.StatusPending, Attempts: 3},
			wantElapsed: 5 * time.Second,
		},
		{
			name: "deadline during a pause", ctx: expiresAfter(3 * time.Second), steps: answers,
			want:        wantCall{Kind: emailable.FailureDeadlineExceeded, Status: emailable.StatusPending, Attempts: 2},
			wantElapsed: 3 * time.Second,
		},
		{
			name: "canceled during an attempt", ctx: canceledAfter(3 * time.Second), steps: block(1),
			want:        wantCall{Kind: emailable.FailureCanceled, Attempts: 2, OutcomeUnknown: true},
			wantElapsed: 3 * time.Second,
		},
		{
			name: "deadline during an attempt", ctx: expiresAfter(5 * time.Second), steps: block(2),
			want:        wantCall{Kind: emailable.FailureDeadlineExceeded, Attempts: 3, OutcomeUnknown: true},
			wantElapsed: 5 * time.Second,
		},
		{
			name: "deadline during the first attempt", ctx: expiresAfter(time.Second), steps: block(0),
			want:        wantCall{Kind: emailable.FailureDeadlineExceeded, Attempts: 1, OutcomeUnknown: true},
			wantElapsed: time.Second,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := pendingStart(t, nil, pendingSteps(tt.steps)...)
				ctx, cancel := tt.ctx(t.Context())
				defer cancel()
				run := h.wait(ctx, t, waitRequest(2*time.Second, 30, emailable.MaxPendingWait))
				tt.want.Op = emailable.OpVerifyEmail
				_ = assertCallError(t, run.err, tt.want)
				require.NotErrorIs(t, run.err, emailable.ErrTimeout, "the caller's deadline is not the SDK's timeout")
				require.NotErrorIs(t, run.err, emailable.ErrBudgetExhausted)
				assert.Equal(t, tt.want.Attempts, run.calls, "Attempts counts the requests made")
				assert.Equal(t, tt.wantElapsed, run.elapsed, "returns as soon as the context ends")
			})
		})
	}
}

// pendingFailure is an answer that ends a wait at once.
type pendingFailure struct {
	name    string
	step    testutil.Step
	kind    emailable.FailureKind
	status  int
	field   string
	timeout bool // the step never answers, so the SDK's timeout ends it
}

func pendingFailures() []pendingFailure {
	status := func(code int) testutil.Step { return testutil.Step{Status: code, Body: `{"message":"no"}`} }
	return []pendingFailure{
		{
			name: "200 malformed JSON", step: testutil.Step{Body: `{"email":`},
			kind: emailable.FailureMalformedResponse, status: http.StatusOK,
		},
		{
			name: "200 without state", step: testutil.Step{Body: `{"email":"` + testEmail + `"}`},
			kind: emailable.FailureMalformedResponse, status: http.StatusOK, field: "state",
		},
		{
			name: "200 with an invalid state token", step: testutil.Step{Body: `{"email":"a@b","state":"Deliverable"}`},
			kind: emailable.FailureUnexpectedResponse, status: http.StatusOK, field: "state",
		},
		{name: "400", step: status(400), kind: emailable.FailureRejected, status: 400},
		{name: "401", step: status(401), kind: emailable.FailureUnauthorized, status: 401},
		{name: "402", step: status(402), kind: emailable.FailurePaymentRequired, status: 402},
		{name: "403 refused captcha", step: status(403), kind: emailable.FailureUnauthorized, status: 403},
		{name: "404", step: status(404), kind: emailable.FailureNotFound, status: 404},
		{name: "429", step: status(429), kind: emailable.FailureRateLimited, status: 429},
		{name: "500", step: status(500), kind: emailable.FailureUpstream, status: 500},
		{name: "502", step: status(502), kind: emailable.FailureUpstream, status: 502},
		{name: "503", step: status(503), kind: emailable.FailureUnavailable, status: 503},
		{name: "302", step: status(302), kind: emailable.FailureUnexpectedResponse, status: 302},
		{name: "transport failure", step: testutil.Step{Err: errPendingTransport}, kind: emailable.FailureTransport},
		{name: "SDK timeout", step: pendingBlock(), kind: emailable.FailureTimeout, timeout: true},
	}
}

// TestWaitForVerificationStopsAtAnyOtherOutcome ends a wait, on its first or
// third attempt, with every outcome other than HTTP 249: the helper returns
// that attempt's own error at once, the error VerifyEmail would return, with
// Attempts counting every request, and never repeats after it, an ambiguous
// failure included.
func TestWaitForVerificationStopsAtAnyOtherOutcome(t *testing.T) {
	const interval, sdkTimeout = 2 * time.Second, 5 * time.Second
	opts := []emailable.Option{emailable.WithTimeout(sdkTimeout)}
	for _, f := range pendingFailures() {
		for _, n := range []int{1, 3} {
			t.Run(f.name+"/attempt "+strconv.Itoa(n), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					direct := newTransportClient(t, testutil.NewScript(f.step), opts...)
					_, want := direct.VerifyEmail(t.Context(), verifyRequest())
					require.Error(t, want)

					h := pendingStart(t, opts, pendingSteps(pendingRepeat(n-1, pendingAnswer()), []testutil.Step{f.step})...)
					run := h.wait(t.Context(), t, waitRequest(interval, 30, emailable.MaxPendingWait))
					ce := assertCallError(t, run.err, wantCall{
						Kind: f.kind, Op: emailable.OpVerifyEmail, Status: f.status, Attempts: n, Field: f.field,
						OutcomeUnknown: outcomeUnknownAfter(true, f.kind),
					})
					wantCE := callError(t, want)
					assert.Equal(t, 1, wantCE.Attempts)
					assert.Equal(t, wantCE.Kind, ce.Kind, "the same failure as VerifyEmail's")
					assert.Equal(t, wantCE.StatusCode, ce.StatusCode)
					assert.Equal(t, wantCE.OutcomeUnknown, ce.OutcomeUnknown)
					assert.Equal(t, wantCE.Field, ce.Field)
					assert.Equal(t, errors.Unwrap(want), errors.Unwrap(run.err), "the same fixed cause")
					assert.Equal(t, n, run.calls, "never repeated after a failure")
					wantElapsed := time.Duration(n-1) * interval
					if f.timeout {
						wantElapsed += sdkTimeout
					}
					assert.Equal(t, wantElapsed, run.elapsed)
				})
			})
		}
	}
}

// TestWaitForVerificationInvalidSendsNothing checks the helper's own rules,
// their order, and that the verify request is validated first. Nothing is
// sent, and no bubble is needed: nothing waits.
func TestWaitForVerificationInvalidSendsNothing(t *testing.T) {
	c, ct := newOfflineClient(t, emailable.WithTimeout(10*time.Second))
	with := func(edit func(*emailable.WaitForVerificationRequest)) emailable.WaitForVerificationRequest {
		r := waitRequest(0, 3, time.Minute)
		edit(&r)
		return r
	}
	interval := func(d time.Duration) emailable.WaitForVerificationRequest {
		return with(func(r *emailable.WaitForVerificationRequest) { r.Interval = d })
	}
	attempts := func(n int) emailable.WaitForVerificationRequest {
		return with(func(r *emailable.WaitForVerificationRequest) { r.MaxAttempts = n })
	}
	maxWait := func(d time.Duration) emailable.WaitForVerificationRequest {
		return with(func(r *emailable.WaitForVerificationRequest) { r.MaxWait = d })
	}
	verify := func(v emailable.VerifyEmailRequest) emailable.WaitForVerificationRequest {
		return with(func(r *emailable.WaitForVerificationRequest) { r.Verify = v })
	}
	tests := []struct {
		name   string
		req    emailable.WaitForVerificationRequest
		field  string
		reason string
	}{
		{"interval 1ns", interval(1), "Interval", reasonOutOfRange},
		{"interval 999ms", interval(999 * time.Millisecond), "Interval", reasonOutOfRange},
		{"interval 30s+1ns", interval(30*time.Second + 1), "Interval", reasonOutOfRange},
		{"interval 31s", interval(31 * time.Second), "Interval", reasonOutOfRange},
		{"interval -1ns", interval(-1), "Interval", reasonOutOfRange},
		{"interval negative", interval(-time.Second), "Interval", reasonOutOfRange},
		{"max attempts zero", attempts(0), "MaxAttempts", reasonRequired},
		{"max attempts 1", attempts(1), "MaxAttempts", reasonOutOfRange},
		{"max attempts 31", attempts(31), "MaxAttempts", reasonOutOfRange},
		{"max attempts negative", attempts(-1), "MaxAttempts", reasonOutOfRange},
		{"max wait zero", maxWait(0), "MaxWait", reasonRequired},
		{"max wait -1ns", maxWait(-1), "MaxWait", reasonOutOfRange},
		{"max wait negative", maxWait(-time.Minute), "MaxWait", reasonOutOfRange},
		{"max wait MaxPendingWait+1ns", maxWait(emailable.MaxPendingWait + 1), "MaxWait", reasonOutOfRange},
		{"max wait 5m", maxWait(5 * time.Minute), "MaxWait", reasonOutOfRange},
		{"max wait equal to the default interval", maxWait(emailable.DefaultPendingInterval), "MaxWait", reasonOutOfRange},
		{"max wait 1ns", maxWait(1), "MaxWait", reasonOutOfRange},
		{
			"max wait below the interval",
			with(func(r *emailable.WaitForVerificationRequest) { r.Interval, r.MaxWait = 5*time.Second, 4*time.Second }),
			"MaxWait", reasonOutOfRange,
		},
		{
			"max wait equal to a 30s interval",
			with(func(r *emailable.WaitForVerificationRequest) { r.Interval, r.MaxWait = 30*time.Second, 30*time.Second }),
			"MaxWait", reasonOutOfRange,
		},
		{"empty email", verify(emailable.VerifyEmailRequest{}), "Verify.Email", reasonRequired},
		{
			"email with surrounding space", verify(emailable.VerifyEmailRequest{Email: " " + testEmail}),
			"Verify.Email", "surrounding_whitespace",
		},
		{
			"provider timeout not whole seconds",
			verify(emailable.VerifyEmailRequest{Email: testEmail, ProviderTimeout: 2500 * time.Millisecond}),
			"Verify.ProviderTimeout", "not_whole_seconds",
		},
		{
			"provider timeout too close to the SDK timeout",
			verify(emailable.VerifyEmailRequest{Email: testEmail, ProviderTimeout: 6 * time.Second}),
			"Verify.ProviderTimeout", "exceeds_client_timeout",
		},
		{
			"captcha with a control character",
			verify(emailable.VerifyEmailRequest{Email: testEmail, CaptchaResponse: "a\x00b"}),
			"Verify.CaptchaResponse", reasonControlCharacter,
		},
		{"zero request", emailable.WaitForVerificationRequest{}, "Verify.Email", reasonRequired},
		{
			"verify request before the budget",
			emailable.WaitForVerificationRequest{Verify: emailable.VerifyEmailRequest{Email: "a\nb"}, Interval: -1},
			"Verify.Email", reasonControlCharacter,
		},
		{
			"interval before max attempts",
			emailable.WaitForVerificationRequest{Verify: verifyRequest(), Interval: -1},
			"Interval", reasonOutOfRange,
		},
		{
			"max attempts before max wait",
			emailable.WaitForVerificationRequest{Verify: verifyRequest(), MaxAttempts: 1, MaxWait: -1},
			"MaxAttempts", reasonOutOfRange,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, err := c.WaitForVerification(t.Context(), tt.req)
			assert.Nil(t, v)
			assertValidation(t, err, emailable.ErrInvalidRequest, tt.field, tt.reason)
		})
	}
	assert.Zero(t, ct.Attempts(), "validation sends nothing")
}

// TestWaitForVerificationAcceptsBoundaryValues runs the smallest and largest
// accepted budgets.
func TestWaitForVerificationAcceptsBoundaryValues(t *testing.T) {
	answerThenResult := []testutil.Step{pendingAnswer()}
	tests := []struct {
		name        string
		req         emailable.WaitForVerificationRequest
		steps       []testutil.Step
		wantCalls   int
		wantElapsed time.Duration
		wantErr     bool // budget_exhausted after wantCalls attempts
	}{
		{
			name: "interval 1s, two attempts", req: waitRequest(time.Second, 2, time.Minute),
			steps: answerThenResult, wantCalls: 2, wantElapsed: time.Second,
		},
		{
			name: "interval 30s, thirty attempts", req: waitRequest(30*time.Second, 30, emailable.MaxPendingWait),
			steps: answerThenResult, wantCalls: 2, wantElapsed: 30 * time.Second,
		},
		{
			name: "MaxWait exactly MaxPendingWait", req: waitRequest(0, 3, emailable.MaxPendingWait),
			steps: answerThenResult, wantCalls: 2, wantElapsed: 2 * time.Second,
		},
		{
			name: "MaxWait just above the interval", req: waitRequest(0, 2, emailable.DefaultPendingInterval+1),
			steps: answerThenResult, wantCalls: 2, wantElapsed: 2 * time.Second,
		},
		{
			name:  "MaxWait just above the interval, and two 249s",
			req:   waitRequest(0, 30, emailable.DefaultPendingInterval+1),
			steps: []testutil.Step{pendingAnswer(), pendingAnswer()}, wantCalls: 2, wantElapsed: 2 * time.Second,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := pendingStart(t, nil, pendingSteps(tt.steps)...)
				run := h.wait(t.Context(), t, tt.req)
				if tt.wantErr {
					_ = assertCallError(t, run.err, wantCall{
						Kind: emailable.FailureBudgetExhausted, Op: emailable.OpVerifyEmail,
						Status: emailable.StatusPending, Attempts: tt.wantCalls,
					})
				} else {
					require.NoError(t, run.err)
					pendingVerified(t, run.v)
				}
				assert.Equal(t, tt.wantCalls, run.calls)
				assert.Equal(t, tt.wantElapsed, run.elapsed)
			})
		})
	}
}

// pendingMessage is the provider message of the 249 numbered n, with the API
// key and the captcha response planted in the last one.
func pendingMessage(n int) string {
	return "still verifying " + strconv.Itoa(n) + " key " + testKey + " captcha " + testCaptcha
}

// pendingNumbered returns the 249 numbered n, with its own message and
// rate-limit headers.
func pendingNumbered(n int) testutil.Step {
	return testutil.Step{
		Status: emailable.StatusPending,
		Header: rateHeader(pendingRateLimit, int64(100-n), int64(60-n)),
		Body:   `{"message":"` + pendingMessage(n) + `"}`,
	}
}

// pendingScrubbed is pendingMessage(n) as the diagnostics report it.
func pendingScrubbed(n int) string {
	return "still verifying " + strconv.Itoa(n) + " key [redacted] captcha [redacted]"
}

// pendingDetailCase is one way a wait ends, and what its error keeps.
type pendingDetailCase struct {
	name        string
	steps       []testutil.Step
	ctx         contextFactory
	want        wantCall
	wantRate    emailable.RateLimit
	wantMessage string // with WithProviderDiagnostics
}

func pendingDetailCases() []pendingDetailCase {
	numbered := []testutil.Step{pendingNumbered(1), pendingNumbered(2), pendingNumbered(3)}
	limited := testutil.Step{
		Status: http.StatusTooManyRequests, Header: rateHeader(pendingRateLimit, 0, 30),
		Body: `{"message":"slow down ` + testKey + `"}`,
	}
	between := func(kind emailable.FailureKind, attempts int) wantCall {
		return wantCall{Kind: kind, Op: emailable.OpVerifyEmail, Status: emailable.StatusPending, Attempts: attempts}
	}
	return []pendingDetailCase{
		{
			name: "budget exhausted", steps: numbered, ctx: expiresAfter(time.Hour),
			want: between(emailable.FailureBudgetExhausted, 3), wantRate: rateLimit(pendingRateLimit, 97, 57), wantMessage: pendingScrubbed(3),
		},
		{
			name: "deadline during the first pause", steps: numbered, ctx: expiresAfter(500 * time.Millisecond),
			want: between(emailable.FailureDeadlineExceeded, 1), wantRate: rateLimit(pendingRateLimit, 99, 59), wantMessage: pendingScrubbed(1),
		},
		{
			name: "canceled during the second pause", steps: numbered, ctx: canceledAfter(1500 * time.Millisecond),
			want: between(emailable.FailureCanceled, 2), wantRate: rateLimit(pendingRateLimit, 98, 58), wantMessage: pendingScrubbed(2),
		},
		{
			name: "stopped by a 429", steps: []testutil.Step{pendingNumbered(1), limited}, ctx: expiresAfter(time.Hour),
			want: wantCall{
				Kind: emailable.FailureRateLimited, Op: emailable.OpVerifyEmail, Status: http.StatusTooManyRequests, Attempts: 2,
			},
			wantRate: rateLimit(pendingRateLimit, 0, 30), wantMessage: "slow down [redacted]",
		},
		{
			name: "canceled during an attempt", steps: []testutil.Step{pendingNumbered(1), pendingBlock()},
			ctx:  canceledAfter(1500 * time.Millisecond),
			want: wantCall{Kind: emailable.FailureCanceled, Op: emailable.OpVerifyEmail, Attempts: 2, OutcomeUnknown: true},
		},
	}
}

// TestWaitForVerificationKeepsTheLastAnswerDetails checks what a wait's error
// keeps. An error between attempts (budget_exhausted, or the context ending
// during a pause) has nothing in flight: it keeps the last 249's status, rate
// limits and, with WithProviderDiagnostics only, its message with the API key
// and the captcha response scrubbed, and its outcome is known. An error from
// an attempt keeps that attempt's own details.
func TestWaitForVerificationKeepsTheLastAnswerDetails(t *testing.T) {
	verify := emailable.VerifyEmailRequest{Email: testEmail, CaptchaResponse: testCaptcha}
	for _, diagnostics := range []bool{false, true} {
		var opts []emailable.Option
		if diagnostics {
			opts = []emailable.Option{emailable.WithProviderDiagnostics()}
		}
		for _, tc := range pendingDetailCases() {
			t.Run("diagnostics "+strconv.FormatBool(diagnostics)+"/"+tc.name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					h := pendingStart(t, opts, pendingSteps(tc.steps)...)
					ctx, cancel := tc.ctx(t.Context())
					defer cancel()
					run := h.wait(ctx, t, emailable.WaitForVerificationRequest{
						Verify: verify, Interval: time.Second, MaxAttempts: 3, MaxWait: time.Minute,
					})
					ce := assertCallError(t, run.err, tc.want, pendingMessage(1), pendingMessage(2), pendingMessage(3))
					assert.Equal(t, tc.wantRate, ce.RateLimit, "the rate limits of the answer the error comes from")
					want := ""
					if diagnostics {
						want = tc.wantMessage
					}
					assert.Equal(t, want, ce.Diagnostics().Message, "the message of the answer the error comes from, scrubbed")
				})
			})
		}
	}
}

// pendingLateContext is a context whose Done channel never closes, but whose
// Err reports err once ended is set. It stands for the instant just after a
// pause in which a deadline has passed but its timer has not yet closed
// Done, so that the pause ends normally: the helper must still look at Err
// before it sends again, and send nothing.
type pendingLateContext struct {
	err   error
	ended atomic.Bool
}

// Deadline reports no deadline.
func (*pendingLateContext) Deadline() (time.Time, bool) { return time.Time{}, false }

// Done returns nil: the channel never closes.
func (*pendingLateContext) Done() <-chan struct{} { return nil }

// Err reports err once the context has ended.
func (c *pendingLateContext) Err() error {
	if c.ended.Load() {
		return c.err
	}
	return nil
}

// Value holds nothing.
func (*pendingLateContext) Value(any) any { return nil }

// TestWaitForVerificationContextDoneAfterAPause ends the caller's context
// while a 249 is answered, so that the pause after it still runs to its end
// and the helper finds the context done only before the next attempt: it
// sends nothing more, and reports the context's failure with the last 249's
// details and a known outcome.
func TestWaitForVerificationContextDoneAfterAPause(t *testing.T) {
	tests := []struct {
		err  error
		kind emailable.FailureKind
	}{
		{context.Canceled, emailable.FailureCanceled},
		{context.DeadlineExceeded, emailable.FailureDeadlineExceeded},
	}
	for _, tt := range tests {
		t.Run(string(tt.kind), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx := &pendingLateContext{err: tt.err}
				ending := pendingNumbered(1)
				ending.Respond = func(r *http.Request) (*http.Response, error) {
					step := pendingNumbered(1)
					resp, err := testutil.Reply(r, step)
					ctx.ended.Store(true)
					return resp, err
				}
				h := pendingStart(t, []emailable.Option{emailable.WithProviderDiagnostics()}, pendingSteps([]testutil.Step{ending})...)
				run := h.wait(ctx, t, emailable.WaitForVerificationRequest{
					Verify:   emailable.VerifyEmailRequest{Email: testEmail, CaptchaResponse: testCaptcha},
					Interval: time.Second, MaxAttempts: 3, MaxWait: time.Minute,
				})
				ce := assertCallError(t, run.err, wantCall{
					Kind: tt.kind, Op: emailable.OpVerifyEmail, Status: emailable.StatusPending, Attempts: 1,
				}, pendingMessage(1))
				assert.Equal(t, 1, run.calls, "nothing is sent once the context is done")
				assert.Equal(t, time.Second, run.elapsed, "the pause ran to its end")
				assert.Equal(t, rateLimit(pendingRateLimit, 99, 59), ce.RateLimit)
				assert.Equal(t, pendingScrubbed(1), ce.Diagnostics().Message)
			})
		})
	}
}

// TestWaitForVerificationNoRequestAfterReturn runs one wait per way of
// ending inside a single bubble and then lets a day pass: no request follows
// any of them, and the bubble ends with no goroutine left.
func TestWaitForVerificationNoRequestAfterReturn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := testutil.NewScript(pendingSteps(
			[]testutil.Step{pendingAnswer(), pendingSuccess()},           // success after one 249
			pendingRepeat(2, pendingAnswer()),                            // budget exhausted
			[]testutil.Step{pendingAnswer()},                             // canceled during a pause
			[]testutil.Step{pendingAnswer(), {Err: errPendingTransport}}, // a failure
		)...)
		c := newTransportClient(t, s)
		req := waitRequest(time.Second, 2, time.Minute)

		_, err := c.WaitForVerification(t.Context(), req)
		require.NoError(t, err)
		_, err = c.WaitForVerification(t.Context(), req)
		require.ErrorIs(t, err, emailable.ErrBudgetExhausted)
		ctx, cancel := canceledAfter(500 * time.Millisecond)(t.Context())
		_, err = c.WaitForVerification(ctx, req)
		cancel()
		require.ErrorIs(t, err, context.Canceled)
		_, err = c.WaitForVerification(t.Context(), req)
		require.ErrorIs(t, err, emailable.ErrTransport)

		calls := s.Calls()
		assert.Equal(t, 7, calls)
		<-time.After(24 * time.Hour)
		synctest.Wait()
		assert.Equal(t, calls, s.Calls(), "nothing is sent after a wait returns")
	})
}
