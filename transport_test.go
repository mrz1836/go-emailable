package emailable_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-emailable"
	"github.com/mrz1836/go-emailable/internal/testutil"
)

// trnMarker is planted in transport errors, headers and bodies that must
// never surface. It is synthetic.
const trnMarker = "TRNMARKER" // gitleaks:allow (synthetic test marker)

// Planted transport errors. Their text holds trnMarker.
var (
	errTrnPlanted = errors.New("transport broke " + trnMarker)
	errTrnRefused = errors.New("connection refused " + trnMarker)
)

// trnTCP is the network of every planted net.OpError.
const trnTCP = "tcp"

// The text of each fixed cause.
const (
	trnCauseDNS      = "emailable: transport: DNS lookup failed"
	trnCauseProxy    = "emailable: transport: proxy connection failed"
	trnCauseDial     = "emailable: transport: connection failed"
	trnCauseTLS      = "emailable: transport: TLS failed"
	trnCauseReset    = "emailable: transport: connection reset"
	trnCauseClosed   = "emailable: transport: connection closed early"
	trnCauseStalled  = "emailable: transport: body read made no progress"
	trnCauseOther    = "emailable: transport: request failed"
	trnCauseTimeout  = "context deadline exceeded"
	trnCauseTooLarge = "emailable: response body exceeds the size limit"
	trnCauseEncoded  = "emailable: response has an undecoded content encoding"
)

// trnFailed asserts that err is the CallError of a failed call of o of kind,
// with status, after one attempt, with the documented OutcomeUnknown, and
// that its cause reads cause.
func trnFailed(tb testing.TB, err error, o opCase, kind emailable.FailureKind, status int, cause string) {
	tb.Helper()
	_ = assertCallError(tb, err, o.want(wantCall{
		Kind: kind, Status: status, Attempts: 1, OutcomeUnknown: outcomeUnknownAfter(o.paid, kind),
	}), trnMarker)
	assert.Equal(tb, cause, causeText(tb, err))
}

// trnWantHeader returns the exact header the SDK sends with method.
func trnWantHeader(method string) http.Header {
	h := http.Header{
		"Accept":        {mediaJSON},
		"User-Agent":    {"go-emailable"},
		"Authorization": {"Bearer " + testKey},
	}
	if method == http.MethodPost {
		h["Content-Type"] = []string{mediaJSON}
	}
	return h
}

func TestTransportWireInMemory(t *testing.T) {
	for _, o := range opCases() {
		t.Run(o.name, func(t *testing.T) {
			c, s := newScriptClient(t, o.ok)
			require.NoError(t, o.call(t.Context(), c))
			r := testutil.OnlyRequest(t, s)
			assertWireRequest(t, r, o.method, o.path)
			assertNoReplay(t, r)
			assert.Equal(t, trnWantHeader(o.method), r.Header, "exactly the SDK's headers")
			assert.True(t, strings.HasPrefix(r.URL, loopbackOrigin+o.path), r.URL)
		})
	}
}

func TestTransportWireOverTLS(t *testing.T) {
	for _, proto := range testutil.Protocols() {
		for _, o := range opCases() {
			t.Run(proto.Name()+"/"+o.name, func(t *testing.T) {
				c, srv := newServerClientProto(t, proto, testutil.Serve(o.ok))
				require.NoError(t, o.call(t.Context(), c))
				r := testutil.OnlyRequest(t, srv)
				assertWireRequest(t, r, o.method, o.path)
				assert.Equal(t, string(proto), r.Proto)
				// The transport adds Accept-Encoding itself, and a POST's length.
				keys := slices.DeleteFunc(slices.Sorted(maps.Keys(r.Header)), func(k string) bool {
					return k == "Accept-Encoding" || k == "Content-Length"
				})
				assert.Equal(t, slices.Sorted(maps.Keys(trnWantHeader(o.method))), keys)
				if o.method == http.MethodGet && proto == testutil.HTTP1 {
					assert.Zero(t, r.ContentLength, "an HTTP/1 GET is neither chunked nor sized")
				}
				assert.Equal(t, trnWantHeader(o.method).Values("Authorization"), r.Header.Values("Authorization"))
			})
		}
	}
}

