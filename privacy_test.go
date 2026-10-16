package emailable_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"testing/iotest"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-emailable"
	"github.com/mrz1836/go-emailable/internal/testutil"
)

// Leak markers of this file, beyond leakMarkers. Each is distinct, so a
// failure names what leaked. None is a real secret.
const (
	privacyDownloadPath = "dlpathmarker4e8a1b"
	privacyDownloadSig  = "dlsigmarker6c2f90" // gitleaks:allow (synthetic test marker)
	privacyMessage      = "providermsgmarker2d9e"
	privacyTransport    = "transportmarker81b4"
	privacyLocation     = "locationmarker5a7c"
	privacyHookMarker   = "hookmarker93c1d7" // the token in testHook's query

	// privacyHookEscaped is testHook as url.QueryEscape spells it, the form
	// it takes inside a query string.
	privacyHookEscaped = "https%3A%2F%2Fhooks.example.com%2Femailable%3Ftoken%3Dhookmarker93c1d7"

	// privacyDownload is a signed download URL: a bearer credential.
	privacyDownload = "https://files.example.com/" + privacyDownloadPath + ".csv?signature=" + privacyDownloadSig

	// privacyDiagMessage is provider text that echoes the request, as
	// provider text may: the key, the address, the captcha response, the
	// callback URL, also as a query string spells it, the owner and the
	// download URL.
	privacyDiagMessage = privacyMessage + " key=" + testKey + " email=" + testEmail + " captcha=" + testCaptcha +
		" url=" + testHook + " query=url%3D" + privacyHookEscaped + " owner=" + testOwner + " file=" + privacyDownload

	// privacyWaitAttempts is the MaxAttempts of every wait.
	privacyWaitAttempts = 3
)

// errPrivacyEcho is the failure whose message echoes the request.
var errPrivacyEcho = errors.New("privacy echo")

// privacyExtraSecrets returns the markers of this file that leakMarkers
// lacks.
func privacyExtraSecrets() []string {
	return []string{
		privacyDownload, privacyDownloadPath, privacyDownloadSig, privacyMessage, privacyTransport, privacyLocation,
	}
}

// privacySecrets returns every marker that must never print.
func privacySecrets() []string { return append(leakMarkers(), privacyExtraSecrets()...) }

// privacyDownloadSecrets returns the parts of the signed download URL.
func privacyDownloadSecrets() []string {
	return []string{privacyDownload, privacyDownloadPath, privacyDownloadSig}
}

// privacyDiagBody returns an error body whose message, and more, is
// privacyDiagMessage.
func privacyDiagBody() string {
	msg := testutil.JSONQuote(privacyDiagMessage)
	return `{"message":` + msg + `,"error":` + msg + `,"email":` + testutil.JSONQuote(testEmail) + `}`
}

// privacyBig is a JSON string of provider text over the 4 KiB text bound.
func privacyBig() string { return testutil.JSONQuote(strings.Repeat(privacyDiagMessage+" ", 20)) }

// privacyVerifyRequest returns a verify request with every field set, its
// text fields holding markers.
func privacyVerifyRequest() emailable.VerifyEmailRequest {
	return emailable.VerifyEmailRequest{
		Email: testEmail, SMTP: new(false), AcceptAll: new(true), ProviderTimeout: 5 * time.Second,
		CaptchaResponse: testCaptcha,
	}
}

// privacyCreateRequest returns a batch request with every field set but
// Simulate, its text fields holding markers.
func privacyCreateRequest() emailable.CreateBatchRequest {
	return emailable.CreateBatchRequest{
		Emails:      []string{testEmail, testOwner},
		CallbackURL: testHook,
		ResponseFields: []emailable.ResponseField{
			emailable.ResponseFieldEmail, emailable.ResponseFieldState, emailable.ResponseFieldReason,
		},
		Retries: new(false),
	}
}

// privacyWaitRequest returns a wait for privacyVerifyRequest.
func privacyWaitRequest() emailable.WaitForVerificationRequest {
	return emailable.WaitForVerificationRequest{
		Verify: privacyVerifyRequest(), MaxAttempts: privacyWaitAttempts, MaxWait: time.Minute,
	}
}

// privacyOp is one operation case called with marker requests.
type privacyOp struct {
	opCase

	secrets []string // the call's own secrets, scrubbed from provider diagnostics
}

// diagWant is privacyDiagMessage as Diagnostics returns it for op: the key
// and the call's own secrets scrubbed, each also as url.QueryEscape spells
// it; the address and the other values remain, as documented.
func (op privacyOp) diagWant() string {
	s := privacyDiagMessage
	for _, secret := range append([]string{testKey}, op.secrets...) {
		s = strings.ReplaceAll(s, secret, "[redacted]")
		s = strings.ReplaceAll(s, url.QueryEscape(secret), "[redacted]")
	}
	return s
}

// privacyOps returns every operation case with marker requests: VerifyEmail
// and WaitForVerification with a captcha response, CreateBatch with a
// callback URL, and a partial GetBatch.
func privacyOps() []privacyOp {
	cases := opCases()
	ops := make([]privacyOp, 0, len(cases))
	for _, o := range cases {
		p := privacyOp{opCase: o}
		switch o.name {
		case "VerifyEmail":
			p.secrets = []string{testCaptcha}
			p.call = func(ctx context.Context, c *emailable.Client) error {
				return noValueWith(c.VerifyEmail(ctx, privacyVerifyRequest()))
			}
		case "WaitForVerification":
			p.secrets = []string{testCaptcha}
			p.call = func(ctx context.Context, c *emailable.Client) error {
				return noValueWith(c.WaitForVerification(ctx, privacyWaitRequest()))
			}
		case "CreateBatch":
			p.secrets = []string{testHook}
			p.call = func(ctx context.Context, c *emailable.Client) error {
				return noValueWith(c.CreateBatch(ctx, privacyCreateRequest()))
			}
		case "GetBatch":
			p.call = func(ctx context.Context, c *emailable.Client) error {
				return noValueWith(c.GetBatch(ctx, emailable.GetBatchRequest{BatchID: testBatchID, Partial: true}))
			}
		}
		ops = append(ops, p)
	}
	return ops
}

// privacyOpNamed returns the operation called name. An unknown name is a bug
// in the test, so it panics with the name.
func privacyOpNamed(name string) privacyOp {
	for _, o := range privacyOps() {
		if o.name == name {
			return o
		}
	}
	panic("no privacy operation named " + strconv.Quote(name))
}

// privacyCallFunc makes one call of op through a client built with opts and
// returns its error.
type privacyCallFunc func(t *testing.T, op privacyOp, opts ...emailable.Option) error

// privacyFlow is a way for a call to fail.
type privacyFlow struct {
	name string
	// kind is the failure kind, or "" for the documented kind of status for
	// the operation.
	kind emailable.FailureKind
	// status is the CallError's StatusCode.
	status int
	// unsent reports a call that fails before its request reaches the
	// transport: no attempt.
	unsent bool
	// attempts is the number of requests made when it is more than one.
	attempts int
	// settled reports a failure whose outcome is known even for a paid call.
	settled bool
	// diag reports that the provider's message reaches Diagnostics with
	// WithProviderDiagnostics.
	diag bool
	// rescue reports a body from which CreateBatch reads the new batch's ID.
	rescue bool
	// field is the CallError's Field.
	field string
	call  privacyCallFunc
}

// privacyCase is one flow applied to one operation.
type privacyCase struct {
	op   privacyOp
	flow privacyFlow
}

// want returns the CallError the case must produce.
func (c privacyCase) want() wantCall {
	f := c.flow
	kind := f.kind
	if kind == "" {
		kind = statusKind(c.op.op, f.status)
	}
	w := wantCall{Kind: kind, Op: c.op.op, Status: f.status, BatchID: c.op.batchID, Attempts: 1, Field: f.field}
	switch {
	case f.unsent:
		w.Attempts = 0
	case f.attempts > 0:
		w.Attempts = f.attempts
	}
	if kind == emailable.FailurePending && c.op.helper {
		// The wait repeats the pending verification until its budget ends.
		w.Kind, w.Attempts = emailable.FailureBudgetExhausted, privacyWaitAttempts
	}
	w.OutcomeUnknown = w.Attempts > 0 && !f.settled && outcomeUnknownAfter(c.op.paid, w.Kind)
	if f.rescue && c.op.op == emailable.OpCreateBatch {
		w.BatchID, w.OutcomeUnknown = testBatchID, false
	}
	return w
}

// privacyInBubble runs call inside a synctest bubble, so that timeouts and
// pauses elapse on the bubble's clock, and returns its error.
func privacyInBubble(t *testing.T, call func(t *testing.T) error) error {
	t.Helper()
	var err error
	synctest.Test(t, func(t *testing.T) { err = call(t) })
	return err
}

// privacyRun calls op through rt, inside a synctest bubble, with a client
// built with opts.
func privacyRun(t *testing.T, op privacyOp, rt http.RoundTripper, opts ...emailable.Option) error {
	t.Helper()
	return privacyInBubble(t, func(t *testing.T) error {
		t.Helper()
		return op.call(t.Context(), newTransportClient(t, rt, opts...))
	})
}

// privacyHeader returns response headers that echo secrets, with contentType
// when it is not empty.
func privacyHeader(contentType string) http.Header {
	h := http.Header{
		"X-Debug":          {testKey + " " + testEmail + " " + testCaptcha},
		"Set-Cookie":       {"session=" + privacyHookMarker + "; Path=/"},
		"Www-Authenticate": {`Bearer realm="` + testKey + `"`},
		"Ratelimit-Limit":  {"100"},
		"Ratelimit-Reset":  {testKey},
	}
	if contentType != "" {
		h.Set("Content-Type", contentType)
	}
	return h
}

// privacyRedirectHeader returns hostile response headers with a Location.
func privacyRedirectHeader(location string) http.Header {
	h := privacyHeader(mediaJSON)
	h.Set("Location", location)
	return h
}

// privacyBodyCall returns a call answered in memory with status, header and
// a body newBody makes for each request, declared with contentLength.
func privacyBodyCall(status int, header http.Header, contentLength int64, newBody func() io.ReadCloser,
	flowOpts ...emailable.Option,
) privacyCallFunc {
	rt := testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		return testutil.TransportWith(status, header, contentLength, newBody())(r)
	})
	return func(t *testing.T, op privacyOp, opts ...emailable.Option) error {
		t.Helper()
		return privacyRun(t, op, rt, slices.Concat(flowOpts, opts)...)
	}
}

// privacyAnswer returns a call answered in memory with status, header and
// body, declared with contentLength.
func privacyAnswer(status int, header http.Header, body string, contentLength int64,
	flowOpts ...emailable.Option,
) privacyCallFunc {
	return privacyBodyCall(status, header, contentLength, func() io.ReadCloser {
		return io.NopCloser(strings.NewReader(body))
	}, flowOpts...)
}

// privacyStatic returns a call answered with status and a JSON body, declared
// with its length.
func privacyStatic(status int, body string, flowOpts ...emailable.Option) privacyCallFunc {
	return privacyAnswer(status, privacyHeader(mediaJSON), body, int64(len(body)), flowOpts...)
}

