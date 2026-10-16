package emailable_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-emailable"
	"github.com/mrz1836/go-emailable/internal/testutil"
)

// The SDK never follows a redirect: its copy of the http.Client refuses
// them, and its transport wrapper removes Location from every 3xx before the
// client sees it. Whether CloseIdleConnections on that copy reaches the
// injected transport is not observable from outside the package, because the
// copy is not exported; the internal tests cover it.

// Redirect fixtures. redirectMarker is planted in every Location, redirect
// body and cookie: it must never surface in an error, however printed or
// unwrapped.
const (
	redirectMarker     = "redirect-location-marker-7a31c4" // gitleaks:allow (synthetic test marker)
	redirectTargetPath = "/redirect-target/" + redirectMarker
	redirectUnparsable = "https://[::1" + redirectMarker + "/next"
	redirectTextPlain  = "text/plain; charset=utf-8"

	// redirectPermissiveTimeout is the injected permissive client's own
	// timeout, which the SDK's copy keeps and the caller's client must still
	// hold.
	redirectPermissiveTimeout = 7 * time.Second
)

// redirectStatuses are the redirect statuses, plus 300 and 304, which
// redirect nowhere but may still carry a Location.
func redirectStatuses() []int {
	return []int{
		http.StatusMultipleChoices, http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusNotModified, http.StatusTemporaryRedirect, http.StatusPermanentRedirect,
	}
}

// redirectFollowedStatuses are the statuses the standard client follows.
func redirectFollowedStatuses() []int {
	return []int{
		http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect,
	}
}

// redirectWhere is where a redirect points.
type redirectWhere int

const (
	redirectSameOriginRelative redirectWhere = iota
	redirectSameOriginAbsolute
	redirectCrossOrigin
)

// redirectWheres names every Location kind.
func redirectWheres() []struct {
	name  string
	where redirectWhere
} {
	return []struct {
		name  string
		where redirectWhere
	}{
		{"same origin relative", redirectSameOriginRelative},
		{"same origin absolute", redirectSameOriginAbsolute},
		{"cross origin", redirectCrossOrigin},
	}
}

// redirectAnswer is a redirect answer: status with the Location loc, a
// cookie, and a text body naming loc when status allows a body.
func redirectAnswer(status int, loc string) testutil.Step {
	step := testutil.Step{Status: status, Header: http.Header{
		"Content-Type": {redirectTextPlain},
		"Location":     {loc},
		"Set-Cookie":   {"redirect=" + redirectMarker + "; Path=/"},
	}}
	if status != http.StatusNotModified {
		step.Body = "moved to " + loc
	}
	return step
}

// redirectPair is an origin that redirects every request to the target
// path, on itself or on a second server. Both answer the target path with
// reply, so a followed redirect would make the call succeed.
type redirectPair struct {
	origin   *testutil.Server
	target   *testutil.Server
	location string
}

// redirectNewPair starts the servers of one redirect case.
func redirectNewPair(t *testing.T, proto testutil.Protocol, status int, where redirectWhere,
	reply testutil.Step,
) *redirectPair {
	t.Helper()
	p := &redirectPair{}
	var loc atomic.Pointer[string]
	serveTarget := testutil.Serve(reply)
	handler := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == redirectTargetPath {
			serveTarget(w, r)
			return
		}
		testutil.Serve(redirectAnswer(status, *loc.Load()))(w, r)
	}
	p.origin = testutil.NewServerProto(t, proto, handler)
	p.target = p.origin
	p.location = redirectTargetPath + "?next=" + redirectMarker
	switch where {
	case redirectSameOriginRelative:
	case redirectSameOriginAbsolute:
		p.location = p.origin.URL() + p.location
	case redirectCrossOrigin:
		p.target = testutil.NewServerProto(t, proto, handler)
		p.location = p.target.URL() + p.location
	}
	loc.Store(&p.location)
	return p
}

// targetHits counts the requests that reached the target path.
func (p *redirectPair) targetHits() int {
	n := 0
	for _, r := range p.target.Requests() {
		if r.Path == redirectTargetPath {
			n++
		}
	}
	return n
}