// TestTransportFullRequestsOverTLS sends each operation with every request
// field set to a TLS server over each protocol, and checks the exact request
// line and body it receives.
func TestTransportFullRequestsOverTLS(t *testing.T) {
	createBody := `{"emails":["` + testEmail + `","` + createBatchOther + `","` + testEmail + `"],"url":"` + testHook +
		`","response_fields":"email,state","retries":true,"simulate":"payment_error"}`
	tests := []struct {
		o    opCase
		call func(ctx context.Context, c *emailable.Client) error
		url  string // the path and query the server receives
		body string // the exact POST body
	}{
		{
			o: opCaseNamed("VerifyEmail"),
			call: func(ctx context.Context, c *emailable.Client) error {
				return noValueWith(c.VerifyEmail(ctx, emailable.VerifyEmailRequest{
					Email: testEmail, SMTP: new(true), AcceptAll: new(false), ProviderTimeout: 3 * time.Second,
				}))
			},
			url: pathVerify + "?accept_all=false&" + vfyEmailQuery + "&smtp=true&timeout=3",
		},
		{
			o: opCaseNamed("CreateBatch"),
			call: func(ctx context.Context, c *emailable.Client) error {
				return noValueWith(c.CreateBatch(ctx, emailable.CreateBatchRequest{
					Emails:         []string{testEmail, createBatchOther, testEmail},
					CallbackURL:    testHook,
					ResponseFields: []emailable.ResponseField{emailable.ResponseFieldEmail, emailable.ResponseFieldState},
					Retries:        new(true),
					Simulate:       emailable.SimulatePaymentError,
				}))
			},
			url: pathBatch, body: createBody,
		},
		{
			o: opCaseNamed("GetBatch"),
			call: func(ctx context.Context, c *emailable.Client) error {
				return noValueWith(c.GetBatch(ctx, emailable.GetBatchRequest{
					BatchID: testBatchID, Partial: true, Simulate: emailable.SimulateVerifying,
				}))
			},
			url: pathBatch + "?id=" + testBatchID + "&partial=true&simulate=verifying",
		},
		{
			o:    opCaseNamed("AccountInfo"),
			call: func(ctx context.Context, c *emailable.Client) error { return noValueWith(callAccount(ctx, c)) },
			url:  pathAccount,
		},
	}
	for _, proto := range testutil.Protocols() {
		for _, tt := range tests {
			t.Run(proto.Name()+"/"+tt.o.name, func(t *testing.T) {
				c, srv := newServerClientProto(t, proto, testutil.Serve(tt.o.ok))
				require.NoError(t, tt.call(t.Context(), c))
				r := testutil.OnlyRequest(t, srv)
				assertWireRequest(t, r, tt.o.method, tt.o.path)
				assert.Equal(t, string(proto), r.Proto)
				assert.Equal(t, tt.url, r.URL)
				if tt.o.method == http.MethodPost {
					assertBody(t, tt.body, r.Body)
				}
			})
		}
	}
}

// trnStatuses returns every status other than 200 the status table tries.
func trnStatuses() []int {
	return []int{
		100, 101, 103, 199, 201, 204, 249, 299, 300, 301, 302, 303, 304, 305, 306, 307, 308,
		400, 401, 402, 403, 404, 405, 409, 422, 429, 499, 500, 502, 503, 504, 599, 600, 999,
	}
}

// TestTransportStatusTable proves that the HTTP status alone decides the
// kind of every operation's failure: a redirect is never followed, even with
// an unparsable Location, which the SDK strips; diagnostics being off, no
// body is ever read; and the body is closed exactly once.
func TestTransportStatusTable(t *testing.T) {
	const content = `{"message":"trn note ` + testKey + `"}`
	header := http.Header{
		"Content-Type": {mediaJSON},
		"Location":     {"https://elsewhere.example.com/%zz?" + trnMarker},
	}
	for _, o := range opCases() {
		for _, code := range trnStatuses() {
			if o.helper && code == emailable.StatusPending {
				continue // WaitForVerification repeats a 249; its own suite covers that
			}
			t.Run(o.name+"/"+strconv.Itoa(code), func(t *testing.T) {
				body := testutil.NewBody(content)
				ct := &testutil.CountingTransport{Next: testutil.TransportWith(code, header, int64(len(content)), body)}
				err := o.call(t.Context(), newTransportClient(t, ct))
				cause := causeStatus
				if code == emailable.StatusPending && o.op == emailable.OpVerifyEmail {
					cause = causePending
				}
				trnFailed(t, err, o, statusKind(o.op, code), code, cause)
				assert.Empty(t, callError(t, err).Diagnostics().Message)
				assert.Equal(t, 1, ct.Attempts(), "exactly one attempt")
				assert.Zero(t, body.Reads(), "the status decides; the body is never read")
				assert.Equal(t, 1, body.Closes(), "the body is closed exactly once")
			})
		}
	}
}

// trnStrictCase is an HTTP 200 body that fails the strict JSON checks.
type trnStrictCase struct {
	name  string
	body  string
	kind  emailable.FailureKind
	cause string
}