// privacyScripted returns a call answered by each step in turn, with hostile
// headers.
func privacyScripted(steps ...testutil.Step) privacyCallFunc {
	return func(t *testing.T, op privacyOp, opts ...emailable.Option) error {
		t.Helper()
		for i := range steps {
			steps[i].Header = privacyHeader(mediaJSON)
		}
		return privacyRun(t, op, testutil.NewScript(steps...), opts...)
	}
}

// privacyEcho reads r and returns everything it carries, the body and the
// URL with the address and the captcha response included, with more markers.
func privacyEcho(r *http.Request) string {
	var body []byte
	if r.Body != nil {
		body, _ = io.ReadAll(r.Body)
		_ = r.Body.Close()
	}
	return string(body) + " " + r.URL.String() + " " + r.Header.Get("Authorization") + " " + privacyDiagMessage +
		" " + privacyTransport
}

// privacyEchoErr returns an error whose text is echo.
func privacyEchoErr(echo string) error { return fmt.Errorf("%w: %s", errPrivacyEcho, echo) }

// privacyFailWith returns a call whose round trip fails with the error build
// makes from the request.
func privacyFailWith(build func(echo string) error) privacyCallFunc {
	rt := testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, build(privacyEcho(r))
	})
	return func(t *testing.T, op privacyOp, opts ...emailable.Option) error {
		t.Helper()
		return privacyRun(t, op, rt, opts...)
	}
}

// privacyReadFails returns a call answered on HTTP 200, whose body fails
// after a prefix that echoes data with the error build makes from the
// request.
func privacyReadFails(build func(echo string) error) privacyCallFunc {
	rt := testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		prefix := `{"message":"` + privacyDiagMessage
		body := io.MultiReader(strings.NewReader(prefix), iotest.ErrReader(build(privacyEcho(r))))
		return testutil.TransportWith(http.StatusOK, privacyHeader(mediaJSON), -1, io.NopCloser(body))(r)
	})
	return func(t *testing.T, op privacyOp, opts ...emailable.Option) error {
		t.Helper()
		return privacyRun(t, op, rt, opts...)
	}
}

// privacyStalled returns a body factory for a testutil.Body over content
// that returns nothing forever after it.
func privacyStalled(content string) func() io.ReadCloser {
	return func() io.ReadCloser {
		b := testutil.NewBody(content)
		b.Stall = true
		return b
	}
}

// privacyBlocked returns a body factory for a testutil.Body over content that
// blocks after it until it is closed.
func privacyBlocked(content string) func() io.ReadCloser {
	return func() io.ReadCloser { return testutil.NewBlockingBody(content) }
}

// privacyBlockUntilDone is a round trip that echoes the request, waits for
// its context to end, and fails with an error that quotes the request.
func privacyBlockUntilDone(r *http.Request) (*http.Response, error) {
	echo := privacyEcho(r)
	<-r.Context().Done()
	return nil, fmt.Errorf("%w: %s: %w", errPrivacyEcho, echo, r.Context().Err())
}

// privacyStatusFlows are failures decided by an HTTP status other than 200.
func privacyStatusFlows() []privacyFlow {
	body := privacyDiagBody()
	msg := testutil.JSONQuote(privacyDiagMessage)
	html := "<html><body>" + privacyDiagMessage + "</body></html>"
	huge := `{"message":` + msg + `,"pad":"` + strings.Repeat("p", 70<<10) + `"}`
	gzip := privacyHeader(mediaJSON)
	gzip.Set("Content-Encoding", "gzip")
	codes := []int{
		emailable.StatusPending, 400, 401, 402, 403, 404, 409, 422, 429, 500, 502, 503, 504, 202, 204, 302, 600,
	}
	flows := make([]privacyFlow, 0, len(codes)+16)
	for _, code := range codes {
		flows = append(flows, privacyFlow{
			name: "HTTP " + strconv.Itoa(code), status: code, diag: true, call: privacyStatic(code, body),
		})
	}
	return append(flows,
		privacyFlow{name: "HTTP 302 hostile Location", status: http.StatusFound, diag: true, call: privacyAnswer(
			http.StatusFound, privacyRedirectHeader("https://"+privacyLocation+".example.com/next?key="+testKey+
				"&email="+testEmail), body, int64(len(body)),
		)},
		privacyFlow{name: "HTTP 307 unparsable Location", status: http.StatusTemporaryRedirect, call: privacyAnswer(
			http.StatusTemporaryRedirect, privacyRedirectHeader("https://%zz"+privacyLocation+"/?key="+testKey),
			html, -1,
		)},
		privacyFlow{name: "HTTP 308 relative Location", status: http.StatusPermanentRedirect, call: privacyAnswer(
			http.StatusPermanentRedirect, privacyRedirectHeader("/v1/"+privacyLocation+"?key="+testKey), "", 0,
		)},
		privacyFlow{name: "HTTP 301 Location with userinfo", status: http.StatusMovedPermanently, call: privacyAnswer(
			http.StatusMovedPermanently, privacyRedirectHeader("https://user:"+privacyHookMarker+"@"+privacyLocation+
				".lists.example.com/"), html, int64(len(html)),
		)},
		privacyFlow{name: "HTTP 101 body never read", status: http.StatusSwitchingProtocols, call: privacyAnswer(
			http.StatusSwitchingProtocols, privacyHeader(""), body, -1,
		)},
		privacyFlow{name: "HTTP 500 HTML", status: http.StatusInternalServerError, call: privacyAnswer(
			http.StatusInternalServerError, privacyHeader("text/html"), html, int64(len(html)),
		)},
		privacyFlow{name: "HTTP 400 message not a string", status: http.StatusBadRequest, call: privacyStatic(
			http.StatusBadRequest, `{"message":{"text":`+msg+`}}`,
		)},
		privacyFlow{name: "HTTP 400 repeated message", status: http.StatusBadRequest, call: privacyStatic(
			http.StatusBadRequest, `{"message":`+msg+`,"Message":`+msg+`}`,
		)},
		privacyFlow{name: "HTTP 400 trailing data", status: http.StatusBadRequest, call: privacyStatic(
			http.StatusBadRequest, body+" "+msg,
		)},
		privacyFlow{name: "HTTP 403 invalid UTF-8", status: http.StatusForbidden, call: privacyStatic(
			http.StatusForbidden, `{"message":"`+privacyDiagMessage+"\xff"+`"}`,
		)},
		privacyFlow{name: "HTTP 404 over the excerpt", status: http.StatusNotFound, call: privacyStatic(
			http.StatusNotFound, huge,
		)},
		privacyFlow{name: "HTTP 404 over the excerpt undeclared", status: http.StatusNotFound, call: privacyAnswer(
			http.StatusNotFound, privacyHeader(mediaJSON), huge, -1,
		)},
		privacyFlow{name: "HTTP 429 undecoded gzip", status: http.StatusTooManyRequests, call: privacyAnswer(
			http.StatusTooManyRequests, gzip, body, int64(len(body)),
		)},
		// The excerpt is best effort: a complete body that ends early, as an
		// injected transport may end it, still yields the message.
		privacyFlow{
			name: "HTTP 503 declared longer than its body", status: http.StatusServiceUnavailable, diag: true,
			call: privacyAnswer(http.StatusServiceUnavailable, privacyHeader(mediaJSON), body, int64(len(body)+100)),
		},
		privacyFlow{name: "HTTP 500 stalled body", status: http.StatusInternalServerError, call: privacyBodyCall(
			http.StatusInternalServerError, privacyHeader(mediaJSON), -1, privacyStalled(body[:40]),
		)},
		privacyFlow{name: "HTTP 502 body blocks until the timeout", status: http.StatusBadGateway, call: privacyBodyCall(
			http.StatusBadGateway, privacyHeader(mediaJSON), -1, privacyBlocked(body[:40]),
		)},
		privacyFlow{name: "HTTP 401 body read fails", status: http.StatusUnauthorized, call: privacyBodyCall(
			http.StatusUnauthorized, privacyHeader(mediaJSON), -1, func() io.ReadCloser {
				return io.NopCloser(io.MultiReader(strings.NewReader(body[:40]),
					iotest.ErrReader(privacyEchoErr(privacyDiagMessage))))
			},
		)},
	)
}

// privacyBodyFlows are HTTP 200 bodies that cannot be read.
func privacyBodyFlows() []privacyFlow {
	const (
		ok        = http.StatusOK
		malformed = emailable.FailureMalformedResponse
	)
	msg := testutil.JSONQuote(privacyDiagMessage)
	id := `"id":"` + testBatchID + `"`
	pad := messageBody(strings.Repeat(privacyDiagMessage, 8))
	limit := emailable.WithMaxResponseBytes(1024)
	gzip := privacyHeader(mediaJSON)
	gzip.Set("Content-Encoding", "gzip")
	containers := `{"message":` + msg + `,"pad":[` + strings.Repeat("[],", 100_000) + `[]]}`
	return []privacyFlow{
		{name: "invalid JSON", kind: malformed, status: ok, call: privacyStatic(ok, `{"message":`+msg)},
		{name: "trailing data", kind: malformed, status: ok, call: privacyStatic(ok, `{"message":`+msg+`} `+msg)},
		{name: "repeated key", kind: malformed, status: ok, call: privacyStatic(ok,
			`{"message":`+msg+`,"message":`+msg+`}`)},
		{name: "case variant key", kind: malformed, status: ok, call: privacyStatic(ok,
			`{"Message":`+msg+`,"message":`+msg+`}`)},
		{name: "escaped repeated key", kind: malformed, status: ok, call: privacyStatic(ok,
			testutil.Esc(`{"message":`+msg+`,"~u006dessage":`+msg+`}`))},
		{name: "invalid UTF-8", kind: malformed, status: ok, call: privacyStatic(ok,
			`{"message":"`+privacyDiagMessage+"\xff"+`"}`)},
		{name: "top-level array", kind: emailable.FailureUnexpectedResponse, status: ok, call: privacyStatic(ok,
			`[`+msg+`]`)},
		{name: "top-level string", kind: emailable.FailureUnexpectedResponse, status: ok, call: privacyStatic(ok, msg)},
		{name: "empty body", kind: malformed, status: ok, call: privacyStatic(ok, "")},
		{name: "undecoded gzip", kind: emailable.FailureUnexpectedResponse, status: ok, call: privacyAnswer(ok, gzip,
			privacyDiagBody(), int64(len(privacyDiagBody())))},
		{name: "too many containers", kind: emailable.FailureResponseTooLarge, status: ok, call: privacyStatic(ok,
			containers)},
		{name: "declared too large", kind: emailable.FailureResponseTooLarge, status: ok, call: privacyStatic(ok, pad,
			limit)},
		{name: "streamed too large", kind: emailable.FailureResponseTooLarge, status: ok, call: privacyAnswer(ok,
			privacyHeader(mediaJSON), pad, -1, limit)},
		{name: "body shorter than declared", kind: emailable.FailureTransport, status: ok, call: privacyAnswer(ok,
			privacyHeader(mediaJSON), pad, int64(len(pad)+100))},
		{name: "body longer than declared", kind: emailable.FailureTransport, status: ok, call: privacyAnswer(ok,
			privacyHeader(mediaJSON), pad, int64(len(pad)-10))},
		{name: "read fails mid-body", kind: emailable.FailureTransport, status: ok, call: privacyReadFails(
			privacyEchoErr,
		)},
		{name: "read times out mid-body", kind: emailable.FailureTimeout, status: ok, call: privacyReadFails(
			func(echo string) error { return testutil.TimeoutError{Msg: echo} },
		)},
		{name: "stalled body", kind: emailable.FailureTransport, status: ok, call: privacyBodyCall(ok,
			privacyHeader(mediaJSON), -1, privacyStalled(pad[:60]))},
		{name: "SDK timeout mid-body", kind: emailable.FailureTimeout, status: ok, call: privacyBodyCall(ok,
			privacyHeader(mediaJSON), int64(len(pad)), privacyBlocked(pad[:60]))},
		// CreateBatch still reads the new batch's ID from these.
		{name: "trailing data after an id", kind: malformed, status: ok, rescue: true, call: privacyStatic(ok,
			`{`+id+`,"message":`+msg+`} `+msg)},
		{name: "repeated key with an id", kind: malformed, status: ok, rescue: true, call: privacyStatic(ok,
			`{`+id+`,"message":`+msg+`,"message":`+msg+`}`)},
	}
}

