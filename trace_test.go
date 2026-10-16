package emailable_test

import (
	"context"
	"crypto/tls"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/textproto"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-emailable"
	"github.com/mrz1836/go-emailable/internal/testutil"
)

// traceRecorder collects everything a caller's httptrace hooks receive.
type traceRecorder struct {
	mu     sync.Mutex
	calls  map[string]int
	values []string            // every value any hook received
	fields map[string][]string // header field values written, by lowercased name
	names  []string            // header field names written, lowercased, in order
}

// newTraceRecorder returns an empty recorder.
func newTraceRecorder() *traceRecorder {
	return &traceRecorder{calls: map[string]int{}, fields: map[string][]string{}}
}

// record records one call of hook with the values it received.
func (r *traceRecorder) record(hook string, values ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls[hook]++
	r.values = append(r.values, values...)
}

// wrote records one header field written.
func (r *traceRecorder) wrote(name string, values []string) {
	r.record("WroteHeaderField", append([]string{name}, values...)...)
	r.mu.Lock()
	defer r.mu.Unlock()
	lower := strings.ToLower(name)
	r.names = append(r.names, lower)
	r.fields[lower] = append(r.fields[lower], values...)
}

// traceSnapshot is what a recorder holds at one moment.
type traceSnapshot struct {
	calls  map[string]int
	values []string
	fields map[string][]string
	names  []string
}

// snapshot returns a copy of what the recorder holds.
func (r *traceRecorder) snapshot() traceSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	return traceSnapshot{
		calls: maps.Clone(r.calls), values: slices.Clone(r.values), fields: maps.Clone(r.fields),
		names: slices.Clone(r.names),
	}
}

// trace returns a ClientTrace with every hook set, each recording what it
// receives.
func (r *traceRecorder) trace() *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		GetConn: func(hostPort string) { r.record("GetConn", hostPort) },
		GotConn: func(info httptrace.GotConnInfo) {
			r.record("GotConn", info.Conn.RemoteAddr().String(), info.Conn.LocalAddr().String())
		},
		PutIdleConn:          func(err error) { r.record("PutIdleConn", traceErrText(err)) },
		GotFirstResponseByte: func() { r.record("GotFirstResponseByte") },
		Got100Continue:       func() { r.record("Got100Continue") },
		Got1xxResponse: func(_ int, header textproto.MIMEHeader) error {
			r.record("Got1xxResponse", traceMIMEText(header)...)
			return nil
		},
		DNSStart: func(info httptrace.DNSStartInfo) { r.record("DNSStart", info.Host) },
		DNSDone: func(info httptrace.DNSDoneInfo) {
			values := make([]string, 0, 1+len(info.Addrs))
			values = append(values, traceErrText(info.Err))
			for _, a := range info.Addrs {
				values = append(values, a.String())
			}
			r.record("DNSDone", values...)
		},
		ConnectStart:      func(network, addr string) { r.record("ConnectStart", network, addr) },
		ConnectDone:       func(network, addr string, err error) { r.record("ConnectDone", network, addr, traceErrText(err)) },
		TLSHandshakeStart: func() { r.record("TLSHandshakeStart") },
		TLSHandshakeDone: func(cs tls.ConnectionState, err error) {
			r.record("TLSHandshakeDone", cs.ServerName, cs.NegotiatedProtocol, traceErrText(err))
		},
		WroteHeaderField: r.wrote,
		WroteHeaders:     func() { r.record("WroteHeaders") },
		Wait100Continue:  func() { r.record("Wait100Continue") },
		WroteRequest:     func(info httptrace.WroteRequestInfo) { r.record("WroteRequest", traceErrText(info.Err)) },
	}
}

// traceErrText returns err's text, or "" for nil.
func traceErrText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// traceMIMEText returns every name and value of h.
func traceMIMEText(h textproto.MIMEHeader) []string {
	var out []string
	for name, values := range h {
		out = append(append(out, name), values...)
	}
	return out
}

// traceHookPointers returns the code pointer of every hook of trace, to
// detect a hook that was replaced.
func traceHookPointers(trace *httptrace.ClientTrace) []uintptr {
	v := reflect.ValueOf(trace).Elem()
	out := make([]uintptr, v.NumField())
	for i := range out {
		if f := v.Field(i); f.Kind() == reflect.Func && !f.IsNil() {
			out[i] = f.Pointer()
		}
	}
	return out
}

// traceSDKHeaders are the request headers the SDK sets, lowercased:
// Content-Type only on a POST.
func traceSDKHeaders(method string) []string {
	h := []string{"accept", "authorization", "user-agent"}
	if method == http.MethodPost {
		h = append(h, "content-type")
	}
	return h
}

// traceTransportHeaders are the header fields the standard library's
// transport adds on its own, lowercased: the host (the pseudo-header fields on
// HTTP/2), Accept-Encoding, since it decodes gzip itself, and the length of a
// POST body.
func traceTransportHeaders(proto testutil.Protocol, method string) []string {
	h := []string{"accept-encoding", "host"}
	if proto == testutil.HTTP2 {
		h = []string{":authority", ":method", ":path", ":scheme", "accept-encoding"}
	}
	if method == http.MethodPost {
		h = append(h, "content-length")
	}
	return h
}

