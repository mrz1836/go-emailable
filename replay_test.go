package emailable_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-emailable"
	"github.com/mrz1836/go-emailable/internal/testutil"
)

// The standard library resends a request on its own when it judges that
// safe. HTTP/1 resends a request whose body is nil or http.NoBody, or that
// has a GetBody, when a reused connection fails, even after the server read
// it; HTTP/2 does the same after a refused stream or a GOAWAY that disowns
// the stream. The SDK sends every request with a body that is neither nil
// nor http.NoBody and a nil GetBody, so that each call is exactly one
// attempt. The control tests below prove that the servers here detect a
// resend, by making the standard library resend the same requests sent the
// usual way; the other tests prove the SDK's requests are never resent.

// HTTP/2 frame types, flags and error codes the raw server uses (RFC 9113).
const (
	replayFrameData     = 0x0
	replayFrameHeaders  = 0x1
	replayFrameRST      = 0x3
	replayFrameSettings = 0x4
	replayFramePing     = 0x6
	replayFrameGoAway   = 0x7
	replayFrameWindow   = 0x8
	replayFlagEnd       = 0x1 // END_STREAM on HEADERS and DATA, ACK on SETTINGS and PING
	replayFlagHeaders   = 0x4 // END_HEADERS
	replayCodeNoError   = 0x0
	replayCodeProtocol  = 0x1 // PROTOCOL_ERROR
	replayCodeRefused   = 0x7 // REFUSED_STREAM
	replayPreface       = "PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n"
	replayMaxFrameBytes = 1<<24 - 1
)

// replayGoAwayMarker is GOAWAY debug data. The standard library quotes it in
// its error text, so it must never reach anything the SDK returns.
const replayGoAwayMarker = "replay-goaway-debug-marker-2c9f" // gitleaks:allow (synthetic test marker)

// replayTimeout is the SDK timeout of the replay calls. A call that fails
// must fail at once, never retry until this expires.
const replayTimeout = 10 * time.Second

// replayBatchBody is the body of the minimal batch request.
const replayBatchBody = `{"emails":["` + testEmail + `"]}`

var (
	errReplayFrameTooLarge = errors.New("replay: frame too large")
	errReplayHPACKTooLong  = errors.New("replay: header string too long for this encoder")
)

// replayTLSBase returns the certificate of the httptest servers and a client
// transport that trusts it.
func replayTLSBase(t *testing.T) (tls.Certificate, *http.Transport) {
	t.Helper()
	ts := httptest.NewTLSServer(http.NotFoundHandler())
	defer ts.Close()
	base, ok := ts.Client().Transport.(*http.Transport)
	require.True(t, ok)
	return ts.TLS.Certificates[0], base.Clone()
}

// replayListen starts a TLS listener on loopback with the test certificate,
// offering protos through ALPN.
func replayListen(t *testing.T, cert tls.Certificate, protos ...string) net.Listener {
	t.Helper()
	lc := net.ListenConfig{}
	inner, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	return tls.NewListener(inner, &tls.Config{
		Certificates: []tls.Certificate{cert},
		NextProtos:   protos,
		MinVersion:   tls.VersionTLS12,
	})
}

// replayAssertTransport asserts o's transport failure after one attempt, with
// no HTTP status and no trace of the GOAWAY debug data.
func replayAssertTransport(t *testing.T, err error, o opCase) {
	t.Helper()
	_ = assertCallError(t, err, o.want(wantCall{
		Kind: emailable.FailureTransport, Attempts: 1, OutcomeUnknown: o.paid,
	}), replayGoAwayMarker)
}

// replayEmptyBody is a body like the SDK's GET body: empty, and neither nil
// nor http.NoBody, with no GetBody to rewind it. Like the SDK, a GET that
// carries it sets TransferEncoding to "identity", so that HTTP/1 sends no
// length, no chunking and no body byte without probing the body first.
type replayEmptyBody struct{}

// Read reports the end of the empty body.
func (replayEmptyBody) Read([]byte) (int, error) { return 0, io.EOF }

// Close does nothing.
func (replayEmptyBody) Close() error { return nil }

// replayShape builds one raw request, the standard library's way or the
// SDK's, to target.
type replayShape struct {
	name  string
	build func(t *testing.T, target string) *http.Request
}

// replayNewRequest returns a request built by the standard library.
func replayNewRequest(t *testing.T, method, target string, body io.Reader) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, target, body)
	require.NoError(t, err)
	return req
}

// replayNilGet is a GET with a nil body.
func replayNilGet() replayShape {
	return replayShape{"GET with a nil body", func(t *testing.T, target string) *http.Request {
		t.Helper()
		return replayNewRequest(t, http.MethodGet, target, nil)
	}}
}

// replayNoBodyGet is a GET with http.NoBody.
func replayNoBodyGet() replayShape {
	return replayShape{"GET with http.NoBody", func(t *testing.T, target string) *http.Request {
		t.Helper()
		return replayNewRequest(t, http.MethodGet, target, http.NoBody)
	}}
}

// replaySDKGet is a GET shaped as the SDK sends it.
func replaySDKGet() replayShape {
	return replayShape{"GET with the SDK's body", func(t *testing.T, target string) *http.Request {
		t.Helper()
		req := replayNewRequest(t, http.MethodGet, target, nil)
		req.Body, req.ContentLength, req.GetBody = replayEmptyBody{}, 0, nil
		req.TransferEncoding = []string{"identity"}
		return req
	}}
}

