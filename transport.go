package emailable

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"syscall"
)

// Transport constants.
const (
	// userAgent is the User-Agent header of every call.
	userAgent = "go-emailable"

	// maxErrorBodyBytes bounds the excerpt read from a response whose status
	// is not 200, which only feeds the opt-in diagnostics.
	maxErrorBodyBytes = 64 << 10

	// maxResponseHeaderBytes bounds the response headers on the transport the
	// SDK builds itself.
	maxResponseHeaderBytes = 64 << 10

	// maxPrealloc bounds the buffer sized from a declared length before any
	// byte arrives, so that a response that declares a large body and then
	// stalls cannot pin that much memory until the timeout.
	maxPrealloc = 1 << 20

	// maxEmptyReads is how many reads in a row may return nothing before a
	// body counts as stalled.
	maxEmptyReads = 100

	// redacted replaces secrets in provider text and formatted values.
	redacted = "[redacted]"
)

// request is the one HTTP attempt of an operation.
type request struct {
	op     Operation
	method string
	path   string // under apiRoot, such as "verify"
	query  string // already encoded
	body   []byte // POST only

	// paid marks an operation that may be billed or change state, so that a
	// failure after sending leaves its outcome unknown.
	paid bool

	// secrets are per-call values scrubbed from provider diagnostics, such
	// as a captcha response or a callback URL.
	secrets []string
}

// sender makes the one attempt of a call: it builds the request for r on the
// configured host and sends it. Everything it needs, the API key included, is
// captured inside the function: fmt prints a func value only as an address,
// and reflection-based dumpers cannot see what a closure captures. An error is
// errBuildRequest or the http.Client's own error, which the caller classifies
// and drops.
type sender func(ctx context.Context, r *request) (*http.Response, error)

// newSender returns the sender for a validated configuration.
func newSender(hc *http.Client, host, authorization string) sender {
	return func(ctx context.Context, r *request) (*http.Response, error) {
		u := url.URL{Scheme: "https", Host: host, Path: apiRoot + r.path, RawQuery: r.query}
		req, err := http.NewRequestWithContext(ctx, r.method, u.String(), nil)
		if err != nil {
			// The standard library's error quotes the URL.
			return nil, errBuildRequest
		}
		// The standard library resends a request on its own when it thinks
		// that is safe: HTTP/1 retries a request with a nil body, http.NoBody
		// or a GetBody on a reused connection that fails, even after the
		// server has read it, and the HTTP/2 transport resends such requests
		// after some stream and connection errors. Every request therefore
		// carries a body that is neither nil nor http.NoBody, with GetBody
		// nil: a POST its bytes, a GET an empty body. Every call is then one
		// attempt, and a 307 or 308 cannot be followed either.
		//
		// A GET's TransferEncoding is "identity", so that HTTP/1 sends it
		// without Content-Length, Transfer-Encoding or body bytes, without
		// first probing the body against a timer (which, if late, would send
		// an empty chunked body). HTTP/2 ignores the field and ends the
		// stream with an empty DATA frame.
		if r.method == http.MethodPost {
			req.Body = io.NopCloser(bytes.NewReader(r.body))
			req.ContentLength = int64(len(r.body))
		} else {
			req.Body = noReplayBody{}
			req.ContentLength = 0
			req.TransferEncoding = []string{"identity"}
		}
		req.GetBody = nil
		req.Header = requestHeader(r.method == http.MethodPost, authorization)
		return hc.Do(req)
	}
}

// noReplayBody is the body of every GET: always empty, and neither nil nor
// http.NoBody, which the standard library would treat as safe to resend. It
// holds no state, so one value serves every request.
type noReplayBody struct{}

// Read reports the end of the empty body.
func (noReplayBody) Read([]byte) (int, error) { return 0, io.EOF }

// Close does nothing.
func (noReplayBody) Close() error { return nil }

// requestHeader returns a fresh header map for one call. The values share one
// backing array, allocated per call so that no two requests share header
// memory; each is a full slice expression, so an append to one cannot
// overwrite the next.
//
// It never sets Cookie or an idempotency key: with an Idempotency-Key header,
// the standard library may replay a POST. Accept-Encoding is left to the
// transport, which then decodes gzip itself.
func requestHeader(post bool, authorization string) http.Header {
	v := append(make([]string, 0, 4), "application/json", userAgent, authorization, "application/json")
	h := make(http.Header, 4)
	h["Accept"] = v[0:1:1]
	h["User-Agent"] = v[1:2:2]
	h["Authorization"] = v[2:3:3]
	if post {
		h["Content-Type"] = v[3:4:4]
	}
	return h
}

// exchange is a received HTTP response. For status 200, body is the whole
// body, within the size limit. For any other status, body is the body if
// diagnostics are enabled and it could be read within maxErrorBodyBytes, else
// nil, and rateLimit holds the response's rate-limit headers.
type exchange struct {
	status    int
	body      []byte
	rateLimit RateLimit
}

