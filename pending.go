package emailable

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// WaitForVerification bounds.
const (
	minPendingInterval = time.Second
	maxPendingInterval = 30 * time.Second
	minPendingAttempts = 2
	maxPendingAttempts = 30
)

// WaitForVerificationRequest is one WaitForVerification call.
type WaitForVerificationRequest struct {
	// Verify is the verification to make. It is validated and encoded once:
	// every attempt sends a byte-identical request.
	Verify VerifyEmailRequest

	// Interval is the pause after each HTTP 249 before the next attempt: 0
	// means DefaultPendingInterval, otherwise 1 second to 30 seconds.
	Interval time.Duration

	// MaxAttempts is the required total number of requests, the first
	// included: 2 to 30.
	MaxAttempts int

	// MaxWait is the required elapsed-time budget, more than the effective
	// Interval and at most MaxPendingWait, counted locally from just before
	// the first attempt. No attempt starts unless it can start, after its
	// pause, before MaxWait ends; an attempt already running is never cut
	// off by it.
	MaxWait time.Duration
}

// String describes the request's shape only, such as
// "emailable.WaitForVerificationRequest{verify: emailable.VerifyEmailRequest{...},
// interval: 2s, maxAttempts: 6, maxWait: 1m30s}": never the address or the
// captcha response.
func (r WaitForVerificationRequest) String() string {
	interval := "default"
	if r.Interval != 0 {
		interval = r.Interval.String()
	}
	return describe("emailable.WaitForVerificationRequest",
		field{"verify", r.Verify.String()},
		field{"interval", interval},
		field{"maxAttempts", strconv.Itoa(r.MaxAttempts)},
		field{"maxWait", r.MaxWait.String()},
	)
}

// GoString returns the same text as String, so that %#v prints the shape only.
func (r WaitForVerificationRequest) GoString() string { return r.String() }

// Format prints the same text as String for every fmt verb (quoted for %q),
// so that no verb reaches the address or the captcha response of Verify by
// reflection, which a value held in a slice, map or struct would otherwise
// allow. fmt cannot call it for %p on a value, nor for a request held in an
// unexported struct field: keep requests out of formatted output there.
func (r WaitForVerificationRequest) Format(f fmt.State, verb rune) { formatAs(f, verb, r.String()) }

// WaitForVerification verifies one address as VerifyEmail does, and when the
// provider answers HTTP 249 (still verifying) it pauses Interval and sends
// the identical request again, within MaxAttempts and MaxWait. Call it
// instead of VerifyEmail, never after one: it cannot know when an earlier
// request was sent.
//
// PAID: every attempt is a request. The provider documents a repeat within
// five minutes of a 249 as free; MaxPendingWait keeps the whole wait a minute
// inside that window, measured by the local clock, which cannot prove when
// the provider accepted a request. It returns:
//   - (result, nil) as soon as an attempt succeeds;
//   - (nil, *CallError{Kind: budget_exhausted}) when the last allowed attempt
//     still saw 249, or the next one could not start before MaxWait ends;
//   - (nil, a canceled or deadline_exceeded *CallError) when ctx ends between
//     attempts; the attempt before it ended with 249, so nothing is in
//     flight and the outcome is known;
//   - (nil, err) for any other outcome of an attempt, which ends the wait at
//     once: it never repeats after a failure, an ambiguous one included.
//
// Attempts is always the number of requests made. An error from an attempt
// keeps that attempt's StatusCode, RateLimit and Diagnostics; an error
// between attempts (budget_exhausted, or ctx ending) carries those of the
// last 249.
//
// Between attempts it only waits on a timer that ctx can end, never sleeps.
// The worst-case wall time is about MaxWait plus one WithTimeout, because an
// attempt in flight is never cut short: that would turn a paid call into an
// unknown outcome. A captcha response is likely single-use, so a repeat may
// be refused with HTTP 403, which ends the wait.
//
// Errors are a *ValidationError (nothing sent; a Verify field is named as
// "Verify.Email" and so on) or a *CallError with Op verify_email.
func (c *Client) WaitForVerification(ctx context.Context, req WaitForVerificationRequest) (*Verification, error) {
	if err := c.prepare(ctx); err != nil {
		return nil, err
	}
	r, err := req.Verify.request(c)
	if err != nil {
		if ve, ok := errors.AsType[*ValidationError](err); ok {
			ve.Field = "Verify." + ve.Field
		}
		return nil, err
	}
	interval, err := req.validate()
	if err != nil {
		return nil, err
	}
	start := time.Now()
	var last *CallError // the last 249
	for attempt := 1; ; attempt++ {
		v, cerr := c.verify(ctx, r)
		switch {
		case cerr == nil:
			return v, nil
		case cerr.Attempts == 0 && last != nil:
			// ctx ended right after a pause: nothing more was sent.
			return nil, between(last, cerr.Kind, cerr.Unwrap())
		case cerr.Attempts == 0:
			return nil, cerr
		}
		cerr.Attempts = attempt
		if cerr.Kind != FailurePending {
			return nil, cerr
		}
		last = cerr
		if attempt >= req.MaxAttempts || time.Since(start)+interval >= req.MaxWait {
			return nil, between(last, FailureBudgetExhausted, errWaitBudget)
		}
		if err := pause(ctx, interval); err != nil {
			kind, cause := contextKind(err)
			return nil, between(last, kind, cause)
		}
		if time.Since(start) >= req.MaxWait {
			// The timer fired late: the next attempt would start past MaxWait.
			return nil, between(last, FailureBudgetExhausted, errWaitBudget)
		}
	}
}

// validate checks the request's budget and returns the effective interval.
// MaxWait must exceed the interval, or no repeat could ever start.
func (r WaitForVerificationRequest) validate() (time.Duration, error) {
	interval := r.Interval
	switch {
	case interval == 0:
		interval = DefaultPendingInterval
	case interval < minPendingInterval || interval > maxPendingInterval:
		return 0, invalidRequest("Interval", reasonOutOfRange)
	}
	switch {
	case r.MaxAttempts == 0:
		return 0, invalidRequest("MaxAttempts", reasonRequired)
	case r.MaxAttempts < minPendingAttempts || r.MaxAttempts > maxPendingAttempts:
		return 0, invalidRequest("MaxAttempts", reasonOutOfRange)
	}
	switch {
	case r.MaxWait == 0:
		return 0, invalidRequest("MaxWait", reasonRequired)
	case r.MaxWait <= interval || r.MaxWait > MaxPendingWait:
		return 0, invalidRequest("MaxWait", reasonOutOfRange)
	}
	return interval, nil
}

// between returns the CallError of kind for a wait that ended between
// attempts, after last, the most recent 249: it keeps that answer's status,
// attempts, rate limits and diagnostics, because nothing was in flight.
func between(last *CallError, kind FailureKind, cause error) *CallError {
	ce := newCallErrorWithDiagnostics(OpVerifyEmail, kind, last.StatusCode, cause, last.Diagnostics())
	ce.Attempts = last.Attempts
	ce.RateLimit = last.RateLimit
	return ce
}

// pause waits d, or returns the error of ctx when it ends first. It uses a
// timer it stops, never time.Sleep, so a cancellation ends it at once.
func pause(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