// replayRewindablePost is a POST with GetBody, and the header key, if any.
func replayRewindablePost(key string) replayShape {
	name := "POST with GetBody"
	if key != "" {
		name += " and " + key
	}
	return replayShape{name, func(t *testing.T, target string) *http.Request {
		t.Helper()
		req := replayNewRequest(t, http.MethodPost, target, strings.NewReader(replayBatchBody))
		require.NotNil(t, req.GetBody, "a strings.Reader body is rewindable")
		if key != "" {
			req.Header.Set(key, "replay-idempotency-0001")
		}
		return req
	}}
}

// replaySDKPost is a POST shaped as the SDK sends it: no GetBody.
func replaySDKPost() replayShape {
	return replayShape{"POST without GetBody", func(t *testing.T, target string) *http.Request {
		t.Helper()
		req := replayNewRequest(t, http.MethodPost, target, strings.NewReader(replayBatchBody))
		req.GetBody = nil
		return req
	}}
}

// replayDo sends req through hc, then reads and closes the response body.
func replayDo(hc *http.Client, req *http.Request) error {
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.Body.Close()
}

// replayAction answers the failing request once its stream has ended: it
// writes frames to w and reports whether the server then hangs up.
type replayAction func(w io.Writer, stream uint32) (hangUp bool)

// replayStream is what the raw HTTP/2 server saw of one request stream.
type replayStream struct {
	headersEnd bool // the HEADERS frame ended the stream: no DATA at all
	dataFrames int
	dataBytes  int
}

// replayH2Server is a minimal HTTP/2 server built from hand-written frames.
// It reads each request completely, as a server that may already have acted
// on it would, and answers it with reply, except request number failAt (from
// 1; 0 for none), which gets action instead. A request the client resent
// would thus be answered with success, and counted.
type replayH2Server struct {
	ln      net.Listener
	reply   testutil.Step
	failAt  int64
	action  replayAction
	headers atomic.Int64 // HEADERS frames received, on every connection
	bodies  atomic.Int64 // requests received in full
	notH2   atomic.Bool  // a client spoke something other than HTTP/2

	mu      sync.Mutex
	conns   []net.Conn
	perConn []int64        // HEADERS frames per accepted connection, in accept order
	streams []replayStream // per request, in arrival order
	wg      sync.WaitGroup
}

// replayStartH2 starts a replayH2Server and returns it with an HTTP/2 client
// transport that trusts its certificate, and the count of that transport's
// dials.
func replayStartH2(t *testing.T, reply testutil.Step, failAt int64,
	action replayAction,
) (*replayH2Server, *http.Transport, *atomic.Int64) {
	t.Helper()
	cert, tr := replayTLSBase(t)
	tr.ForceAttemptHTTP2 = true
	dials := &atomic.Int64{}
	dialer := &net.Dialer{}
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		dials.Add(1)
		return dialer.DialContext(ctx, network, addr)
	}
	s := &replayH2Server{ln: replayListen(t, cert, "h2"), reply: reply, failAt: failAt, action: action}
	s.wg.Go(s.accept)
	t.Cleanup(func() {
		_ = s.ln.Close()
		tr.CloseIdleConnections()
		s.mu.Lock()
		for _, c := range s.conns {
			_ = c.Close()
		}
		s.mu.Unlock()
		s.wg.Wait()
	})
	return s, tr, dials
}

func (s *replayH2Server) origin() string { return "https://" + s.ln.Addr().String() }

// connHeaders returns the HEADERS frames received on each connection, in
// accept order.
func (s *replayH2Server) connHeaders() []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.perConn)
}

// requestStreams returns what the server saw of each request.
func (s *replayH2Server) requestStreams() []replayStream {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.streams)
}

// requireH2 skips the test when the client did not speak HTTP/2, so that the
// replay checks never pass vacuously.
func (s *replayH2Server) requireH2(t *testing.T) {
	t.Helper()
	if s.notH2.Load() {
		t.Skip("HTTP/2 was not negotiated (is GODEBUG=http2client=0 set?)")
	}
}

func (s *replayH2Server) accept() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		s.conns = append(s.conns, c)
		s.perConn = append(s.perConn, 0)
		idx := len(s.perConn) - 1
		s.mu.Unlock()
		s.wg.Go(func() { s.serve(c, idx) })
	}
}

func (s *replayH2Server) serve(c net.Conn, idx int) {
	defer func() { _ = c.Close() }()
	r := bufio.NewReader(c)
	preface := make([]byte, len(replayPreface))
	if _, err := io.ReadFull(r, preface); err != nil || string(preface) != replayPreface {
		s.notH2.Store(true)
		return
	}
	if replayWriteFrame(c, replayFrameSettings, 0, 0, nil) != nil {
		return
	}
	numbers := map[uint32]int64{} // request number of each stream
	for {
		typ, flags, stream, payload, err := replayReadFrame(r)
		if err != nil {
			return
		}
		if s.handle(c, idx, numbers, replayFrame{typ: typ, flags: flags, stream: stream, payload: payload}) {
			// Hang up: end the write side with a TLS close_notify, so the
			// client reads every frame sent and then EOF, and drain until the
			// client closes. Closing outright with unread client frames
			// pending could reset the connection and discard the GOAWAY.
			if tc, ok := c.(*tls.Conn); ok {
				_ = tc.CloseWrite()
			}
			_, _ = io.Copy(io.Discard, r)
			return
		}
	}
}

// replayFrame is one frame read from the client.
type replayFrame struct {
	typ, flags byte
	stream     uint32
	payload    []byte
}