// exchange makes the one attempt of r and reads its response, all within the
// Client's timeout. A context that is already done sends nothing. A failure
// after the request was handed to the transport is marked as sent.
func (c *Client) exchange(ctx context.Context, r *request) (*exchange, *CallError) {
	if err := ctx.Err(); err != nil {
		return nil, contextFailure(r.op, err, 0)
	}
	callCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	resp, release, cerr := open(ctx, callCtx, c.send, r) //nolint:bodyclose // closed exactly once by release
	if cerr != nil {
		return nil, cerr
	}
	defer release()
	ex, cerr := c.readExchange(ctx, callCtx, r.op, resp)
	if cerr != nil {
		return nil, r.sent(cerr)
	}
	return ex, nil
}

// open makes the one attempt of r within callCtx and returns the response.
// Its body is closed exactly once: when callCtx ends, so that a read that does
// not watch the context still stops, or when release runs, which the caller
// must do before returning. A close error after a complete read changes
// nothing.
func open(ctx, callCtx context.Context, send sender, r *request) (*http.Response, func(), *CallError) {
	resp, err := send(callCtx, r)
	if err != nil {
		if errors.Is(err, errBuildRequest) {
			return nil, nil, newCallError(r.op, FailureTransport, 0, errBuildRequest)
		}
		return nil, nil, r.sent(classify(ctx, callCtx, r.op, err, 0))
	}
	var closeOnce sync.Once
	closeBody := func() { closeOnce.Do(func() { _ = resp.Body.Close() }) }
	stop := context.AfterFunc(callCtx, closeBody)
	return resp, func() {
		stop()
		closeBody()
	}, nil
}

// sent completes ce, the failure of a request that reached the transport:
// one attempt, and an unknown outcome when r is paid and the failure leaves
// open whether the provider processed it.
func (r *request) sent(ce *CallError) *CallError {
	ce.Attempts = 1
	ce.OutcomeUnknown = r.paid && outcomeOpen(ce.Kind)
	return ce
}

// outcomeOpen reports whether a failure of kind, after sending, leaves open
// whether the provider processed the request. A 4xx and HTTP 249 are the
// provider's own answer that it did not complete it.
func outcomeOpen(kind FailureKind) bool {
	switch kind {
	case FailurePending, FailureUnauthorized, FailurePaymentRequired, FailureNotFound,
		FailureRateLimited, FailureRejected, FailureBudgetExhausted:
		return false
	case FailureUpstream, FailureUnavailable, FailureUnexpectedResponse, FailureMalformedResponse,
		FailureResponseTooLarge, FailureTransport, FailureTimeout, FailureCanceled, FailureDeadlineExceeded:
		return true
	}
	return true
}

// readExchange reads a response: the whole body for status 200, within the
// operation's limit, and for any other status the rate-limit headers and, when
// diagnostics are enabled, a best-effort excerpt of the body.
func (c *Client) readExchange(ctx, callCtx context.Context, op Operation, resp *http.Response) (*exchange, *CallError) {
	maxBytes := c.responseLimit(op)
	switch {
	case resp.StatusCode < http.StatusOK:
		// A 1xx that reaches here, such as 101 Switching Protocols, has the
		// raw connection as its body, which no deadline watches: never read it.
		return &exchange{status: resp.StatusCode, rateLimit: parseRateLimit(resp.Header)}, nil
	case resp.StatusCode != http.StatusOK:
		// The status decides the outcome; the body only feeds diagnostics, so
		// it is read only for them, and an oversized or unreadable one is
		// skipped.
		ex := &exchange{status: resp.StatusCode, rateLimit: parseRateLimit(resp.Header)}
		if c.diagnostics && !encoded(resp.Header) {
			ex.body = excerpt(resp.Body, resp.ContentLength)
		}
		return ex, nil
	case encoded(resp.Header):
		return nil, newCallError(op, FailureUnexpectedResponse, resp.StatusCode, errContentEncoding)
	case resp.ContentLength > maxBytes:
		return nil, newCallError(op, FailureResponseTooLarge, resp.StatusCode, errResponseTooLarge)
	}
	data, err := readBody(resp.Body, resp.ContentLength, maxBytes)
	switch {
	case err != nil:
		return nil, classify(ctx, callCtx, op, err, resp.StatusCode)
	case int64(len(data)) > maxBytes:
		return nil, newCallError(op, FailureResponseTooLarge, resp.StatusCode, errResponseTooLarge)
	case truncated(int64(len(data)), resp.ContentLength):
		// The standard transport reports a short body as io.ErrUnexpectedEOF;
		// an injected one might end it cleanly instead. Either way it is
		// incomplete.
		return nil, newCallError(op, FailureTransport, resp.StatusCode, errTransportClosed)
	}
	return &exchange{status: resp.StatusCode, body: data}, nil
}