// trnStrictCases returns bodies that every operation refuses before decoding
// anything. None holds an "id", so CreateBatch rescues no batch ID.
func trnStrictCases() []trnStrictCase {
	const (
		malformed  = emailable.FailureMalformedResponse
		unexpected = emailable.FailureUnexpectedResponse
		utf8Cause  = "emailable: response is not valid UTF-8"
		jsonCause  = "emailable: response is not valid JSON"
		trailing   = "emailable: response has data after the top-level value"
		notObject  = "emailable: response is not a JSON object"
		duplicate  = "emailable: response repeats an object key"
	)
	containers := `{"x":[` + strings.Repeat("[],", 100_000) + `[]]}`
	return []trnStrictCase{
		{"invalid UTF-8 in a value", "{\"message\":\"\xff\"}", malformed, utf8Cause},
		{"invalid UTF-8 in a key", "{\"\xc3\":1}", malformed, utf8Cause},
		{"overlong UTF-8", "{\"message\":\"\xc0\xaf\"}", malformed, utf8Cause},
		{"encoded surrogate", "{\"message\":\"\xed\xa0\x80\"}", malformed, utf8Cause},
		{"invalid JSON", `{"message":`, malformed, jsonCause},
		{"single quotes", `{'message':'x'}`, malformed, jsonCause},
		{"unquoted key", `{message:"x"}`, malformed, jsonCause},
		{"trailing comma", `{"message":"x",}`, malformed, jsonCause},
		{"empty body", ``, malformed, jsonCause},
		{"only whitespace", " \n\t", malformed, jsonCause},
		{"NaN", `{"score":NaN}`, malformed, jsonCause},
		{"trailing data", `{"message":"x"} x`, malformed, trailing},
		{"two objects", `{"message":"x"}{"message":"y"}`, malformed, trailing},
		{"trailing comma after the object", `{"message":"x"},`, malformed, trailing},
		{"top-level array", `[{"message":"x"}]`, unexpected, notObject},
		{"top-level string", `"x"`, unexpected, notObject},
		{"top-level number", `42`, unexpected, notObject},
		{"top-level null", `null`, unexpected, notObject},
		{"top-level boolean", `true`, unexpected, notObject},
		{"duplicate key", `{"message":"a","message":"b"}`, malformed, duplicate},
		{"duplicate key, escaped spelling", testutil.Esc(`{"message":"a","~u006dessage":"b"}`), malformed, duplicate},
		{"duplicate email, escaped spelling", testutil.Esc(`{"email":"a","~u0065mail":"b"}`), malformed, duplicate},
		{"duplicate email, fully escaped", testutil.Esc(`{"~u0065~u006d~u0061~u0069~u006c":"a","email":"b"}`), malformed, duplicate},
		{"duplicate key, escaped slash", testutil.Esc(`{"a/b":1,"a~/b":2}`), malformed, duplicate},
		{"case variants", `{"Email":"a","email":"b"}`, malformed, duplicate},
		{"upper case variant", `{"message":"a","MESSAGE":"b"}`, malformed, duplicate},
		{"Kelvin sign", "{\"kind\":1,\"\u212aind\":2}", malformed, duplicate},
		{"escaped Kelvin sign", testutil.Esc(`{"kind":1,"~u212aind":2}`), malformed, duplicate},
		{"long s", "{\"state\":1,\"\u017ftate\":2}", malformed, duplicate},
		{"nested duplicate", `{"x":{"y":1,"Y":2}}`, malformed, duplicate},
		{"duplicate in an array element", `{"x":[{},{"y":1,"y":2}]}`, malformed, duplicate},
		{"too many containers", containers, emailable.FailureResponseTooLarge, "emailable: response has too many JSON values"},
	}
}

func TestTransportStrictJSON(t *testing.T) {
	for _, o := range operations() {
		for _, tt := range trnStrictCases() {
			t.Run(o.name+"/"+tt.name, func(t *testing.T) {
				c, s := newScriptClient(t, testutil.Step{Body: tt.body})
				err := o.call(t.Context(), c)
				trnFailed(t, err, o, tt.kind, http.StatusOK, tt.cause)
				assert.Empty(t, callError(t, err).Field, "the whole body is refused, not a field")
				assert.Equal(t, 1, s.Calls())
			})
		}
	}
}

// TestTransportTooManyValues proves the value bound, which only a GetBatch
// body can reach within its larger size limit.
func TestTransportTooManyValues(t *testing.T) {
	o := opCaseNamed("GetBatch")
	body := `{"id":"` + testBatchID + `","x":[` + strings.Repeat("0,", 1_000_000) + `0]}`
	c, _ := newScriptClient(t, testutil.Step{Body: body})
	trnFailed(t, o.call(t.Context(), c), o, emailable.FailureResponseTooLarge, http.StatusOK,
		"emailable: response has too many JSON values")
}

func TestTransportContentEncoding(t *testing.T) {
	tests := []struct {
		name    string
		key     string // the header's name as the transport sets it; "" for Content-Encoding
		values  []string
		refused bool
	}{
		{name: "gzip", values: []string{"gzip"}, refused: true},
		{name: "lowercase header name", key: "content-encoding", values: []string{"gzip"}, refused: true},
		{name: "upper-case header name", key: "CONTENT-ENCODING", values: []string{"br"}, refused: true},
		{name: "lowercase header name with identity", key: "content-encoding", values: []string{"identity"}},
		{name: "upper-case gzip", values: []string{"GZIP"}, refused: true},
		{name: "x-gzip", values: []string{"x-gzip"}, refused: true},
		{name: "br", values: []string{"br"}, refused: true},
		{name: "deflate", values: []string{"deflate"}, refused: true},
		{name: "zstd", values: []string{"zstd"}, refused: true},
		{name: "unknown coding", values: []string{"x-" + strings.ToLower(trnMarker)}, refused: true},
		{name: "gzip then identity", values: []string{"gzip, identity"}, refused: true},
		{name: "identity then br", values: []string{"identity, br"}, refused: true},
		{name: "list without spaces", values: []string{"identity,br"}, refused: true},
		{name: "list with tabs", values: []string{"identity,\tgzip\t"}, refused: true},
		{name: "two values", values: []string{"identity", "br"}, refused: true},
		{name: "identity with a parameter", values: []string{"identity;q=1"}, refused: true},
		{name: "identity", values: []string{"identity"}},
		{name: "capitalized identity", values: []string{"Identity, "}},
		{name: "upper-case identity", values: []string{"IDENTITY"}},
		{name: "padded identity", values: []string{" identity\t"}},
		{name: "identity twice", values: []string{"identity, identity"}},
		{name: "identity in two values", values: []string{"identity", "identity"}},
		{name: "empty value", values: []string{""}},
		{name: "only separators", values: []string{" , "}},
		{name: "only commas", values: []string{",,"}},
		{name: "no header", values: nil},
	}
	o := opCaseNamed("VerifyEmail")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := testutil.NewBody(verifyOK)
			header := http.Header{"Content-Type": {mediaJSON}}
			if tt.values != nil {
				key := tt.key
				if key == "" {
					key = "Content-Encoding"
				}
				header[key] = tt.values
			}
			c := newTransportClient(t, testutil.TransportWith(http.StatusOK, header, int64(len(verifyOK)), body))
			err := o.call(t.Context(), c)
			assert.Equal(t, 1, body.Closes())
			if !tt.refused {
				require.NoError(t, err)
				return
			}
			trnFailed(t, err, o, emailable.FailureUnexpectedResponse, http.StatusOK, trnCauseEncoded)
			assert.Zero(t, body.Reads(), "an encoded body is never read")
		})
	}
}