// handle answers one client frame: it acknowledges settings and pings,
// records requests, returns connection flow-control credit, and answers each
// request once its stream has ended. It reports whether to hang up.
func (s *replayH2Server) handle(w io.Writer, idx int, numbers map[uint32]int64, f replayFrame) bool {
	end := f.flags&replayFlagEnd != 0
	switch f.typ {
	case replayFrameSettings:
		if !end {
			_ = replayWriteFrame(w, replayFrameSettings, replayFlagEnd, 0, nil)
		}
		return false
	case replayFramePing:
		if !end {
			_ = replayWriteFrame(w, replayFramePing, replayFlagEnd, 0, f.payload)
		}
		return false
	case replayFrameHeaders:
		numbers[f.stream] = s.headers.Add(1)
		s.mu.Lock()
		s.perConn[idx]++
		s.streams = append(s.streams, replayStream{headersEnd: end})
		s.mu.Unlock()
	case replayFrameData:
		n, ok := numbers[f.stream]
		if !ok {
			return false
		}
		s.mu.Lock()
		s.streams[n-1].dataFrames++
		s.streams[n-1].dataBytes += len(f.payload)
		s.mu.Unlock()
		if size := len(f.payload); size > 0 {
			_ = replayWriteFrame(w, replayFrameWindow, 0, 0, binary.BigEndian.AppendUint32(nil, uint32(size))) //nolint:gosec // a frame payload is at most 2^24-1 bytes
		}
	default:
		return false
	}
	if !end {
		return false
	}
	s.bodies.Add(1)
	if s.action != nil && numbers[f.stream] == s.failAt {
		return s.action(w, f.stream)
	}
	return replayWriteReply(w, f.stream, s.reply) != nil
}

func replayReadFrame(r io.Reader) (typ, flags byte, stream uint32, payload []byte, err error) {
	var hdr [9]byte
	if _, err = io.ReadFull(r, hdr[:]); err != nil {
		return 0, 0, 0, nil, err
	}
	n := int(hdr[0])<<16 | int(hdr[1])<<8 | int(hdr[2])
	payload = make([]byte, n)
	if _, err = io.ReadFull(r, payload); err != nil {
		return 0, 0, 0, nil, err
	}
	return hdr[3], hdr[4], binary.BigEndian.Uint32(hdr[5:]) & 0x7fffffff, payload, nil
}

func replayWriteFrame(w io.Writer, typ, flags byte, stream uint32, payload []byte) error {
	n := len(payload)
	if n > replayMaxFrameBytes {
		return errReplayFrameTooLarge
	}
	frame := make([]byte, 9, 9+n)
	binary.BigEndian.PutUint32(frame[0:4], uint32(n)<<8) // a 24-bit length, then the type byte
	frame[3], frame[4] = typ, flags
	binary.BigEndian.PutUint32(frame[5:], stream)
	_, err := w.Write(append(frame, payload...))
	return err
}

// replayWriteReply answers stream as a Script answers with reply: one
// HEADERS frame, then the body, if any, in one DATA frame.
func replayWriteReply(w io.Writer, stream uint32, reply testutil.Step) error {
	block, err := replayHeaderBlock(reply)
	if err != nil {
		return err
	}
	if reply.Body == "" {
		return replayWriteFrame(w, replayFrameHeaders, replayFlagHeaders|replayFlagEnd, stream, block)
	}
	if err = replayWriteFrame(w, replayFrameHeaders, replayFlagHeaders, stream, block); err != nil {
		return err
	}
	return replayWriteFrame(w, replayFrameData, replayFlagEnd, stream, []byte(reply.Body))
}

// replayHeaderBlock encodes reply's status (200 when zero) and header fields
// (Content-Type application/json when nil) as an HPACK block of literal
// fields that are never indexed, so no table state is involved. After
// :status come the header fields, sorted by name, each name in lower case as
// HTTP/2 requires.
func replayHeaderBlock(reply testutil.Step) ([]byte, error) {
	status, header := reply.Status, reply.Header
	if status == 0 {
		status = http.StatusOK
	}
	if header == nil {
		header = http.Header{"Content-Type": {mediaJSON}}
	}
	fields := [][2]string{{":status", strconv.Itoa(status)}}
	for _, name := range slices.Sorted(maps.Keys(header)) {
		for _, value := range header[name] {
			fields = append(fields, [2]string{strings.ToLower(name), value})
		}
	}
	var block []byte
	for _, f := range fields {
		// A literal header field without indexing, with a literal name
		// (RFC 7541 section 6.2.2): a zero byte, then the name and value as
		// raw strings, each prefixed by its 7-bit length.
		block = append(block, 0)
		for _, s := range f {
			n := len(s)
			if n > 126 {
				return nil, errReplayHPACKTooLong
			}
			block = append(block, byte(n))
			block = append(block, s...)
		}
	}
	return block, nil
}

// replayReset resets the request's stream with code and keeps the
// connection, so that a resend could reuse it.
func replayReset(code uint32) replayAction {
	return func(w io.Writer, stream uint32) bool {
		_ = replayWriteFrame(w, replayFrameRST, 0, stream, binary.BigEndian.AppendUint32(nil, code))
		return false
	}
}

// replayGoAway sends GOAWAY with code and the marker as debug data, and hangs
// up. When covered, its last stream ID is the request's: the server claims
// it received the request, and the client fails it with an error quoting the
// debug data. Otherwise the last stream ID is the stream before (0 on a new
// connection): the server claims it never processed the request, which
// tells the client it may resend it, though the server has in fact read it
// in full.
func replayGoAway(code uint32, covered bool) replayAction {
	return func(w io.Writer, stream uint32) bool {
		last := stream
		if !covered {
			last = max(stream, 2) - 2
		}
		payload := binary.BigEndian.AppendUint32(nil, last)
		payload = binary.BigEndian.AppendUint32(payload, code)
		payload = append(payload, replayGoAwayMarker...)
		_ = replayWriteFrame(w, replayFrameGoAway, 0, 0, payload)
		return true
	}
}