// redirectCaller is the *http.Client a caller injects, with what the test
// needs to prove afterward that the SDK left it alone.
type redirectCaller struct {
	hc       *http.Client
	followed *atomic.Int32
	jar      *cookiejar.Jar

	transport, cookieJar, checkRedirect string
	timeout                             time.Duration
}

// redirectServerCaller injects the test server's own client, which follows
// redirects by default.
func redirectServerCaller(srv *testutil.Server) *redirectCaller {
	return redirectWatch(srv.Client(), &atomic.Int32{}, nil)
}

// redirectPermissiveCaller injects a client over base that follows every
// redirect and keeps every cookie. The test servers share one certificate,
// so base trusts every one of them: only the SDK stands between the key and
// a redirect target.
func redirectPermissiveCaller(t *testing.T, base http.RoundTripper) *redirectCaller {
	t.Helper()
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	followed := &atomic.Int32{}
	hc := &http.Client{
		Transport: base,
		Jar:       jar,
		Timeout:   redirectPermissiveTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			followed.Add(1)
			return nil
		},
	}
	return redirectWatch(hc, followed, jar)
}

func redirectWatch(hc *http.Client, followed *atomic.Int32, jar *cookiejar.Jar) *redirectCaller {
	return &redirectCaller{
		hc: hc, followed: followed, jar: jar,
		transport:     redirectIdentity(hc.Transport),
		cookieJar:     redirectIdentity(hc.Jar),
		checkRedirect: redirectIdentity(hc.CheckRedirect),
		timeout:       hc.Timeout,
	}
}

// redirectIdentity identifies a pointer or function value by type and
// address.
func redirectIdentity(v any) string {
	rv := reflect.ValueOf(v)
	if !rv.IsValid() || rv.IsNil() {
		return fmt.Sprintf("%T(nil)", v)
	}
	return fmt.Sprintf("%T@%x", v, rv.Pointer())
}

// assertUntouched asserts that the caller's client was never consulted about
// a redirect, received no cookie for any of origins, and holds the same
// transport, jar, redirect policy and timeout as before.
func (c *redirectCaller) assertUntouched(t *testing.T, origins ...string) {
	t.Helper()
	assert.Zero(t, c.followed.Load(), "the caller's CheckRedirect is never consulted")
	assert.Equal(t, c.transport, redirectIdentity(c.hc.Transport), "the caller's transport is kept")
	assert.Equal(t, c.cookieJar, redirectIdentity(c.hc.Jar), "the caller's jar is kept")
	assert.Equal(t, c.checkRedirect, redirectIdentity(c.hc.CheckRedirect), "the caller's redirect policy is kept")
	assert.Equal(t, c.timeout, c.hc.Timeout, "the caller's timeout is kept")
	if c.jar == nil {
		return
	}
	for _, o := range origins {
		u, err := url.Parse(o)
		require.NoError(t, err)
		assert.Empty(t, c.jar.Cookies(u), "the caller's jar receives no cookie")
	}
}

// redirectAssertRefused asserts the outcome every redirect must have: o's
// unexpected_response carrying the 3xx status, after one attempt, its
// outcome unknown exactly for a paid operation, its cause the fixed
// unexpected-status error, with no trace of the Location, the marker or any
// of secrets, however printed or unwrapped.
func redirectAssertRefused(t *testing.T, err error, o opCase, status int, secrets ...string) {
	t.Helper()
	_ = assertCallError(t, err, o.want(wantCall{
		Kind: emailable.FailureUnexpectedResponse, Status: status, Attempts: 1,
		OutcomeUnknown: outcomeUnknownAfter(o.paid, emailable.FailureUnexpectedResponse),
	}), append([]string{redirectMarker}, secrets...)...)
	assert.Equal(t, causeStatus, causeText(t, err))
}

// redirectCase is one cell of the redirect matrix.
type redirectCase struct {
	name       string
	proto      testutil.Protocol
	status     int
	where      redirectWhere
	permissive bool
	op         opCase
}

