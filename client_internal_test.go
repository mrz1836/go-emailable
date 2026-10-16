package emailable

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-emailable/internal/testutil"
)

// errCinGuard is the error a failing transport returns under the guard.
var errCinGuard = errors.New("cin: transport failed")

// cinIdleTransport counts CloseIdleConnections calls.
type cinIdleTransport struct {
	http.RoundTripper

	closed int
}

func (t *cinIdleTransport) CloseIdleConnections() { t.closed++ }

// cinGuard returns the redirect guard that wraps the transport of c, failing
// tb when the transport is not wrapped.
func cinGuard(tb testing.TB, c *http.Client) redirectGuard {
	tb.Helper()
	g, ok := c.Transport.(redirectGuard)
	require.True(tb, ok, "the transport is wrapped in the redirect guard, got %T", c.Transport)
	return g
}

func TestClientInternalRefuseRedirect(t *testing.T) {
	require.ErrorIs(t, refuseRedirect(nil, nil), http.ErrUseLastResponse)
}

func TestClientInternalGuardForwardsCloseIdleConnections(t *testing.T) {
	idle := &cinIdleTransport{}
	o := options{httpClient: &http.Client{Transport: idle}, httpClientSet: true}
	c := o.client()
	c.CloseIdleConnections()
	assert.Equal(t, 1, idle.closed, "(*http.Client).CloseIdleConnections reaches the wrapped transport")
	cinGuard(t, c).CloseIdleConnections()
	assert.Equal(t, 2, idle.closed)

	assert.NotPanics(t, func() {
		redirectGuard{next: testutil.RoundTripFunc(nil)}.CloseIdleConnections()
	}, "a transport without the method is tolerated")
	assert.NotPanics(t, (&options{}).client().CloseIdleConnections, "the SDK's own transport has the method")
}

func TestClientInternalHTTPClientCopy(t *testing.T) {
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	permissive := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return nil },
		Jar:           jar,
		Transport:     &cinIdleTransport{},
		Timeout:       7 * time.Second,
	}
	tests := []struct {
		name    string
		o       options
		timeout time.Duration
	}{
		{name: "default", o: options{}},
		{name: "injected", o: options{httpClient: permissive, httpClientSet: true}, timeout: 7 * time.Second},
		{name: "injected without transport", o: options{httpClient: &http.Client{}, httpClientSet: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := tt.o.client()
			require.NotNil(t, c.CheckRedirect)
			require.ErrorIs(t, c.CheckRedirect(nil, nil), http.ErrUseLastResponse)
			assert.Nil(t, c.Jar, "calls keep no cookies")
			assert.Equal(t, tt.timeout, c.Timeout, "an injected Timeout is kept")
			cinGuard(t, c)
			if tt.o.httpClientSet {
				assert.NotSame(t, tt.o.httpClient, c, "the SDK uses its own copy")
			}
		})
	}
	assert.Same(t, jar, permissive.Jar, "the caller's client is never modified")
	assert.NotNil(t, permissive.CheckRedirect)
	assert.IsType(t, &cinIdleTransport{}, permissive.Transport)
	assert.Equal(t, 7*time.Second, permissive.Timeout)
}

func TestClientInternalTransportWrapped(t *testing.T) {
	inner := &cinIdleTransport{}
	c := (&options{httpClient: &http.Client{Transport: inner}, httpClientSet: true}).client()
	assert.Same(t, inner, cinGuard(t, c).next)

	c = (&options{httpClient: &http.Client{}, httpClientSet: true}).client()
	assert.Equal(t, http.DefaultTransport, cinGuard(t, c).next, "a nil transport means http.DefaultTransport")
}

func TestClientInternalGuardStripsLocationOnlyOn3xx(t *testing.T) {
	const location = "https://elsewhere.example.com/?key=" + internalKey
	for _, status := range []int{100, 101, 199, 200, 204, 299, 300, 301, 302, 303, 304, 305, 306, 307, 308, 399, 400, 404, 500, 999} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			header := http.Header{"Location": {location}, "X-Other": {"kept"}}
			g := redirectGuard{next: testutil.TransportWith(status, header, 0, io.NopCloser(strings.NewReader("")))}
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, internalOrigin+"/v1/account", nil)
			require.NoError(t, err)
			resp, err := g.RoundTrip(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()
			if status >= 300 && status < 400 {
				assert.Empty(t, resp.Header.Values("Location"), "a 3xx loses its Location")
			} else {
				assert.Equal(t, location, resp.Header.Get("Location"), "any other status keeps it")
			}
			assert.Equal(t, "kept", resp.Header.Get("X-Other"))
		})
	}
}

