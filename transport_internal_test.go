package emailable

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-emailable/internal/testutil"
)

// errTinRead is the error a failing reader returns.
var errTinRead = errors.New("tin: read failed")

// tinTCP is the network of every planted net.OpError.
const tinTCP = "tcp"

func TestTransportInternalClass(t *testing.T) {
	reset := os.NewSyscallError("read", syscall.ECONNRESET)
	dnsErr := &net.DNSError{Err: "no such host", Name: "x.example.com", IsNotFound: true}
	tests := []struct {
		name string
		err  error
		want error
	}{
		{name: "stalled", err: errTransportStalled, want: errTransportStalled},
		{name: "stalled wrapped", err: fmt.Errorf("read: %w", errTransportStalled), want: errTransportStalled},
		{name: "stalled before EOF", err: errors.Join(io.EOF, errTransportStalled), want: errTransportStalled},
		{name: "DNS", err: dnsErr, want: errTransportDNS},
		{name: "DNS inside dial", err: &net.OpError{Op: "dial", Net: tinTCP, Err: dnsErr}, want: errTransportDNS},
		{name: "DNS reaching the proxy", err: &net.OpError{Op: "proxyconnect", Net: tinTCP, Err: dnsErr}, want: errTransportDNS},
		{name: "DNS inside a URL error", err: &url.Error{Op: "Get", URL: "https://x", Err: dnsErr}, want: errTransportDNS},
		{name: "proxyconnect", err: &net.OpError{Op: "proxyconnect", Net: tinTCP, Err: errTinRead}, want: errTransportProxy},
		{name: "proxyconnect reset", err: &net.OpError{Op: "proxyconnect", Net: tinTCP, Err: reset}, want: errTransportProxy},
		{name: "proxyconnect TLS", err: &net.OpError{Op: "proxyconnect", Net: tinTCP, Err: tls.AlertError(40)}, want: errTransportProxy},
		{name: "TLS record header", err: tls.RecordHeaderError{Msg: "bad"}, want: errTransportTLS},
		{name: "TLS alert", err: tls.AlertError(42), want: errTransportTLS},
		{name: "TLS certificate", err: &tls.CertificateVerificationError{Err: errTinRead}, want: errTransportTLS},
		{name: "TLS inside a read", err: &net.OpError{Op: "remote error", Net: tinTCP, Err: tls.AlertError(80)}, want: errTransportTLS},
		{name: "TLS inside a URL error", err: &url.Error{Op: "Get", URL: "https://x", Err: tls.AlertError(40)}, want: errTransportTLS},
		{name: "ECONNRESET", err: syscall.ECONNRESET, want: errTransportReset},
		{name: "ECONNABORTED", err: syscall.ECONNABORTED, want: errTransportReset},
		{name: "EPIPE", err: syscall.EPIPE, want: errTransportReset},
		{name: "reset in a read", err: &net.OpError{Op: "read", Net: tinTCP, Err: reset}, want: errTransportReset},
		{name: "broken pipe in a write", err: &net.OpError{Op: "write", Net: tinTCP, Err: os.NewSyscallError("write", syscall.EPIPE)}, want: errTransportReset},
		{name: "reset while dialing", err: &net.OpError{Op: "dial", Net: tinTCP, Err: reset}, want: errTransportReset},
		{name: "dial refused", err: &net.OpError{Op: "dial", Net: tinTCP, Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}, want: errTransportDial},
		{name: "dial other", err: &net.OpError{Op: "dial", Net: tinTCP, Err: errTinRead}, want: errTransportDial},
		{name: "dial inside a URL error", err: &url.Error{Op: "Get", URL: "https://x", Err: &net.OpError{Op: "dial", Net: tinTCP, Err: errTinRead}}, want: errTransportDial},
		{name: "EOF", err: io.EOF, want: errTransportClosed},
		{name: "unexpected EOF", err: io.ErrUnexpectedEOF, want: errTransportClosed},
		{name: "net.ErrClosed", err: net.ErrClosed, want: errTransportClosed},
		{name: "EOF in a read", err: &net.OpError{Op: "read", Net: tinTCP, Err: io.EOF}, want: errTransportClosed},
		{name: "EOF wrapped", err: fmt.Errorf("x: %w", io.ErrUnexpectedEOF), want: errTransportClosed},
		{name: "other", err: errTinRead, want: errTransportOther},
		{name: "other in a read", err: &net.OpError{Op: "read", Net: tinTCP, Err: errTinRead}, want: errTransportOther},
		{name: "ECONNREFUSED outside a dial", err: syscall.ECONNREFUSED, want: errTransportOther},
		{name: "context canceled", err: context.Canceled, want: errTransportOther},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Same(t, tt.want, transportClass(tt.err))
		})
	}
}