func TestTransportContentEncodingEveryOperation(t *testing.T) {
	for _, o := range operations() {
		for _, coding := range []string{"gzip", "br"} {
			t.Run(o.name+"/"+coding, func(t *testing.T) {
				body := testutil.NewBody(o.ok.Body)
				header := http.Header{"Content-Type": {mediaJSON}, "Content-Encoding": {coding}}
				c := newTransportClient(t, testutil.TransportWith(http.StatusOK, header, -1, body))
				trnFailed(t, o.call(t.Context(), c), o, emailable.FailureUnexpectedResponse, http.StatusOK, trnCauseEncoded)
				assert.Zero(t, body.Reads())
			})
		}
	}
}

// trnGzipHandler answers with body gzip-encoded, whatever the request
// accepts.
func trnGzipHandler(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		_, _ = zw.Write([]byte(body))
		_ = zw.Close()
		w.Header().Set("Content-Type", mediaJSON)
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(buf.Bytes())
	}
}

func TestTransportGzipDecodedByTheTransport(t *testing.T) {
	for _, proto := range testutil.Protocols() {
		for _, o := range operations() {
			t.Run(proto.Name()+"/"+o.name, func(t *testing.T) {
				c, srv := newServerClientProto(t, proto, trnGzipHandler(o.ok.Body))
				require.NoError(t, o.call(t.Context(), c), "the transport decodes gzip and removes the header")
				r := testutil.OnlyRequest(t, srv)
				assert.Equal(t, "gzip", r.Header.Get("Accept-Encoding"), "the transport asked for gzip")
			})
		}
	}
}

func TestTransportGzipUndecoded(t *testing.T) {
	for _, proto := range testutil.Protocols() {
		t.Run(proto.Name(), func(t *testing.T) {
			srv := testutil.NewServerProto(t, proto, trnGzipHandler(verifyOK))
			tr, ok := srv.Client().Transport.(*http.Transport)
			require.True(t, ok)
			tr = tr.Clone()
			tr.DisableCompression = true
			t.Cleanup(tr.CloseIdleConnections)
			c := newClient(t, emailable.WithTestBaseURL(srv.URL()), emailable.WithHTTPClient(&http.Client{Transport: tr}))
			o := opCaseNamed("VerifyEmail")
			trnFailed(t, o.call(t.Context(), c), o, emailable.FailureUnexpectedResponse, http.StatusOK, trnCauseEncoded)
			assert.Empty(t, testutil.OnlyRequest(t, srv).Header.Values("Accept-Encoding"))
		})
	}
}

// trnLimit returns the body limit of op under a client configured with
// configured (0 for the default): the configured limit for GetBatch, and at
// most 1 MiB for every other operation.
func trnLimit(op emailable.Operation, configured int64) int64 {
	if configured == 0 {
		configured = emailable.DefaultMaxResponseBytes
	}
	if op == emailable.OpGetBatch {
		return configured
	}
	return min(configured, 1<<20)
}

// trnLimitOptions returns the option that sets configured, none for 0.
func trnLimitOptions(configured int64) []emailable.Option {
	if configured == 0 {
		return nil
	}
	return []emailable.Option{emailable.WithMaxResponseBytes(configured)}
}

func TestTransportDeclaredLengthOverLimit(t *testing.T) {
	for _, configured := range []int64{0, 1024, 2 << 20, 64 << 20} {
		for _, o := range operations() {
			t.Run(strconv.FormatInt(configured, 10)+"/"+o.name, func(t *testing.T) {
				body := testutil.NewBody(o.ok.Body)
				declared := trnLimit(o.op, configured) + 1
				rt := testutil.TransportWith(http.StatusOK, nil, declared, body)
				err := o.call(t.Context(), newTransportClient(t, rt, trnLimitOptions(configured)...))
				trnFailed(t, err, o, emailable.FailureResponseTooLarge, http.StatusOK, trnCauseTooLarge)
				assert.Zero(t, body.Reads(), "a declared length over the limit is refused unread")
				assert.Equal(t, 1, body.Closes())
			})
		}
	}
}