// redirectCases returns protocol x status x Location kind x injected client
// x method.
func redirectCases() []redirectCase {
	cases := make([]redirectCase, 0, 2*len(redirectStatuses())*len(redirectWheres())*2*len(opCases()))
	for _, proto := range testutil.Protocols() {
		for _, status := range redirectStatuses() {
			for _, w := range redirectWheres() {
				for _, permissive := range []bool{false, true} {
					client := "server client"
					if permissive {
						client = "permissive client"
					}
					for _, o := range opCases() {
						cases = append(cases, redirectCase{
							name:  fmt.Sprintf("%s/%d/%s/%s/%s", proto.Name(), status, w.name, client, o.name),
							proto: proto, status: status, where: w.where, permissive: permissive, op: o,
						})
					}
				}
			}
		}
	}
	return cases
}

// TestRedirectNeverFollowed runs every redirect status, to the same origin
// (relative and absolute) and to another server, over HTTP/1.1 and HTTP/2,
// through the test server's own client and through a caller's client that
// would follow anything and keep every cookie, for every method. Each call
// makes exactly one request and fails as unexpected_response with the 3xx
// status; the target, which would answer with success, receives nothing, so
// neither the key nor the request ever reaches it.
func TestRedirectNeverFollowed(t *testing.T) {
	for _, tc := range redirectCases() {
		t.Run(tc.name, func(t *testing.T) {
			p := redirectNewPair(t, tc.proto, tc.status, tc.where, tc.op.ok)
			caller := redirectServerCaller(p.origin)
			if tc.permissive {
				caller = redirectPermissiveCaller(t, p.origin.Client().Transport)
			}
			c := newClient(t, emailable.WithTestBaseURL(p.origin.URL()), emailable.WithHTTPClient(caller.hc))

			err := tc.op.call(t.Context(), c)

			redirectAssertRefused(t, err, tc.op, tc.status, p.location, p.target.URL()+redirectTargetPath)
			r := testutil.OnlyRequest(t, p.origin)
			assert.Equal(t, tc.op.method, r.Method)
			assert.Equal(t, tc.op.path, r.Path)
			assert.Zero(t, p.targetHits(), "the redirect target is never reached")
			if p.target != p.origin {
				assert.Zero(t, p.target.Hits(), "the other origin receives nothing")
			}
			caller.assertUntouched(t, p.origin.URL(), p.target.URL())
		})
	}
}

// TestRedirectControlRawClientFollows proves the harness would notice a
// followed redirect: the permissive client of TestRedirectNeverFollowed,
// used directly with a plain GET, follows every redirect status the standard
// library follows to the target, which answers with success, and fills its
// jar.
func TestRedirectControlRawClientFollows(t *testing.T) {
	for _, proto := range testutil.Protocols() {
		for _, status := range redirectFollowedStatuses() {
			for _, w := range redirectWheres() {
				t.Run(fmt.Sprintf("%s/%d/%s", proto.Name(), status, w.name), func(t *testing.T) {
					p := redirectNewPair(t, proto, status, w.where, testutil.Step{Body: verifyOK})
					caller := redirectPermissiveCaller(t, p.origin.Client().Transport)
					req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, p.origin.URL()+pathVerify, nil)
					require.NoError(t, err)
					resp, err := caller.hc.Do(req)
					require.NoError(t, err)
					body, err := io.ReadAll(resp.Body)
					require.NoError(t, err)
					require.NoError(t, resp.Body.Close())

					assert.Equal(t, http.StatusOK, resp.StatusCode)
					assertBody(t, verifyOK, body)
					assert.Equal(t, 1, p.targetHits(), "the raw client reached the target")
					assert.Equal(t, int32(1), caller.followed.Load(), "the raw client consulted its CheckRedirect")
					u, err := url.Parse(p.origin.URL())
					require.NoError(t, err)
					assert.NotEmpty(t, caller.jar.Cookies(u), "the raw client filled its jar")
				})
			}
		}
	}
}

