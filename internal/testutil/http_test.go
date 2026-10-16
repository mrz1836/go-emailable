package testutil_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-emailable/internal/testutil"
)

// newRequest returns a POST to a loopback URL with body.
func newRequest(t *testing.T, body string) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://127.0.0.1/p?q=1", strings.NewReader(body))
	require.NoError(t, err)
	return req
}

// readAll reads and closes resp's body.
func readAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	return string(data)
}

func TestTransportAnswersInMemory(t *testing.T) {
	bodies := 0
	ct := &testutil.CountingTransport{Next: testutil.Transport(http.StatusTeapot, -1, func() io.ReadCloser {
		bodies++
		return io.NopCloser(strings.NewReader("tea"))
	})}
	for range 2 {
		req := newRequest(t, "payload")
		resp, err := ct.RoundTrip(req)
		require.NoError(t, err)
		assert.Equal(t, http.StatusTeapot, resp.StatusCode)
		assert.Equal(t, http.StatusText(http.StatusTeapot), resp.Status)
		assert.Equal(t, int64(-1), resp.ContentLength)
		assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))
		assert.Same(t, req, resp.Request)
		assert.Equal(t, "tea", readAll(t, resp))
		n, err := req.Body.Read(make([]byte, 1))
		assert.Zero(t, n, "the request body was drained")
		require.ErrorIs(t, err, io.EOF)
	}
	assert.Equal(t, 2, ct.Attempts())
	assert.Equal(t, 2, bodies, "each response gets a fresh body")
}

func TestStaticTransport(t *testing.T) {
	resp, err := testutil.StaticTransport(http.StatusOK, "static").RoundTrip(newRequest(t, ""))
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, int64(len("static")), resp.ContentLength)
	assert.Equal(t, "static", readAll(t, resp))
}

func TestDrainAndRecord(t *testing.T) {
	req := newRequest(t, "payload")
	req.Header.Set("X-Test", "v")
	rec := testutil.Record(req)
	assert.Equal(t, testutil.Request{
		Method:        http.MethodPost,
		Proto:         "HTTP/1.1",
		URL:           "https://127.0.0.1/p?q=1",
		Path:          "/p",
		Header:        http.Header{"X-Test": {"v"}},
		Body:          []byte("payload"),
		GetBody:       true,
		ContentLength: int64(len("payload")),
	}, rec)
	req.Header.Set("X-Test", "changed")
	assert.Equal(t, "v", rec.Header.Get("X-Test"), "the header is a copy")
	assert.Zero(t, testutil.Drain(req), "Record consumed the body")

	assert.Equal(t, int64(len("payload")), testutil.Drain(newRequest(t, "payload")))

	bare := &http.Request{Method: http.MethodGet, URL: &url.URL{Path: "/bare"}, Header: http.Header{}}
	assert.Zero(t, testutil.Drain(bare))
	assert.Equal(t, testutil.Request{Method: http.MethodGet, URL: "/bare", Path: "/bare", Header: http.Header{}},
		testutil.Record(bare))

	empty := &http.Request{Method: http.MethodPost, URL: &url.URL{Path: "/e"}, Header: http.Header{}, Body: http.NoBody}
	assert.True(t, testutil.Record(empty).NoBody)
}

func TestBlockUntilDone(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://127.0.0.1/", strings.NewReader("payload"))
	require.NoError(t, err)
	resp, err := testutil.BlockUntilDone(req) //nolint:bodyclose // it never returns a response
	assert.Nil(t, resp)
	require.ErrorIs(t, err, context.Canceled)
}

func TestProtocols(t *testing.T) {
	assert.Equal(t, []testutil.Protocol{testutil.HTTP1, testutil.HTTP2}, testutil.Protocols())
	assert.Equal(t, "http1", testutil.HTTP1.Name())
	assert.Equal(t, "http2", testutil.HTTP2.Name())
}

func TestServerSpeaksEachProtocol(t *testing.T) {
	for _, proto := range testutil.Protocols() {
		t.Run(proto.Name(), func(t *testing.T) {
			srv := testutil.NewServerProto(t, proto, func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				_, _ = w.Write(body)
			})
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL()+"/echo", strings.NewReader("ping"))
			require.NoError(t, err)
			resp, err := srv.Client().Do(req)
			require.NoError(t, err)
			assert.Equal(t, "ping", readAll(t, resp), "the handler can still read the body")
			assert.Equal(t, string(proto), testutil.OnlyRequest(t, srv).Proto)
		})
	}
}