func TestClientInternalGuardPassesErrorsThrough(t *testing.T) {
	g := redirectGuard{next: testutil.RoundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errCinGuard
	})}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, internalOrigin+"/v1/account", nil)
	require.NoError(t, err)
	resp, err := g.RoundTrip(req) //nolint:bodyclose // the response is nil
	assert.Nil(t, resp)
	require.ErrorIs(t, err, errCinGuard)
}

// TestClientInternalNewTransport pins the transport the SDK builds when no
// client is injected: its own pool, never the shared http.DefaultTransport,
// the standard proxy behavior, a 64 KiB response-header bound, and HTTP/2.
func TestClientInternalNewTransport(t *testing.T) {
	c := (&options{}).client()
	assert.Zero(t, c.Timeout, "the per-call context governs")
	tr, ok := cinGuard(t, c).next.(*http.Transport)
	require.True(t, ok)
	assert.NotSame(t, http.DefaultTransport, tr)
	require.NotNil(t, tr.Proxy)
	assert.Equal(t, reflect.ValueOf(http.ProxyFromEnvironment).Pointer(), reflect.ValueOf(tr.Proxy).Pointer())
	assert.Equal(t, int64(64<<10), tr.MaxResponseHeaderBytes)
	assert.True(t, tr.ForceAttemptHTTP2)
	assert.Equal(t, 100, tr.MaxIdleConns)
	assert.Equal(t, 90*time.Second, tr.IdleConnTimeout)
	assert.Equal(t, 10*time.Second, tr.TLSHandshakeTimeout)
	assert.Equal(t, time.Second, tr.ExpectContinueTimeout)
	assert.NotNil(t, tr.DialContext)
	assert.Nil(t, tr.TLSClientConfig, "the standard library's TLS defaults apply")
	assert.False(t, tr.DisableCompression, "the transport decodes gzip itself")
	assert.False(t, tr.DisableKeepAlives)

	other, ok := cinGuard(t, (&options{}).client()).next.(*http.Transport)
	require.True(t, ok)
	assert.NotSame(t, tr, other, "each client has its own pool")
}

func TestClientInternalResponseLimit(t *testing.T) {
	small := []Operation{OpVerifyEmail, OpCreateBatch, OpAccountInfo}
	tests := []struct {
		configured int64
		small      int64
	}{
		{configured: 1 << 10, small: 1 << 10},
		{configured: 1<<20 - 1, small: 1<<20 - 1},
		{configured: 1 << 20, small: 1 << 20},
		{configured: 1<<20 + 1, small: 1 << 20},
		{configured: DefaultMaxResponseBytes, small: 1 << 20},
		{configured: 64 << 20, small: 1 << 20},
	}
	for _, tt := range tests {
		c := &Client{maxResponseBytes: tt.configured}
		assert.Equal(t, tt.configured, c.responseLimit(OpGetBatch), "GetBatch gets the configured limit")
		for _, op := range small {
			assert.Equal(t, tt.small, c.responseLimit(op), "%s with %d", op, tt.configured)
		}
	}
	assert.Equal(t, int64(1<<20), newInternalClient(t).responseLimit(OpVerifyEmail))
	assert.Equal(t, int64(DefaultMaxResponseBytes), newInternalClient(t).responseLimit(OpGetBatch))
}

func TestClientInternalReady(t *testing.T) {
	full := newInternalClient(t)
	require.NoError(t, full.ready())
	tests := []struct {
		name string
		c    *Client
	}{
		{name: "nil", c: nil},
		{name: "zero", c: &Client{}},
		{name: "no sender", c: &Client{scrub: full.scrub, timeout: full.timeout}},
		{name: "no scrubber", c: &Client{send: full.send, timeout: full.timeout}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.c.ready()
			requireValidation(t, err, ErrInvalidConfig, "Client", reasonNotInitialized)
			requireValidation(t, tt.c.prepare(t.Context()), ErrInvalidConfig, "Client", reasonNotInitialized)
		})
	}
}