// replayH2Scenario is one way an HTTP/2 server refuses or drops a request it
// has read in full. With failAt 2, an earlier call succeeds on the
// connection first, so the failing request travels on a reused connection.
//
// "Before" and "after the stream" name the GOAWAY's last stream ID; every
// action runs once the request's body has been read, the case in which a
// resend duplicates work. A GOAWAY written before the client sends the
// request at all is left out on purpose: whether the client reads it before
// or after it opens the stream is a race, so neither the frame count nor the
// error is deterministic there, and a resend in that case would carry a body
// the server never read.
type replayH2Scenario struct {
	name   string
	action replayAction
	failAt int64
}

func replayH2Scenarios() []replayH2Scenario {
	return []replayH2Scenario{
		{"refused stream", replayReset(replayCodeRefused), 1},
		{"refused stream on a reused connection", replayReset(replayCodeRefused), 2},
		{"protocol error", replayReset(replayCodeProtocol), 1},
		{"protocol error on a reused connection", replayReset(replayCodeProtocol), 2},
		{"GOAWAY before the stream", replayGoAway(replayCodeNoError, false), 1},
		{"GOAWAY with error before the stream", replayGoAway(replayCodeProtocol, false), 1},
		{"GOAWAY before the stream on a reused connection", replayGoAway(replayCodeNoError, false), 2},
		{"GOAWAY with error before the stream on a reused connection", replayGoAway(replayCodeProtocol, false), 2},
		{"GOAWAY after the stream", replayGoAway(replayCodeNoError, true), 1},
		{"GOAWAY with error after the stream", replayGoAway(replayCodeProtocol, true), 1},
		{"GOAWAY after the stream on a reused connection", replayGoAway(replayCodeProtocol, true), 2},
	}
}

// TestReplayHTTP2ControlStdlibResends shows the raw server detects a resend:
// the same transport resends a GET with a nil body, a GET with http.NoBody
// and a POST with GetBody after a refused stream or a GOAWAY that disowns
// the stream, on a new and on a reused connection, and the resent request is
// answered with success. The same requests shaped as the SDK sends them, a
// GET with a non-rewindable empty body and a POST without GetBody, are
// never resent. (Go 1.26 also resent after a PROTOCOL_ERROR reset; Go 1.27
// no longer does, so that case has no control.)
func TestReplayHTTP2ControlStdlibResends(t *testing.T) {
	scenarios := []struct {
		replayH2Scenario

		wantDials int64 // dials when the request is resent
	}{
		{replayH2Scenario{"refused stream", replayReset(replayCodeRefused), 1}, 1},
		{replayH2Scenario{"refused stream on a reused connection", replayReset(replayCodeRefused), 2}, 1},
		{replayH2Scenario{"GOAWAY before the stream", replayGoAway(replayCodeNoError, false), 1}, 2},
		{
			replayH2Scenario{
				"GOAWAY with error before the stream on a reused connection", replayGoAway(replayCodeProtocol, false), 2,
			},
			2,
		},
	}
	shapes := []struct {
		replayShape

		resent bool
	}{
		{replayNilGet(), true},
		{replayNoBodyGet(), true},
		{replayRewindablePost(""), true},
		{replaySDKGet(), false},
		{replaySDKPost(), false},
	}
	for _, sc := range scenarios {
		for _, sh := range shapes {
			t.Run(sc.name+"/"+sh.name, func(t *testing.T) {
				s, tr, dials := replayStartH2(t, testutil.Step{Body: verifyOK}, sc.failAt, sc.action)
				hc := &http.Client{Transport: tr, Timeout: replayTimeout}
				for range sc.failAt - 1 {
					require.NoError(t, replayDo(hc, sh.build(t, s.origin()+pathVerify)), "an earlier call keeps the connection")
				}
				err := replayDo(hc, sh.build(t, s.origin()+pathVerify))
				s.requireH2(t)
				if sh.resent {
					require.NoError(t, err, "the resent request is answered")
					assert.Equal(t, sc.failAt+1, s.headers.Load(), "the standard library resent the request")
					assert.Equal(t, sc.failAt+1, s.bodies.Load(), "and the server read it twice")
					assert.Equal(t, sc.wantDials, dials.Load())
					return
				}
				require.Error(t, err)
				assert.Equal(t, sc.failAt, s.headers.Load(), "the request was sent once")
				assert.Equal(t, int64(1), dials.Load())
			})
		}
	}
}

// TestReplayNeverOverHTTP2 guards the one-attempt contract against the
// standard HTTP/2 client. For every method, on a new and on a reused
// connection, the server sees the failing request once: one HEADERS frame
// and one complete request, on the one connection ever dialed. The call
// fails at once as a transport failure that never quotes the GOAWAY debug
// data.
func TestReplayNeverOverHTTP2(t *testing.T) {
	for _, sc := range replayH2Scenarios() {
		for _, o := range opCases() {
			t.Run(sc.name+"/"+o.name, func(t *testing.T) {
				s, tr, dials := replayStartH2(t, o.ok, sc.failAt, sc.action)
				c := newClient(t,
					emailable.WithTestBaseURL(s.origin()),
					emailable.WithHTTPClient(&http.Client{Transport: tr}),
					emailable.WithTimeout(replayTimeout),
				)
				for range sc.failAt - 1 {
					require.NoError(t, o.call(t.Context(), c), "an earlier call succeeds and keeps the connection")
				}

				start := time.Now()
				err := o.call(t.Context(), c)
				elapsed := time.Since(start)
				s.requireH2(t)

				replayAssertTransport(t, err, o)
				assert.Equal(t, sc.failAt, s.headers.Load(), "HEADERS frames: the failed request is sent once")
				assert.Equal(t, sc.failAt, s.bodies.Load(), "complete requests received")
				assert.Equal(t, []int64{sc.failAt}, s.connHeaders(), "HEADERS per connection: one connection only")
				assert.Equal(t, int64(1), dials.Load(), "no second connection is dialed to resend")
				assert.Less(t, elapsed, replayTimeout/2, "the call fails at once instead of resending until the timeout")
			})
		}
	}
}