// privacyTransportFlows are round trips that fail with errors quoting the
// request.
func privacyTransportFlows() []privacyFlow {
	const transport = emailable.FailureTransport
	wrap := func(target error) func(echo string) error {
		return func(echo string) error { return fmt.Errorf("%w: %s: %w", errPrivacyEcho, echo, target) }
	}
	return []privacyFlow{
		{name: "echoing error", kind: transport, call: privacyFailWith(privacyEchoErr)},
		{name: "url.Error with secrets in the URL", kind: transport, call: privacyFailWith(func(echo string) error {
			return &url.Error{
				Op:  http.MethodGet,
				URL: loopbackOrigin + pathVerify + "?email=" + testEmail + "&captcha_response=" + testCaptcha + "&key=" + testKey,
				Err: privacyEchoErr(echo),
			}
		})},
		{name: "dial", kind: transport, call: privacyFailWith(func(echo string) error {
			return &net.OpError{Op: "dial", Net: "tcp", Err: privacyEchoErr(echo)}
		})},
		{name: "proxy with credentials", kind: transport, call: privacyFailWith(func(echo string) error {
			return &net.OpError{Op: "proxyconnect", Net: "tcp", Err: &url.Error{
				Op: "Get", URL: "http://proxyuser:" + privacyHookMarker + "@proxy.example.com:3128", Err: privacyEchoErr(echo),
			}}
		})},
		{name: "DNS", kind: transport, call: privacyFailWith(func(echo string) error {
			return &net.DNSError{Err: echo, Name: privacyLocation + ".example.com", Server: "192.0.2.53:53", IsNotFound: true}
		})},
		{name: "TLS record header", kind: transport, call: privacyFailWith(func(echo string) error {
			return tls.RecordHeaderError{Msg: echo}
		})},
		{name: "TLS certificate", kind: transport, call: privacyFailWith(func(echo string) error {
			return &tls.CertificateVerificationError{Err: privacyEchoErr(echo)}
		})},
		{name: "TLS alert", kind: transport, call: privacyFailWith(wrap(tls.AlertError(42)))},
		{name: "connection reset", kind: transport, call: privacyFailWith(wrap(syscall.ECONNRESET))},
		{name: "connection aborted", kind: transport, call: privacyFailWith(wrap(syscall.ECONNABORTED))},
		{name: "broken pipe", kind: transport, call: privacyFailWith(wrap(syscall.EPIPE))},
		{name: "unexpected EOF", kind: transport, call: privacyFailWith(wrap(io.ErrUnexpectedEOF))},
		{name: "closed connection", kind: transport, call: privacyFailWith(wrap(net.ErrClosed))},
		{name: "network timeout", kind: emailable.FailureTimeout, call: privacyFailWith(func(echo string) error {
			return testutil.TimeoutError{Msg: echo}
		})},
		{name: "deadline error", kind: emailable.FailureTimeout, call: privacyFailWith(wrap(context.DeadlineExceeded))},
	}
}

// privacyContextFlows are SDK timeouts and the caller's cancellations and
// deadlines, before the call, in flight and while the body is read.
func privacyContextFlows() []privacyFlow {
	block := testutil.RoundTripFunc(privacyBlockUntilDone)
	blockedBody := testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		return testutil.TransportWith(http.StatusOK, nil, -1, testutil.NewBlockingBody(`{"message":"`+privacyDiagMessage))(r)
	})
	inBubble := func(rt http.RoundTripper, ctxOf contextFactory) privacyCallFunc {
		return func(t *testing.T, op privacyOp, opts ...emailable.Option) error {
			t.Helper()
			return privacyInBubble(t, func(t *testing.T) error {
				t.Helper()
				ctx, cancel := ctxOf(t.Context())
				defer cancel()
				return op.call(ctx, newTransportClient(t, rt, opts...))
			})
		}
	}
	second := expiresAfter(time.Second)
	return []privacyFlow{
		{name: "SDK timeout", kind: emailable.FailureTimeout, call: func(t *testing.T, op privacyOp, opts ...emailable.Option) error {
			t.Helper()
			return inBubble(block, cancelable)(t, op, append([]emailable.Option{emailable.WithTimeout(15 * time.Second)}, opts...)...)
		}},
		{name: "canceled in flight", kind: emailable.FailureCanceled, call: func(t *testing.T, op privacyOp, opts ...emailable.Option) error {
			t.Helper()
			return privacyInBubble(t, func(t *testing.T) error {
				t.Helper()
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				rt := testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
					cancel()
					return privacyBlockUntilDone(r)
				})
				return op.call(ctx, newTransportClient(t, rt, opts...))
			})
		}},
		{name: "canceled before sending", kind: emailable.FailureCanceled, unsent: true, call: inBubble(block, alreadyCanceled)},
		{name: "deadline in flight", kind: emailable.FailureDeadlineExceeded, call: inBubble(block, second)},
		{name: "deadline before sending", kind: emailable.FailureDeadlineExceeded, unsent: true, call: inBubble(block, expiresAfter(-time.Second))},
		{
			name: "deadline mid-body", kind: emailable.FailureDeadlineExceeded, status: http.StatusOK,
			call: inBubble(blockedBody, second),
		},
	}
}

// privacyGenericFlows are the flows every operation can meet.
func privacyGenericFlows() []privacyFlow {
	return slices.Concat(privacyStatusFlows(), privacyBodyFlows(), privacyTransportFlows(), privacyContextFlows())
}

// privacyDecodeFlow returns an HTTP 200 flow of kind at field answered with
// body.
func privacyDecodeFlow(name string, kind emailable.FailureKind, field, body string) privacyFlow {
	return privacyFlow{
		name: name, kind: kind, status: http.StatusOK, field: field, call: privacyStatic(http.StatusOK, body),
	}
}

// privacyVerifyFlows are verify answers that fail to decode while echoing
// data.
func privacyVerifyFlows() []privacyFlow {
	const (
		malformed  = emailable.FailureMalformedResponse
		unexpected = emailable.FailureUnexpectedResponse
		tooLarge   = emailable.FailureResponseTooLarge
	)
	msg := testutil.JSONQuote(privacyDiagMessage)
	email := testutil.JSONQuote(testEmail)
	ok := func(members string) string { return `{"email":` + email + `,"state":"deliverable",` + members + `}` }
	return []privacyFlow{
		privacyDecodeFlow("email missing", malformed, "email", `{"state":"deliverable","message":`+msg+`}`),
		privacyDecodeFlow("email empty", malformed, "email", `{"email":"","state":"deliverable","message":`+msg+`}`),
		privacyDecodeFlow("email of the wrong type", malformed, "email", `{"email":[`+email+`],"state":"deliverable"}`),
		privacyDecodeFlow("email over 4 KiB", tooLarge, "email", `{"email":`+privacyBig()+`,"state":"deliverable"}`),
		privacyDecodeFlow("state missing", malformed, "state", `{"email":`+email+`,"message":`+msg+`}`),
		privacyDecodeFlow("state echoes the address", unexpected, "state", `{"email":`+email+`,"state":`+email+`}`),
		privacyDecodeFlow("state echoes provider text", unexpected, "state", `{"email":`+email+`,"state":`+msg+`}`),
		privacyDecodeFlow("reason echoes the address", unexpected, "reason", ok(`"reason":`+email)),
		privacyDecodeFlow("score out of range", malformed, "score", ok(`"score":101,"message":`+msg)),
		privacyDecodeFlow("score echoes data", malformed, "score", ok(`"score":`+msg)),
		privacyDecodeFlow("flag echoes data", malformed, "free", ok(`"free":`+msg)),
		privacyDecodeFlow("birth year out of range", malformed, "birth_year", ok(`"birth_year":10000`)),
		privacyDecodeFlow("did_you_mean over 4 KiB", tooLarge, "did_you_mean", ok(`"did_you_mean":`+privacyBig())),
		privacyDecodeFlow("domain of the wrong type", malformed, "domain", ok(`"domain":[`+email+`]`)),
		privacyDecodeFlow("duration negative", malformed, "duration", ok(`"duration":-1,"message":`+msg)),
		privacyDecodeFlow("duration echoes data", malformed, "duration", ok(`"duration":`+msg)),
		privacyDecodeFlow("member names echo data", malformed, "email", `{`+msg+`:`+msg+`,`+email+`:1}`),
	}
}

// privacyCreateFlows are batch creation answers without a usable ID.
func privacyCreateFlows() []privacyFlow {
	const (
		malformed  = emailable.FailureMalformedResponse
		unexpected = emailable.FailureUnexpectedResponse
	)
	msg := testutil.JSONQuote(privacyDiagMessage)
	return []privacyFlow{
		privacyDecodeFlow("id missing", malformed, "id", `{"message":`+msg+`}`),
		privacyDecodeFlow("id null", malformed, "id", `{"id":null,"message":`+msg+`}`),
		privacyDecodeFlow("id of the wrong type", malformed, "id", `{"id":[`+msg+`]}`),
		privacyDecodeFlow("id echoes provider text", unexpected, "id", `{"id":`+msg+`}`),
		privacyDecodeFlow("id echoes the address with a space", unexpected, "id", `{"id":"`+testEmail+` x"}`),
		privacyDecodeFlow("id over 256 bytes", unexpected, "id", `{"id":"`+strings.Repeat("b", 257)+`","message":`+msg+`}`),
		// Two keys fold to "id", so the ID is in doubt and is not read.
		privacyDecodeFlow("id repeated in another case", malformed, "",
			`{"id":"`+testBatchID+`","ID":"other","message":`+msg+`}`),
		// An echoed secret is never kept as the rescued ID: GoString prints
		// BatchID.
		privacyDecodeFlow("rescued id is the API key", malformed, "", `{"id":"`+testKey+`","message":`+msg+`} x`),
		privacyDecodeFlow("rescued id holds the API key", malformed, "",
			`{"id":"batch-`+testKey+`-1","message":`+msg+`,"message":`+msg+`}`),
		privacyDecodeFlow("rescued id is the callback URL", malformed, "", `{"id":"`+testHook+`"} `+msg),
		privacyDecodeFlow("rescued id holds the escaped callback URL", malformed, "",
			`{"id":"u=`+privacyHookEscaped+`"} `+msg),
	}
}