func TestTransportInternalIsNetTimeout(t *testing.T) {
	timeout := testutil.TimeoutError{}
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "timeout error", err: timeout, want: true},
		{name: "wrapped", err: fmt.Errorf("x: %w", timeout), want: true},
		{name: "inside an OpError", err: &net.OpError{Op: "dial", Net: tinTCP, Err: timeout}, want: true},
		{name: "inside a URL error", err: &url.Error{Op: "Get", URL: "https://x", Err: timeout}, want: true},
		{name: "DNS timeout", err: &net.DNSError{IsTimeout: true}, want: true},
		{name: "deadline exceeded", err: context.DeadlineExceeded, want: true},
		{name: "OS deadline", err: os.ErrDeadlineExceeded, want: true},
		{name: "DNS not found", err: &net.DNSError{IsNotFound: true}, want: false},
		{name: "OpError without a timeout", err: &net.OpError{Op: "read", Net: tinTCP, Err: errTinRead}, want: false},
		{name: "URL error without a timeout", err: &url.Error{Op: "Get", URL: "https://x", Err: errTinRead}, want: false},
		{name: "canceled", err: context.Canceled, want: false},
		{name: "plain error", err: errTinRead, want: false},
		{name: "nil", err: nil, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isNetTimeout(tt.err))
		})
	}
}

// tinHeader returns a header holding name and values exactly as given, the
// name not canonicalized.
func tinHeader(name string, values ...string) http.Header {
	h := http.Header{"Content-Type": {"application/json"}}
	h[name] = values
	return h
}

func TestTransportInternalEncoded(t *testing.T) {
	const name = "Content-Encoding"
	tests := []struct {
		name string
		h    http.Header
		want bool
	}{
		{name: "no header", h: http.Header{}, want: false},
		{name: "nil header", h: nil, want: false},
		{name: "no values", h: tinHeader(name), want: false},
		{name: "empty value", h: tinHeader(name, ""), want: false},
		{name: "identity", h: tinHeader(name, "identity"), want: false},
		{name: "capitalized identity with a trailing separator", h: tinHeader(name, "Identity, "), want: false},
		{name: "only separators", h: tinHeader(name, " , "), want: false},
		{name: "padded identity", h: tinHeader(name, " \tidentity\t "), want: false},
		{name: "identity list", h: tinHeader(name, ",identity,,IDENTITY"), want: false},
		{name: "identity in several values", h: tinHeader(name, "identity", "", "identity"), want: false},
		{name: "lowercase name with identity", h: tinHeader("content-encoding", "identity"), want: false},
		{name: "gzip", h: tinHeader(name, "gzip"), want: true},
		{name: "br", h: tinHeader(name, "br"), want: true},
		{name: "gzip after identity", h: tinHeader(name, "identity", "gzip"), want: true},
		{name: "gzip in a list", h: tinHeader(name, "identity,gzip"), want: true},
		{name: "identity with a parameter", h: tinHeader(name, "identity;q=1"), want: true},
		{name: "unknown coding", h: tinHeader(name, "x"), want: true},
		{name: "lowercase name", h: tinHeader("content-encoding", "gzip"), want: true},
		{name: "upper-case name", h: tinHeader("CONTENT-ENCODING", "br"), want: true},
		{name: "similar name", h: tinHeader("Content-Encodings", "gzip"), want: false},
		{name: "transfer encoding", h: tinHeader("Transfer-Encoding", "gzip"), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, encoded(tt.h))
		})
	}
}