// TestReplayHTTP2ControlStdlibQuotesGoAwayDebug shows that the standard
// library's own error quotes GOAWAY debug data, which is why the SDK
// discards that error.
func TestReplayHTTP2ControlStdlibQuotesGoAwayDebug(t *testing.T) {
	s, tr, _ := replayStartH2(t, testutil.Step{Body: verifyOK}, 1, replayGoAway(replayCodeProtocol, true))
	err := replayDo(&http.Client{Transport: tr, Timeout: replayTimeout}, replaySDKGet().build(t, s.origin()+pathVerify))
	s.requireH2(t)
	require.Error(t, err)
	assert.Contains(t, err.Error(), replayGoAwayMarker)
	assert.Equal(t, int64(1), s.headers.Load())
}

// TestReplayHTTP2WireFrames checks the frames each method's request puts on
// an HTTP/2 wire: a GET's HEADERS frame does not end the stream, and one
// empty DATA frame does; a POST's DATA frames carry exactly its body.
func TestReplayHTTP2WireFrames(t *testing.T) {
	for _, o := range opCases() {
		t.Run(o.name, func(t *testing.T) {
			s, tr, _ := replayStartH2(t, o.ok, 0, nil)
			c := newClient(t, emailable.WithTestBaseURL(s.origin()), emailable.WithHTTPClient(&http.Client{Transport: tr}))
			require.NoError(t, o.call(t.Context(), c))
			s.requireH2(t)
			streams := s.requestStreams()
			require.Len(t, streams, 1)
			st := streams[0]
			assert.False(t, st.headersEnd, "the HEADERS frame never ends the stream: the body is not nil")
			if o.method == http.MethodGet {
				assert.Equal(t, replayStream{dataFrames: 1}, st, "a GET ends with one empty END_STREAM DATA frame")
				return
			}
			assert.Equal(t, len(replayBatchBody), st.dataBytes, "a POST carries exactly its body")
		})
	}
}

// replayH1Server starts an HTTP/1.1 TLS server that answers every request
// with reply, except request number failAt (from 1; 0 for none): the
// recording server reads that one in full, and the handler then closes the
// connection without answering, as a server that may already have acted on
// it would.
func replayH1Server(t *testing.T, reply testutil.Step, failAt int64) *testutil.Server {
	t.Helper()
	var n atomic.Int64
	serve := testutil.Serve(reply)
	return testutil.NewServer(t, func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) != failAt {
			serve(w, r)
			return
		}
		conn, _, err := http.NewResponseController(w).Hijack()
		if err == nil {
			_ = conn.Close()
		}
	})
}

// replayArmedConn fails its next write, once armed, with nothing written:
// what a kept-alive connection that the server has meanwhile closed looks
// like.
type replayArmedConn struct {
	net.Conn

	armed atomic.Bool
}

func (c *replayArmedConn) Write(p []byte) (int, error) {
	if c.armed.Load() {
		return 0, &net.OpError{Op: "write", Net: "tcp", Err: os.NewSyscallError("write", syscall.EPIPE)}
	}
	return c.Conn.Write(p)
}

// replayH1Client is an HTTP/1.1 transport to one test server that counts its
// dials, keeps its connections so that a test can arm them, and records, for
// each connection a request got, whether it was a reused one.
type replayH1Client struct {
	tr    *http.Transport
	dials atomic.Int64

	mu     sync.Mutex
	conns  []*replayArmedConn
	reused []bool
}

func replayNewH1Client(t *testing.T, srv *testutil.Server) *replayH1Client {
	t.Helper()
	base, ok := srv.Client().Transport.(*http.Transport)
	require.True(t, ok)
	h := &replayH1Client{tr: base.Clone()}
	dialer := &net.Dialer{}
	h.tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		h.dials.Add(1)
		c, err := dialer.DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		ac := &replayArmedConn{Conn: c}
		h.mu.Lock()
		h.conns = append(h.conns, ac)
		h.mu.Unlock()
		return ac, nil
	}
	t.Cleanup(h.tr.CloseIdleConnections)
	return h
}

// RoundTrip traces which connection r gets, then passes it on.
func (h *replayH1Client) RoundTrip(r *http.Request) (*http.Response, error) {
	trace := &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) {
		h.mu.Lock()
		h.reused = append(h.reused, info.Reused)
		h.mu.Unlock()
	}}
	return h.tr.RoundTrip(r.WithContext(httptrace.WithClientTrace(r.Context(), trace)))
}

// arm makes every connection dialed so far fail its next write.
func (h *replayH1Client) arm() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, c := range h.conns {
		c.armed.Store(true)
	}
}

// reusedConns returns, for each connection a request got, whether it was
// reused.
func (h *replayH1Client) reusedConns() []bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.reused)
}