// TestRedirectUnparsableLocation sends a Location that url.Parse rejects
// over the wire. The standard client parses the Location of 301, 302, 303,
// 307 and 308 before it consults CheckRedirect, and its error quotes the
// value; the SDK still reports unexpected_response with the status, after
// exactly one request.
func TestRedirectUnparsableLocation(t *testing.T) {
	for _, proto := range testutil.Protocols() {
		for _, status := range redirectStatuses() {
			for _, permissive := range []bool{false, true} {
				for _, o := range opCases() {
					t.Run(fmt.Sprintf("%s/%d/permissive %t/%s", proto.Name(), status, permissive, o.name), func(t *testing.T) {
						srv := testutil.NewServerProto(t, proto, testutil.Serve(redirectAnswer(status, redirectUnparsable)))
						caller := redirectServerCaller(srv)
						if permissive {
							caller = redirectPermissiveCaller(t, srv.Client().Transport)
						}
						c := newClient(t, emailable.WithTestBaseURL(srv.URL()), emailable.WithHTTPClient(caller.hc))
						err := o.call(t.Context(), c)
						redirectAssertRefused(t, err, o, status, redirectUnparsable)
						assert.Equal(t, 1, srv.Hits(), "exactly one request")
						caller.assertUntouched(t, srv.URL())
					})
				}
			}
		}
	}
}

// redirectRawServer is an HTTP/1.1 TLS server that reads each request in
// full and answers it with fixed bytes, so that the response can hold what
// net/http would never write.
type redirectRawServer struct {
	ln       net.Listener
	response string
	requests atomic.Int64
	wg       sync.WaitGroup
}

// redirectStartRaw starts a redirectRawServer and returns it with a
// transport that trusts it.
func redirectStartRaw(t *testing.T, response string) (*redirectRawServer, *http.Transport) {
	t.Helper()
	cert, tr := replayTLSBase(t)
	s := &redirectRawServer{ln: replayListen(t, cert), response: response}
	s.wg.Go(s.accept)
	t.Cleanup(func() {
		_ = s.ln.Close()
		tr.CloseIdleConnections()
		s.wg.Wait()
	})
	return s, tr
}

func (s *redirectRawServer) origin() string { return "https://" + s.ln.Addr().String() }

func (s *redirectRawServer) accept() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.wg.Go(func() { s.serve(c) })
	}
}

// serve answers one request and closes the connection.
func (s *redirectRawServer) serve(c net.Conn) {
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(replayTimeout))
	req, err := http.ReadRequest(bufio.NewReader(c))
	if err != nil {
		return
	}
	_, _ = io.Copy(io.Discard, req.Body)
	_ = req.Body.Close()
	s.requests.Add(1)
	_, _ = io.WriteString(c, s.response)
}

// redirectRaw is a raw HTTP/1.1 redirect response with Location loc.
func redirectRaw(status int, loc string) string {
	return "HTTP/1.1 " + strconv.Itoa(status) + " " + http.StatusText(status) + "\r\n" +
		"Location: " + loc + "\r\nContent-Length: 0\r\nConnection: close\r\n\r\n"
}

// redirectTabLocation holds a tab: valid in a header field, but a control
// character url.Parse rejects. The raw servers put it on the wire byte for
// byte.
const redirectTabLocation = "https://127.0.0.1/\t" + redirectMarker