func TestServerRecordsRequests(t *testing.T) {
	srv := testutil.NewServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "pong")
	})
	assert.True(t, strings.HasPrefix(srv.URL(), "https://127.0.0.1:"), srv.URL())
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL()+"/p?q=1", strings.NewReader("ping"))
	require.NoError(t, err)
	req.Header.Set("X-Test", "v")
	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	assert.Equal(t, "pong", readAll(t, resp))

	assert.Equal(t, 1, srv.Hits())
	reqs := srv.Requests()
	require.Len(t, reqs, 1)
	assert.Equal(t, http.MethodPost, reqs[0].Method)
	assert.Equal(t, "/p?q=1", reqs[0].URL)
	assert.Equal(t, "ping", string(reqs[0].Body))
	assert.Equal(t, "v", reqs[0].Header.Get("X-Test"))

	reqs[0] = testutil.Request{}
	assert.Equal(t, "ping", string(testutil.OnlyRequest(t, srv).Body), "Requests returns a copy")
}

func TestBodies(t *testing.T) {
	b := testutil.NewBody("hello")
	data, err := io.ReadAll(b)
	require.NoError(t, err)
	assert.Equal(t, "hello", string(data))
	assert.Equal(t, int64(5), b.BytesRead())
	assert.Zero(t, b.Closes())
	require.NoError(t, b.Close())
	require.NoError(t, b.Close())
	assert.Equal(t, 2, b.Closes(), "every close is counted")

	e := &testutil.EndlessBody{}
	p := make([]byte, 8)
	for range 3 {
		n, err := e.Read(p)
		require.NoError(t, err)
		assert.Equal(t, len(p), n)
		assert.Equal(t, "xxxxxxxx", string(p))
	}
	assert.Equal(t, int64(24), e.BytesRead())
	assert.Equal(t, 3, e.Reads())
	require.NoError(t, e.Close())
	assert.Equal(t, 1, e.Closes())
}

func TestOnlyRequest(t *testing.T) {
	s := testutil.NewScript(testutil.Step{Body: "{}"}, testutil.Step{Body: "{}"})
	_, err := s.RoundTrip(newRequest(t, "first")) //nolint:bodyclose // the script's body needs no close
	require.NoError(t, err)
	assert.Equal(t, "first", string(testutil.OnlyRequest(t, s).Body))

	_, err = s.RoundTrip(newRequest(t, "second")) //nolint:bodyclose // the script's body needs no close
	require.NoError(t, err)
	rec := &recorder{}
	assert.Equal(t, "first", string(testutil.OnlyRequest(rec, s).Body))
	assert.True(t, rec.failed, "two requests are not one")

	srv := testutil.NewServer(t, testutil.Serve(testutil.Step{}))
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL()+"/only", strings.NewReader("x"))
	require.NoError(t, err)
	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	readAll(t, resp)
	assert.Equal(t, "/only", testutil.OnlyRequest(t, srv).Path, "a Server is a Recorder too")
}

func TestTransportWith(t *testing.T) {
	body := testutil.NewBody("{}")
	header := http.Header{"Content-Type": {"text/plain"}}
	rt := testutil.TransportWith(http.StatusAccepted, header, 3, body)
	req := newRequest(t, "payload")
	resp, err := rt.RoundTrip(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusAccepted, resp.StatusCode)
	assert.Equal(t, int64(3), resp.ContentLength)
	assert.Equal(t, header, resp.Header)
	resp.Header.Set("Content-Type", "changed")
	assert.Equal(t, "text/plain", header.Get("Content-Type"), "each answer gets a copy of the header")
	assert.Same(t, body, resp.Body)
	assert.Equal(t, "{}", readAll(t, resp))
	assert.Zero(t, testutil.Drain(req), "the request body was drained")

	resp, err = testutil.TransportWith(http.StatusOK, nil, -1, testutil.NewBody("{}")).RoundTrip(newRequest(t, ""))
	require.NoError(t, err)
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"), "a nil header keeps the default")
	assert.Equal(t, "{}", readAll(t, resp))
}