// TestReplayHTTP1ControlStdlibResends shows the harness detects a resend. A
// first request succeeds and leaves an idle keep-alive connection; the server
// then drops the second request after reading it in full, or the second
// request's write fails with nothing written. The standard library resends a
// GET with a nil body or http.NoBody in both cases, a POST with GetBody when
// nothing was written or when it carries an Idempotency-Key, on a second
// connection that answers with success. The same requests shaped as the SDK
// sends them are never resent.
func TestReplayHTTP1ControlStdlibResends(t *testing.T) {
	tests := []struct {
		shape  replayShape
		failAt int64 // the request the server drops; 0 for none
		arm    bool  // fail the next write on the kept-alive connection
		resent bool
	}{
		{shape: replayNilGet(), failAt: 2, resent: true},
		{shape: replayNoBodyGet(), failAt: 2, resent: true},
		{shape: replayRewindablePost("Idempotency-Key"), failAt: 2, resent: true},
		{shape: replayRewindablePost("X-Idempotency-Key"), failAt: 2, resent: true},
		{shape: replayRewindablePost(""), failAt: 2},
		{shape: replaySDKGet(), failAt: 2},
		{shape: replaySDKPost(), failAt: 2},
		{shape: replayNilGet(), arm: true, resent: true},
		{shape: replayNoBodyGet(), arm: true, resent: true},
		{shape: replayRewindablePost(""), arm: true, resent: true},
		{shape: replaySDKGet(), arm: true},
		{shape: replaySDKPost(), arm: true},
	}
	for _, tt := range tests {
		how := "dropped after it was read"
		if tt.arm {
			how = "nothing written"
		}
		t.Run(tt.shape.name+"/"+how, func(t *testing.T) {
			srv := replayH1Server(t, testutil.Step{Body: verifyOK}, tt.failAt)
			h := replayNewH1Client(t, srv)
			hc := &http.Client{Transport: h, Timeout: replayTimeout}
			require.NoError(t, replayDo(hc, tt.shape.build(t, srv.URL()+pathVerify)), "the first call leaves an idle connection")
			if tt.arm {
				h.arm()
			}
			err := replayDo(hc, tt.shape.build(t, srv.URL()+pathVerify))
			reused := h.reusedConns()
			require.GreaterOrEqual(t, len(reused), 2)
			assert.True(t, reused[1], "the second call starts on the kept-alive connection")
			// The first request always arrives; the second unless its write
			// failed; a resend adds one more, on a second connection.
			wantHits, wantDials := 2, int64(1)
			if tt.arm {
				wantHits = 1
			}
			if tt.resent {
				require.NoError(t, err, "the resent request is answered")
				wantHits++
				wantDials = 2
			} else {
				require.Error(t, err)
			}
			assert.Equal(t, wantHits, srv.Hits())
			assert.Equal(t, wantDials, h.dials.Load())
		})
	}
}

// TestReplayNeverOverHTTP1 closes the connection after the server has read
// the whole request, on a new connection and on a reused keep-alive one, and
// fails the write on a reused connection with nothing written: the paths on
// which the standard HTTP/1 client resends a request it judges safe. For
// every method the failing request reaches the server at most once, on the
// one connection ever dialed, and the call fails at once as a transport
// failure after one attempt.
func TestReplayNeverOverHTTP1(t *testing.T) {
	tests := []struct {
		name     string
		failAt   int64 // the request the server drops; 0 for none
		earlier  int   // calls that succeed first
		arm      bool  // fail the next write on the kept-alive connection
		wantHits int
	}{
		{name: "new connection closed after the request", failAt: 1, wantHits: 1},
		{name: "reused connection closed after the request", failAt: 2, earlier: 1, wantHits: 2},
		{name: "reused connection with nothing written", earlier: 1, arm: true, wantHits: 1},
	}
	for _, tt := range tests {
		for _, o := range opCases() {
			t.Run(tt.name+"/"+o.name, func(t *testing.T) {
				srv := replayH1Server(t, o.ok, tt.failAt)
				h := replayNewH1Client(t, srv)
				c := newClient(t, emailable.WithTestBaseURL(srv.URL()), emailable.WithHTTPClient(&http.Client{Transport: h}),
					emailable.WithTimeout(replayTimeout))
				for range tt.earlier {
					require.NoError(t, o.call(t.Context(), c), "an earlier call succeeds and leaves an idle connection")
				}
				if tt.arm {
					h.arm()
				}

				start := time.Now()
				err := o.call(t.Context(), c)
				elapsed := time.Since(start)

				replayAssertTransport(t, err, o)
				assert.Equal(t, tt.wantHits, srv.Hits(), "the failing request reaches the server at most once")
				assert.Equal(t, int64(1), h.dials.Load(), "no second connection is dialed to resend")
				reused := h.reusedConns()
				require.Len(t, reused, tt.earlier+1, "one connection per call: no resend")
				assert.Equal(t, tt.earlier > 0, reused[tt.earlier], "the failing call reuses the kept-alive connection")
				assert.Less(t, elapsed, replayTimeout/2, "the call fails at once")
			})
		}
	}
}