// traceWantNames returns every header field name the trace must see written,
// once each, sorted.
func traceWantNames(proto testutil.Protocol, method string) []string {
	return slices.Sorted(slices.Values(slices.Concat(traceSDKHeaders(method), traceTransportHeaders(proto, method))))
}

// traceAssertOneAttempt asserts that the trace saw exactly one connection
// request, one connection and one request written.
func traceAssertOneAttempt(tb testing.TB, snap traceSnapshot) {
	tb.Helper()
	for _, hook := range []string{"GetConn", "GotConn", "WroteHeaders", "WroteRequest", "GotFirstResponseByte"} {
		assert.Equal(tb, 1, snap.calls[hook], "hook %s runs once per call", hook)
	}
	assert.LessOrEqual(tb, snap.calls["ConnectStart"], 1, "at most one dial")
	assert.Zero(tb, snap.calls["Got100Continue"]+snap.calls["Wait100Continue"], "no Expect: 100-continue")
}

// TestTraceOneAttemptPerCall runs every operation over HTTP/1.1 and HTTP/2
// with an httptrace.ClientTrace on the context. Each call is exactly one
// attempt: one GetConn, one GotConn and one WroteRequest. It also documents
// the trust boundary: a trace hook sees every header field written, the
// Authorization header with the API key included, and over HTTP/2 the
// request path with its query, which for VerifyEmail holds the address. The
// SDK sets exactly Accept, User-Agent, Authorization and, on a POST,
// Content-Type; the transport adds the host, Accept-Encoding and a POST's
// Content-Length. The caller's context, values and hooks are left as they
// were.
func TestTraceOneAttemptPerCall(t *testing.T) {
	for _, proto := range testutil.Protocols() {
		t.Run(proto.Name(), func(t *testing.T) {
			c, srv := newServerClientProto(t, proto, okHandler())
			for i, o := range opCases() {
				t.Run(o.name, func(t *testing.T) {
					rec := newTraceRecorder()
					trace := rec.trace()
					before := traceHookPointers(trace)
					type ctxKey struct{}
					ctx := httptrace.WithClientTrace(context.WithValue(t.Context(), ctxKey{}, "caller value"), trace)

					require.NoError(t, o.call(ctx, c))

					snap := rec.snapshot()
					traceAssertOneAttempt(t, snap)
					assert.Equal(t, traceWantNames(proto, o.method), slices.Sorted(slices.Values(snap.names)),
						"exactly these header fields, each once")
					assert.Equal(t, []string{"Bearer " + testKey}, snap.fields["authorization"],
						"a trace hook sees the API key: it is trusted code")
					assert.Equal(t, []string{"go-emailable"}, snap.fields["user-agent"])
					assert.Equal(t, []string{mediaJSON}, snap.fields["accept"])
					traceAssertPath(t, proto, o, snap)

					assert.Equal(t, before, traceHookPointers(trace), "the caller's hooks are never replaced")
					assert.Same(t, trace, httptrace.ContextClientTrace(ctx), "the caller's context keeps its trace")
					assert.Equal(t, "caller value", ctx.Value(ctxKey{}))
					require.NoError(t, ctx.Err(), "the call never ends the caller's context")
					assert.Equal(t, i+1, srv.Hits(), "one request per call")
				})
			}
		})
	}
}

// traceAssertPath asserts what the trace saw of the request URL: over HTTP/2
// the path and query, the address for a verification included; over HTTP/1.1
// nothing of either, since the request line is not a header field.
func traceAssertPath(tb testing.TB, proto testutil.Protocol, o opCase, snap traceSnapshot) {
	tb.Helper()
	encoded := url.QueryEscape(testEmail)
	if proto == testutil.HTTP1 {
		assert.Empty(tb, snap.fields[":path"])
		testutil.AssertRedacted(tb, strings.Join(snap.values, "\n"), []string{testEmail, encoded}, "HTTP/1.1 trace")
		return
	}
	paths := snap.fields[":path"]
	require.Len(tb, paths, 1)
	assert.True(tb, strings.HasPrefix(paths[0], o.path), "path %q", paths[0])
	if o.path == pathVerify {
		assert.Contains(tb, paths[0], "email="+encoded, "an HTTP/2 trace hook sees the address in the query")
	} else {
		assert.NotContains(tb, paths[0], encoded)
	}
}

// TestTraceSDKAddsNoHooks proves, over an in-memory transport that never
// runs a hook, that the SDK neither calls a trace hook itself nor adds hooks
// of its own: the transport receives the caller's trace as it is (composing
// another trace would make a new one), with the caller's values, and no hook
// ever runs.
func TestTraceSDKAddsNoHooks(t *testing.T) {
	for _, o := range opCases() {
		t.Run(o.name, func(t *testing.T) {
			rec := newTraceRecorder()
			trace := rec.trace()
			type ctxKey struct{}
			ctx := httptrace.WithClientTrace(context.WithValue(t.Context(), ctxKey{}, "caller value"), trace)
			var gotTrace *httptrace.ClientTrace
			var gotValue any
			rt := testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
				gotTrace = httptrace.ContextClientTrace(r.Context())
				gotValue = r.Context().Value(ctxKey{})
				return testutil.Reply(r, o.ok)
			})
			require.NoError(t, o.call(ctx, newTransportClient(t, rt)))
			assert.Same(t, trace, gotTrace, "the transport sees the caller's own trace")
			assert.Equal(t, "caller value", gotValue)
			assert.Empty(t, rec.snapshot().calls, "the SDK never runs a hook itself")
		})
	}
}

