package emailable

import (
	"context"
	"net/http"
	"strconv"
	"strings"
)

// Rate-limit header names, compared ignoring case.
const (
	headerRateLimit     = "ratelimit-limit"
	headerRateRemaining = "ratelimit-remaining"
	headerRateReset     = "ratelimit-reset"
	maxHeaderDigits     = 19
)

// call makes the one attempt of r and returns the body of its HTTP 200
// answer, which checkStrict has accepted. Every failure is a *CallError: the
// caller's context, the SDK's timeout and transport failures first, then an
// HTTP status other than 200, then a body that is not strict JSON. For that
// last failure body is returned too, so that CreateBatch can still look for
// the new batch's ID.
func (c *Client) call(ctx context.Context, r *request) ([]byte, *CallError) {
	ex, cerr := c.exchange(ctx, r)
	if cerr != nil {
		return nil, cerr
	}
	if ex.status != http.StatusOK {
		return nil, r.sent(c.statusFailure(r, ex))
	}
	if kind, cause := checkStrict(ex.body); cause != nil {
		return ex.body, r.sent(newCallError(r.op, kind, ex.status, cause))
	}
	return ex.body, nil
}

// decodeFailure returns the CallError for an HTTP 200 answer to r that
// decoding refused.
func (r *request) decodeFailure(derr *decodeError) *CallError {
	return r.sent(derr.callError(r.op))
}

// statusFailure returns the CallError for an HTTP status other than 200. The
// status alone decides the kind; the body, when diagnostics are enabled, only
// supplies the provider's message.
func (c *Client) statusFailure(r *request, ex *exchange) *CallError {
	kind := statusKind(r.op, ex.status)
	cause := errStatus
	if kind == FailurePending {
		cause = errPendingStatus
	}
	ce := newCallErrorWithDiagnostics(r.op, kind, ex.status, cause, c.diagnosticsFrom(ex.body, r.secrets))
	ce.RateLimit = ex.rateLimit
	return ce
}

// statusKind classifies an HTTP status other than 200. HTTP 249 means a
// pending verification only from VerifyEmail; from any other operation it is
// unexpected.
func statusKind(op Operation, code int) FailureKind {
	switch {
	case code == StatusPending && op == OpVerifyEmail:
		return FailurePending
	case code == http.StatusUnauthorized || code == http.StatusForbidden:
		return FailureUnauthorized
	case code == http.StatusPaymentRequired:
		return FailurePaymentRequired
	case code == http.StatusNotFound:
		return FailureNotFound
	case code == http.StatusTooManyRequests:
		return FailureRateLimited
	case code >= 400 && code < 500:
		return FailureRejected
	case code == http.StatusServiceUnavailable:
		return FailureUnavailable
	case code >= 500 && code < 600:
		return FailureUpstream
	}
	return FailureUnexpectedResponse
}

// diagnosticsFrom returns the provider's message from an error body when
// diagnostics are enabled and the body is a strict JSON object whose "message"
// is a string, with the API key and the call's secrets scrubbed, cut to
// 1 KiB. Anything else yields no message; it never fails the call.
func (c *Client) diagnosticsFrom(body []byte, secrets []string) ProviderDiagnostics {
	if !c.diagnostics || body == nil {
		return ProviderDiagnostics{}
	}
	if kind, _ := checkStrict(body); kind != "" {
		return ProviderDiagnostics{}
	}
	o := readObject(body, []string{"message"})
	raw := o.fields[0]
	if !isString(raw) {
		return ProviderDiagnostics{}
	}
	return ProviderDiagnostics{Message: truncateUTF8(c.scrub(decodeKey(raw), secrets), maxDiagnosticMessageBytes)}
}

// parseRateLimit reads the rate-limit headers of a response.
func parseRateLimit(h http.Header) RateLimit {
	return RateLimit{
		Limit:     headerInt(h, headerRateLimit),
		Remaining: headerInt(h, headerRateRemaining),
		Reset:     headerInt(h, headerRateReset),
	}
}

// headerInt returns the value of the header name, matched ignoring case so
// that the non-canonical keys an injected transport may set count too, when
// it appears exactly once and is 1 to 19 decimal digits that fit an int64.
// Anything else, a repeated or ambiguous header included, is nil.
func headerInt(h http.Header, name string) *int64 {
	var value string
	n := 0
	for k, vs := range h {
		if strings.EqualFold(k, name) {
			n += len(vs)
			if len(vs) > 0 {
				value = vs[0]
			}
		}
	}
	if n != 1 || value == "" || len(value) > maxHeaderDigits || !asciiRange(value, '0', '9') {
		return nil
	}
	v, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return nil
	}
	return &v
}