// tinFailingBody returns a body that serves content, then fails with
// errTinRead.
func tinFailingBody(content string) *testutil.Body {
	body := testutil.NewBody(content)
	body.End = errTinRead
	return body
}

func TestTransportInternalReadBody(t *testing.T) {
	big := strings.Repeat("b", maxPrealloc+10)
	tests := []struct {
		name     string
		r        io.Reader
		declared int64
		limit    int64
		want     string
		err      error
	}{
		{name: "unknown length", r: strings.NewReader("hello"), declared: -1, limit: 10, want: "hello"},
		{name: "exact declared", r: strings.NewReader("hello"), declared: 5, limit: 10, want: "hello"},
		{name: "declared longer than sent", r: strings.NewReader("hel"), declared: 5, limit: 10, want: "hel"},
		{name: "declared shorter than sent", r: strings.NewReader("hello"), declared: 2, limit: 10, want: "hello"},
		{name: "declared zero, sent nothing", r: strings.NewReader(""), declared: 0, limit: 10, want: ""},
		{name: "exactly the limit", r: strings.NewReader("hello"), declared: -1, limit: 5, want: "hello"},
		{name: "over the limit by one", r: strings.NewReader("hello!"), declared: -1, limit: 5, want: "hello!"},
		{name: "far over the limit", r: strings.NewReader(strings.Repeat("x", 100)), declared: -1, limit: 5, want: "xxxxxx"},
		{name: "declared short, far over the limit", r: strings.NewReader(strings.Repeat("x", 100)), declared: 2, limit: 5, want: "xxxxxx"},
		{name: "large declared length", r: strings.NewReader(big), declared: int64(len(big)), limit: int64(len(big)), want: big},
		{name: "read error", r: tinFailingBody("he"), declared: -1, limit: 10, want: "he", err: errTinRead},
		{name: "read error with declared length", r: tinFailingBody("he"), declared: 5, limit: 10, want: "he", err: errTinRead},
		{name: "read error past the declared length", r: tinFailingBody("hello"), declared: 2, limit: 10, want: "hello", err: errTinRead},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := readBody(tt.r, tt.declared, tt.limit)
			if tt.err != nil {
				require.ErrorIs(t, err, tt.err)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.want, string(got))
		})
	}
}

func TestTransportInternalReadBodyStall(t *testing.T) {
	for _, tt := range []struct {
		empties int
		stalled bool
	}{{empties: 0}, {empties: 99}, {empties: 100, stalled: true}, {empties: 250, stalled: true}} {
		for _, declared := range []int64{-1, 11} {
			body := testutil.NewBody("hello world")
			body.Step = 3
			body.Empties = tt.empties
			got, err := readBody(body, declared, 64)
			if tt.stalled {
				require.ErrorIs(t, err, errTransportStalled, "%d empty reads, declared %d", tt.empties, declared)
				continue
			}
			require.NoError(t, err)
			assert.Equal(t, "hello world", string(got))
		}
	}
}

func TestTransportInternalTruncated(t *testing.T) {
	tests := []struct {
		n, declared int64
		want        bool
	}{
		{n: 5, declared: -1, want: false},
		{n: 0, declared: -1, want: false},
		{n: 5, declared: 5, want: false},
		{n: 0, declared: 0, want: false},
		{n: 4, declared: 5, want: true},
		{n: 0, declared: 5, want: true},
		{n: 6, declared: 5, want: true},
		{n: 1, declared: 0, want: true},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, truncated(tt.n, tt.declared), "%d bytes of %d", tt.n, tt.declared)
	}
}

// tinEmptyReader answers every read with nothing and no error.
type tinEmptyReader struct{ reads int }

func (r *tinEmptyReader) Read([]byte) (int, error) {
	r.reads++
	return 0, nil
}