// trnSized calls o through a body of exactly n bytes, declared or of unknown
// length, under a client configured with configured (0 for the default), and
// returns the body and the error.
func trnSized(t *testing.T, o opCase, n int, declared bool, configured int64) (*testutil.Body, error) {
	t.Helper()
	body := testutil.NewBody(padded(t, o.ok.Body, n))
	cl := int64(-1)
	if declared {
		cl = int64(n)
	}
	c := newTransportClient(t, testutil.TransportWith(http.StatusOK, nil, cl, body), trnLimitOptions(configured)...)
	return body, o.call(t.Context(), c)
}

// TestTransportBodyLimits proves the limit of every operation: exactly the
// limit is accepted and one byte more refused, declared or not, and never
// more than one byte past it is read. Every operation but GetBatch is capped
// at 1 MiB whatever the configured limit; GetBatch uses the configured one.
func TestTransportBodyLimits(t *testing.T) {
	for _, configured := range []int64{1024, 4096, 2 << 20, 64 << 20} {
		for _, o := range operations() {
			if o.op == emailable.OpGetBatch && configured == 64<<20 {
				continue // 64 MiB bodies: the declared-length test covers this limit
			}
			for _, declared := range []bool{false, true} {
				limit := int(trnLimit(o.op, configured))
				name := strconv.FormatInt(configured, 10) + "/" + o.name + "/declared=" + strconv.FormatBool(declared)
				t.Run(name, func(t *testing.T) {
					body, err := trnSized(t, o, limit, declared, configured)
					require.NoError(t, err, "exactly the limit, %d bytes, is accepted", limit)
					assert.Equal(t, int64(limit), body.BytesRead())

					body, err = trnSized(t, o, limit+1, declared, configured)
					trnFailed(t, err, o, emailable.FailureResponseTooLarge, http.StatusOK, trnCauseTooLarge)
					assert.LessOrEqual(t, body.BytesRead(), int64(limit+1), "never more than limit+1 bytes are read")
					assert.Equal(t, 1, body.Closes())
				})
			}
		}
	}
}

func TestTransportDefaultBodyLimits(t *testing.T) {
	for _, o := range operations() {
		t.Run(o.name, func(t *testing.T) {
			limit := int(trnLimit(o.op, 0))
			_, err := trnSized(t, o, limit, false, 0)
			require.NoError(t, err, "the default limit, %d bytes, is accepted", limit)
			_, err = trnSized(t, o, limit+1, false, 0)
			trnFailed(t, err, o, emailable.FailureResponseTooLarge, http.StatusOK, trnCauseTooLarge)
		})
	}
	assert.Equal(t, int64(8<<20), trnLimit(emailable.OpGetBatch, 0))
	assert.Equal(t, int64(1<<20), trnLimit(emailable.OpVerifyEmail, 0))
}

func TestTransportTruncatedBody(t *testing.T) {
	tests := []struct {
		name     string
		content  func(ok string) string
		declared func(ok string) int64
	}{
		{
			name:     "one byte short",
			content:  func(ok string) string { return ok[:len(ok)-1] },
			declared: func(ok string) int64 { return int64(len(ok)) },
		},
		{
			name:     "empty body declared",
			content:  func(string) string { return "" },
			declared: func(ok string) int64 { return int64(len(ok)) },
		},
		{
			name:     "longer than declared",
			content:  func(ok string) string { return ok },
			declared: func(ok string) int64 { return int64(len(ok) - 1) },
		},
		{
			name:     "much longer than declared",
			content:  func(ok string) string { return ok },
			declared: func(string) int64 { return 2 },
		},
		{
			name:     "declared zero",
			content:  func(ok string) string { return ok },
			declared: func(string) int64 { return 0 },
		},
		{
			name:     "large declared length",
			content:  func(ok string) string { return ok },
			declared: func(string) int64 { return 1<<20 - 1 },
		},
	}
	for _, tt := range tests {
		for _, o := range operations() {
			t.Run(tt.name+"/"+o.name, func(t *testing.T) {
				body := testutil.NewBody(tt.content(o.ok.Body))
				c := newTransportClient(t, testutil.TransportWith(http.StatusOK, nil, tt.declared(o.ok.Body), body))
				trnFailed(t, o.call(t.Context(), c), o, emailable.FailureTransport, http.StatusOK, trnCauseClosed)
				assert.Equal(t, 1, body.Closes())
			})
		}
	}
	// A GetBatch body declared longer than 1 MiB, within its configured
	// limit, is read as it arrives rather than into a buffer of that size.
	t.Run("declared over 1 MiB/GetBatch", func(t *testing.T) {
		o := opCaseNamed("GetBatch")
		body := testutil.NewBody(o.ok.Body)
		c := newTransportClient(t, testutil.TransportWith(http.StatusOK, nil, 2<<20, body),
			emailable.WithMaxResponseBytes(4<<20))
		trnFailed(t, o.call(t.Context(), c), o, emailable.FailureTransport, http.StatusOK, trnCauseClosed)
		assert.Equal(t, int64(len(o.ok.Body)), body.BytesRead())
	})
}