// TestRedirectRawLocationHoldingATab sends, in a raw HTTP/1.1 and a raw
// HTTP/2 response, a Location holding a tab. Both transports accept the
// header and url.Parse rejects it: the SDK reports unexpected_response with
// the status, after exactly one request.
func TestRedirectRawLocationHoldingATab(t *testing.T) {
	for _, status := range redirectStatuses() {
		for _, o := range opCases() {
			t.Run(fmt.Sprintf("http1/%d/%s", status, o.name), func(t *testing.T) {
				s, tr := redirectStartRaw(t, redirectRaw(status, redirectTabLocation))
				c := newClient(t, emailable.WithTestBaseURL(s.origin()), emailable.WithHTTPClient(&http.Client{Transport: tr}))
				err := o.call(t.Context(), c)
				redirectAssertRefused(t, err, o, status, redirectTabLocation)
				assert.Equal(t, int64(1), s.requests.Load(), "exactly one request")
			})
			t.Run(fmt.Sprintf("http2/%d/%s", status, o.name), func(t *testing.T) {
				reply := testutil.Step{Status: status, Header: http.Header{"Location": {redirectTabLocation}}}
				s, tr, dials := replayStartH2(t, reply, 0, nil)
				c := newClient(t, emailable.WithTestBaseURL(s.origin()), emailable.WithHTTPClient(&http.Client{Transport: tr}))
				err := o.call(t.Context(), c)
				s.requireH2(t)
				redirectAssertRefused(t, err, o, status, redirectTabLocation)
				assert.Equal(t, int64(1), s.headers.Load(), "exactly one request")
				assert.Equal(t, int64(1), dials.Load())
			})
		}
	}
}

// TestRedirectControlByteRejectedByTransport sends a raw HTTP/1.1 redirect
// whose Location holds a byte that no header field may hold. The transport
// rejects the response before any redirect logic runs, with an error that
// quotes the header line, so the SDK reports a transport failure that quotes
// nothing.
func TestRedirectControlByteRejectedByTransport(t *testing.T) {
	for _, b := range []string{"\x01", "\x7f"} {
		loc := "https://127.0.0.1/" + b + redirectMarker
		for _, o := range opCases() {
			t.Run(fmt.Sprintf("%q/%s", b, o.name), func(t *testing.T) {
				s, tr := redirectStartRaw(t, redirectRaw(http.StatusFound, loc))
				c := newClient(t, emailable.WithTestBaseURL(s.origin()), emailable.WithHTTPClient(&http.Client{Transport: tr}))
				err := o.call(t.Context(), c)
				_ = assertCallError(t, err, o.want(wantCall{
					Kind: emailable.FailureTransport, Attempts: 1, OutcomeUnknown: o.paid,
				}), redirectMarker, loc)
				assert.Equal(t, int64(1), s.requests.Load(), "exactly one request")
			})
		}
	}
}

// TestRedirectControlUnguardedClientQuotesLocation shows why the SDK strips
// Location instead of relying on CheckRedirect alone: a client that refuses
// redirects the same way, but without the guard, fails on an unparsable
// Location with an error that quotes it, from a net/http server and from raw
// responses alike.
func TestRedirectControlUnguardedClientQuotesLocation(t *testing.T) {
	refuse := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	get := func(t *testing.T, hc *http.Client, origin string) error {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, origin+pathAccount, nil)
		require.NoError(t, err)
		resp, err := hc.Do(req)
		if resp != nil {
			_ = resp.Body.Close()
		}
		return err
	}
	t.Run("net/http server", func(t *testing.T) {
		for _, proto := range testutil.Protocols() {
			srv := testutil.NewServerProto(t, proto, testutil.Serve(redirectAnswer(http.StatusFound, redirectUnparsable)))
			err := get(t, &http.Client{Transport: srv.Client().Transport, CheckRedirect: refuse}, srv.URL())
			require.Error(t, err)
			assert.Contains(t, err.Error(), redirectMarker, proto.Name())
		}
	})
	t.Run("raw tab", func(t *testing.T) {
		s, tr := redirectStartRaw(t, redirectRaw(http.StatusFound, redirectTabLocation))
		err := get(t, &http.Client{Transport: tr, CheckRedirect: refuse}, s.origin())
		require.Error(t, err)
		assert.Contains(t, err.Error(), redirectMarker)
	})
	t.Run("raw control byte", func(t *testing.T) {
		s, tr := redirectStartRaw(t, redirectRaw(http.StatusFound, "https://127.0.0.1/\x01"+redirectMarker))
		err := get(t, &http.Client{Transport: tr, CheckRedirect: refuse}, s.origin())
		require.Error(t, err)
		assert.Contains(t, err.Error(), redirectMarker)
	})
}