func TestTransportInternalProgressReader(t *testing.T) {
	t.Run("stalls on the 100th empty read in a row", func(t *testing.T) {
		src := &tinEmptyReader{}
		p := &progressReader{r: src}
		buf := make([]byte, 8)
		for i := range maxEmptyReads - 1 {
			n, err := p.Read(buf)
			require.NoError(t, err, "read %d", i+1)
			assert.Zero(t, n)
		}
		n, err := p.Read(buf)
		assert.Zero(t, n)
		require.ErrorIs(t, err, errTransportStalled)
		assert.Equal(t, maxEmptyReads, src.reads)
	})
	t.Run("zero-length reads never count", func(t *testing.T) {
		p := &progressReader{r: &tinEmptyReader{}}
		for range 3 * maxEmptyReads {
			n, err := p.Read(nil)
			require.NoError(t, err)
			assert.Zero(t, n)
		}
	})
	t.Run("progress resets the count", func(t *testing.T) {
		body := testutil.NewBody(strings.Repeat("x", 10))
		body.Step = 1
		body.Empties = maxEmptyReads - 1
		got, err := io.ReadAll(&progressReader{r: body})
		require.NoError(t, err, "99 empty reads before each byte never add up")
		assert.Len(t, got, 10)
	})
	t.Run("errors pass through", func(t *testing.T) {
		p := &progressReader{r: tinFailingBody("")}
		_, err := p.Read(make([]byte, 4))
		require.ErrorIs(t, err, errTinRead)
	})
}

func TestTransportInternalExcerpt(t *testing.T) {
	full := strings.Repeat("e", maxErrorBodyBytes)
	tests := []struct {
		name     string
		body     *testutil.Body
		declared int64
		want     []byte
		read     int64
	}{
		{name: "small", body: testutil.NewBody("{}"), declared: 2, want: []byte("{}"), read: 2},
		{name: "small, unknown length", body: testutil.NewBody("{}"), declared: -1, want: []byte("{}"), read: 2},
		{name: "exactly the bound", body: testutil.NewBody(full), declared: -1, want: []byte(full), read: maxErrorBodyBytes},
		{name: "declared over the bound", body: testutil.NewBody(full + "e"), declared: maxErrorBodyBytes + 1, read: 0},
		{name: "found over the bound", body: testutil.NewBody(full + "e"), declared: -1, read: maxErrorBodyBytes + 1},
		{name: "empty", body: testutil.NewBody(""), declared: 0, want: []byte{}, read: 0},
		{name: "read failure", body: tinFailingBody("{}"), declared: -1, read: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, excerpt(tt.body, tt.declared))
			assert.Equal(t, tt.read, tt.body.BytesRead())
		})
	}
	stalled := testutil.NewBody("{}")
	stalled.Stall = true
	assert.Nil(t, excerpt(stalled, -1), "a stalled body yields nothing")
}

func TestTransportInternalRequestHeader(t *testing.T) {
	const authorization = "Bearer " + internalKey
	for _, post := range []bool{false, true} {
		t.Run(fmt.Sprintf("post=%t", post), func(t *testing.T) {
			h := requestHeader(post, authorization)
			want := http.Header{
				"Accept":        {"application/json"},
				"User-Agent":    {"go-emailable"},
				"Authorization": {authorization},
			}
			if post {
				want["Content-Type"] = []string{"application/json"}
			}
			assert.Equal(t, want, h, "Content-Type only on a POST")
			for k, v := range h {
				assert.Equal(t, 1, cap(v), "a value leaves no room for an append to overwrite the next: %s", k)
			}

			// An append to one value never overwrites the next.
			h["Accept"] = append(h["Accept"], "text/plain")
			assert.Equal(t, []string{"go-emailable"}, h["User-Agent"])
			assert.Equal(t, []string{authorization}, h["Authorization"])

			// Each call gets fresh memory: changing one header map changes no other.
			other := requestHeader(post, authorization)
			h["User-Agent"][0] = "changed"
			h["Authorization"][0] = "changed"
			assert.Equal(t, []string{"application/json"}, other["Accept"])
			assert.Equal(t, []string{"go-emailable"}, other["User-Agent"])
			assert.Equal(t, []string{authorization}, other["Authorization"])
			for _, banned := range []string{"Cookie", "Idempotency-Key", "Accept-Encoding", "Content-Length"} {
				assert.Empty(t, other.Values(banned))
			}
		})
	}
}