// privacyGetFlows are batch snapshots that fail to decode while echoing data.
func privacyGetFlows() []privacyFlow {
	const (
		malformed  = emailable.FailureMalformedResponse
		unexpected = emailable.FailureUnexpectedResponse
		tooLarge   = emailable.FailureResponseTooLarge
	)
	msg := testutil.JSONQuote(privacyDiagMessage)
	email := testutil.JSONQuote(testEmail)
	id := `"id":"` + testBatchID + `"`
	keys := make([]string, 257)
	for i := range keys {
		keys[i] = `"k` + strconv.Itoa(i) + `":1`
	}
	bigFile := testutil.JSONQuote(privacyDownload + "&pad=" + strings.Repeat(privacyDownloadSig, 500))
	return []privacyFlow{
		privacyDecodeFlow("another batch", unexpected, "id", `{"id":"other-batch","message":`+msg+`}`),
		privacyDecodeFlow("id echoes provider text", unexpected, "id", `{"id":`+msg+`}`),
		privacyDecodeFlow("no documented member", unexpected, "", `{`+msg+`:1}`),
		privacyDecodeFlow("id of the wrong type", malformed, "id", `{"id":[`+msg+`]}`),
		privacyDecodeFlow("message of the wrong type", malformed, "message", `{`+id+`,"message":[`+msg+`]}`),
		privacyDecodeFlow("message over 4 KiB", tooLarge, "message", `{`+id+`,"message":`+privacyBig()+`}`),
		privacyDecodeFlow("processed echoes data", malformed, "processed", `{`+id+`,"processed":`+msg+`}`),
		privacyDecodeFlow("total negative", malformed, "total", `{`+id+`,"total":-1,"message":`+msg+`}`),
		privacyDecodeFlow("emails not an array", malformed, "emails", `{`+id+`,"emails":`+msg+`}`),
		privacyDecodeFlow("row not an object", malformed, "emails[0]", `{`+id+`,"emails":[`+msg+`]}`),
		privacyDecodeFlow("row email over 4 KiB", tooLarge, "emails[0].email", `{`+id+`,"emails":[{"email":`+privacyBig()+`}]}`),
		privacyDecodeFlow("row state echoes the address", unexpected, "emails[1].state",
			`{`+id+`,"emails":[{},{"email":`+email+`,"state":`+email+`}]}`),
		privacyDecodeFlow("row score echoes data", malformed, "emails[0].score", `{`+id+`,"emails":[{"score":`+msg+`}]}`),
		privacyDecodeFlow("download_file over 8 KiB", tooLarge, "download_file", `{`+id+`,"download_file":`+bigFile+`}`),
		privacyDecodeFlow("download_file of the wrong type", malformed, "download_file",
			`{`+id+`,"download_file":[`+testutil.JSONQuote(privacyDownload)+`]}`),
		privacyDecodeFlow("download_file beside a bad member", malformed, "reason_counts",
			`{`+id+`,"download_file":`+testutil.JSONQuote(privacyDownload)+`,"reason_counts":`+msg+`}`),
		privacyDecodeFlow("reason_counts key echoes the address", unexpected, "reason_counts",
			`{`+id+`,"reason_counts":{`+email+`:1}}`),
		privacyDecodeFlow("total_counts value echoes data", malformed, "total_counts.processed",
			`{`+id+`,"total_counts":{"processed":`+msg+`}}`),
		privacyDecodeFlow("over 256 count keys", tooLarge, "total_counts",
			`{`+id+`,"total_counts":{`+strings.Join(keys, ",")+`}}`),
	}
}

// privacyAccountFlows are account answers that fail to decode while echoing
// data.
func privacyAccountFlows() []privacyFlow {
	const malformed = emailable.FailureMalformedResponse
	msg := testutil.JSONQuote(privacyDiagMessage)
	owner := `"owner_email":` + testutil.JSONQuote(testOwner)
	return []privacyFlow{
		privacyDecodeFlow("owner_email missing", malformed, "owner_email", `{"available_credits":1,"message":`+msg+`}`),
		privacyDecodeFlow("owner_email of the wrong type", malformed, "owner_email",
			`{"owner_email":[`+testutil.JSONQuote(testOwner)+`],"available_credits":1}`),
		privacyDecodeFlow("owner_email over 4 KiB", emailable.FailureResponseTooLarge, "owner_email",
			`{"owner_email":`+privacyBig()+`,"available_credits":1}`),
		privacyDecodeFlow("credits missing", malformed, "available_credits", `{`+owner+`,"message":`+msg+`}`),
		privacyDecodeFlow("credits echo data", malformed, "available_credits", `{`+owner+`,"available_credits":`+msg+`}`),
		privacyDecodeFlow("credits negative", malformed, "available_credits", `{`+owner+`,"available_credits":-1}`),
		privacyDecodeFlow("credits fractional", malformed, "available_credits", `{`+owner+`,"available_credits":1.5}`),
		privacyDecodeFlow("credits over int64", malformed, "available_credits",
			`{`+owner+`,"available_credits":9223372036854775808}`),
	}
}

// privacyWaitFlows are WaitForVerification outcomes after a pending answer.
func privacyWaitFlows() []privacyFlow {
	pendingStep := testutil.Step{Status: emailable.StatusPending, Body: privacyDiagBody()}
	// pausing answers every attempt with HTTP 249, within a context ctxOf
	// derives, which the answer cancels when cancelOnAnswer is set, so that
	// the context ends while the wait pauses.
	pausing := func(ctxOf contextFactory, cancelOnAnswer bool) privacyCallFunc {
		return func(t *testing.T, op privacyOp, opts ...emailable.Option) error {
			t.Helper()
			return privacyInBubble(t, func(t *testing.T) error {
				t.Helper()
				ctx, cancel := ctxOf(t.Context())
				defer cancel()
				rt := testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
					if cancelOnAnswer {
						cancel()
					}
					return testutil.Reply(r, pendingStep)
				})
				return op.call(ctx, newTransportClient(t, rt, opts...))
			})
		}
	}
	return []privacyFlow{
		{
			name: "upstream failure after a pending answer", status: http.StatusInternalServerError, attempts: 2,
			diag: true, call: privacyScripted(pendingStep, testutil.Step{
				Status: http.StatusInternalServerError, Body: privacyDiagBody(),
			}),
		},
		{
			name: "malformed answer after a pending answer", kind: emailable.FailureMalformedResponse,
			status: http.StatusOK, attempts: 2, call: privacyScripted(pendingStep, testutil.Step{
				Body: `{"message":` + testutil.JSONQuote(privacyDiagMessage),
			}),
		},
		// An error between attempts carries the last 249's status, attempts
		// and diagnostics.
		{
			name: "canceled during a pause", kind: emailable.FailureCanceled, status: emailable.StatusPending,
			settled: true, diag: true, call: pausing(cancelable, true),
		},
		{
			name: "deadline during a pause", kind: emailable.FailureDeadlineExceeded, status: emailable.StatusPending,
			settled: true, diag: true, call: pausing(expiresAfter(time.Second), false),
		},
	}
}

// privacyCases returns every flow on every operation it applies to.
func privacyCases() []privacyCase {
	var out []privacyCase
	add := func(flows []privacyFlow, names ...string) {
		for _, name := range names {
			op := privacyOpNamed(name)
			for _, f := range flows {
				out = append(out, privacyCase{op: op, flow: f})
			}
		}
	}
	generic := privacyGenericFlows()
	for _, op := range privacyOps() {
		add(generic, op.name)
	}
	add(privacyVerifyFlows(), "VerifyEmail", "WaitForVerification")
	add(privacyCreateFlows(), "CreateBatch")
	add(privacyGetFlows(), "GetBatch")
	add(privacyAccountFlows(), "AccountInfo")
	add(privacyWaitFlows(), "WaitForVerification")
	return out
}

// TestPrivacyCallErrorsNeverPrintSecrets runs every failure flow on every
// operation with WithProviderDiagnostics. No marker reaches Error(), any fmt
// verb, the Unwrap chain, a wrapped or joined form, a reflection walk or the
// error's JSON; the provider's message reaches only Diagnostics, scrubbed.
func TestPrivacyCallErrorsNeverPrintSecrets(t *testing.T) {
	secrets := privacySecrets()
	for _, c := range privacyCases() {
		t.Run(c.op.name+"/"+c.flow.name, func(t *testing.T) {
			err := c.flow.call(t, c.op, emailable.WithProviderDiagnostics())
			ce := assertCallError(t, err, c.want(), privacyExtraSecrets()...)
			data, jerr := json.Marshal(err)
			require.NoError(t, jerr)
			testutil.AssertRedacted(t, string(data), secrets, "JSON")
			if c.flow.diag {
				assert.Equal(t, c.op.diagWant(), ce.Diagnostics().Message, "only the key and the call's secrets are scrubbed")
			} else {
				assert.Empty(t, ce.Diagnostics().Message, "no readable provider message")
			}
		})
	}
}

func TestPrivacyDiagnosticsOnlyWithOptIn(t *testing.T) {
	for _, c := range privacyCases() {
		t.Run(c.op.name+"/"+c.flow.name, func(t *testing.T) {
			err := c.flow.call(t, c.op)
			ce := assertCallError(t, err, c.want(), privacyExtraSecrets()...)
			assert.Equal(t, emailable.ProviderDiagnostics{}, ce.Diagnostics(), "no provider text without the option")
		})
	}
}

// TestPrivacyErrorBodyUnreadByDefault proves that without
// WithProviderDiagnostics the SDK never reads the body of an error
// response, so provider text never even reaches memory it owns.
func TestPrivacyErrorBodyUnreadByDefault(t *testing.T) {
	for _, op := range privacyOps() {
		for _, diag := range []bool{false, true} {
			t.Run(op.name+"/diagnostics "+strconv.FormatBool(diag), func(t *testing.T) {
				var bodies []*testutil.Body
				rt := testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
					b := testutil.NewBody(privacyDiagBody())
					bodies = append(bodies, b)
					return testutil.TransportWith(http.StatusBadRequest, nil, -1, b)(r)
				})
				var opts []emailable.Option
				if diag {
					opts = append(opts, emailable.WithProviderDiagnostics())
				}
				err := op.call(t.Context(), newTransportClient(t, rt, opts...))
				ce := callError(t, err)
				require.Len(t, bodies, 1)
				assert.Equal(t, 1, bodies[0].Closes(), "the body is closed once")
				if diag {
					assert.Positive(t, bodies[0].Reads())
					assert.Equal(t, op.diagWant(), ce.Diagnostics().Message)
					return
				}
				assert.Zero(t, bodies[0].Reads(), "the error body is never read")
			})
		}
	}
}

// privacyOddKey is an API key with characters a query string escapes.
const privacyOddKey = "test_odd+key/marker=7&x" // gitleaks:allow (synthetic test marker)

// privacyOddCaptcha is a captcha response with characters a query string
// escapes.
const privacyOddCaptcha = "cap+tcha/marker=1&x"