func TestTransportTruncatedByTheServer(t *testing.T) {
	o := opCaseNamed("VerifyEmail")
	t.Run("http1 connection closed", func(t *testing.T) {
		c, _ := newServerClient(t, func(w http.ResponseWriter, _ *http.Request) {
			conn, buf, err := w.(http.Hijacker).Hijack()
			if err != nil {
				return
			}
			_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: " +
				strconv.Itoa(len(verifyOK)) + "\r\n\r\n" + verifyOK[:10])
			_ = buf.Flush()
			_ = conn.Close()
		})
		trnFailed(t, o.call(t.Context(), c), o, emailable.FailureTransport, http.StatusOK, trnCauseClosed)
	})
	t.Run("http2 stream aborted", func(t *testing.T) {
		c, _ := newServerClientProto(t, testutil.HTTP2, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", mediaJSON)
			w.Header().Set("Content-Length", strconv.Itoa(len(verifyOK)))
			_, _ = io.WriteString(w, verifyOK[:10])
			w.(http.Flusher).Flush()
			panic(http.ErrAbortHandler)
		})
		err := o.call(t.Context(), c)
		_ = assertCallError(t, err, wantCall{
			Kind: emailable.FailureTransport, Op: emailable.OpVerifyEmail, Status: http.StatusOK, Attempts: 1,
			OutcomeUnknown: true,
		}, "INTERNAL_ERROR", "stream")
	})
}

func TestTransportBodyReadError(t *testing.T) {
	tests := []struct {
		name  string
		end   error
		cause string
	}{
		{name: "unexpected EOF", end: io.ErrUnexpectedEOF, cause: trnCauseClosed},
		{name: "closed connection", end: net.ErrClosed, cause: trnCauseClosed},
		{name: "reset", end: &net.OpError{Op: "read", Net: trnTCP, Err: os.NewSyscallError("read", syscall.ECONNRESET)}, cause: trnCauseReset},
		{name: "planted error", end: errTrnPlanted, cause: trnCauseOther},
	}
	for _, tt := range tests {
		for _, cl := range []int64{-1, int64(len(verifyOK))} {
			t.Run(tt.name+"/"+strconv.FormatInt(cl, 10), func(t *testing.T) {
				body := testutil.NewBody(verifyOK[:10])
				body.End = tt.end
				o := opCaseNamed("VerifyEmail")
				c := newTransportClient(t, testutil.TransportWith(http.StatusOK, nil, cl, body))
				trnFailed(t, o.call(t.Context(), c), o, emailable.FailureTransport, http.StatusOK, tt.cause)
				assert.Equal(t, 1, body.Closes())
			})
		}
	}
}

func TestTransportStalledBody(t *testing.T) {
	tests := []struct {
		name    string
		empties int  // reads that return nothing before each read of content
		stall   bool // the body answers (0, nil) forever after its content
		ok      bool
	}{
		{name: "99 empty reads before each chunk", empties: 99, ok: true},
		{name: "100 empty reads", empties: 100},
		{name: "stalls after the content", stall: true},
	}
	for _, tt := range tests {
		for _, o := range operations() {
			t.Run(tt.name+"/"+o.name, func(t *testing.T) {
				body := testutil.NewBody(o.ok.Body)
				body.Step = 8
				body.Empties = tt.empties
				body.Stall = tt.stall
				c := newTransportClient(t, testutil.TransportWith(http.StatusOK, nil, -1, body))
				err := o.call(t.Context(), c)
				assert.Equal(t, 1, body.Closes())
				if tt.ok {
					require.NoError(t, err)
					return
				}
				trnFailed(t, err, o, emailable.FailureTransport, http.StatusOK, trnCauseStalled)
			})
		}
	}
}

// trnTransportError is a transport error and the fixed cause it reduces to.
type trnTransportError struct {
	name  string
	err   error
	cause string
}

