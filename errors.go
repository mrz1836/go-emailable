package emailable

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Sentinel errors. Match them with errors.Is.
//
// ErrInvalidConfig and ErrInvalidRequest are local: an error matching either
// means no request was sent. Every other sentinel describes a call that was
// attempted, a call that a canceled or expired context stopped before it was
// sent, a callback body that failed to decode, or a helper that stopped short.
var (
	// ErrInvalidConfig reports an invalid NewClient argument or option, or a
	// Client that was not created by NewClient. No request was sent.
	ErrInvalidConfig = errors.New("emailable: invalid configuration")

	// ErrInvalidRequest reports a request that fails local validation, a nil
	// context, or an invalid DecodeBatchCallback argument. No request was
	// sent.
	ErrInvalidRequest = errors.New("emailable: invalid request")

	// ErrPending reports HTTP 249 from VerifyEmail: the provider is still
	// verifying the address and has no result yet. The SDK never repeats the
	// request on its own; see WaitForVerification.
	ErrPending = errors.New("emailable: verification pending")

	// ErrUnauthorized reports HTTP 401 (missing key) or 403 (invalid key, or a
	// refused captcha or public-key request).
	ErrUnauthorized = errors.New("emailable: unauthorized")

	// ErrPaymentRequired reports HTTP 402: the account lacks credits, or
	// auto-refill needs a card charge that did not happen.
	ErrPaymentRequired = errors.New("emailable: payment required")

	// ErrNotFound reports HTTP 404, such as an unknown batch ID.
	ErrNotFound = errors.New("emailable: not found")

	// ErrRateLimited reports HTTP 429. The provider documents 25 requests a
	// second for verify and 5 for batch and account calls (custom for
	// enterprise accounts). The SDK never sleeps or retries; the raw
	// rate-limit headers are in CallError.RateLimit.
	ErrRateLimited = errors.New("emailable: rate limited")

	// ErrRejected reports HTTP 400 or any other 4xx status not named above.
	ErrRejected = errors.New("emailable: rejected by provider")

	// ErrUpstream reports an HTTP 5xx status other than 503.
	ErrUpstream = errors.New("emailable: provider server error")

	// ErrUnavailable reports HTTP 503.
	ErrUnavailable = errors.New("emailable: temporarily unavailable")

	// ErrUnexpectedResponse reports a response this SDK cannot interpret: an
	// HTTP status that is not 200 and not 4xx or 5xx (a redirect, which is
	// never followed, included, and 249 from any operation but VerifyEmail),
	// a body that is not a JSON object, an undecoded Content-Encoding, an
	// invalid token, a batch snapshot with no documented member, or a batch
	// ID other than the one requested.
	ErrUnexpectedResponse = errors.New("emailable: unexpected response")

	// ErrMalformedResponse reports an HTTP 200 body, or a callback body, that
	// is not valid UTF-8 or valid JSON, has data after the top-level value,
	// repeats a key (compared ignoring case, as encoding/json matches keys),
	// lacks a required field, or holds a field of the wrong JSON type or out
	// of range. A CreateBatch answer whose ID could still be read is reported
	// as malformed with that BatchID.
	ErrMalformedResponse = errors.New("emailable: malformed response")

	// ErrResponseTooLarge reports a body larger than the configured limit
	// (WithMaxResponseBytes, or DecodeBatchCallback's maxBytes), than the
	// fixed 1 MiB cap on every operation but GetBatch, or than an SDK safety
	// bound: 100,000 JSON objects and arrays or 1,000,000 values in one body,
	// or one field over its bound.
	ErrResponseTooLarge = errors.New("emailable: response too large")

	// ErrTransport reports a failure in transit, such as DNS, connection, TLS
	// or proxy errors, a reset, a truncated body, a stalled body (100 reads
	// in a row that return nothing), or a callback body whose reader failed.
	// The request may or may not have reached the provider.
	ErrTransport = errors.New("emailable: transport failure")

	// ErrTimeout reports that the SDK's own timeout (WithTimeout) or an
	// injected http.Client's Timeout expired, or that the network reported a
	// timeout. It also matches context.DeadlineExceeded, so check it first to
	// tell it apart from the caller's own deadline.
	ErrTimeout = errors.New("emailable: timeout")

	// ErrBudgetExhausted reports that WaitForVerification still saw HTTP 249
	// when its MaxAttempts or MaxWait ran out.
	ErrBudgetExhausted = errors.New("emailable: budget exhausted")

	// ErrOutcomeUnknown matches a CallError whose OutcomeUnknown is true: a
	// VerifyEmail or CreateBatch request that reached the transport and then
	// failed in a way that leaves open whether the provider processed it,
	// billed it or created a batch (see CallError.OutcomeUnknown). Reconcile
	// before repeating; never resubmit a batch automatically.
	ErrOutcomeUnknown = errors.New("emailable: outcome unknown")
)