func TestPrivacyDiagnosticsScrubSecrets(t *testing.T) {
	require.Equal(t, privacyHookEscaped, url.QueryEscape(testHook))
	verifyOdd := func(ctx context.Context, c *emailable.Client) error {
		r := privacyVerifyRequest()
		r.CaptchaResponse = privacyOddCaptcha
		return noValueWith(c.VerifyEmail(ctx, r))
	}
	tests := []struct {
		name string
		op   string
		key  string                                               // the API key, testKey when empty
		call func(ctx context.Context, c *emailable.Client) error // the call, op's when nil
		raw  string                                               // the message as a JSON string literal's content
		want string
	}{
		{
			name: "key across the 1 KiB cut", op: "GetBatch",
			raw:  strings.Repeat("m", 1010) + testKey + " tail",
			want: strings.Repeat("m", 1010) + "[redacted] tai",
		},
		{
			name: "key spelled with escapes", op: "AccountInfo",
			raw:  testutil.Esc(`key=test~u005fkeymarker7f3a2c91 email=`) + testEmail,
			want: "key=[redacted] email=" + testEmail,
		},
		{
			name: "key repeated", op: "AccountInfo",
			raw:  testKey + testKey + " " + testKey,
			want: "[redacted][redacted] [redacted]",
		},
		{
			name: "captcha response of the call", op: "VerifyEmail",
			raw:  "captcha " + testCaptcha + " refused for " + testEmail,
			want: "captcha [redacted] refused for " + testEmail,
		},
		{
			name: "captcha response spelled with escapes", op: "WaitForVerification",
			raw:  testutil.Esc(`captcha~u006darker0b6e2f`),
			want: "[redacted]",
		},
		{
			name: "callback URL of the call", op: "CreateBatch",
			raw:  "bad url " + testHook + " for " + testOwner,
			want: "bad url [redacted] for " + testOwner,
		},
		{
			name: "callback URL spelled with escapes", op: "CreateBatch",
			raw:  testutil.Esc(`https:~/~/hooks.example.com~/emailable?token=hookmarker93c1d7`),
			want: "[redacted]",
		},
		{
			name: "callback URL escaped as in a query", op: "CreateBatch",
			raw:  "callback=" + privacyHookEscaped + "&x=1",
			want: "callback=[redacted]&x=1",
		},
		{
			name: "key with reserved characters, raw and escaped", op: "AccountInfo", key: privacyOddKey,
			raw:  "raw " + privacyOddKey + " escaped " + url.QueryEscape(privacyOddKey),
			want: "raw [redacted] escaped [redacted]",
		},
		{
			name: "captcha with reserved characters, raw and escaped", op: "VerifyEmail", call: verifyOdd,
			raw:  "captcha_response=" + url.QueryEscape(privacyOddCaptcha) + " was " + privacyOddCaptcha,
			want: "captcha_response=[redacted] was [redacted]",
		},
		{
			name: "another call's secrets are kept", op: "GetBatch",
			raw:  testCaptcha + " " + testHook,
			want: testCaptcha + " " + testHook,
		},
		{
			name: "multi-byte text cut on a rune boundary", op: "AccountInfo",
			raw:  strings.Repeat("€", 400),
			want: strings.Repeat("€", 341),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := `{"message":"` + tt.raw + `"}`
			key, call := testKey, privacyOpNamed(tt.op).call
			if tt.key != "" {
				key = tt.key
			}
			if tt.call != nil {
				call = tt.call
			}
			c, err := emailable.NewClient(key, emailable.WithTestBaseURL(loopbackOrigin), emailable.WithHTTPClient(
				&http.Client{Transport: testutil.StaticTransport(http.StatusBadRequest, body)},
			),
				emailable.WithProviderDiagnostics())
			require.NoError(t, err)
			err = call(t.Context(), c)
			msg := callError(t, err).Diagnostics().Message
			assert.Equal(t, tt.want, msg)
			assert.LessOrEqual(t, len(msg), 1024)
			assert.NotContains(t, msg, key[:8], "no prefix of the key survives the cut")
			assertErrorClean(t, err, append(leakMarkers(), key, privacyOddCaptcha, "mmmmmmmm", "€€€")...)
		})
	}
}

// TestPrivacyEchoedTokenNeverReachesField proves that a count key the
// provider echoes, which may have the token shape of an API key or a
// captcha response, never reaches CallError.Field and so Error().
func TestPrivacyEchoedTokenNeverReachesField(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"key as a total_counts key", `{"id":"` + testBatchID + `","total_counts":{"` + testKey + `":"x"}}`},
		{"captcha as a reason_counts key", `{"id":"` + testBatchID + `","reason_counts":{"` + testCaptcha + `":-5}}`},
		{"hook token as a total_counts key", `{"id":"` + testBatchID + `","total_counts":{"` + privacyHookMarker + `":-1}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name+"/GetBatch", func(t *testing.T) {
			c, _ := newScriptClient(t, testutil.Step{Body: tt.body})
			_, err := c.GetBatch(t.Context(), emailable.GetBatchRequest{BatchID: testBatchID})
			assert.Equal(t, emailable.FailureMalformedResponse, callError(t, err).Kind)
			assertErrorClean(t, err, leakMarkers()...)
		})
		t.Run(tt.name+"/DecodeBatchCallback", func(t *testing.T) {
			_, err := emailable.DecodeBatchCallback(strings.NewReader(tt.body), emailable.DefaultMaxResponseBytes)
			assert.Equal(t, emailable.FailureMalformedResponse, callError(t, err).Kind)
			assertErrorClean(t, err, leakMarkers()...)
		})
	}
}

// privacyCallback is one DecodeBatchCallback failure.
type privacyCallback struct {
	name     string
	body     func() io.Reader
	maxBytes int64
	want     wantCall
}

// privacyCallbackCases returns DecodeBatchCallback failures whose bodies and
// reader errors echo every marker.
func privacyCallbackCases() []privacyCallback {
	const (
		malformed  = emailable.FailureMalformedResponse
		unexpected = emailable.FailureUnexpectedResponse
		tooLarge   = emailable.FailureResponseTooLarge
		transport  = emailable.FailureTransport
	)
	msg := testutil.JSONQuote(privacyDiagMessage)
	email := testutil.JSONQuote(testEmail)
	id := `"id":"` + testBatchID + `"`
	text := func(s string) func() io.Reader { return func() io.Reader { return strings.NewReader(s) } }
	failing := func(err error) func() io.Reader {
		return func() io.Reader {
			return io.MultiReader(strings.NewReader(`{`+id+`,"message":"`+privacyDiagMessage), iotest.ErrReader(err))
		}
	}
	want := func(kind emailable.FailureKind, batchID, field string) wantCall {
		return wantCall{Kind: kind, Op: emailable.OpBatchCallback, BatchID: batchID, Field: field}
	}
	big := `{` + id + `,"message":` + testutil.JSONQuote(strings.Repeat(privacyDiagMessage, 8)) + `}`
	return []privacyCallback{
		{"reader fails", failing(privacyEchoErr(privacyDiagMessage)), 1 << 20, want(transport, "", "")},
		{"reader times out", failing(testutil.TimeoutError{Msg: privacyDiagMessage}), 1 << 20, want(transport, "", "")},
		{"reader stalls", func() io.Reader {
			b := testutil.NewBody(`{` + id + `,"message":"` + privacyDiagMessage)
			b.Stall = true
			return b
		}, 1 << 20, want(transport, "", "")},
		{"over maxBytes", text(big), 1024, want(tooLarge, "", "")},
		{"invalid JSON", text(`{"message":` + msg), 1 << 20, want(malformed, "", "")},
		{"trailing data after an id", text(`{` + id + `,"message":` + msg + `} ` + msg), 1 << 20, want(malformed, testBatchID, "")},
		{
			"repeated key with an id", text(`{` + id + `,"message":` + msg + `,"Message":` + msg + `}`), 1 << 20,
			want(malformed, testBatchID, ""),
		},
		{
			"invalid UTF-8 with an id", text(`{` + id + `,"message":"` + privacyDiagMessage + "\xff" + `"}`), 1 << 20,
			want(malformed, testBatchID, ""),
		},
		{"too many containers with an id", text(`{` + id + `,"message":` + msg + `,"pad":[` +
			strings.Repeat("[],", 100_000) + `[]]}`), 1 << 20, want(tooLarge, testBatchID, "")},
		{"top-level array", text(`[` + msg + `]`), 1 << 20, want(unexpected, "", "")},
		{"empty body", text(""), 1 << 20, want(malformed, "", "")},
		{"id missing", text(`{"message":` + msg + `,"emails":[{"email":` + email + `}]}`), 1 << 20, want(malformed, "", "id")},
		{"id echoes provider text", text(`{"id":` + msg + `}`), 1 << 20, want(unexpected, "", "id")},
		{"no documented member", text(`{` + msg + `:1}`), 1 << 20, want(malformed, "", "id")},
		{
			"row state echoes the address", text(`{` + id + `,"emails":[{"email":` + email + `,"state":` + email + `}]}`),
			1 << 20, want(unexpected, testBatchID, "emails[0].state"),
		},
		{
			"download_file over 8 KiB", text(`{` + id + `,"download_file":` +
				testutil.JSONQuote(privacyDownload+"&pad="+strings.Repeat(privacyDownloadSig, 500)) + `}`), 1 << 20,
			want(tooLarge, testBatchID, "download_file"),
		},
	}
}

func TestPrivacyBatchCallbackErrorsNeverPrintSecrets(t *testing.T) {
	secrets := privacySecrets()
	for _, tt := range privacyCallbackCases() {
		t.Run(tt.name, func(t *testing.T) {
			b, err := emailable.DecodeBatchCallback(tt.body(), tt.maxBytes)
			assert.Nil(t, b)
			_ = assertCallError(t, err, tt.want, privacyExtraSecrets()...)
			testutil.AssertShapesNeverPrint(t, callError(t, err), secrets...)
			data, jerr := json.Marshal(err)
			require.NoError(t, jerr)
			testutil.AssertRedacted(t, string(data), secrets, "JSON")
		})
	}
}

func TestPrivacyCallErrorShapesNeverPrintSecrets(t *testing.T) {
	secrets := privacySecrets()
	var prev *emailable.CallError
	for _, c := range privacyCases() {
		t.Run(c.op.name+"/"+c.flow.name, func(t *testing.T) {
			ce := callError(t, c.flow.call(t, c.op, emailable.WithProviderDiagnostics()))
			testutil.AssertShapesNeverPrint(t, ce, secrets...)
			if prev != nil {
				// The private function makes two SDK errors always differ.
				testutil.AssertDiffRedacted(t, prev, ce, secrets...)
			}
			prev = ce
		})
	}
}

// privacyInvalid is one call that fails local validation.
type privacyInvalid struct {
	name     string
	sentinel error              // ErrInvalidRequest when nil
	opts     []emailable.Option // the client's options
	run      func(ctx context.Context, c *emailable.Client) error
}

// privacyVerifyInvalid returns a VerifyEmail run with the marker request
// changed by mutate.
func privacyVerifyInvalid(mutate func(*emailable.VerifyEmailRequest)) func(context.Context, *emailable.Client) error {
	return func(ctx context.Context, c *emailable.Client) error {
		r := privacyVerifyRequest()
		mutate(&r)
		return noValueWith(c.VerifyEmail(ctx, r))
	}
}