// trnTransportErrors lists transport errors and the class each reduces to.
func trnTransportErrors() []trnTransportError {
	dnsErr := &net.DNSError{Err: "no such host " + trnMarker, Name: trnMarker + ".example.com", Server: trnMarker, IsNotFound: true}
	dnsTimeout := &net.DNSError{Err: "timeout " + trnMarker, Name: trnMarker + ".example.com", IsTimeout: true}
	timeout := testutil.TimeoutError{Msg: "synthetic network timeout " + trnMarker}
	reset := os.NewSyscallError("read", syscall.ECONNRESET)
	tlsCert := &tls.CertificateVerificationError{Err: errTrnPlanted}
	return []trnTransportError{
		{"dns", dnsErr, trnCauseDNS},
		{"dns inside dial", &net.OpError{Op: "dial", Net: trnTCP, Err: dnsErr}, trnCauseDNS},
		{"dns wrapped", fmt.Errorf("lookup %s: %w", trnMarker, dnsErr), trnCauseDNS},
		{"dns timeout", dnsTimeout, trnCauseTimeout},
		{"proxy", &net.OpError{Op: "proxyconnect", Net: trnTCP, Err: errTrnRefused}, trnCauseProxy},
		{"proxy reset", &net.OpError{Op: "proxyconnect", Net: trnTCP, Err: reset}, trnCauseProxy},
		{"proxy TLS", &net.OpError{Op: "proxyconnect", Net: trnTCP, Err: tlsCert}, trnCauseProxy},
		{"TLS record header", tls.RecordHeaderError{Msg: "bad record " + trnMarker, RecordHeader: [5]byte{'X', 'Y', 'Z', 0, 0}}, trnCauseTLS},
		{"TLS alert", tls.AlertError(40), trnCauseTLS},
		{"TLS certificate", tlsCert, trnCauseTLS},
		{"TLS inside a read", &net.OpError{Op: "remote error", Net: trnTCP, Err: tls.AlertError(42)}, trnCauseTLS},
		{"connection reset", &net.OpError{Op: "read", Net: trnTCP, Err: reset}, trnCauseReset},
		{"connection aborted", &net.OpError{Op: "read", Net: trnTCP, Err: os.NewSyscallError("read", syscall.ECONNABORTED)}, trnCauseReset},
		{"broken pipe", &net.OpError{Op: "write", Net: trnTCP, Err: os.NewSyscallError("write", syscall.EPIPE)}, trnCauseReset},
		{"bare reset errno", syscall.ECONNRESET, trnCauseReset},
		{"reset while dialing", &net.OpError{Op: "dial", Net: trnTCP, Err: reset}, trnCauseReset},
		{"dial refused", &net.OpError{Op: "dial", Net: trnTCP, Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}, trnCauseDial},
		{"dial other", &net.OpError{Op: "dial", Net: trnTCP, Err: errTrnRefused}, trnCauseDial},
		{"eof", io.EOF, trnCauseClosed},
		{"unexpected eof", io.ErrUnexpectedEOF, trnCauseClosed},
		{"wrapped eof", fmt.Errorf("read %s: %w", trnMarker, io.EOF), trnCauseClosed},
		{"closed connection", net.ErrClosed, trnCauseClosed},
		{"closed inside a read", &net.OpError{Op: "read", Net: trnTCP, Err: net.ErrClosed}, trnCauseClosed},
		{"other", errTrnPlanted, trnCauseOther},
		{"other inside a read", &net.OpError{Op: "read", Net: trnTCP, Err: errTrnPlanted}, trnCauseOther},
		{"canceled by the transport alone", context.Canceled, trnCauseOther},
		{"URL error from the transport", &url.Error{Op: "Get", URL: "https://127.0.0.1/?m=" + trnMarker, Err: errTrnPlanted}, trnCauseOther},
		{"network timeout", timeout, trnCauseTimeout},
		{"dial timeout", &net.OpError{Op: "dial", Net: trnTCP, Err: timeout}, trnCauseTimeout},
		{"deadline from the transport", context.DeadlineExceeded, trnCauseTimeout},
		{"wrapped deadline", fmt.Errorf("%s: %w", trnMarker, context.DeadlineExceeded), trnCauseTimeout},
	}
}

func TestTransportErrorClasses(t *testing.T) {
	for _, tt := range trnTransportErrors() {
		t.Run(tt.name, func(t *testing.T) {
			ct := &testutil.CountingTransport{Next: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
				testutil.Drain(r)
				return nil, tt.err
			})}
			kind := emailable.FailureTransport
			if tt.cause == trnCauseTimeout {
				kind = emailable.FailureTimeout
			}
			cases := opCases()
			for _, o := range cases {
				err := o.call(t.Context(), newTransportClient(t, ct))
				_ = assertCallError(t, err, o.want(wantCall{Kind: kind, Attempts: 1, OutcomeUnknown: o.paid}),
					trnMarker, "127.0.0.1", "/v1/")
				assert.Equal(t, tt.cause, causeText(t, err), "the fixed class of the cause, for %s", o.name)
			}
			assert.Equal(t, len(cases), ct.Attempts(), "exactly one attempt per call")
		})
	}
}

func TestTransportRealTLSFailure(t *testing.T) {
	for _, proto := range testutil.Protocols() {
		t.Run(proto.Name(), func(t *testing.T) {
			srv := testutil.NewServerProto(t, proto, testutil.Serve(testutil.Step{Body: verifyOK}))
			// A client without a transport uses http.DefaultTransport, which
			// does not trust the test server's certificate.
			c := newClient(t, emailable.WithTestBaseURL(srv.URL()), emailable.WithHTTPClient(&http.Client{}))
			_, err := c.VerifyEmail(t.Context(), verifyRequest())
			_ = assertCallError(t, err, wantCall{
				Kind: emailable.FailureTransport, Op: emailable.OpVerifyEmail, Attempts: 1, OutcomeUnknown: true,
			}, srv.URL(), strings.TrimPrefix(srv.URL(), "https://"), "x509", "certificate")
			assert.Equal(t, trnCauseTLS, causeText(t, err))
			assert.Zero(t, srv.Hits(), "the handshake fails before any request is sent")
		})
	}
}

func TestTransportRealDialFailure(t *testing.T) {
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	base, hc := srv.URL, srv.Client()
	srv.Close()
	_, port, err := net.SplitHostPort(strings.TrimPrefix(base, "https://"))
	require.NoError(t, err)

	ct := &testutil.CountingTransport{Next: hc.Transport}
	c := newClient(t, emailable.WithTestBaseURL(base), emailable.WithHTTPClient(&http.Client{Transport: ct}))
	_, err = c.AccountInfo(t.Context(), emailable.AccountInfoRequest{})
	_ = assertCallError(t, err, wantCall{Kind: emailable.FailureTransport, Op: emailable.OpAccountInfo, Attempts: 1},
		":"+port, "127.0.0.1", "refused")
	assert.Equal(t, trnCauseDial, causeText(t, err))
	assert.Equal(t, 1, ct.Attempts())
}