// traceAssertHooks asserts that each hook of once ran exactly once and each
// hook of never did not run.
func traceAssertHooks(tb testing.TB, snap traceSnapshot, once, never []string) {
	tb.Helper()
	for _, hook := range once {
		assert.Equal(tb, 1, snap.calls[hook], "hook %s runs once", hook)
	}
	for _, hook := range never {
		assert.Zero(tb, snap.calls[hook], "hook %s never runs", hook)
	}
}

// TestTraceFailingCallOneAttempt proves that a call the provider fails still
// reports exactly one attempt in the trace, and in CallError.Attempts.
func TestTraceFailingCallOneAttempt(t *testing.T) {
	for _, proto := range testutil.Protocols() {
		down := testutil.Step{Status: http.StatusServiceUnavailable, Body: `{"message":"down"}`}
		c, _ := newServerClientProto(t, proto, testutil.Serve(down))
		for _, o := range opCases() {
			t.Run(proto.Name()+"/"+o.name+"/HTTP 503", func(t *testing.T) {
				rec := newTraceRecorder()
				err := o.call(httptrace.WithClientTrace(t.Context(), rec.trace()), c)
				_ = assertCallError(t, err, o.want(wantCall{
					Kind: emailable.FailureUnavailable, Status: http.StatusServiceUnavailable, Attempts: 1,
					OutcomeUnknown: o.paid,
				}))
				traceAssertOneAttempt(t, rec.snapshot())
			})
		}
	}
	t.Run("VerifyEmail/HTTP 249", func(t *testing.T) {
		o := opCaseNamed("VerifyEmail")
		c, srv := newServerClient(t, testutil.Serve(testutil.Step{Status: emailable.StatusPending, Body: pending}))
		rec := newTraceRecorder()
		err := o.call(httptrace.WithClientTrace(t.Context(), rec.trace()), c)
		_ = assertCallError(t, err, o.want(wantCall{
			Kind: emailable.FailurePending, Status: emailable.StatusPending, Attempts: 1,
		}))
		traceAssertOneAttempt(t, rec.snapshot())
		assert.Equal(t, 1, srv.Hits(), "a pending answer is never repeated")
	})
}

// TestTraceConnectionFailureOneAttempt proves that a call whose connection
// fails, in the TLS handshake or at the dial, reports exactly one attempt in
// the trace, and in CallError.Attempts, and writes nothing.
func TestTraceConnectionFailureOneAttempt(t *testing.T) {
	srv := testutil.NewServer(t, okHandler())
	closed := httptest.NewTLSServer(http.NotFoundHandler())
	refusedBase, refusedClient := closed.URL, closed.Client()
	closed.Close()
	tests := []struct {
		name   string
		client func(t *testing.T) *emailable.Client
		once   []string
		never  []string
	}{
		{
			name: "TLS failure",
			client: func(t *testing.T) *emailable.Client {
				t.Helper()
				// A transport that does not trust the test certificate.
				tr := &http.Transport{}
				t.Cleanup(tr.CloseIdleConnections)
				return newClient(t, emailable.WithTestBaseURL(srv.URL()), emailable.WithHTTPClient(&http.Client{Transport: tr}))
			},
			once:  []string{"GetConn", "ConnectStart", "ConnectDone", "TLSHandshakeStart", "TLSHandshakeDone"},
			never: []string{"GotConn", "WroteHeaders", "WroteRequest", "GotFirstResponseByte"},
		},
		{
			name: "connection refused",
			client: func(t *testing.T) *emailable.Client {
				t.Helper()
				return newClient(t, emailable.WithTestBaseURL(refusedBase), emailable.WithHTTPClient(refusedClient))
			},
			once:  []string{"GetConn", "ConnectStart", "ConnectDone"},
			never: []string{"TLSHandshakeStart", "GotConn", "WroteHeaders", "WroteRequest", "GotFirstResponseByte"},
		},
	}
	for _, tt := range tests {
		for _, o := range opCases() {
			t.Run(tt.name+"/"+o.name, func(t *testing.T) {
				rec := newTraceRecorder()
				err := o.call(httptrace.WithClientTrace(t.Context(), rec.trace()), tt.client(t))
				_ = assertCallError(t, err, o.want(wantCall{
					Kind: emailable.FailureTransport, Attempts: 1, OutcomeUnknown: o.paid,
				}))
				traceAssertHooks(t, rec.snapshot(), tt.once, tt.never)
			})
		}
	}
	assert.Zero(t, srv.Hits(), "no request got through")
}