// privacyWaitInvalid returns a WaitForVerification run with the marker
// request changed by mutate.
func privacyWaitInvalid(mutate func(*emailable.WaitForVerificationRequest)) func(context.Context, *emailable.Client) error {
	return func(ctx context.Context, c *emailable.Client) error {
		r := privacyWaitRequest()
		mutate(&r)
		return noValueWith(c.WaitForVerification(ctx, r))
	}
}

// privacyCreateInvalid returns a CreateBatch run with the marker request
// changed by mutate.
func privacyCreateInvalid(mutate func(*emailable.CreateBatchRequest)) func(context.Context, *emailable.Client) error {
	return func(ctx context.Context, c *emailable.Client) error {
		r := privacyCreateRequest()
		mutate(&r)
		return noValueWith(c.CreateBatch(ctx, r))
	}
}

// privacyGetInvalid returns a GetBatch run with req.
func privacyGetInvalid(req emailable.GetBatchRequest) func(context.Context, *emailable.Client) error {
	return func(ctx context.Context, c *emailable.Client) error {
		return noValueWith(c.GetBatch(ctx, req))
	}
}

// privacyVerifyInvalidCases are verify and wait requests that fail
// validation, the rejected value holding a marker.
func privacyVerifyInvalidCases() []privacyInvalid {
	email := func(e string) func(context.Context, *emailable.Client) error {
		return privacyVerifyInvalid(func(r *emailable.VerifyEmailRequest) { r.Email = e })
	}
	captcha := func(s string) func(context.Context, *emailable.Client) error {
		return privacyVerifyInvalid(func(r *emailable.VerifyEmailRequest) { r.CaptchaResponse = s })
	}
	return []privacyInvalid{
		{name: "email empty", run: email("")},
		{name: "email control", run: email(testEmail + "\n")},
		{name: "email C1 control", run: email(testEmail + "\u0085")},
		{name: "email surrounding whitespace", run: email(" " + testEmail)},
		{name: "email too long", run: email(strings.Repeat(testEmail, 13))},
		{name: "email invalid UTF-8", run: email(testEmail + "\xff")},
		{name: "captcha control", run: captcha(testCaptcha + "\r\n")},
		{name: "captcha too long", run: captcha(strings.Repeat(testCaptcha, 500))},
		{name: "captcha invalid UTF-8", run: captcha(testCaptcha + "\xc3")},
		{name: "provider timeout fraction", run: privacyVerifyInvalid(func(r *emailable.VerifyEmailRequest) {
			r.ProviderTimeout = 2500 * time.Millisecond
		})},
		{name: "provider timeout out of range", run: privacyVerifyInvalid(func(r *emailable.VerifyEmailRequest) {
			r.ProviderTimeout = 11 * time.Second
		})},
		{
			name: "provider timeout over the client's", opts: []emailable.Option{emailable.WithTimeout(9 * time.Second)},
			run: privacyVerifyInvalid(func(*emailable.VerifyEmailRequest) {}),
		},
		{name: "wait email control", run: privacyWaitInvalid(func(r *emailable.WaitForVerificationRequest) {
			r.Verify.Email = testEmail + "\t"
		})},
		{name: "wait captcha control", run: privacyWaitInvalid(func(r *emailable.WaitForVerificationRequest) {
			r.Verify.CaptchaResponse = testCaptcha + "\x00"
		})},
		{name: "wait interval", run: privacyWaitInvalid(func(r *emailable.WaitForVerificationRequest) {
			r.Interval = 500 * time.Millisecond
		})},
		{name: "wait attempts missing", run: privacyWaitInvalid(func(r *emailable.WaitForVerificationRequest) {
			r.MaxAttempts = 0
		})},
		{name: "wait attempts out of range", run: privacyWaitInvalid(func(r *emailable.WaitForVerificationRequest) {
			r.MaxAttempts = 31
		})},
		{name: "wait budget missing", run: privacyWaitInvalid(func(r *emailable.WaitForVerificationRequest) {
			r.MaxWait = 0
		})},
		{name: "wait budget out of range", run: privacyWaitInvalid(func(r *emailable.WaitForVerificationRequest) {
			r.MaxWait = 5 * time.Minute
		})},
	}
}

// privacyCreateInvalidCases are batch requests that fail validation, the
// rejected value holding a marker.
func privacyCreateInvalidCases() []privacyInvalid {
	set := privacyCreateInvalid
	callback := func(u string) func(context.Context, *emailable.Client) error {
		return set(func(r *emailable.CreateBatchRequest) { r.CallbackURL = u })
	}
	return []privacyInvalid{
		{name: "no emails", run: set(func(r *emailable.CreateBatchRequest) { r.Emails = nil })},
		{name: "email with a comma", run: set(func(r *emailable.CreateBatchRequest) {
			r.Emails = []string{testOwner, testEmail + "," + testOwner}
		})},
		{name: "email control", run: set(func(r *emailable.CreateBatchRequest) { r.Emails[0] = testEmail + "\n" })},
		{name: "email invalid UTF-8", run: set(func(r *emailable.CreateBatchRequest) { r.Emails[1] = testOwner + "\xff" })},
		{name: "email too long", run: set(func(r *emailable.CreateBatchRequest) { r.Emails[0] = strings.Repeat(testEmail, 13) })},
		{
			name: "too many emails", opts: []emailable.Option{emailable.WithMaxBatchEmails(1)},
			run: set(func(*emailable.CreateBatchRequest) {}),
		},
		{
			name: "over 64 MiB encoded", opts: []emailable.Option{emailable.WithMaxBatchEmails(300_000)},
			run: set(func(r *emailable.CreateBatchRequest) {
				long := strings.Repeat("x", 254-len(testEmail)) + testEmail
				r.Emails = slices.Repeat([]string{long}, 262_144)
			}),
		},
		{name: "callback http", run: callback("http://hooks.example.com/emailable?token=" + privacyHookMarker)},
		{name: "callback userinfo", run: callback("https://user:" + privacyHookMarker + "@hooks.example.com/")},
		{name: "callback fragment", run: callback(testHook + "#" + privacyHookMarker)},
		{name: "callback whitespace", run: callback(testHook + " " + privacyHookMarker)},
		{name: "callback relative", run: callback("/emailable?token=" + privacyHookMarker)},
		{name: "callback opaque", run: callback("mailto:" + testOwner + "?token=" + privacyHookMarker)},
		{name: "callback unparsable", run: callback("https://[::1/" + privacyHookMarker)},
		{name: "callback control", run: callback(testHook + "\x7f")},
		{name: "callback too long", run: callback(testHook + "&pad=" + strings.Repeat(privacyHookMarker, 130))},
		{name: "unknown response field", run: set(func(r *emailable.CreateBatchRequest) {
			r.ResponseFields = []emailable.ResponseField{emailable.ResponseField(testKey), emailable.ResponseField(testEmail)}
		})},
		{name: "repeated response field", run: set(func(r *emailable.CreateBatchRequest) {
			r.ResponseFields = append(r.ResponseFields, emailable.ResponseFieldEmail)
		})},
		{name: "unknown simulation", run: set(func(r *emailable.CreateBatchRequest) {
			r.Simulate = emailable.Simulation(testCaptcha)
		})},
		{name: "simulation of another operation", run: set(func(r *emailable.CreateBatchRequest) {
			r.Simulate = emailable.SimulateImporting
		})},
	}
}

// privacyGetInvalidCases are batch reads that fail validation.
func privacyGetInvalidCases() []privacyInvalid {
	return []privacyInvalid{
		{name: "batch ID missing", run: privacyGetInvalid(emailable.GetBatchRequest{})},
		{name: "batch ID with a space", run: privacyGetInvalid(emailable.GetBatchRequest{BatchID: testBatchID + " " + testKey})},
		{name: "batch ID non-ASCII", run: privacyGetInvalid(emailable.GetBatchRequest{BatchID: testKey + "é"})},
		{name: "batch ID too long", run: privacyGetInvalid(emailable.GetBatchRequest{BatchID: strings.Repeat(testKey, 20)})},
		{name: "batch ID the address with a newline", run: privacyGetInvalid(emailable.GetBatchRequest{BatchID: testEmail + "\n"})},
		{name: "unknown simulation", run: privacyGetInvalid(emailable.GetBatchRequest{
			BatchID: testBatchID, Simulate: emailable.Simulation(testKey),
		})},
		{name: "simulation of another operation", run: privacyGetInvalid(emailable.GetBatchRequest{
			BatchID: testBatchID, Simulate: emailable.SimulatePaymentError,
		})},
	}
}

// privacyContextInvalidCases are calls with a nil context, a nil Client or a
// zero Client, for every operation.
func privacyContextInvalidCases() []privacyInvalid {
	ops := privacyOps()
	out := make([]privacyInvalid, 0, 3*len(ops))
	var nilCtx context.Context
	var nilClient *emailable.Client
	for _, op := range ops {
		out = append(out,
			privacyInvalid{name: op.name + " nil context", run: func(_ context.Context, c *emailable.Client) error {
				return op.call(nilCtx, c)
			}},
			privacyInvalid{
				name: op.name + " nil client", sentinel: emailable.ErrInvalidConfig,
				run: func(ctx context.Context, _ *emailable.Client) error { return op.call(ctx, nilClient) },
			},
			privacyInvalid{
				name: op.name + " zero client", sentinel: emailable.ErrInvalidConfig,
				run: func(ctx context.Context, _ *emailable.Client) error { return op.call(ctx, &emailable.Client{}) },
			},
		)
	}
	return out
}

// privacyConfigInvalidCases are NewClient calls and DecodeBatchCallback
// arguments that fail validation.
func privacyConfigInvalidCases() []privacyInvalid {
	build := func(key string, opts ...emailable.Option) func(context.Context, *emailable.Client) error {
		return func(context.Context, *emailable.Client) error {
			return noValueWith(emailable.NewClient(key, opts...))
		}
	}
	base := func(u string) func(context.Context, *emailable.Client) error {
		return build(testKey, emailable.WithTestBaseURL(u))
	}
	decode := func(r io.Reader, maxBytes int64) func(context.Context, *emailable.Client) error {
		return func(context.Context, *emailable.Client) error {
			return noValueWith(emailable.DecodeBatchCallback(r, maxBytes))
		}
	}
	cfg := emailable.ErrInvalidConfig
	return []privacyInvalid{
		{name: "key empty", sentinel: cfg, run: build("")},
		{name: "key with a space", sentinel: cfg, run: build(testKey + " " + testCaptcha)},
		{name: "key with a newline", sentinel: cfg, run: build(testKey + "\n")},
		{name: "key non-ASCII", sentinel: cfg, run: build(testKey + "é")},
		{name: "key too long", sentinel: cfg, run: build(strings.Repeat(testKey, 200))},
		{name: "test URL userinfo", sentinel: cfg, run: base("https://user:" + testKey + "@127.0.0.1")},
		{name: "test URL key in the query", sentinel: cfg, run: base("https://127.0.0.1/?key=" + testKey)},
		{name: "test URL fragment", sentinel: cfg, run: base("https://127.0.0.1#" + testCaptcha)},
		{name: "test URL path", sentinel: cfg, run: base("https://127.0.0.1/" + privacyLocation)},
		{name: "test URL http", sentinel: cfg, run: base("http://127.0.0.1/" + privacyLocation)},
		{name: "test URL remote host", sentinel: cfg, run: base("https://" + privacyLocation + ".example.com")},
		{name: "test URL localhost", sentinel: cfg, run: base("https://localhost:8443")},
		{name: "test URL control", sentinel: cfg, run: base("https://127.0.0.1\n" + privacyLocation)},
		{name: "nil HTTP client", sentinel: cfg, run: build(testKey, emailable.WithHTTPClient(nil))},
		{name: "timeout zero", sentinel: cfg, run: build(testKey, emailable.WithTimeout(0))},
		{name: "timeout too long", sentinel: cfg, run: build(testKey, emailable.WithTimeout(11*time.Minute))},
		{name: "response limit too small", sentinel: cfg, run: build(testKey, emailable.WithMaxResponseBytes(1023))},
		{name: "batch limit zero", sentinel: cfg, run: build(testKey, emailable.WithMaxBatchEmails(0))},
		{name: "callback nil reader", run: decode(nil, emailable.DefaultMaxResponseBytes)},
		{name: "callback limit too small", run: decode(strings.NewReader(privacyDiagBody()), 1023)},
		{name: "callback limit too large", run: decode(strings.NewReader(privacyDiagBody()), 64<<20+1)},
	}
}