func TestServe(t *testing.T) {
	tests := []struct {
		name   string
		step   testutil.Step
		status int
		ct     string
	}{
		{"defaults", testutil.Step{Body: `{"a":1}`}, http.StatusOK, "application/json"},
		{"status and header", testutil.Step{
			Status: http.StatusTeapot, Header: http.Header{"Content-Type": {"text/plain"}, "X-Extra": {"1", "2"}}, Body: "x y",
		}, http.StatusTeapot, "text/plain"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := testutil.NewServer(t, testutil.Serve(tt.step))
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL(), http.NoBody)
			require.NoError(t, err)
			resp, err := srv.Client().Do(req)
			require.NoError(t, err)
			assert.Equal(t, tt.status, resp.StatusCode)
			assert.Equal(t, tt.ct, resp.Header.Get("Content-Type"))
			assert.Equal(t, tt.step.Header.Values("X-Extra"), resp.Header.Values("X-Extra"))
			assert.Equal(t, tt.step.Body, readAll(t, resp))
		})
	}
}

func TestBodyScripts(t *testing.T) {
	read := func(b *testutil.Body, size int) (string, error) {
		p := make([]byte, size)
		n, err := b.Read(p)
		return string(p[:n]), err
	}
	t.Run("step and empties", func(t *testing.T) {
		b := testutil.NewBody("abcde")
		b.Step, b.Empties = 2, 1
		got := make([]string, 0, 6)
		for range 6 {
			s, err := read(b, 8)
			require.NoError(t, err)
			got = append(got, s)
		}
		assert.Equal(t, []string{"", "ab", "", "cd", "", "e"}, got)
		_, err := read(b, 8)
		require.ErrorIs(t, err, io.EOF)
		assert.Equal(t, 7, b.Reads())
		assert.Equal(t, int64(5), b.BytesRead())
	})
	t.Run("end", func(t *testing.T) {
		b := testutil.NewBody("ab")
		b.End = errBoom
		s, err := read(b, 8)
		require.NoError(t, err)
		assert.Equal(t, "ab", s)
		_, err = read(b, 8)
		require.ErrorIs(t, err, errBoom)
	})
	t.Run("stall until close", func(t *testing.T) {
		b := testutil.NewBody("")
		b.Stall = true
		for range 3 {
			s, err := read(b, 8)
			require.NoError(t, err)
			assert.Empty(t, s)
		}
		require.NoError(t, b.Close())
		_, err := read(b, 8)
		require.ErrorIs(t, err, net.ErrClosed, "a read after Close fails")
		assert.Equal(t, 4, b.Reads())
	})
	t.Run("ignore close", func(t *testing.T) {
		b := testutil.NewBody("abc")
		b.Step, b.IgnoreClose = 2, true
		require.NoError(t, b.Close())
		got, err := io.ReadAll(b)
		require.NoError(t, err)
		assert.Equal(t, "abc", string(got), "reads go on after Close")
		assert.Equal(t, 1, b.Closes(), "the close is still counted")
	})
	t.Run("gap", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			b := testutil.NewBody("abcd")
			b.Step, b.Gap = 2, time.Second
			start := time.Now()
			_, err := io.ReadAll(b)
			require.NoError(t, err)
			assert.Equal(t, 2*time.Second, time.Since(start), "one gap before each read of content")
		})
	})
	t.Run("close interrupts a gap", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			b := testutil.NewBody("ab")
			b.Gap = time.Hour
			time.AfterFunc(time.Second, func() { _ = b.Close() })
			start := time.Now()
			_, err := read(b, 8)
			require.ErrorIs(t, err, net.ErrClosed)
			assert.Equal(t, time.Second, time.Since(start))
		})
	})
	t.Run("blocking body", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			b := testutil.NewBlockingBody("ab")
			assert.True(t, b.Block)
			s, err := read(b, 8)
			require.NoError(t, err)
			assert.Equal(t, "ab", s)
			time.AfterFunc(time.Minute, func() { _ = b.Close() })
			start := time.Now()
			_, err = read(b, 8)
			require.ErrorIs(t, err, net.ErrClosed, "after its content it blocks until closed")
			assert.Equal(t, time.Minute, time.Since(start))
		})
	})
	t.Run("block until close", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			b := testutil.NewBody("ab")
			b.Block = true
			s, err := read(b, 8)
			require.NoError(t, err)
			assert.Equal(t, "ab", s)
			time.AfterFunc(time.Minute, func() { _ = b.Close() })
			start := time.Now()
			_, err = read(b, 8)
			require.ErrorIs(t, err, net.ErrClosed)
			assert.Equal(t, time.Minute, time.Since(start))
			assert.Equal(t, 1, b.Closes())
		})
	})
}