// replayShapeCases returns every method's case, and requests with every
// option set, whose encodings differ.
func replayShapeCases() []opCase {
	no, yes := false, true
	full := opCaseNamed("VerifyEmail")
	full.name = "VerifyEmail with every option"
	full.call = func(ctx context.Context, c *emailable.Client) error {
		return noValueWith(c.VerifyEmail(ctx, emailable.VerifyEmailRequest{
			Email: testEmail, SMTP: &no, AcceptAll: &yes, ProviderTimeout: 10 * time.Second, CaptchaResponse: testCaptcha,
		}))
	}
	batch := opCaseNamed("CreateBatch")
	batch.name = "CreateBatch with every option"
	batch.call = func(ctx context.Context, c *emailable.Client) error {
		return noValueWith(c.CreateBatch(ctx, emailable.CreateBatchRequest{
			Emails: []string{testEmail, "john.doe@example.com"}, CallbackURL: testHook,
			ResponseFields: []emailable.ResponseField{emailable.ResponseFieldEmail, emailable.ResponseFieldState},
			Retries:        &no, Simulate: emailable.SimulateGenericError,
		}))
	}
	read := opCaseNamed("GetBatch")
	read.name = "GetBatch partial and simulated"
	read.call = func(ctx context.Context, c *emailable.Client) error {
		return noValueWith(c.GetBatch(ctx, emailable.GetBatchRequest{
			BatchID: testBatchID, Partial: true, Simulate: emailable.SimulateVerifying,
		}))
	}
	return append(opCases(), full, batch, read)
}

// replaySeen is what a transport saw of a request's body.
type replaySeen struct {
	nilBody, noBody, getBody bool
	contentLength            int64
	transferEncoding         []string
	header                   http.Header
}

// replayRecorder returns a transport that records what it sees of each
// request's body before next answers it.
func replayRecorder(next http.RoundTripper) (http.RoundTripper, func() []replaySeen) {
	var mu sync.Mutex
	var seen []replaySeen
	rt := testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		seen = append(seen, replaySeen{
			nilBody: r.Body == nil, noBody: r.Body == http.NoBody, getBody: r.GetBody != nil,
			contentLength: r.ContentLength, transferEncoding: slices.Clone(r.TransferEncoding), header: r.Header.Clone(),
		})
		mu.Unlock()
		return next.RoundTrip(r)
	})
	return rt, func() []replaySeen {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(seen)
	}
}

// replayAssertUnrewindable asserts that a request, as the transport saw it,
// is one no transport can resend: its body is neither nil nor http.NoBody,
// GetBody is nil, its declared length is exact, and no header marks it as
// safe to replay.
func replayAssertUnrewindable(t *testing.T, seen replaySeen, method string, bodyLen int) {
	t.Helper()
	assert.False(t, seen.nilBody, "the body is never nil")
	assert.False(t, seen.noBody, "the body is never http.NoBody")
	assert.False(t, seen.getBody, "GetBody is nil")
	if method == http.MethodGet {
		assert.Equal(t, int64(0), seen.contentLength, "a GET declares no length")
		assert.Equal(t, []string{"identity"}, seen.transferEncoding, "a GET is sent as is, never probed or chunked")
	} else {
		assert.Equal(t, int64(bodyLen), seen.contentLength, "a POST declares its exact length")
		assert.Empty(t, seen.transferEncoding, "a POST is sent with its length")
	}
	for _, h := range bannedHeaders() {
		assert.Empty(t, seen.header.Values(h), h)
	}
}

// TestReplayRequestsCannotBeRewound checks, in memory, the request property
// the one-attempt contract rests on, for every method and every encoding.
func TestReplayRequestsCannotBeRewound(t *testing.T) {
	for _, o := range replayShapeCases() {
		t.Run(o.name, func(t *testing.T) {
			s := testutil.NewScript(o.ok)
			rt, seen := replayRecorder(s)
			c := newTransportClient(t, rt)
			require.NoError(t, o.call(t.Context(), c))
			r := testutil.OnlyRequest(t, s)
			got := seen()
			require.Len(t, got, 1)
			replayAssertUnrewindable(t, got[0], o.method, len(r.Body))
			assertNoReplay(t, r)
			if o.method == http.MethodGet {
				assert.Empty(t, r.Body, "a GET's body is empty")
			} else {
				assert.NotEmpty(t, r.Body)
			}
		})
	}
}

// TestReplayRequestsCannotBeRewoundOnTheWire checks the same property on the
// request a caller's transport receives over each protocol, and what the
// server then sees.
func TestReplayRequestsCannotBeRewoundOnTheWire(t *testing.T) {
	for _, proto := range testutil.Protocols() {
		for _, o := range opCases() {
			t.Run(proto.Name()+"/"+o.name, func(t *testing.T) {
				srv := testutil.NewServerProto(t, proto, testutil.Serve(o.ok))
				rt, seen := replayRecorder(srv.Client().Transport)
				c := newClient(t, emailable.WithTestBaseURL(srv.URL()), emailable.WithHTTPClient(&http.Client{Transport: rt}))
				require.NoError(t, o.call(t.Context(), c))
				r := testutil.OnlyRequest(t, srv)
				got := seen()
				require.Len(t, got, 1)
				replayAssertUnrewindable(t, got[0], o.method, len(r.Body))
				assert.Equal(t, string(proto), r.Proto)
				assertWireRequest(t, r, o.method, o.path)
			})
		}
	}
}

// replayWireServer is an HTTP/1.1 TLS server that records the raw bytes of
// each connection: it reads one request in full, answers it with reply and
// "Connection: close", then reads on until the client hangs up, so that any
// byte the client sent after the request is captured too.
type replayWireServer struct {
	ln    net.Listener
	reply testutil.Step
	raw   chan []byte
	wg    sync.WaitGroup
}

// replayStartWire starts a replayWireServer and returns it with a transport
// that trusts it.
func replayStartWire(t *testing.T, reply testutil.Step) (*replayWireServer, *http.Transport) {
	t.Helper()
	cert, tr := replayTLSBase(t)
	s := &replayWireServer{ln: replayListen(t, cert), reply: reply, raw: make(chan []byte, 4)}
	s.wg.Go(s.accept)
	t.Cleanup(func() {
		_ = s.ln.Close()
		tr.CloseIdleConnections()
		s.wg.Wait()
	})
	return s, tr
}