// trnStalling returns a transport that never answers, or, with midBody, one
// that answers HTTP 200 with a body that stops after a few bytes until it is
// closed.
func trnStalling(midBody bool) testutil.RoundTripFunc {
	if !midBody {
		return testutil.BlockUntilDone
	}
	return func(r *http.Request) (*http.Response, error) {
		return testutil.TransportWith(http.StatusOK, nil, -1, testutil.NewBlockingBody(`{"email":`))(r)
	}
}

// trnStage names where a call stalls.
func trnStage(midBody bool) string {
	if midBody {
		return "mid-body"
	}
	return "awaiting headers"
}

// TestTransportTimeoutClassification proves how a stalled call ends: at the
// SDK's timeout as FailureTimeout, which matches ErrTimeout and
// context.DeadlineExceeded, or at the caller's earlier deadline as
// FailureDeadlineExceeded, which matches only context.DeadlineExceeded. The
// request was sent either way, so a paid operation's outcome is unknown.
func TestTransportTimeoutClassification(t *testing.T) {
	tests := []struct {
		name       string
		sdkTimeout time.Duration // 0 keeps DefaultTimeout
		deadline   time.Duration // the caller's; 0 for none
		kind       emailable.FailureKind
		elapsed    time.Duration
	}{
		{name: "SDK timeout", sdkTimeout: 7 * time.Second, kind: emailable.FailureTimeout, elapsed: 7 * time.Second},
		{name: "default timeout", kind: emailable.FailureTimeout, elapsed: emailable.DefaultTimeout},
		{
			name: "SDK timeout before the caller deadline", sdkTimeout: 4 * time.Second, deadline: 6 * time.Second,
			kind: emailable.FailureTimeout, elapsed: 4 * time.Second,
		},
		{
			name: "caller deadline before the SDK timeout", sdkTimeout: 10 * time.Second, deadline: 3 * time.Second,
			kind: emailable.FailureDeadlineExceeded, elapsed: 3 * time.Second,
		},
	}
	for _, tt := range tests {
		for _, midBody := range []bool{false, true} {
			for _, o := range opCases() {
				t.Run(tt.name+"/"+trnStage(midBody)+"/"+o.name, func(t *testing.T) {
					synctest.Test(t, func(t *testing.T) {
						trnTimeoutCase(t, o, midBody, tt.sdkTimeout, tt.deadline, tt.kind, tt.elapsed)
					})
				})
			}
		}
	}
}

// trnTimeoutCase runs one case of TestTransportTimeoutClassification inside
// its bubble.
func trnTimeoutCase(t *testing.T, o opCase, midBody bool, sdkTimeout, deadline time.Duration,
	kind emailable.FailureKind, elapsed time.Duration,
) {
	t.Helper()
	ct := &testutil.CountingTransport{Next: trnStalling(midBody)}
	var opts []emailable.Option
	if sdkTimeout > 0 {
		opts = append(opts, emailable.WithTimeout(sdkTimeout))
	}
	c := newTransportClient(t, ct, opts...)
	ctx := t.Context()
	if deadline > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, deadline)
		defer cancel()
	}
	start := time.Now()
	err := o.call(ctx, c)
	assert.Equal(t, elapsed, time.Since(start), "the call ends exactly at the first deadline")
	status := 0
	if midBody {
		status = http.StatusOK
	}
	_ = assertCallError(t, err, o.want(wantCall{Kind: kind, Status: status, Attempts: 1, OutcomeUnknown: o.paid}))
	assert.Equal(t, context.DeadlineExceeded, errors.Unwrap(callError(t, err)), "the cause is the context error alone")
	assert.Equal(t, 1, ct.Attempts())
}

// TestTransportCallerCancelMidFlight proves that a cancellation after the
// request was handed to the transport is FailureCanceled with one attempt,
// and leaves a paid operation's outcome unknown.
func TestTransportCallerCancelMidFlight(t *testing.T) {
	for _, midBody := range []bool{false, true} {
		for _, o := range opCases() {
			t.Run(trnStage(midBody)+"/"+o.name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					ct := &testutil.CountingTransport{Next: trnStalling(midBody)}
					c := newTransportClient(t, ct)
					ctx, cancel := context.WithCancel(t.Context())
					defer cancel()
					done := make(chan error, 1)
					go func() { done <- o.call(ctx, c) }()
					synctest.Wait() // the call is stalled in the transport or the body
					start := time.Now()
					cancel()
					err := <-done
					assert.Zero(t, time.Since(start), "a cancellation ends the call at once")
					status := 0
					if midBody {
						status = http.StatusOK
					}
					_ = assertCallError(t, err, o.want(wantCall{
						Kind: emailable.FailureCanceled, Status: status, Attempts: 1, OutcomeUnknown: o.paid,
					}))
					assert.Same(t, context.Canceled, errors.Unwrap(callError(t, err)))
					assert.Equal(t, 1, ct.Attempts())
				})
			})
		}
	}
}