func TestTransportInternalNoReplayBody(t *testing.T) {
	var b noReplayBody
	n, err := b.Read(make([]byte, 8))
	assert.Zero(t, n)
	require.ErrorIs(t, err, io.EOF)
	require.NoError(t, b.Close())
	n, err = b.Read(nil)
	assert.Zero(t, n)
	require.ErrorIs(t, err, io.EOF)
}

// tinCapture is a transport that keeps the request it is given and answers
// HTTP 200 with an empty object.
type tinCapture struct {
	req *http.Request
}

func (c *tinCapture) RoundTrip(r *http.Request) (*http.Response, error) {
	c.req = r
	return testutil.Response(r, http.StatusOK, io.NopCloser(strings.NewReader("{}")), 2), nil
}

func TestTransportInternalSenderRequests(t *testing.T) {
	t.Run("GET", func(t *testing.T) {
		capture := &tinCapture{}
		send := newSender(&http.Client{Transport: capture}, "[::1]:8443", "Bearer "+internalKey)
		resp, err := send(t.Context(), &request{op: OpVerifyEmail, method: http.MethodGet, path: "verify", query: "email=a%40example.com"})
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
		r := capture.req
		require.NotNil(t, r)
		assert.Equal(t, "https://[::1]:8443/v1/verify?email=a%40example.com", r.URL.String())
		assert.Equal(t, http.MethodGet, r.Method)
		assert.IsType(t, noReplayBody{}, r.Body, "a GET carries the non-rewindable empty body")
		assert.Nil(t, r.GetBody)
		assert.Zero(t, r.ContentLength)
		assert.Equal(t, []string{"identity"}, r.TransferEncoding)
		assert.Equal(t, requestHeader(false, "Bearer "+internalKey), r.Header)
	})
	t.Run("POST", func(t *testing.T) {
		capture := &tinCapture{}
		send := newSender(&http.Client{Transport: capture}, "127.0.0.1", "Bearer "+internalKey)
		body := []byte(`{"emails":["a@example.com"]}`)
		resp, err := send(t.Context(), &request{op: OpCreateBatch, method: http.MethodPost, path: "batch", body: body})
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
		r := capture.req
		require.NotNil(t, r)
		assert.Equal(t, "https://127.0.0.1/v1/batch", r.URL.String())
		assert.Nil(t, r.GetBody, "a POST is never replayable either")
		assert.Equal(t, int64(len(body)), r.ContentLength)
		assert.Empty(t, r.TransferEncoding)
		got, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		assert.Equal(t, body, got)
		assert.Equal(t, requestHeader(true, "Bearer "+internalKey), r.Header)
	})
}

func TestTransportInternalSenderRefusesAnUnbuildableRequest(t *testing.T) {
	ct := &testutil.CountingTransport{Next: testutil.StaticTransport(http.StatusOK, "{}")}
	tests := []struct {
		name string
		host string
		r    *request
	}{
		{name: "invalid method", host: "127.0.0.1", r: &request{op: OpVerifyEmail, method: "BAD METHOD", path: "verify", paid: true}},
		{name: "control byte in the host", host: "127.0.0.1\x00", r: &request{op: OpVerifyEmail, method: http.MethodGet, path: "verify", paid: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			send := newSender(&http.Client{Transport: ct}, tt.host, "Bearer "+internalKey)
			resp, err := send(t.Context(), tt.r) //nolint:bodyclose // the response is nil
			assert.Nil(t, resp)
			require.ErrorIs(t, err, errBuildRequest)

			resp, release, cerr := open(t.Context(), t.Context(), send, tt.r) //nolint:bodyclose // nothing to close
			assert.Nil(t, resp)
			assert.Nil(t, release)
			require.NotNil(t, cerr)
			assert.Equal(t, FailureTransport, cerr.Kind)
			assert.Same(t, errBuildRequest, cerr.Unwrap())
			assert.Zero(t, cerr.Attempts, "nothing was sent")
			assert.False(t, cerr.OutcomeUnknown, "nothing was sent, even for a paid call")
		})
	}
	assert.Zero(t, ct.Attempts())
}

// tinSender returns a sender that answers HTTP 200 with body.
func tinSender(body io.ReadCloser) sender {
	return func(context.Context, *request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: body, ContentLength: -1}, nil
	}
}