// privacyInvalidCases returns every validation failure.
func privacyInvalidCases() []privacyInvalid {
	return slices.Concat(privacyVerifyInvalidCases(), privacyCreateInvalidCases(), privacyGetInvalidCases(),
		privacyContextInvalidCases(), privacyConfigInvalidCases())
}

// privacyRunInvalid makes the call of tt through a client built with its
// options over rt.
func privacyRunInvalid(t *testing.T, tt privacyInvalid, rt http.RoundTripper) error {
	t.Helper()
	return tt.run(t.Context(), newTransportClient(t, rt, tt.opts...))
}

func TestPrivacyValidationErrorsNeverPrintSecrets(t *testing.T) {
	secrets := privacySecrets()
	ct := &testutil.CountingTransport{Next: testutil.StaticTransport(http.StatusOK, privacyDiagBody())}
	for _, tt := range privacyInvalidCases() {
		t.Run(tt.name, func(t *testing.T) {
			err := privacyRunInvalid(t, tt, ct)
			sentinel := tt.sentinel
			if sentinel == nil {
				sentinel = emailable.ErrInvalidRequest
			}
			require.ErrorIs(t, err, sentinel)
			ve := validationError(t, err)
			assert.Regexp(t, `^(Verify\.)?[A-Za-z]+(\[[0-9]+\])?$`, ve.Field)
			assert.Regexp(t, `^[a-z0-9_]+$`, ve.Reason)
			assert.Equal(t, sentinel.Error()+": "+ve.Field+": "+ve.Reason, err.Error())
			testutil.AssertShapesNeverPrint(t, ve, secrets...)
			assertErrorClean(t, err, secrets...)
			data, jerr := json.Marshal(err)
			require.NoError(t, jerr)
			testutil.AssertRedacted(t, string(data), secrets, "JSON")
		})
	}
	assert.Zero(t, ct.Attempts(), "nothing invalid is ever sent")
}

// privacyMarkerTransport is an injected transport that holds a marker. It is
// never called.
type privacyMarkerTransport struct{ marker string }

// RoundTrip fails with the marker.
func (m privacyMarkerTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, privacyEchoErr(m.marker)
}

func TestPrivacyClientNeverPrintsSecrets(t *testing.T) {
	const otherKey = "secret_otherkeymarker55aa" // gitleaks:allow (synthetic test marker)
	secrets := append(privacySecrets(), otherKey, "127.0.0.1:47613", "api.emailable.com")
	injected := &http.Client{Transport: privacyMarkerTransport{marker: privacyTransport}, Timeout: time.Minute}
	options := map[string]emailable.Option{
		"WithHTTPClient":          emailable.WithHTTPClient(injected),
		"WithTimeout":             emailable.WithTimeout(45 * time.Second),
		"WithMaxResponseBytes":    emailable.WithMaxResponseBytes(1 << 16),
		"WithMaxBatchEmails":      emailable.WithMaxBatchEmails(10),
		"WithTestBaseURL":         emailable.WithTestBaseURL("https://127.0.0.1:47613"),
		"WithProviderDiagnostics": emailable.WithProviderDiagnostics(),
		"nil option":              nil,
	}
	clients := map[string]*emailable.Client{"defaults": newClient(t), "zero": {}}
	all := make([]emailable.Option, 0, len(options))
	for name, opt := range options {
		clients[name] = newClient(t, opt)
		all = append(all, opt)
		testutil.AssertShapesNeverPrint(t, &opt, secrets...)
	}
	clients["every option"] = newClient(t, all...)
	other, err := emailable.NewClient(otherKey, emailable.WithTimeout(time.Second))
	require.NoError(t, err)

	for name, c := range clients {
		t.Run(name, func(t *testing.T) {
			testutil.AssertShapesNeverPrint(t, c, secrets...)
			testutil.AssertRedacted(t, strings.Join(testutil.ReflectStrings(c), "\n"), secrets, "reflection walk")
			testutil.AssertDiffRedacted(t, c, other, secrets...)
			assert.True(t, strings.HasPrefix(c.String(), "emailable.Client{timeout: "), c.String())
			assert.Equal(t, c.String(), c.GoString())
			assert.Equal(t, c.String(), fmt.Sprintf("%#v", c))
		})
	}
	var none *emailable.Client
	assert.Equal(t, "emailable.Client(nil)", none.String())
	assert.Equal(t, "emailable.Client(nil)", fmt.Sprintf("%v", none))
}

// TestPrivacyClientAfterCalls makes every call with marker requests through a
// loopback server and checks where each secret traveled: the key only in the
// Authorization header, the address and the captcha response only in the
// verify query, as documented, the callback URL only in the batch body. The
// Client still prints nothing afterwards.
func TestPrivacyClientAfterCalls(t *testing.T) {
	c, srv := newServerClient(t, okHandler())
	for _, op := range privacyOps() {
		require.NoError(t, op.call(t.Context(), c), op.name)
	}
	testutil.AssertShapesNeverPrint(t, c, privacySecrets()...)
	testutil.AssertRedacted(t, strings.Join(testutil.ReflectStrings(c), "\n"), privacySecrets(), "reflection walk")

	reqs := srv.Requests()
	require.Len(t, reqs, len(privacyOps()))
	for _, r := range reqs {
		assert.Equal(t, []string{"Bearer " + testKey}, r.Header.Values("Authorization"))
		for name, values := range r.Header {
			if name != "Authorization" {
				testutil.AssertRedacted(t, name+": "+strings.Join(values, ", "), leakMarkers(), "header %s", name)
			}
		}
		testutil.AssertRedacted(t, string(r.Body), []string{testKey, testCaptcha}, "body")
		testutil.AssertRedacted(t, r.URL, []string{testKey, testHook, privacyHookMarker, testOwner}, "URL")
		switch {
		case r.Path == pathVerify:
			q := query(t, r)
			assert.Equal(t, testEmail, q.Get("email"), "the address travels in the query, as documented")
			assert.Equal(t, testCaptcha, q.Get("captcha_response"))
			assert.Empty(t, r.Body)
		case r.Method == http.MethodPost:
			assert.Contains(t, string(r.Body), `"url":"`+testHook+`"`, "the callback URL reaches the provider, and only it")
		default:
			testutil.AssertRedacted(t, r.URL+string(r.Body), leakMarkers(), "a read carries no secret")
		}
	}
}

// requestPrint is one request case of TestPrivacyRequestsPrintShapeOnly.
type requestPrint struct {
	name  string
	check func(t *testing.T)
}

// printsAs returns the case of req, whose String and GoString, and every
// printable shape with every verb but %p, print exactly want.
func printsAs[T interface {
	String() string
	GoString() string
}](name string, req T, want string,
) requestPrint {
	return requestPrint{name: name, check: func(t *testing.T) {
		t.Helper()
		assert.Equal(t, want, req.String())
		assert.Equal(t, want, req.GoString())
		assertShapeOnly(t, &req, want, privacyExtraSecrets()...)
	}}
}

// TestPrivacyRequestsPrintShapeOnly pins the shape-only description of each
// request type, zero and with every field set: String, GoString and every
// verb but %p print it exactly, on the value, a pointer and every way of
// holding it through which fmt calls its methods, and never the addresses,
// the captcha response or the callback URL. The exceptions are documented by
// TestPrivacyRequestFmtLimits.
func TestPrivacyRequestsPrintShapeOnly(t *testing.T) {
	const verify = "emailable.VerifyEmailRequest{email: set, smtp: false, acceptAll: true, providerTimeout: 5s, " +
		"captchaResponse: set}"
	simulated := privacyCreateRequest()
	simulated.Simulate = emailable.SimulateGenericError
	wait := privacyWaitRequest()
	every := wait
	every.Interval = 5 * time.Second
	tests := []requestPrint{
		printsAs("VerifyEmailRequest/zero", emailable.VerifyEmailRequest{},
			"emailable.VerifyEmailRequest{email: unset, smtp: unset, acceptAll: unset, providerTimeout: unset, "+
				"captchaResponse: unset}"),
		printsAs("VerifyEmailRequest/minimal", verifyRequest(),
			"emailable.VerifyEmailRequest{email: set, smtp: unset, acceptAll: unset, providerTimeout: unset, "+
				"captchaResponse: unset}"),
		printsAs("VerifyEmailRequest/every field", privacyVerifyRequest(), verify),
		printsAs("CreateBatchRequest/zero", emailable.CreateBatchRequest{},
			"emailable.CreateBatchRequest{emails: 0, callbackURL: unset, responseFields: 0, retries: unset, "+
				"simulate: unset}"),
		printsAs("CreateBatchRequest/retries true", emailable.CreateBatchRequest{Emails: []string{testEmail}, Retries: new(true)},
			"emailable.CreateBatchRequest{emails: 1, callbackURL: unset, responseFields: 0, retries: true, "+
				"simulate: unset}"),
		printsAs("CreateBatchRequest/every field", simulated,
			"emailable.CreateBatchRequest{emails: 2, callbackURL: set, responseFields: 3, retries: false, "+
				"simulate: set}"),
		printsAs("WaitForVerificationRequest/zero", emailable.WaitForVerificationRequest{},
			"emailable.WaitForVerificationRequest{verify: emailable.VerifyEmailRequest{email: unset, smtp: unset, "+
				"acceptAll: unset, providerTimeout: unset, captchaResponse: unset}, interval: default, maxAttempts: 0, "+
				"maxWait: 0s}"),
		printsAs("WaitForVerificationRequest/default interval", wait,
			"emailable.WaitForVerificationRequest{verify: "+verify+", interval: default, maxAttempts: 3, maxWait: 1m0s}"),
		printsAs("WaitForVerificationRequest/every field", every,
			"emailable.WaitForVerificationRequest{verify: "+verify+", interval: 5s, maxAttempts: 3, maxWait: 1m0s}"),
	}
	for _, tt := range tests {
		t.Run(tt.name, tt.check)
	}
}