// Static causes kept inside a CallError. They replace standard-library
// errors, whose messages can quote URLs, header bytes or response content.
var (
	errBuildRequest     = errors.New("emailable: cannot build the HTTP request")
	errStatus           = errors.New("emailable: unexpected HTTP status")
	errPendingStatus    = errors.New("emailable: HTTP 249: verification still pending")
	errResponseTooLarge = errors.New("emailable: response body exceeds the size limit")
	errContentEncoding  = errors.New("emailable: response has an undecoded content encoding")
	errInvalidUTF8      = errors.New("emailable: response is not valid UTF-8")
	errInvalidJSON      = errors.New("emailable: response is not valid JSON")
	errTrailingData     = errors.New("emailable: response has data after the top-level value")
	errDuplicateKey     = errors.New("emailable: response repeats an object key")
	errNotObject        = errors.New("emailable: response is not a JSON object")
	errTooManyValues    = errors.New("emailable: response has too many JSON values")
	errMissingField     = errors.New("emailable: response lacks a required field")
	errWrongType        = errors.New("emailable: response field has the wrong JSON type")
	errOutOfRange       = errors.New("emailable: response field is out of range")
	errInvalidToken     = errors.New("emailable: response field is not a valid token")
	errFieldTooLarge    = errors.New("emailable: response field exceeds its size bound")
	errNoMembers        = errors.New("emailable: response has no documented member")
	errIDMismatch       = errors.New("emailable: response is for another batch")
	errWaitBudget       = errors.New("emailable: still pending when the wait budget ran out")
	errCallbackRead     = errors.New("emailable: callback body could not be read")

	// Transport classes. The original error is discarded, because standard
	// library errors can quote URLs, proxy credentials or server bytes.
	errTransportDNS     = errors.New("emailable: transport: DNS lookup failed")
	errTransportProxy   = errors.New("emailable: transport: proxy connection failed")
	errTransportDial    = errors.New("emailable: transport: connection failed")
	errTransportTLS     = errors.New("emailable: transport: TLS failed")
	errTransportReset   = errors.New("emailable: transport: connection reset")
	errTransportClosed  = errors.New("emailable: transport: connection closed early")
	errTransportStalled = errors.New("emailable: transport: body read made no progress")
	errTransportOther   = errors.New("emailable: transport: request failed")
)

// FailureKind classifies a failed call. Each kind matches one target with
// errors.Is (a sentinel of this package, context.Canceled or
// context.DeadlineExceeded), except FailureTimeout, which matches both
// ErrTimeout and context.DeadlineExceeded. ErrOutcomeUnknown is not a kind: it
// matches through CallError.OutcomeUnknown. The values are short, fixed and
// free of personal data, so they are safe to use as metric labels.
type FailureKind string

// Failure kinds.
const (
	// FailurePending matches ErrPending.
	FailurePending FailureKind = "pending"

	// FailureUnauthorized matches ErrUnauthorized.
	FailureUnauthorized FailureKind = "unauthorized"

	// FailurePaymentRequired matches ErrPaymentRequired.
	FailurePaymentRequired FailureKind = "payment_required"

	// FailureNotFound matches ErrNotFound.
	FailureNotFound FailureKind = "not_found"

	// FailureRateLimited matches ErrRateLimited.
	FailureRateLimited FailureKind = "rate_limited"

	// FailureRejected matches ErrRejected.
	FailureRejected FailureKind = "rejected"

	// FailureUpstream matches ErrUpstream.
	FailureUpstream FailureKind = "upstream"

	// FailureUnavailable matches ErrUnavailable.
	FailureUnavailable FailureKind = "unavailable"

	// FailureUnexpectedResponse matches ErrUnexpectedResponse.
	FailureUnexpectedResponse FailureKind = "unexpected_response"

	// FailureMalformedResponse matches ErrMalformedResponse.
	FailureMalformedResponse FailureKind = "malformed_response"

	// FailureResponseTooLarge matches ErrResponseTooLarge.
	FailureResponseTooLarge FailureKind = "response_too_large"

	// FailureTransport matches ErrTransport.
	FailureTransport FailureKind = "transport"

	// FailureTimeout matches ErrTimeout, and also context.DeadlineExceeded.
	FailureTimeout FailureKind = "timeout"

	// FailureCanceled means the caller's context was canceled. It matches
	// context.Canceled.
	FailureCanceled FailureKind = "canceled"

	// FailureDeadlineExceeded means the caller's context deadline expired. It
	// matches context.DeadlineExceeded, but not ErrTimeout.
	FailureDeadlineExceeded FailureKind = "deadline_exceeded"

	// FailureBudgetExhausted matches ErrBudgetExhausted.
	FailureBudgetExhausted FailureKind = "budget_exhausted"
)