func TestTransportInternalOpenClosesOnce(t *testing.T) {
	r := &request{op: OpVerifyEmail, method: http.MethodGet, path: "verify"}
	t.Run("release", func(t *testing.T) {
		body := testutil.NewBody("{}")
		resp, release, cerr := open(t.Context(), t.Context(), tinSender(body), r) //nolint:bodyclose // closed by release
		require.Nil(t, cerr)
		require.NotNil(t, resp)
		assert.Zero(t, body.Closes())
		release()
		release()
		assert.Equal(t, 1, body.Closes())
	})
	t.Run("call context ends first", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			body := testutil.NewBody("{}")
			callCtx, cancel := context.WithCancel(t.Context())
			_, release, cerr := open(t.Context(), callCtx, tinSender(body), r) //nolint:bodyclose // closed by release
			require.Nil(t, cerr)
			cancel()
			synctest.Wait()
			assert.Equal(t, 1, body.Closes(), "the call context's end closes the body")
			release()
			assert.Equal(t, 1, body.Closes(), "release never closes it again")
		})
	})
}

func TestTransportInternalClassifyPrecedence(t *testing.T) {
	live := t.Context()
	noTimeout := &net.OpError{Op: "read", Net: tinTCP, Err: errTinRead}
	canceled, cancel := context.WithCancel(live)
	cancel()
	expired, cancelExpired := context.WithDeadline(live, time.Time{})
	defer cancelExpired()
	tests := []struct {
		name    string
		caller  context.Context //nolint:containedctx // each case runs with its own contexts
		callCtx context.Context //nolint:containedctx // each case runs with its own contexts
		err     error
		kind    FailureKind
		cause   error
	}{
		{name: "caller canceled beats everything", caller: canceled, callCtx: expired, err: context.DeadlineExceeded, kind: FailureCanceled, cause: context.Canceled},
		{name: "caller deadline beats the call's", caller: expired, callCtx: expired, err: errTinRead, kind: FailureDeadlineExceeded, cause: context.DeadlineExceeded},
		{name: "call deadline", caller: live, callCtx: expired, err: errTinRead, kind: FailureTimeout, cause: context.DeadlineExceeded},
		{name: "deadline error alone", caller: live, callCtx: live, err: context.DeadlineExceeded, kind: FailureTimeout, cause: context.DeadlineExceeded},
		{name: "network timeout", caller: live, callCtx: live, err: testutil.TimeoutError{}, kind: FailureTimeout, cause: context.DeadlineExceeded},
		{name: "network error without timeout", caller: live, callCtx: live, err: noTimeout, kind: FailureTransport, cause: errTransportOther},
		{name: "transport failure", caller: live, callCtx: live, err: io.ErrUnexpectedEOF, kind: FailureTransport, cause: errTransportClosed},
		{name: "stall", caller: live, callCtx: live, err: errTransportStalled, kind: FailureTransport, cause: errTransportStalled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ce := classify(tt.caller, tt.callCtx, OpGetBatch, tt.err, 200)
			assert.Equal(t, tt.kind, ce.Kind)
			assert.Equal(t, OpGetBatch, ce.Op)
			assert.Equal(t, 200, ce.StatusCode)
			assert.Equal(t, tt.cause, ce.Unwrap())
			assert.Zero(t, ce.Attempts, "the caller completes Attempts")
		})
	}
}

// tinKinds returns every failure kind.
func tinKinds() []FailureKind {
	return []FailureKind{
		FailurePending, FailureUnauthorized, FailurePaymentRequired, FailureNotFound, FailureRateLimited,
		FailureRejected, FailureUpstream, FailureUnavailable, FailureUnexpectedResponse, FailureMalformedResponse,
		FailureResponseTooLarge, FailureTransport, FailureTimeout, FailureCanceled, FailureDeadlineExceeded,
		FailureBudgetExhausted,
	}
}