// TestPrivacyRequestFmtLimits documents the places where a request's fields
// are written out without its Format method, as the package documentation
// warns: fmt's %p on a value, which fmt reports as a bad verb and then prints
// with every method disabled, a request held in an unexported struct field,
// whose methods fmt cannot call, and encoding/json, which writes every
// field. A pointer printed with %p is only an address.
func TestPrivacyRequestFmtLimits(t *testing.T) {
	verify := privacyVerifyRequest()
	create := privacyCreateRequest()
	data, err := json.Marshal(verify) //nolint:musttag // documents that encoding/json writes an untagged request in full
	require.NoError(t, err)
	assert.Contains(t, string(data), testEmail, "encoding/json writes the address")
	assert.Contains(t, string(data), testCaptcha, "encoding/json writes the captcha response")
	data, err = json.Marshal(create) //nolint:musttag // documents that encoding/json writes an untagged request in full
	require.NoError(t, err)
	assert.Contains(t, string(data), testHook, "encoding/json writes the callback URL")
	assert.Contains(t, string(data), testOwner)
	assert.Contains(t, fmt.Sprintf("%p", verify), testEmail, "%p on a value prints its fields")
	assert.Contains(t, fmt.Sprintf("%p", verify), testCaptcha)
	assert.Contains(t, fmt.Sprintf("%p", create), testHook)
	assert.Regexp(t, `^0x[0-9a-f]+$`, fmt.Sprintf("%p", &verify), "%p on a pointer prints an address")
	assert.Regexp(t, `^0x[0-9a-f]+$`, fmt.Sprintf("%p", &create))
	for _, s := range testutil.Shapes(&verify) {
		if strings.Contains(s.Name, "unexported") {
			assert.Contains(t, fmt.Sprintf("%v", s.Value), testCaptcha, "%s: fmt cannot call Format", s.Name)
		}
	}
	for _, s := range testutil.Shapes(&create) {
		if strings.Contains(s.Name, "unexported") {
			assert.Contains(t, fmt.Sprintf("%v", s.Value), testHook, "%s: fmt cannot call Format", s.Name)
		}
	}
}

func TestPrivacyDownloadURLNeverPrints(t *testing.T) {
	secrets := privacyDownloadSecrets()
	u := emailable.NewDownloadURL(privacyDownload)
	zero := emailable.DownloadURL{}
	testutil.AssertShapesNeverPrint(t, &u, secrets...)
	testutil.AssertRedacted(t, strings.Join(testutil.ReflectStrings(u), "\n"), secrets, "reflection walk")
	for _, verb := range verbsButP() {
		want, wantZero := "[redacted]", ""
		if verb == "%q" {
			want, wantZero = `"[redacted]"`, `""`
		}
		assert.Equal(t, want, fmt.Sprintf(verb, u), verb)
		assert.Equal(t, want, fmt.Sprintf(verb, &u), verb)
		assert.Equal(t, wantZero, fmt.Sprintf(verb, zero), verb)
	}
	data, err := json.Marshal(u) //nolint:staticcheck // SA9005: documents that a DownloadURL marshals as {}
	require.NoError(t, err)
	assertBody(t, `{}`, data)
	assert.Equal(t, "[redacted]", u.String())
	assert.Equal(t, "[redacted]", u.GoString())
	assert.Equal(t, privacyDownload, u.Reveal(), "only Reveal returns it")
	assert.True(t, u.Equal(emailable.NewDownloadURL(privacyDownload)))
	assert.True(t, emailable.NewDownloadURL("").IsZero())
}

// privacyBatches returns snapshots that carry the signed URL: read with
// GetBatch, decoded from a callback, and built with NewDownloadURL.
func privacyBatches(t *testing.T) map[string]*emailable.Batch {
	t.Helper()
	body := `{"id":"` + testBatchID + `","message":"Batch verification completed.","download_file":` +
		testutil.JSONQuote(privacyDownload) + `,"emails":[{"email":"` + testEmail + `","state":"deliverable"}],` +
		`"reason_counts":{"accepted_email":1},"total_counts":{"processed":1,"total":1}}`
	read, err := callOnce(t, pathBatch, body, func(ctx context.Context, c *emailable.Client) (*emailable.Batch, error) {
		return c.GetBatch(ctx, emailable.GetBatchRequest{BatchID: testBatchID, Partial: true})
	})
	require.NoError(t, err)
	decoded, err := emailable.DecodeBatchCallback(strings.NewReader(body), emailable.DefaultMaxResponseBytes)
	require.NoError(t, err)
	require.Equal(t, privacyDownload, read.DownloadFile.Reveal())
	require.Equal(t, privacyDownload, decoded.DownloadFile.Reveal())
	return map[string]*emailable.Batch{
		"GetBatch":            read,
		"DecodeBatchCallback": decoded,
		"built":               {ID: testBatchID, DownloadFile: emailable.NewDownloadURL(privacyDownload)},
	}
}

func TestPrivacyBatchNeverPrintsDownloadURL(t *testing.T) {
	const otherDownload = "https://files.example.com/otherdlmarker7d3e.csv?signature=othersigmarker91a"
	secrets := append(privacyDownloadSecrets(), otherDownload, "otherdlmarker7d3e", "othersigmarker91a")
	other := &emailable.Batch{ID: testBatchID, DownloadFile: emailable.NewDownloadURL(otherDownload)}
	for name, b := range privacyBatches(t) {
		t.Run(name, func(t *testing.T) {
			testutil.AssertShapesNeverPrint(t, b, secrets...)
			testutil.AssertRedacted(t, strings.Join(testutil.ReflectStrings(b), "\n"), secrets,
				"a reflection walk cannot see behind the function value")
			data, err := json.Marshal(b)
			require.NoError(t, err)
			testutil.AssertRedacted(t, string(data), secrets, "JSON")
			testutil.AssertDiffRedacted(t, b, other, secrets...)
			assert.Contains(t, fmt.Sprintf("%+v", b), "DownloadFile:[redacted]")
			assert.Contains(t, fmt.Sprintf("%#v", *b), "DownloadFile:[redacted]")
		})
	}
}

// TestPrivacyResultsNeverPrintRequestSecrets proves that no value the SDK
// returns carries the API key, the captcha response or the callback URL of
// the request it answers: results hold the provider's answer only.
func TestPrivacyResultsNeverPrintRequestSecrets(t *testing.T) {
	secrets := []string{testKey, testCaptcha, testHook, privacyHookMarker}
	c, _ := newServerClient(t, okHandler())
	ctx := t.Context()
	v, err := c.VerifyEmail(ctx, privacyVerifyRequest())
	require.NoError(t, err)
	testutil.AssertShapesNeverPrint(t, v, secrets...)
	w, err := c.WaitForVerification(ctx, privacyWaitRequest())
	require.NoError(t, err)
	testutil.AssertShapesNeverPrint(t, w, secrets...)
	created, err := c.CreateBatch(ctx, privacyCreateRequest())
	require.NoError(t, err)
	testutil.AssertShapesNeverPrint(t, created, secrets...)
	b, err := c.GetBatch(ctx, emailable.GetBatchRequest{BatchID: testBatchID})
	require.NoError(t, err)
	testutil.AssertShapesNeverPrint(t, b, secrets...)
	info, err := c.AccountInfo(ctx, emailable.AccountInfoRequest{})
	require.NoError(t, err)
	testutil.AssertShapesNeverPrint(t, info, secrets...)
}

// privacyCapture runs f with os.Stdout, os.Stderr and the log package's
// output redirected to pipes, and returns everything written to them.
func privacyCapture(t *testing.T, f func()) (captured string) {
	t.Helper()
	stdout, stderr, logOut := os.Stdout, os.Stderr, log.Writer()
	rOut, wOut, err := os.Pipe()
	require.NoError(t, err)
	rErr, wErr, err := os.Pipe()
	require.NoError(t, err)
	var outBuf, errBuf bytes.Buffer
	var wg sync.WaitGroup
	wg.Go(func() { _, _ = io.Copy(&outBuf, rOut) })
	wg.Go(func() { _, _ = io.Copy(&errBuf, rErr) })
	os.Stdout, os.Stderr = wOut, wErr
	log.SetOutput(wErr)
	defer func() {
		os.Stdout, os.Stderr = stdout, stderr
		log.SetOutput(logOut)
		_ = wOut.Close()
		_ = wErr.Close()
		wg.Wait()
		_ = rOut.Close()
		_ = rErr.Close()
		captured = outBuf.String() + errBuf.String()
	}()
	f()
	return ""
}

// privacyFailEverything makes every failing call of this file: every flow on
// every operation with diagnostics, every validation failure, every callback
// failure, and every operation over the SDK's own transport, which does not
// trust the test server's certificate. It returns the number of calls that
// failed.
func privacyFailEverything(t *testing.T) int {
	t.Helper()
	failures := 0
	count := func(err error) {
		if err != nil {
			failures++
		}
	}
	for _, c := range privacyCases() {
		count(c.flow.call(t, c.op, emailable.WithProviderDiagnostics()))
	}
	for _, tt := range privacyInvalidCases() {
		count(privacyRunInvalid(t, tt, testutil.StaticTransport(http.StatusOK, privacyDiagBody())))
	}
	for _, tt := range privacyCallbackCases() {
		count(noValueWith(emailable.DecodeBatchCallback(tt.body(), tt.maxBytes)))
	}
	srv := testutil.NewServer(t, okHandler())
	own := newClient(t, emailable.WithTestBaseURL(srv.URL()), emailable.WithProviderDiagnostics())
	for _, op := range privacyOps() {
		count(op.call(t.Context(), own))
	}
	return failures
}

// privacySucceedEverywhere makes every operation succeed over loopback
// servers on HTTP/1.1 and HTTP/2 and returns the number of calls that
// succeeded.
func privacySucceedEverywhere(t *testing.T) int {
	t.Helper()
	successes := 0
	for _, proto := range testutil.Protocols() {
		c, _ := newServerClientProto(t, proto, okHandler(), emailable.WithProviderDiagnostics())
		for _, op := range privacyOps() {
			if op.call(t.Context(), c) == nil {
				successes++
			}
		}
	}
	return successes
}

// TestPrivacyNeverWritesStandardStreams makes every call this file makes,
// failing and succeeding, over in-memory transports, loopback servers on
// HTTP/1.1 and HTTP/2 and the SDK's own transport, with os.Stdout, os.Stderr
// and the log package redirected: the SDK writes nothing to any of them.
// Printing through the print and println builtins, which bypass os.Stderr,
// is ruled out for library code by the forbidigo lint rules.
func TestPrivacyNeverWritesStandardStreams(t *testing.T) {
	var failures, successes int
	captured := privacyCapture(t, func() {
		failures = privacyFailEverything(t)
		successes = privacySucceedEverywhere(t)
	})
	assert.Empty(t, captured, "the SDK never prints or logs")
	assert.Equal(t, len(privacyCases())+len(privacyInvalidCases())+len(privacyCallbackCases())+len(privacyOps()),
		failures, "every failing call failed")
	assert.Equal(t, 2*len(privacyOps()), successes, "every other call succeeded")
}