// readBody reads r to EOF, but never more than maxBytes+1 bytes, so a result
// longer than maxBytes means the body is too large. A declared length, already
// known to be within the limit, sizes the buffer exactly when it is small,
// with one spare byte to see EOF without growing. A body of unknown or large
// declared length is read by io.ReadAll, which grows as bytes arrive.
func readBody(r io.Reader, contentLength, maxBytes int64) ([]byte, error) {
	limited := io.LimitReader(&progressReader{r: r}, maxBytes+1)
	if contentLength < 0 || contentLength > maxPrealloc {
		return io.ReadAll(limited)
	}
	buf := make([]byte, 0, contentLength+1)
	for len(buf) < cap(buf) {
		n, err := limited.Read(buf[len(buf):cap(buf)])
		buf = buf[:len(buf)+n]
		if errors.Is(err, io.EOF) {
			return buf, nil
		}
		if err != nil {
			return buf, err
		}
	}
	// The body is longer than declared: read on, still within the limit.
	rest, err := io.ReadAll(limited)
	return append(buf, rest...), err
}

// truncated reports whether a body of n bytes falls short of, or runs past,
// its declared length (-1 for unknown).
func truncated(n, declared int64) bool { return declared >= 0 && n != declared }

// progressReader fails a body that makes no progress: maxEmptyReads reads in
// a row that return neither bytes nor an error. Without it, a body from an
// injected transport that keeps answering (0, nil) would spin a CPU until the
// call's timeout.
type progressReader struct {
	r     io.Reader
	empty int
}

// Read reads from the wrapped reader, counting reads that make no progress.
func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 || err != nil || len(b) == 0 {
		p.empty = 0
		return n, err
	}
	if p.empty++; p.empty >= maxEmptyReads {
		return 0, errTransportStalled
	}
	return 0, nil
}

// excerpt returns the body of a response whose status is not 200 when it
// reads completely within maxErrorBodyBytes, and nil when it is declared or
// found longer, or its read fails. A stalled read ends with the call's
// context, which closes the body.
func excerpt(body io.Reader, contentLength int64) []byte {
	if contentLength > maxErrorBodyBytes {
		return nil
	}
	b, err := readBody(body, contentLength, maxErrorBodyBytes)
	if err != nil || len(b) > maxErrorBodyBytes {
		return nil
	}
	return b
}

// encoded reports whether h carries a Content-Encoding other than identity.
// The transport removes the header when it decodes gzip itself, so one that
// remains names an encoding nobody decoded. Every value counts, as a list of
// codings, under any spelling of the header name an injected transport may
// set; empty list elements are ignored.
func encoded(h http.Header) bool {
	for k, vs := range h {
		if !strings.EqualFold(k, "Content-Encoding") {
			continue
		}
		for _, v := range vs {
			for coding := range strings.SplitSeq(v, ",") {
				coding = strings.Trim(coding, " \t")
				if coding != "" && !strings.EqualFold(coding, "identity") {
					return true
				}
			}
		}
	}
	return false
}

// classify maps a failed exchange onto a CallError: the caller's context
// first, then a timeout (this call's deadline, an http.Client Timeout, or any
// network timeout), else a transport failure of a fixed class. The original
// error is never kept.
func classify(caller, callCtx context.Context, op Operation, err error, status int) *CallError {
	if cerr := caller.Err(); cerr != nil {
		return contextFailure(op, cerr, status)
	}
	if callCtx.Err() != nil || errors.Is(err, context.DeadlineExceeded) || isNetTimeout(err) {
		return newCallError(op, FailureTimeout, status, context.DeadlineExceeded)
	}
	return newCallError(op, FailureTransport, status, transportClass(err))
}

// isNetTimeout reports whether any error in err's tree reports a timeout,
// such as a dial or TLS handshake timeout. It walks the whole tree, because
// the *url.Error that http.Client adds reports a timeout only when its direct
// cause does, and a transport may wrap a timeout further down.
func isNetTimeout(err error) bool {
	if err == nil {
		return false
	}
	if t, ok := err.(interface{ Timeout() bool }); ok && t.Timeout() {
		return true
	}
	switch u := err.(type) { //nolint:errorlint // walking the tree by hand
	case interface{ Unwrap() error }:
		return isNetTimeout(u.Unwrap())
	case interface{ Unwrap() []error }:
		return slices.ContainsFunc(u.Unwrap(), isNetTimeout)
	}
	return false
}

// transportClass reduces a transport error to one of a few fixed classes.
func transportClass(err error) error {
	if errors.Is(err, errTransportStalled) {
		return errTransportStalled
	}
	if _, ok := errors.AsType[*net.DNSError](err); ok {
		return errTransportDNS
	}
	var op string
	if oe, ok := errors.AsType[*net.OpError](err); ok {
		op = oe.Op
	}
	if op == "proxyconnect" {
		return errTransportProxy
	}
	if isTLSError(err) {
		return errTransportTLS
	}
	if errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNABORTED) || errors.Is(err, syscall.EPIPE) {
		return errTransportReset
	}
	if op == "dial" {
		return errTransportDial
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) {
		return errTransportClosed
	}
	return errTransportOther
}

// isTLSError reports whether err comes from the TLS handshake or record layer.
func isTLSError(err error) bool {
	if _, ok := errors.AsType[*tls.CertificateVerificationError](err); ok {
		return true
	}
	if _, ok := errors.AsType[tls.RecordHeaderError](err); ok {
		return true
	}
	_, ok := errors.AsType[tls.AlertError](err)
	return ok
}