func TestTransportInternalOutcomeOpen(t *testing.T) {
	closed := map[FailureKind]bool{
		FailurePending: true, FailureUnauthorized: true, FailurePaymentRequired: true, FailureNotFound: true,
		FailureRateLimited: true, FailureRejected: true, FailureBudgetExhausted: true,
	}
	for _, kind := range tinKinds() {
		assert.Equal(t, !closed[kind], outcomeOpen(kind), "%s", kind)
	}
	for _, kind := range []FailureKind{"", "made_up", "Timeout"} {
		assert.True(t, outcomeOpen(kind), "an unknown kind %q leaves the outcome open", kind)
	}
}

func TestTransportInternalSent(t *testing.T) {
	for _, kind := range tinKinds() {
		for _, paid := range []bool{false, true} {
			r := &request{op: OpCreateBatch, paid: paid}
			ce := r.sent(newCallError(OpCreateBatch, kind, 0, errStatus))
			assert.Equal(t, 1, ce.Attempts)
			assert.Equal(t, paid && outcomeOpen(kind), ce.OutcomeUnknown, "%s paid=%t", kind, paid)
		}
	}
}

// tinWantKind is the documented kind of an HTTP status other than 200,
// written out independently of the library.
func tinWantKind(op Operation, code int) FailureKind {
	switch {
	case code == 249 && op == OpVerifyEmail:
		return FailurePending
	case code == 401, code == 403:
		return FailureUnauthorized
	case code == 402:
		return FailurePaymentRequired
	case code == 404:
		return FailureNotFound
	case code == 429:
		return FailureRateLimited
	case code >= 400 && code <= 499:
		return FailureRejected
	case code == 503:
		return FailureUnavailable
	case code >= 500 && code <= 599:
		return FailureUpstream
	}
	return FailureUnexpectedResponse
}

func TestTransportInternalStatusKind(t *testing.T) {
	ops := []Operation{OpVerifyEmail, OpCreateBatch, OpGetBatch, OpAccountInfo}
	for _, op := range ops {
		for code := -1; code <= 1000; code++ {
			if code == http.StatusOK {
				continue
			}
			if got, want := statusKind(op, code), tinWantKind(op, code); got != want {
				assert.Equal(t, want, got, "%s HTTP %d", op, code)
			}
		}
	}
	assert.Equal(t, FailurePending, statusKind(OpVerifyEmail, 249))
	assert.Equal(t, FailureUnexpectedResponse, statusKind(OpGetBatch, 249))
	assert.Equal(t, FailureUnavailable, statusKind(OpAccountInfo, 503))
	assert.Equal(t, FailureUpstream, statusKind(OpCreateBatch, 599))
	assert.Equal(t, FailureUnexpectedResponse, statusKind(OpCreateBatch, 600))
}

func TestTransportInternalHeaderInt(t *testing.T) {
	const name = "ratelimit-limit"
	tests := []struct {
		name string
		h    http.Header
		want *int64
	}{
		{name: "nil header", h: nil},
		{name: "absent", h: http.Header{}},
		{name: "canonical", h: tinHeader("Ratelimit-Limit", "42"), want: new(int64(42))},
		{name: "lowercase", h: tinHeader(name, "42"), want: new(int64(42))},
		{name: "upper case", h: tinHeader("RATELIMIT-LIMIT", "42"), want: new(int64(42))},
		{name: "zero", h: tinHeader(name, "0"), want: new(int64(0))},
		{name: "19 digits", h: tinHeader(name, "9223372036854775807"), want: new(int64(9223372036854775807))},
		{name: "19 digits over int64", h: tinHeader(name, "9223372036854775808")},
		{name: "20 digits", h: tinHeader(name, "00000000000000000001")},
		{name: "repeated", h: tinHeader(name, "1", "1")},
		{name: "no values", h: tinHeader(name)},
		{name: "empty", h: tinHeader(name, "")},
		{name: "plus", h: tinHeader(name, "+1")},
		{name: "minus", h: tinHeader(name, "-1")},
		{name: "space", h: tinHeader(name, " 1")},
		{name: "letters", h: tinHeader(name, "1a")},
		{name: "similar name", h: tinHeader("x-ratelimit-limit", "1")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, headerInt(tt.h, name))
		})
	}
	t.Run("two spellings", func(t *testing.T) {
		h := tinHeader("Ratelimit-Limit", "1")
		h[name] = []string{"1"}
		assert.Nil(t, headerInt(h, name), "two keys differing in case are ambiguous")
	})
}