func (s *replayWireServer) origin() string { return "https://" + s.ln.Addr().String() }

// next returns the raw bytes of the next connection to end.
func (s *replayWireServer) next(t *testing.T) []byte {
	t.Helper()
	select {
	case raw := <-s.raw:
		return raw
	case <-time.After(replayTimeout):
		require.FailNow(t, "no connection ended")
		return nil
	}
}

func (s *replayWireServer) accept() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.wg.Go(func() { s.serve(c) })
	}
}

func (s *replayWireServer) serve(c net.Conn) {
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(replayTimeout))
	var captured bytes.Buffer
	br := bufio.NewReader(io.TeeReader(c, &captured))
	req, err := http.ReadRequest(br)
	if err == nil {
		_, _ = io.Copy(io.Discard, req.Body)
		_ = req.Body.Close()
		_, _ = io.WriteString(c, replayRawResponse(s.reply))
		_, _ = io.Copy(io.Discard, br) // until the client hangs up
	}
	s.raw <- bytes.Clone(captured.Bytes())
}

// replayRawResponse is reply as raw HTTP/1.1 bytes that close the connection.
func replayRawResponse(reply testutil.Step) string {
	return "HTTP/1.1 200 OK\r\nContent-Type: " + mediaJSON + "\r\nContent-Length: " + strconv.Itoa(len(reply.Body)) +
		"\r\nConnection: close\r\n\r\n" + reply.Body
}

// replayRawRequest is a raw HTTP/1.1 request split into its parts.
type replayRawRequest struct {
	line   string
	header map[string][]string // by lower-case name, in order
	body   []byte              // every byte after the header section
}

// replayParseRaw splits the raw bytes of a request.
func replayParseRaw(t *testing.T, raw []byte) replayRawRequest {
	t.Helper()
	head, body, found := bytes.Cut(raw, []byte("\r\n\r\n"))
	require.True(t, found, "a complete header section")
	lines := strings.Split(string(head), "\r\n")
	req := replayRawRequest{line: lines[0], header: map[string][]string{}, body: body}
	for _, l := range lines[1:] {
		name, value, ok := strings.Cut(l, ":")
		require.True(t, ok, "a header line has a colon")
		key := strings.ToLower(name)
		req.header[key] = append(req.header[key], strings.TrimSpace(value))
	}
	return req
}

// replayTarget returns the request target the SDK must send for each
// method's minimal request.
func replayTarget(o opCase) string {
	switch o.op {
	case emailable.OpVerifyEmail:
		return pathVerify + "?email=jane.doe%40example.com"
	case emailable.OpGetBatch:
		return pathBatch + "?id=" + testBatchID
	case emailable.OpCreateBatch, emailable.OpAccountInfo, emailable.OpBatchCallback:
	}
	return o.path
}

// TestReplayWireBytesOverHTTP1 captures the raw bytes each method sends over
// HTTP/1.1. A GET carries no Content-Length, no Transfer-Encoding and not a
// single body byte; a POST carries an exact Content-Length and exactly its
// body. No request ever carries an idempotency key, Expect, Cookie, Range or
// proxy credentials, and every header appears once.
func TestReplayWireBytesOverHTTP1(t *testing.T) {
	for _, o := range opCases() {
		t.Run(o.name, func(t *testing.T) {
			s, tr := replayStartWire(t, o.ok)
			c := newClient(t, emailable.WithTestBaseURL(s.origin()), emailable.WithHTTPClient(&http.Client{Transport: tr}))
			require.NoError(t, o.call(t.Context(), c))
			req := replayParseRaw(t, s.next(t))

			assert.Equal(t, o.method+" "+replayTarget(o)+" HTTP/1.1", req.line)
			for name, values := range req.header {
				assert.Len(t, values, 1, "header %s appears once", name)
			}
			assert.Equal(t, []string{strings.TrimPrefix(s.origin(), "https://")}, req.header["host"])
			assert.Equal(t, []string{"Bearer " + testKey}, req.header["authorization"])
			assert.Equal(t, []string{"go-emailable"}, req.header["user-agent"])
			assert.Equal(t, []string{mediaJSON}, req.header["accept"])
			for _, h := range append(bannedHeaders(), "Transfer-Encoding") {
				assert.NotContains(t, req.header, strings.ToLower(h), "header %s is never sent", h)
			}
			if o.method == http.MethodGet {
				assert.NotContains(t, req.header, "content-length", "a GET declares no length")
				assert.NotContains(t, req.header, "content-type")
				assert.Empty(t, req.body, "a GET puts no body byte on the wire")
				return
			}
			assert.Equal(t, []string{strconv.Itoa(len(replayBatchBody))}, req.header["content-length"])
			assert.Equal(t, []string{mediaJSON}, req.header["content-type"])
			assertBody(t, replayBatchBody, req.body)
		})
	}
}

// TestReplayWireControlCapturesBody shows the raw capture sees body bytes:
// the standard library sends a GET with a one-byte body with its length and
// that byte.
func TestReplayWireControlCapturesBody(t *testing.T) {
	s, tr := replayStartWire(t, testutil.Step{Body: verifyOK})
	hc := &http.Client{Transport: tr, Timeout: replayTimeout}
	require.NoError(t, replayDo(hc, replayNewRequest(t, http.MethodGet, s.origin()+pathVerify, strings.NewReader("x"))))
	req := replayParseRaw(t, s.next(t))
	assert.Equal(t, []string{"1"}, req.header["content-length"])
	assert.Equal(t, "x", string(req.body))
}