func TestClientInternalPrepare(t *testing.T) {
	var nilCtx context.Context
	full := newInternalClient(t)
	require.NoError(t, full.prepare(t.Context()))
	requireValidation(t, full.prepare(nilCtx), ErrInvalidRequest, "ctx", reasonNil)
	var none *Client
	requireValidation(t, none.prepare(nilCtx), ErrInvalidRequest, "ctx", reasonNil)
}

// TestClientInternalKeyHeldOnlyInClosures proves that the API key and the
// base URL are reachable only through function values: no field of a Client
// holds them as data.
func TestClientInternalKeyHeldOnlyInClosures(t *testing.T) {
	c := newInternalClient(t, WithTestBaseURL("https://127.0.0.1:51234"), WithProviderDiagnostics())
	secrets := []string{internalKey, "51234", "127.0.0.1", productionHost}
	testutil.AssertRedacted(t, strings.Join(testutil.ReflectStrings(c), "\n"), secrets, "reflection walk")
	v := reflect.ValueOf(c).Elem()
	for i := range v.NumField() {
		f := v.Type().Field(i)
		assert.Contains(t, []reflect.Kind{reflect.Func, reflect.Bool, reflect.Int64, reflect.Int}, f.Type.Kind(),
			"field %s: only functions and scalars may hold configuration", f.Name)
	}
}

func TestClientInternalScrub(t *testing.T) {
	const key = "test_cin+key/9=" // gitleaks:allow (synthetic test marker)
	c := newInternalClient(t)
	odd, err := NewClient(key)
	require.NoError(t, err)
	tests := []struct {
		name    string
		c       *Client
		s       string
		secrets []string
		want    string
	}{
		{name: "key", c: c, s: "a " + internalKey + " b " + internalKey, want: "a [redacted] b [redacted]"},
		{name: "nothing to scrub", c: c, s: "plain text", want: "plain text"},
		{name: "secrets", c: c, s: "x cap y hook", secrets: []string{"cap", "hook"}, want: "x [redacted] y [redacted]"},
		{name: "empty secret skipped", c: c, s: "abc", secrets: []string{""}, want: "abc"},
		{name: "query-escaped secret", c: c, s: "u a+b%2Fc", secrets: []string{"a b/c"}, want: "u [redacted]"},
		{name: "query-escaped key", c: odd, s: "k " + url.QueryEscape(key) + " " + key, want: "k [redacted] [redacted]"},
		{
			name: "a short secret never matches inside a redaction", c: c, s: "k " + internalKey + " a",
			secrets: []string{"a"}, want: "k [redacted] [redacted]",
		},
		{
			name: "a short secret never matches inside a longer one", c: c, s: "abc ab b",
			secrets: []string{"b", "abc"}, want: "[redacted] a[redacted] [redacted]",
		},
		{name: "a secret inside the key", c: c, s: internalKey, secrets: []string{"internal"}, want: "[redacted]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.c.scrub(tt.s, tt.secrets))
		})
	}
}

func TestClientInternalCheckKey(t *testing.T) {
	tests := []struct {
		key    string
		reason string
	}{
		{key: "", reason: reasonRequired},
		{key: "k"},
		{key: strings.Repeat("k", maxKeyBytes)},
		{key: strings.Repeat("k", maxKeyBytes+1), reason: reasonInvalidFormat},
		{key: "!~\"\\"},
		{key: "k k", reason: reasonInvalidFormat},
		{key: "k\x7f", reason: reasonInvalidFormat},
		{key: "k\x20", reason: reasonInvalidFormat},
		{key: "ké", reason: reasonInvalidFormat},
	}
	for _, tt := range tests {
		err := checkKey(tt.key)
		if tt.reason == "" {
			require.NoError(t, err, "%q", tt.key)
			continue
		}
		requireValidation(t, err, ErrInvalidConfig, "apiKey", tt.reason)
	}
}