// Operation names the operation a CallError concerns.
type Operation string

// Operations.
const (
	// OpVerifyEmail is GET /v1/verify (VerifyEmail and WaitForVerification).
	OpVerifyEmail Operation = "verify_email"

	// OpCreateBatch is POST /v1/batch (CreateBatch).
	OpCreateBatch Operation = "create_batch"

	// OpGetBatch is GET /v1/batch (GetBatch).
	OpGetBatch Operation = "get_batch"

	// OpAccountInfo is GET /v1/account (AccountInfo).
	OpAccountInfo Operation = "account_info"

	// OpBatchCallback is DecodeBatchCallback, which makes no request.
	OpBatchCallback Operation = "batch_callback"
)

// ValidationError describes a local validation failure: nothing was sent. Err
// is ErrInvalidConfig or ErrInvalidRequest. Field names the argument, option or
// request field as a Go path, such as "Emails[17]" or "Timeout", and Reason is
// a short fixed code, such as "control_character". Neither ever contains the
// rejected value.
type ValidationError struct {
	Err    error
	Field  string
	Reason string
}

// Error returns the sentinel's message followed by the field and the reason,
// such as "emailable: invalid request: Email: control_character".
func (e *ValidationError) Error() string {
	if e == nil {
		return "emailable: <nil ValidationError>"
	}
	var b strings.Builder
	if e.Err != nil {
		b.WriteString(e.Err.Error())
	} else {
		b.WriteString("emailable: validation failed")
	}
	for _, part := range [...]string{e.Field, e.Reason} {
		if part != "" {
			b.WriteString(": ")
			b.WriteString(part)
		}
	}
	return b.String()
}