// redirectGuardStatuses are the statuses on each side of the guard's 3xx
// range, and 200, which succeeds. HTTP 249 is left out: WaitForVerification
// repeats it.
func redirectGuardStatuses() []int {
	return []int{
		101, 199, 200, 204, 299, 300, 301, 302, 303, 304, 305, 306, 307, 308, 309, 399,
		400, 404, 429, 500, 503, 599, 600, 999,
	}
}

// TestRedirectGuardStripsLocationInMemory checks the redirect guard on
// responses from an injected in-memory transport, which hands the SDK the
// very header map the test keeps. Every 3xx loses its Location, and nothing
// else, before the http.Client sees it: the Location here is unparsable, and
// for 301, 302, 303, 307 and 308 the client would otherwise fail with an
// error quoting it. Every other status keeps all of its headers, Location
// included, and fails with its own kind, or succeeds for 200.
func TestRedirectGuardStripsLocationInMemory(t *testing.T) {
	for _, status := range redirectGuardStatuses() {
		for _, o := range opCases() {
			t.Run(fmt.Sprintf("%d/%s", status, o.name), func(t *testing.T) {
				header := http.Header{
					"Content-Type":        {mediaJSON},
					"Location":            {redirectUnparsable},
					"Set-Cookie":          {"redirect=" + redirectMarker + "; Path=/"},
					"X-Redirect-Marker":   {redirectMarker, "second"},
					"Ratelimit-Remaining": {"7"},
				}
				want := header.Clone()
				body := "moved to " + redirectUnparsable
				if status == http.StatusOK {
					body = o.ok.Body
				}
				var seen *http.Response
				answer := testutil.TransportWith(status, header, int64(len(body)), io.NopCloser(strings.NewReader(body)))
				ct := &testutil.CountingTransport{Next: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
					var err error
					seen, err = answer(r)
					return seen, err
				})}
				caller := redirectWatch(&http.Client{Transport: ct}, &atomic.Int32{}, nil)
				c := newClient(t, emailable.WithTestBaseURL(loopbackOrigin), emailable.WithHTTPClient(caller.hc))

				err := o.call(t.Context(), c)

				assert.Equal(t, 1, ct.Attempts(), "exactly one attempt")
				require.NotNil(t, seen)
				if status >= 300 && status < 400 {
					want.Del("Location")
				}
				if status == http.StatusOK {
					require.NoError(t, err)
				} else {
					kind := statusKind(o.op, status)
					_ = assertCallError(t, err, o.want(wantCall{
						Kind: kind, Status: status, Attempts: 1, OutcomeUnknown: outcomeUnknownAfter(o.paid, kind),
					}), redirectMarker, redirectUnparsable)
				}
				assert.Equal(t, want, seen.Header, "the guard removes Location from a 3xx only, and nothing else")
				caller.assertUntouched(t)
			})
		}
	}
}

// TestRedirectGuardNilHeader checks that a 3xx without a header map, which
// only an injected transport can produce, is reported like any other.
func TestRedirectGuardNilHeader(t *testing.T) {
	for _, o := range opCases() {
		t.Run(o.name, func(t *testing.T) {
			rt := testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
				testutil.Drain(r)
				resp := testutil.Response(r, http.StatusFound, http.NoBody, 0)
				resp.Header = nil
				return resp, nil
			})
			err := o.call(t.Context(), newTransportClient(t, rt))
			redirectAssertRefused(t, err, o, http.StatusFound)
		})
	}
}

// redirectUnparsableLocations are Locations url.Parse rejects. An in-memory
// transport delivers bytes no wire format would carry.
func redirectUnparsableLocations() []struct{ name, loc string } {
	return []struct{ name, loc string }{
		{"unclosed bracket", redirectUnparsable},
		{"control byte", "https://127.0.0.1/\x01" + redirectMarker},
		{"delete byte", "https://127.0.0.1/\x7f" + redirectMarker},
		{"nul byte", "https://127.0.0.1/\x00" + redirectMarker},
		{"newline", "https://127.0.0.1/\r\n" + redirectMarker},
		{"missing scheme", "://" + redirectMarker},
		{"bad escape", "https://127.0.0.1/%zz" + redirectMarker},
		{"space in host", "https://exa mple.com/" + redirectMarker},
	}
}