func TestClientInternalLoopbackHostPort(t *testing.T) {
	tests := []struct {
		hostport string
		want     bool
	}{
		{"127.0.0.1", true},
		{"127.0.0.1:1", true},
		{"127.0.0.1:65535", true},
		{"127.1.2.3:8443", true},
		{"127.255.255.255", true},
		{"[::1]", true},
		{"[::1]:443", true},
		{"127.0.0.1:", false},
		{"127.0.0.1:0", false},
		{"127.0.0.1:65536", false},
		{"127.0.0.1:080", false},
		{"127.0.0.1:-1", false},
		{"127.0.0.1:+1", false},
		{"127.0.0.1:1:1", false},
		{"127.000.0.1", false},
		{"127.1", false},
		{"128.0.0.1", false},
		{"[::1]443", false},
		{"[::1]:", false},
		{"[::1]:0", false},
		{"[::2]", false},
		{"[0:0:0:0:0:0:0:1]", false},
		{"[::ffff:127.0.0.1]", false},
		{"[::1", false},
		{"::1", false},
		{"[127.0.0.1]", false},
		{"localhost", false},
		{"", false},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, loopbackHostPort(tt.hostport), "%q", tt.hostport)
	}
}

func TestClientInternalTestHost(t *testing.T) {
	tests := []struct {
		raw  string
		host string // "" when refused
	}{
		{raw: "https://127.0.0.1", host: "127.0.0.1"},
		{raw: "https://127.0.0.1/", host: "127.0.0.1"},
		{raw: "https://127.1.2.3:8443", host: "127.1.2.3:8443"},
		{raw: "https://[::1]", host: "[::1]"},
		{raw: "https://[::1]:443/", host: "[::1]:443"},
		{raw: ""},
		{raw: "http://127.0.0.1"},
		{raw: "https:127.0.0.1"},
		{raw: "https://u@127.0.0.1"},
		{raw: "https://127.0.0.1?"},
		{raw: "https://127.0.0.1/?a=b"},
		{raw: "https://127.0.0.1#"},
		{raw: "https://127.0.0.1/v1"},
		{raw: "https://127.0.0.1//"},
		{raw: "https://127.0.0.1/%2F"},
		{raw: "https://127.0.0.1\\x"},
		{raw: "https://localhost"},
		{raw: "https://[::2]"},
	}
	for _, tt := range tests {
		host, ok := testHost(tt.raw)
		assert.Equal(t, tt.host, host, "%q", tt.raw)
		assert.Equal(t, tt.host != "", ok, "%q", tt.raw)
	}
}

// cinValidOptions returns the options NewClient starts from.
func cinValidOptions() options {
	return options{timeout: DefaultTimeout, maxResponseBytes: DefaultMaxResponseBytes, maxBatchEmails: DefaultMaxBatchEmails}
}

func TestClientInternalOptionsValidate(t *testing.T) {
	o := cinValidOptions()
	host, err := o.validate()
	require.NoError(t, err)
	assert.Equal(t, productionHost, host, "production is the only host without a test URL")

	o.testBaseURL, o.testBaseURLSet = "https://[::1]:8443/", true
	host, err = o.validate()
	require.NoError(t, err)
	assert.Equal(t, "[::1]:8443", host)

	o = cinValidOptions()
	o.testBaseURLSet = true
	_, err = o.validate()
	requireValidation(t, err, ErrInvalidConfig, "TestBaseURL", reasonInvalidURL)

	o = cinValidOptions()
	o.httpClientSet = true
	_, err = o.validate()
	requireValidation(t, err, ErrInvalidConfig, "HTTPClient", reasonNil)

	tests := []struct {
		name  string
		edit  func(*options)
		field string
	}{
		{name: "timeout 0", edit: func(o *options) { o.timeout = 0 }, field: "Timeout"},
		{name: "timeout over 10m", edit: func(o *options) { o.timeout = maxTimeout + 1 }, field: "Timeout"},
		{name: "response bytes under 1 KiB", edit: func(o *options) { o.maxResponseBytes = minResponseBytesLimit - 1 }, field: "MaxResponseBytes"},
		{name: "response bytes over 64 MiB", edit: func(o *options) { o.maxResponseBytes = maxResponseBytesLimit + 1 }, field: "MaxResponseBytes"},
		{name: "batch emails 0", edit: func(o *options) { o.maxBatchEmails = 0 }, field: "MaxBatchEmails"},
		{name: "batch emails over a million", edit: func(o *options) { o.maxBatchEmails = maxBatchEmailsLimit + 1 }, field: "MaxBatchEmails"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			edited := cinValidOptions()
			tt.edit(&edited)
			_, verr := edited.validate()
			requireValidation(t, verr, ErrInvalidConfig, tt.field, reasonOutOfRange)
		})
	}
}