// Unwrap returns the sentinel, ErrInvalidConfig or ErrInvalidRequest.
func (e *ValidationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// CallError describes a call that was attempted, a call that a canceled or
// expired context stopped before it was sent, a callback body that failed to
// decode, or a WaitForVerification that stopped short.
//
// Its Error text is built only from Op, Kind, StatusCode and Field: it never
// holds the API key, email addresses, captcha responses, callback or download
// URLs, header values, response bodies or provider messages, so it is safe to
// log. GoString and every fmt verb add only the other exported fields. The
// cause and the provider diagnostics sit behind a function value that fmt and
// reflection-based dumpers print only as an address.
//
// Tests and fakes may build a CallError from its exported fields alone; it
// then matches errors.Is like one the SDK returns.
type CallError struct {
	// Kind classifies the failure.
	Kind FailureKind

	// Op is the operation concerned.
	Op Operation

	// StatusCode is the last HTTP status received, or 0 when none was.
	StatusCode int

	// OutcomeUnknown reports that a VerifyEmail or CreateBatch request
	// reached the transport and then failed in a way that leaves open
	// whether the provider processed it, billed it or created a batch: a
	// timeout, a cancellation after sending, a transport failure (any class,
	// even one that may have happened before anything was written), a 5xx,
	// an unexpected status (249 from CreateBatch included) or an unreadable
	// 200 answer. It is never set for a 4xx, for VerifyEmail's HTTP 249, for
	// a CreateBatch answer whose batch ID could be read, or for a free read.
	// It drives errors.Is(err, ErrOutcomeUnknown).
	OutcomeUnknown bool

	// BatchID is the batch concerned when it is known: the requested ID for
	// GetBatch, and the ID read from the body for CreateBatch and
	// DecodeBatchCallback when one could be read.
	BatchID string

	// Attempts is the number of HTTP requests made: 0 when nothing was sent,
	// 1 for a single call, and up to MaxAttempts for WaitForVerification.
	Attempts int

	// Field is a JSON path, such as "emails[17].score", for type and shape
	// failures. It names a location, never a value.
	Field string

	// RateLimit holds the raw rate-limit headers of the last error response.
	RateLimit RateLimit

	// private holds the sanitized cause and the provider diagnostics.
	private func() callDetail
}

// callDetail is the part of a CallError that is never printed.
type callDetail struct {
	cause       error
	diagnostics ProviderDiagnostics
}

// ProviderDiagnostics holds provider text about a failed call. It is empty
// unless the Client was created with WithProviderDiagnostics.
type ProviderDiagnostics struct {
	// Message is the response body's "message", cut to at most 1 KiB on a
	// UTF-8 boundary, with every occurrence of the API key, the request's
	// captcha response, its callback URL and that URL's query values of 8
	// bytes or more, as written or query-escaped, replaced by "[redacted]".
	// Other spellings of them are not recognized, and it may echo request
	// data, such as an email address.
	Message string
}

// RateLimit holds the provider's rate-limit headers (ratelimit-limit,
// ratelimit-remaining and ratelimit-reset) as raw integers. A field is nil
// when its header is absent, repeated, or not 1 to 19 decimal digits that fit
// an int64. The provider documents Reset as a "timestamp, in seconds" without
// saying whether it is an epoch time or a delay, so the SDK never converts it,
// sleeps on it or retries.
type RateLimit struct {
	Limit     *int64
	Remaining *int64
	Reset     *int64
}

// newCallError returns a CallError without diagnostics whose cause is
// reachable only through Unwrap.
func newCallError(op Operation, kind FailureKind, status int, cause error) *CallError {
	return newCallErrorWithDiagnostics(op, kind, status, cause, ProviderDiagnostics{})
}

// newCallErrorWithDiagnostics returns a CallError whose cause and diagnostics
// are reachable only through Unwrap and Diagnostics.
func newCallErrorWithDiagnostics(op Operation, kind FailureKind, status int, cause error,
	diag ProviderDiagnostics,
) *CallError {
	d := callDetail{cause: cause, diagnostics: diag}
	return &CallError{Kind: kind, Op: op, StatusCode: status, private: func() callDetail { return d }}
}

// Error returns "emailable: <op>: <kind>", followed by " (HTTP <status>)" when
// a status is known and ": <field>" when a field is named, such as
// "emailable: verify_email: malformed_response (HTTP 200): score".
func (e *CallError) Error() string {
	if e == nil {
		return "emailable: <nil CallError>"
	}
	var b strings.Builder
	b.WriteString("emailable: ")
	if e.Op != "" {
		b.WriteString(string(e.Op))
		b.WriteString(": ")
	}
	if e.Kind != "" {
		b.WriteString(string(e.Kind))
	} else {
		b.WriteString("call_failed")
	}
	if e.StatusCode != 0 {
		b.WriteString(" (HTTP ")
		b.WriteString(strconv.Itoa(e.StatusCode))
		b.WriteString(")")
	}
	if e.Field != "" {
		b.WriteString(": ")
		b.WriteString(e.Field)
	}
	return b.String()
}

// Unwrap returns the sanitized cause: a context error or a fixed error of this
// package. It is never a *url.Error, a JSON decoding error or anything else
// that could quote a URL, a header or response content. It is nil for a
// CallError built from its exported fields.
func (e *CallError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.detail().cause
}

// Is reports whether target is the sentinel for e.Kind, or ErrOutcomeUnknown
// when e.OutcomeUnknown is set. A timeout also matches
// context.DeadlineExceeded, a caller deadline matches context.DeadlineExceeded
// and a cancellation matches context.Canceled.
func (e *CallError) Is(target error) bool {
	if e == nil {
		return false
	}
	switch target {
	case ErrOutcomeUnknown:
		return e.OutcomeUnknown
	case context.Canceled:
		return e.Kind == FailureCanceled
	case context.DeadlineExceeded:
		return e.Kind == FailureDeadlineExceeded || e.Kind == FailureTimeout
	}
	sentinel := kindSentinel(e.Kind)
	return sentinel != nil && target == sentinel
}

// GoString prints the exported metadata only, so that %#v cannot reach the
// cause or the provider diagnostics.
func (e *CallError) GoString() string {
	if e == nil {
		return "(*emailable.CallError)(nil)"
	}
	return fmt.Sprintf("&emailable.CallError{Kind:%q, Op:%q, StatusCode:%d, OutcomeUnknown:%t, BatchID:%q, "+
		"Attempts:%d, Field:%q, RateLimit:%s}",
		string(e.Kind), string(e.Op), e.StatusCode, e.OutcomeUnknown, e.BatchID,
		e.Attempts, e.Field, e.RateLimit.goString())
}

// Diagnostics returns the provider's message for this failure. It is empty
// unless the Client was created with WithProviderDiagnostics. Provider text
// may echo request data, such as an email address: inspect it, but think
// before logging or storing it.
func (e *CallError) Diagnostics() ProviderDiagnostics {
	if e == nil {
		return ProviderDiagnostics{}
	}
	return e.detail().diagnostics
}

func (e *CallError) detail() callDetail {
	if e.private == nil {
		return callDetail{}
	}
	return e.private()
}

// goString prints the rate limits as integers, or nil when absent.
func (r RateLimit) goString() string {
	field := func(p *int64) string {
		if p == nil {
			return "nil"
		}
		return strconv.FormatInt(*p, 10)
	}
	return "emailable.RateLimit{Limit:" + field(r.Limit) + ", Remaining:" + field(r.Remaining) +
		", Reset:" + field(r.Reset) + "}"
}

// kindSentinel returns the sentinel a kind matches, or nil for the context
// kinds and any kind this package does not define.
func kindSentinel(kind FailureKind) error {
	switch kind {
	case FailurePending:
		return ErrPending
	case FailureUnauthorized:
		return ErrUnauthorized
	case FailurePaymentRequired:
		return ErrPaymentRequired
	case FailureNotFound:
		return ErrNotFound
	case FailureRateLimited:
		return ErrRateLimited
	case FailureRejected:
		return ErrRejected
	case FailureUpstream:
		return ErrUpstream
	case FailureUnavailable:
		return ErrUnavailable
	case FailureUnexpectedResponse:
		return ErrUnexpectedResponse
	case FailureMalformedResponse:
		return ErrMalformedResponse
	case FailureResponseTooLarge:
		return ErrResponseTooLarge
	case FailureTransport:
		return ErrTransport
	case FailureTimeout:
		return ErrTimeout
	case FailureBudgetExhausted:
		return ErrBudgetExhausted
	case FailureCanceled, FailureDeadlineExceeded:
		return nil
	}
	return nil
}

// contextFailure returns the CallError for a caller context that is done, with
// the context's error as its cause.
func contextFailure(op Operation, err error, status int) *CallError {
	kind, cause := contextKind(err)
	return newCallError(op, kind, status, cause)
}

// contextKind returns the kind and the cause of a done context's error.
func contextKind(err error) (FailureKind, error) {
	if errors.Is(err, context.DeadlineExceeded) {
		return FailureDeadlineExceeded, context.DeadlineExceeded
	}
	return FailureCanceled, context.Canceled
}

// Validation reasons, reported in ValidationError.Reason.
const (
	reasonRequired             = "required"
	reasonNil                  = "nil"
	reasonNotInitialized       = "not_initialized"
	reasonOutOfRange           = "out_of_range"
	reasonInvalidURL           = "invalid_url"
	reasonInvalidFormat        = "invalid_format"
	reasonInvalidUTF8          = "invalid_utf8"
	reasonControlCharacter     = "control_character"
	reasonSurroundingSpace     = "surrounding_whitespace"
	reasonTooLong              = "too_long"
	reasonNotWholeSeconds      = "not_whole_seconds"
	reasonExceedsClientTimeout = "exceeds_client_timeout"
	reasonTooMany              = "too_many"
	reasonDuplicate            = "duplicate"
	reasonTooLarge             = "too_large"
	reasonUnsupportedScheme    = "unsupported_scheme"
	reasonContainsComma        = "contains_comma"
	reasonUnknownValue         = "unknown_value"
	reasonWrongOperation       = "wrong_operation"
)

// invalidConfig returns a *ValidationError matching ErrInvalidConfig.
func invalidConfig(field, reason string) error {
	return &ValidationError{Err: ErrInvalidConfig, Field: field, Reason: reason}
}

// invalidRequest returns a *ValidationError matching ErrInvalidRequest.
func invalidRequest(field, reason string) error {
	return &ValidationError{Err: ErrInvalidRequest, Field: field, Reason: reason}
}