// TestRedirectUnparsableLocationInMemory sends every kind of unparsable
// Location, for every redirect status, from an in-memory transport: one
// attempt, unexpected_response with the status, the redirect body closed
// exactly once, and no trace of the Location.
func TestRedirectUnparsableLocationInMemory(t *testing.T) {
	for _, status := range redirectStatuses() {
		for _, l := range redirectUnparsableLocations() {
			for _, o := range opCases() {
				t.Run(fmt.Sprintf("%d/%s/%s", status, l.name, o.name), func(t *testing.T) {
					body := testutil.NewBody("moved to " + l.loc)
					header := http.Header{"Content-Type": {mediaJSON}, "Location": {l.loc}}
					ct := &testutil.CountingTransport{Next: testutil.TransportWith(status, header, -1, body)}
					err := o.call(t.Context(), newTransportClient(t, ct))
					redirectAssertRefused(t, err, o, status, l.loc)
					assert.Equal(t, 1, ct.Attempts(), "exactly one attempt")
					assert.Equal(t, 1, body.Closes(), "the redirect body is closed exactly once")
					assert.Zero(t, body.Reads(), "a redirect body is never read without diagnostics")
				})
			}
		}
	}
}

// TestRedirectInjectedClientNotModified checks that NewClient and calls
// through the injected client leave every one of its fields as they were: a
// client with no transport, which means http.DefaultTransport, and one with
// every field set.
func TestRedirectInjectedClientNotModified(t *testing.T) {
	t.Run("no transport", func(t *testing.T) {
		hc := &http.Client{}
		caller := redirectWatch(hc, &atomic.Int32{}, nil)
		_, err := emailable.NewClient(testKey, emailable.WithHTTPClient(hc))
		require.NoError(t, err)
		assert.Nil(t, hc.Transport, "the caller's client is never modified")
		assert.Nil(t, hc.CheckRedirect)
		assert.Nil(t, hc.Jar)
		assert.Zero(t, hc.Timeout)
		caller.assertUntouched(t)
	})
	t.Run("every field set", func(t *testing.T) {
		for _, o := range opCases() {
			ct := &testutil.CountingTransport{Next: testutil.NewScript(o.ok)}
			caller := redirectPermissiveCaller(t, ct)
			c := newClient(t, emailable.WithTestBaseURL(loopbackOrigin), emailable.WithHTTPClient(caller.hc))
			require.NoError(t, o.call(t.Context(), c), o.name)
			assert.Equal(t, 1, ct.Attempts(), o.name)
			caller.assertUntouched(t, loopbackOrigin)
		}
	})
}

// redirectContextKey keys a context value, so that the test can see a
// context pass through untouched.
type redirectContextKey struct{}

// TestRedirectGuardKeepsRequestContext checks that the guard passes the
// SDK's request on as it is: the caller's context values, and the call's
// deadline, reach the caller's transport.
func TestRedirectGuardKeepsRequestContext(t *testing.T) {
	for _, o := range opCases() {
		t.Run(o.name, func(t *testing.T) {
			var got any
			var deadline bool
			s := testutil.NewScript(testutil.Step{Respond: func(r *http.Request) (*http.Response, error) {
				got = r.Context().Value(redirectContextKey{})
				_, deadline = r.Context().Deadline()
				return testutil.Reply(r, o.ok)
			}})
			c := newTransportClient(t, s)
			ctx := context.WithValue(t.Context(), redirectContextKey{}, redirectMarker)
			require.NoError(t, o.call(ctx, c))
			assert.Equal(t, redirectMarker, got)
			assert.True(t, deadline, "the SDK's timeout reaches the transport")
		})
	}
}