func TestTransportInternalExchangeDoneContextSendsNothing(t *testing.T) {
	ct := &testutil.CountingTransport{Next: testutil.StaticTransport(http.StatusOK, "{}")}
	c := newInternalTransportClient(t, ct)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	ex, cerr := c.exchange(ctx, &request{op: OpCreateBatch, method: http.MethodPost, path: "batch", paid: true})
	assert.Nil(t, ex)
	require.NotNil(t, cerr)
	assert.Equal(t, FailureCanceled, cerr.Kind)
	assert.Zero(t, cerr.Attempts)
	assert.False(t, cerr.OutcomeUnknown, "nothing was sent")
	assert.Zero(t, ct.Attempts())
}

func TestTransportInternalStatusFailure(t *testing.T) {
	c := newInternalClient(t, WithProviderDiagnostics())
	limit := int64(5)
	ex := &exchange{status: 249, body: []byte(`{"message":"m ` + internalKey + `"}`), rateLimit: RateLimit{Limit: &limit}}
	ce := c.statusFailure(&request{op: OpVerifyEmail}, ex)
	assert.Equal(t, FailurePending, ce.Kind)
	assert.Same(t, errPendingStatus, ce.Unwrap())
	assert.Equal(t, RateLimit{Limit: &limit}, ce.RateLimit)
	assert.Equal(t, "m [redacted]", ce.Diagnostics().Message)

	ce = c.statusFailure(&request{op: OpAccountInfo}, ex)
	assert.Equal(t, FailureUnexpectedResponse, ce.Kind)
	assert.Same(t, errStatus, ce.Unwrap())

	quiet := newInternalClient(t)
	assert.Empty(t, quiet.statusFailure(&request{op: OpVerifyEmail}, ex).Diagnostics().Message,
		"without the option, even a read body yields no message")
}

// TestTransportInternalCausesAreFixed proves, through whole calls, that the
// cause a CallError keeps is the package's own fixed error itself, never the
// transport's or the reader's.
func TestTransportInternalCausesAreFixed(t *testing.T) {
	stalled := func() io.ReadCloser {
		b := testutil.NewBody("{}")
		b.Stall = true
		return b
	}
	tests := []struct {
		name string
		rt   http.RoundTripper
		want error
	}{
		{name: "status", rt: testutil.StaticTransport(http.StatusBadRequest, "{}"), want: errStatus},
		{name: "pending", rt: testutil.StaticTransport(StatusPending, "{}"), want: errPendingStatus},
		{
			name: "content encoding",
			rt:   testutil.TransportWith(http.StatusOK, tinHeader("Content-Encoding", "gzip"), 2, io.NopCloser(strings.NewReader("{}"))),
			want: errContentEncoding,
		},
		{name: "declared too large", rt: testutil.Transport(http.StatusOK, 2<<20, func() io.ReadCloser { return io.NopCloser(strings.NewReader("{}")) }), want: errResponseTooLarge},
		{name: "truncated", rt: testutil.Transport(http.StatusOK, 10, func() io.ReadCloser { return io.NopCloser(strings.NewReader("{}")) }), want: errTransportClosed},
		{name: "stalled", rt: testutil.Transport(http.StatusOK, -1, stalled), want: errTransportStalled},
		{name: "not JSON", rt: testutil.StaticTransport(http.StatusOK, "x"), want: errInvalidJSON},
		{name: "duplicate key", rt: testutil.StaticTransport(http.StatusOK, `{"a":1,"A":2}`), want: errDuplicateKey},
		{name: "missing field", rt: testutil.StaticTransport(http.StatusOK, `{}`), want: errMissingField},
		{
			name: "planted transport error",
			rt: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
				testutil.Drain(r)
				return nil, errTinRead
			}),
			want: errTransportOther,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newInternalTransportClient(t, tt.rt)
			_, err := c.VerifyEmail(t.Context(), VerifyEmailRequest{Email: "jane.doe@example.com"})
			ce := asCallError(t, err)
			assert.Same(t, tt.want, ce.Unwrap())
			assert.Equal(t, 1, ce.Attempts)
		})
	}
}
