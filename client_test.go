package emailable_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-emailable"
	"github.com/mrz1836/go-emailable/internal/testutil"
)

// cliMarker prefixes rejected values, so a test can prove that no error ever
// quotes them. It is synthetic.
const cliMarker = "CLIMARKER" // gitleaks:allow (synthetic test marker)

// cliDefaultString is how a Client built without options describes itself.
const cliDefaultString = "emailable.Client{timeout: 30s, maxResponseBytes: 8388608, maxBatchEmails: 50000, " +
	"testBaseURL: false, providerDiagnostics: false}"

// cliConfigError asserts that NewClient failed with a configuration error
// for field and reason, returned no Client, and quoted none of the secrets.
func cliConfigError(tb testing.TB, c *emailable.Client, err error, field, reason string, secrets ...string) {
	tb.Helper()
	assert.Nil(tb, c)
	assertValidation(tb, err, emailable.ErrInvalidConfig, field, reason)
	secrets = append(secrets, cliMarker, testKey)
	assertErrorClean(tb, err, secrets...)
}

func TestClientKeyRejected(t *testing.T) {
	tests := []struct {
		name   string
		key    string
		reason string
	}{
		{name: "empty", key: "", reason: reasonRequired},
		{name: "4097 bytes", key: cliMarker + strings.Repeat("k", 4097-len(cliMarker)), reason: reasonInvalidFormat},
		{name: "64 KiB", key: cliMarker + strings.Repeat("k", 64<<10), reason: reasonInvalidFormat},
		{name: "inner space", key: cliMarker + " k", reason: reasonInvalidFormat},
		{name: "leading space", key: " " + cliMarker, reason: reasonInvalidFormat},
		{name: "trailing space", key: cliMarker + " ", reason: reasonInvalidFormat},
		{name: "only a space", key: " ", reason: reasonInvalidFormat},
		{name: "tab", key: cliMarker + "\tk", reason: reasonInvalidFormat},
		{name: "line feed", key: cliMarker + "\nk", reason: reasonInvalidFormat},
		{name: "carriage return", key: cliMarker + "\rk", reason: reasonInvalidFormat},
		{name: "NUL", key: cliMarker + "\x00k", reason: reasonInvalidFormat},
		{name: "unit separator", key: cliMarker + "\x1fk", reason: reasonInvalidFormat},
		{name: "DEL", key: cliMarker + "\x7f", reason: reasonInvalidFormat},
		{name: "non-ASCII letter", key: cliMarker + "é", reason: reasonInvalidFormat},
		{name: "byte 0x80", key: cliMarker + "\x80", reason: reasonInvalidFormat},
		{name: "byte 0xff", key: cliMarker + "\xff", reason: reasonInvalidFormat},
		{name: "header injection", key: cliMarker + "\r\nX-Injected: 1", reason: reasonInvalidFormat},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := emailable.NewClient(tt.key)
			var secrets []string
			if strings.TrimSpace(tt.key) != "" {
				secrets = append(secrets, tt.key)
			}
			cliConfigError(t, c, err, "apiKey", tt.reason, secrets...)
		})
	}
}

func TestClientKeyEveryByte(t *testing.T) {
	for b := byte(0); ; b++ {
		key := "k" + string([]byte{b}) + "k"
		_, err := emailable.NewClient(key)
		if b >= 0x21 && b <= 0x7e {
			require.NoError(t, err, "byte 0x%02x must be accepted", b)
		} else {
			require.ErrorIs(t, err, emailable.ErrInvalidConfig, "byte 0x%02x must be rejected", b)
			assert.Equal(t, reasonInvalidFormat, validationError(t, err).Reason, "byte 0x%02x", b)
		}
		if b == 0xff {
			break
		}
	}
}

func TestClientKeyAccepted(t *testing.T) {
	visible := make([]byte, 0, 0x7e-0x21+1)
	for b := byte(0x21); b <= 0x7e; b++ {
		visible = append(visible, b)
	}
	tests := []struct {
		name string
		key  string
	}{
		{name: "one byte", key: "k"},
		{name: "exactly 4096 bytes", key: strings.Repeat("k", 4096)},
		{name: "every visible ASCII byte", key: string(visible)},
		{name: "no test_ prefix needed", key: "live-looking-key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := testutil.NewScript(testutil.Step{Body: accountOK})
			c, err := emailable.NewClient(tt.key, emailable.WithTestBaseURL(loopbackOrigin),
				emailable.WithHTTPClient(&http.Client{Transport: s}))
			require.NoError(t, err)
			require.NotNil(t, c)
			_, err = c.AccountInfo(t.Context(), emailable.AccountInfoRequest{})
			require.NoError(t, err)
			r := testutil.OnlyRequest(t, s)
			assert.Equal(t, []string{"Bearer " + tt.key}, r.Header.Values("Authorization"), "the key is sent as it is")
			assert.NotContains(t, r.URL, tt.key, "never in the URL")
			assert.Empty(t, r.Body)
		})
	}
}

func TestClientOptionRejected(t *testing.T) {
	tests := []struct {
		name    string
		opt     emailable.Option
		field   string
		reason  string
		secrets []string
	}{
		{name: "nil HTTP client", opt: emailable.WithHTTPClient(nil), field: "HTTPClient", reason: reasonNil},
		{name: "zero timeout", opt: emailable.WithTimeout(0), field: "Timeout", reason: reasonOutOfRange},
		{name: "negative timeout", opt: emailable.WithTimeout(-time.Second), field: "Timeout", reason: reasonOutOfRange},
		{
			name: "timeout over 10m", opt: emailable.WithTimeout(10*time.Minute + time.Nanosecond),
			field: "Timeout", reason: reasonOutOfRange, secrets: []string{"10m0.000000001s", "600000000001"},
		},
		{name: "zero max response bytes", opt: emailable.WithMaxResponseBytes(0), field: "MaxResponseBytes", reason: reasonOutOfRange},
		{name: "negative max response bytes", opt: emailable.WithMaxResponseBytes(-1), field: "MaxResponseBytes", reason: reasonOutOfRange},
		{name: "max response bytes under 1 KiB", opt: emailable.WithMaxResponseBytes(1023), field: "MaxResponseBytes", reason: reasonOutOfRange},
		{
			name: "max response bytes over 64 MiB", opt: emailable.WithMaxResponseBytes(64<<20 + 1),
			field: "MaxResponseBytes", reason: reasonOutOfRange, secrets: []string{"67108865"},
		},
		{name: "zero max batch emails", opt: emailable.WithMaxBatchEmails(0), field: "MaxBatchEmails", reason: reasonOutOfRange},
		{name: "negative max batch emails", opt: emailable.WithMaxBatchEmails(-1), field: "MaxBatchEmails", reason: reasonOutOfRange},
		{name: "max batch emails over a million", opt: emailable.WithMaxBatchEmails(1_000_001), field: "MaxBatchEmails", reason: reasonOutOfRange},
		{
			name: "max batch emails far over a million", opt: emailable.WithMaxBatchEmails(7_654_321),
			field: "MaxBatchEmails", reason: reasonOutOfRange, secrets: []string{"7654321"},
		},
		{
			name: "invalid test URL", opt: emailable.WithTestBaseURL("http://" + cliMarker + ".example.com"),
			field: "TestBaseURL", reason: reasonInvalidURL, secrets: []string{"example.com"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := emailable.NewClient(testKey, tt.opt)
			cliConfigError(t, c, err, tt.field, tt.reason, tt.secrets...)
		})
	}
}

func TestClientOptionBoundariesAccepted(t *testing.T) {
	with := func(from, to string) string { return strings.Replace(cliDefaultString, from, to, 1) }
	tests := []struct {
		name string
		opts []emailable.Option
		want string // the Client's String
	}{
		{name: "no options", want: cliDefaultString},
		{name: "nil option", opts: []emailable.Option{nil}, want: cliDefaultString},
		{
			name: "timeout of one nanosecond", opts: []emailable.Option{emailable.WithTimeout(time.Nanosecond)},
			want: with("timeout: 30s", "timeout: 1ns"),
		},
		{
			name: "timeout of exactly 10m", opts: []emailable.Option{emailable.WithTimeout(10 * time.Minute)},
			want: with("timeout: 30s", "timeout: 10m0s"),
		},
		{
			name: "max response bytes of 1 KiB", opts: []emailable.Option{emailable.WithMaxResponseBytes(1024)},
			want: with("maxResponseBytes: 8388608", "maxResponseBytes: 1024"),
		},
		{
			name: "max response bytes of 64 MiB", opts: []emailable.Option{emailable.WithMaxResponseBytes(64 << 20)},
			want: with("maxResponseBytes: 8388608", "maxResponseBytes: 67108864"),
		},
		{
			name: "max batch emails of 1", opts: []emailable.Option{emailable.WithMaxBatchEmails(1)},
			want: with("maxBatchEmails: 50000", "maxBatchEmails: 1"),
		},
		{
			name: "max batch emails of a million", opts: []emailable.Option{emailable.WithMaxBatchEmails(1_000_000)},
			want: with("maxBatchEmails: 50000", "maxBatchEmails: 1000000"),
		},
		{name: "HTTP client", opts: []emailable.Option{emailable.WithHTTPClient(&http.Client{})}, want: cliDefaultString},
		{
			name: "test URL", opts: []emailable.Option{emailable.WithTestBaseURL("https://127.0.0.1:8443")},
			want: with("testBaseURL: false", "testBaseURL: true"),
		},
		{
			name: "provider diagnostics", opts: []emailable.Option{emailable.WithProviderDiagnostics()},
			want: with("providerDiagnostics: false", "providerDiagnostics: true"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Construction performs no I/O, so the default transport is safe here.
			c, err := emailable.NewClient(testKey, tt.opts...)
			require.NoError(t, err)
			require.NotNil(t, c)
			assert.Equal(t, tt.want, c.String())
		})
	}
}

func TestClientDefaults(t *testing.T) {
	assert.Equal(t, 30*time.Second, emailable.DefaultTimeout)
	assert.Equal(t, 8<<20, emailable.DefaultMaxResponseBytes)
	assert.Equal(t, 50_000, emailable.DefaultMaxBatchEmails)
	assert.Equal(t, 2*time.Second, emailable.DefaultPendingInterval)
	assert.Equal(t, 4*time.Minute, emailable.MaxPendingWait)
	assert.Equal(t, 249, emailable.StatusPending)
}

func TestClientValidationOrder(t *testing.T) {
	var (
		badClient   = emailable.WithHTTPClient(nil)
		badTimeout  = emailable.WithTimeout(0)
		badResponse = emailable.WithMaxResponseBytes(0)
		badBatch    = emailable.WithMaxBatchEmails(0)
		badURL      = emailable.WithTestBaseURL("http://127.0.0.1")
	)
	tests := []struct {
		name  string
		key   string
		opts  []emailable.Option
		field string
	}{
		{
			name: "key before every option", key: "",
			opts: []emailable.Option{badURL, badBatch, badResponse, badTimeout, badClient}, field: "apiKey",
		},
		{
			name: "HTTP client before timeout", key: testKey,
			opts: []emailable.Option{badURL, badBatch, badResponse, badTimeout, badClient}, field: "HTTPClient",
		},
		{
			name: "timeout before max response bytes", key: testKey,
			opts: []emailable.Option{badURL, badBatch, badResponse, badTimeout}, field: "Timeout",
		},
		{
			name: "max response bytes before max batch emails", key: testKey,
			opts: []emailable.Option{badURL, badBatch, badResponse}, field: "MaxResponseBytes",
		},
		{
			name: "max batch emails before test URL", key: testKey,
			opts: []emailable.Option{badURL, badBatch}, field: "MaxBatchEmails",
		},
		{name: "test URL last", key: testKey, opts: []emailable.Option{badURL}, field: "TestBaseURL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := emailable.NewClient(tt.key, tt.opts...)
			assert.Nil(t, c)
			require.ErrorIs(t, err, emailable.ErrInvalidConfig)
			assert.Equal(t, tt.field, validationError(t, err).Field)
		})
	}
}

func TestClientNilOptionSkipped(t *testing.T) {
	c, err := emailable.NewClient(testKey, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, cliDefaultString, c.String())

	c, err = emailable.NewClient(testKey, nil, emailable.WithTimeout(time.Second), nil)
	require.NoError(t, err)
	assert.Contains(t, c.String(), "timeout: 1s,")

	_, err = emailable.NewClient(testKey, nil, emailable.WithTimeout(0), nil)
	require.ErrorIs(t, err, emailable.ErrInvalidConfig)
	assert.Equal(t, "Timeout", validationError(t, err).Field)
}

func TestClientLastOptionWins(t *testing.T) {
	tests := []struct {
		name string
		opts [2]emailable.Option // an invalid value, then a valid one
		want string
	}{
		{
			name: "timeout",
			opts: [2]emailable.Option{emailable.WithTimeout(0), emailable.WithTimeout(3 * time.Second)},
			want: "timeout: 3s,",
		},
		{
			name: "max response bytes",
			opts: [2]emailable.Option{emailable.WithMaxResponseBytes(-5), emailable.WithMaxResponseBytes(4096)},
			want: "maxResponseBytes: 4096,",
		},
		{
			name: "max batch emails",
			opts: [2]emailable.Option{emailable.WithMaxBatchEmails(0), emailable.WithMaxBatchEmails(99)},
			want: "maxBatchEmails: 99,",
		},
		{
			name: "test URL",
			opts: [2]emailable.Option{
				emailable.WithTestBaseURL("http://" + cliMarker), emailable.WithTestBaseURL(loopbackOrigin),
			},
			want: "testBaseURL: true,",
		},
		{
			name: "HTTP client",
			opts: [2]emailable.Option{emailable.WithHTTPClient(nil), emailable.WithHTTPClient(&http.Client{})},
			want: "emailable.Client{",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := emailable.NewClient(testKey, tt.opts[0], tt.opts[1])
			require.NoError(t, err, "an invalid value overridden by a later valid one is never seen")
			assert.Contains(t, c.String(), tt.want)

			c, err = emailable.NewClient(testKey, tt.opts[1], tt.opts[0])
			require.ErrorIs(t, err, emailable.ErrInvalidConfig, "the invalid value given last wins")
			assert.Nil(t, c)
		})
	}
}

// TestClientLastOptionWinsOnTheWire proves that the last WithHTTPClient,
// WithTestBaseURL and WithMaxResponseBytes are the ones calls use.
func TestClientLastOptionWinsOnTheWire(t *testing.T) {
	body := padded(t, accountOK, 1025)
	first := testutil.NewScript(testutil.Step{Body: body})
	last := testutil.NewScript(testutil.Step{Body: body})
	c := newClient(t,
		emailable.WithHTTPClient(&http.Client{Transport: first}),
		emailable.WithTestBaseURL("https://127.0.0.2:9443"),
		emailable.WithHTTPClient(&http.Client{Transport: last}),
		emailable.WithTestBaseURL("https://[::1]:8443/"),
		emailable.WithMaxResponseBytes(1024),
		emailable.WithMaxResponseBytes(2048),
	)
	_, err := c.AccountInfo(t.Context(), emailable.AccountInfoRequest{})
	require.NoError(t, err, "the 2048-byte limit given last applies")
	assert.Zero(t, first.Calls())
	assert.Equal(t, "https://[::1]:8443"+pathAccount, testutil.OnlyRequest(t, last).URL)
}

func TestClientNewClientDoesNoIO(t *testing.T) {
	ct := &testutil.CountingTransport{Next: offlineTransport(t)}
	hc := &http.Client{Transport: ct}
	for range 3 {
		_, err := emailable.NewClient(testKey, emailable.WithHTTPClient(hc), emailable.WithTestBaseURL(loopbackOrigin),
			emailable.WithProviderDiagnostics())
		require.NoError(t, err)
		_, err = emailable.NewClient(testKey, emailable.WithHTTPClient(hc))
		require.NoError(t, err)
	}
	assert.Zero(t, ct.Attempts(), "NewClient never sends anything")
}

// TestClientProductionOrigin proves that without WithTestBaseURL every call
// goes to the documented production host. The in-memory transport never
// dials.
func TestClientProductionOrigin(t *testing.T) {
	for _, o := range opCases() {
		t.Run(o.name, func(t *testing.T) {
			s := testutil.NewScript(o.ok)
			c := newClient(t, emailable.WithHTTPClient(&http.Client{Transport: s}))
			require.NoError(t, o.call(t.Context(), c))
			u, err := url.Parse(testutil.OnlyRequest(t, s).URL)
			require.NoError(t, err)
			assert.Equal(t, "https", u.Scheme)
			assert.Equal(t, "api.emailable.com", u.Host)
			assert.Equal(t, o.path, u.Path)
		})
	}
}

func TestClientTestBaseURLAccepted(t *testing.T) {
	tests := []struct{ raw, want string }{
		{raw: "https://127.0.0.1", want: "https://127.0.0.1/v1/account"},
		{raw: "https://127.0.0.1/", want: "https://127.0.0.1/v1/account"},
		{raw: "https://127.1.2.3:8443", want: "https://127.1.2.3:8443/v1/account"},
		{raw: "https://127.0.0.1:1", want: "https://127.0.0.1:1/v1/account"},
		{raw: "https://127.0.0.1:443", want: "https://127.0.0.1:443/v1/account"},
		{raw: "https://127.0.0.1:65535/", want: "https://127.0.0.1:65535/v1/account"},
		{raw: "https://127.0.0.0", want: "https://127.0.0.0/v1/account"},
		{raw: "https://127.255.255.255", want: "https://127.255.255.255/v1/account"},
		{raw: "https://[::1]", want: "https://[::1]/v1/account"},
		{raw: "https://[::1]/", want: "https://[::1]/v1/account"},
		{raw: "https://[::1]:443", want: "https://[::1]:443/v1/account"},
		{raw: "https://[::1]:65535/", want: "https://[::1]:65535/v1/account"},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			s := testutil.NewScript(testutil.Step{Body: accountOK})
			c := newClient(t, emailable.WithHTTPClient(&http.Client{Transport: s}), emailable.WithTestBaseURL(tt.raw))
			_, err := c.AccountInfo(t.Context(), emailable.AccountInfoRequest{})
			require.NoError(t, err)
			assert.Equal(t, tt.want, testutil.OnlyRequest(t, s).URL)
			assert.NotContains(t, c.String(), strings.TrimPrefix(tt.raw, "https://"), "String never prints the URL")
		})
	}
}

func TestClientTestBaseURLRejected(t *testing.T) {
	tests := []struct{ name, raw string }{
		{"empty", ""},
		{"http", "http://127.0.0.1"},
		{"websocket", "wss://127.0.0.1"},
		{"no scheme", "//127.0.0.1"},
		{"bare host", "127.0.0.1"},
		{"opaque", "https:127.0.0.1"},
		{"no host", "https://"},
		{"no host with path", "https:///v1"},
		{"localhost", "https://localhost"},
		{"localhost with port", "https://localhost:8443"},
		{"name", "https://" + cliMarker + ".example.com"},
		{"loopback-looking name", "https://127.0.0.1." + cliMarker + ".example"},
		{"just past 127/8", "https://128.0.0.1"},
		{"just before 127/8", "https://126.255.255.255"},
		{"unspecified IPv4", "https://0.0.0.0"},
		{"private IPv4", "https://10.0.0.1"},
		{"link-local", "https://169.254.169.254"},
		{"IPv6 non-loopback", "https://[::2]"},
		{"IPv6 unspecified", "https://[::]"},
		{"expanded IPv6 loopback", "https://[0:0:0:0:0:0:0:1]"},
		{"IPv4-mapped IPv6", "https://[::ffff:127.0.0.1]"},
		{"IPv6 loopback with zone", "https://[::1%25lo0]"},
		{"unbracketed IPv6", "https://::1"},
		{"userinfo", "https://" + cliMarker + "@127.0.0.1"},
		{"userinfo with password", "https://user:" + cliMarker + "@127.0.0.1:8443"},
		{"empty userinfo", "https://@127.0.0.1"},
		{"query", "https://127.0.0.1/?key=" + cliMarker},
		{"query without path", "https://127.0.0.1?" + cliMarker},
		{"empty query", "https://127.0.0.1?"},
		{"empty query after slash", "https://127.0.0.1/?"},
		{"fragment", "https://127.0.0.1#" + cliMarker},
		{"empty fragment", "https://127.0.0.1/#"},
		{"API path", "https://127.0.0.1/v1"},
		{"API path with trailing slash", "https://127.0.0.1/v1/"},
		{"other path", "https://127.0.0.1/" + cliMarker},
		{"double slash", "https://127.0.0.1//"},
		{"escaped slash", "https://127.0.0.1/%2F"},
		{"backslash", "https://127.0.0.1\\" + cliMarker},
		{"backslash path", "https://127.0.0.1/\\"},
		{"space in host", "https://127.0.0.1 "},
		{"leading space", " https://127.0.0.1"},
		{"tab", "https://127.0.0.1\t"},
		{"short IPv4", "https://127.1"},
		{"leading zeros in an octet", "https://127.000.0.1"},
		{"leading zero in the last octet", "https://127.0.0.01"},
		{"hex address", "https://0x7f.0.0.1"},
		{"decimal address", "https://2130706433"},
		{"port 0", "https://127.0.0.1:0"},
		{"port 65536", "https://127.0.0.1:65536"},
		{"port with a leading zero", "https://127.0.0.1:080"},
		{"empty port", "https://127.0.0.1:"},
		{"signed port", "https://127.0.0.1:+80"},
		{"two ports", "https://127.0.0.1:80:80"},
		{"IPv6 port 0", "https://[::1]:0"},
		{"IPv6 port 65536", "https://[::1]:65536"},
		{"IPv6 port without colon", "https://[::1]8443"},
		{"IPv6 empty port", "https://[::1]:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := emailable.NewClient(testKey, emailable.WithTestBaseURL(tt.raw))
			var secrets []string
			if host := strings.TrimSpace(strings.TrimPrefix(tt.raw, "https://")); len(host) > 3 {
				secrets = append(secrets, tt.raw, host)
			}
			cliConfigError(t, c, err, "TestBaseURL", reasonInvalidURL, secrets...)
		})
	}
}

// cliJar is a cookie jar that counts every use.
type cliJar struct {
	uses atomic.Int32
}

func (j *cliJar) SetCookies(*url.URL, []*http.Cookie) { j.uses.Add(1) }

func (j *cliJar) Cookies(*url.URL) []*http.Cookie {
	j.uses.Add(1)
	return []*http.Cookie{{Name: "session", Value: cliMarker, Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode}}
}

// TestClientKeepsItsOwnCopy proves that changing the injected client after
// NewClient changes nothing about the calls.
func TestClientKeepsItsOwnCopy(t *testing.T) {
	ct := &testutil.CountingTransport{Next: testutil.StaticTransport(http.StatusOK, accountOK)}
	hc := &http.Client{Transport: ct}
	c := newClient(t, emailable.WithTestBaseURL(loopbackOrigin), emailable.WithHTTPClient(hc))
	hc.Transport = offlineTransport(t)
	hc.Timeout = time.Nanosecond
	hc.Jar = &cliJar{}
	_, err := c.AccountInfo(t.Context(), emailable.AccountInfoRequest{})
	require.NoError(t, err)
	assert.Equal(t, 1, ct.Attempts())
}

// TestClientInjectedTimeoutHonored proves that an injected http.Client's
// Timeout bounds a call that the SDK's own, longer timeout would not, and
// that it is classified as the SDK's timeout, never the caller's deadline.
func TestClientInjectedTimeoutHonored(t *testing.T) {
	for _, o := range opCases() {
		t.Run(o.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ct := &testutil.CountingTransport{Next: testutil.RoundTripFunc(testutil.BlockUntilDone)}
				hc := &http.Client{Transport: ct, Timeout: 3 * time.Second}
				c := newClient(t, emailable.WithTestBaseURL(loopbackOrigin), emailable.WithHTTPClient(hc),
					emailable.WithTimeout(time.Minute))
				start := time.Now()
				err := o.call(t.Context(), c)
				assert.Equal(t, 3*time.Second, time.Since(start), "the injected Timeout ends the call")
				_ = assertCallError(t, err, o.want(wantCall{
					Kind: emailable.FailureTimeout, Attempts: 1, OutcomeUnknown: o.paid,
				}))
				assert.Equal(t, 3*time.Second, hc.Timeout, "the injected client keeps its Timeout")
				assert.Equal(t, 1, ct.Attempts())
			})
		})
	}
}

func TestClientCookieJarNeverConsulted(t *testing.T) {
	for _, proto := range testutil.Protocols() {
		t.Run(proto.Name(), func(t *testing.T) {
			srv := testutil.NewServerProto(t, proto, func(w http.ResponseWriter, r *http.Request) {
				http.SetCookie(w, &http.Cookie{
					Name: "session", Value: cliMarker, Path: "/", Secure: true, HttpOnly: true,
					SameSite: http.SameSiteStrictMode,
				})
				testutil.Serve(testutil.Step{Body: accountOK})(w, r)
			})
			jar := &cliJar{}
			hc := srv.Client()
			hc.Jar = jar
			c := newClient(t, emailable.WithTestBaseURL(srv.URL()), emailable.WithHTTPClient(hc))
			for range 2 {
				_, err := c.AccountInfo(t.Context(), emailable.AccountInfoRequest{})
				require.NoError(t, err)
			}
			assert.Zero(t, jar.uses.Load(), "the jar is neither read nor written")
			reqs := srv.Requests()
			require.Len(t, reqs, 2)
			for _, r := range reqs {
				assert.Empty(t, r.Header.Values("Cookie"))
			}
		})
	}
}

func TestClientString(t *testing.T) {
	const raw = "https://127.0.0.1:51234/"
	c := newClient(t, emailable.WithTestBaseURL(raw), emailable.WithTimeout(45*time.Second),
		emailable.WithMaxResponseBytes(4096), emailable.WithMaxBatchEmails(1000), emailable.WithProviderDiagnostics())
	want := "emailable.Client{timeout: 45s, maxResponseBytes: 4096, maxBatchEmails: 1000, testBaseURL: true, " +
		"providerDiagnostics: true}"
	assert.Equal(t, want, c.String())
	assert.Equal(t, want, c.GoString())
	for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
		assert.Equal(t, want, fmt.Sprintf(verb, c), verb)
	}

	secrets := []string{testKey, "Bearer " + testKey, raw, "51234", "127.0.0.1", "api.emailable.com"}
	testutil.AssertShapesNeverPrint(t, c, secrets...)
	testutil.AssertRedacted(t, strings.Join(testutil.ReflectStrings(c), "\n"), secrets, "reflection walk")

	production := newClient(t)
	testutil.AssertShapesNeverPrint(t, production, secrets...)
	testutil.AssertDiffRedacted(t, c, production, secrets...)
}

func TestClientStringNilAndZero(t *testing.T) {
	var nilClient *emailable.Client
	for _, s := range []string{nilClient.String(), nilClient.GoString(), fmt.Sprintf("%v", nilClient), fmt.Sprintf("%#v", nilClient)} {
		assert.Equal(t, "emailable.Client(nil)", s)
	}

	var zero emailable.Client
	want := "emailable.Client{timeout: 0s, maxResponseBytes: 0, maxBatchEmails: 0, testBaseURL: false, " +
		"providerDiagnostics: false}"
	assert.Equal(t, want, zero.String())
	assert.Equal(t, want, zero.GoString())
}

// cliMethod is an operation case together with a call of the same method
// with a request it refuses, nil for AccountInfo, whose request has no
// fields.
type cliMethod struct {
	opCase

	invalid func(ctx context.Context, c *emailable.Client) error
}

// cliMethods returns every method's case, with a call it refuses.
func cliMethods() []cliMethod {
	invalid := map[string]func(context.Context, *emailable.Client) error{
		"VerifyEmail": func(ctx context.Context, c *emailable.Client) error {
			return noValueWith(c.VerifyEmail(ctx, emailable.VerifyEmailRequest{}))
		},
		"CreateBatch": func(ctx context.Context, c *emailable.Client) error {
			return noValueWith(c.CreateBatch(ctx, emailable.CreateBatchRequest{}))
		},
		"GetBatch": func(ctx context.Context, c *emailable.Client) error {
			return noValueWith(c.GetBatch(ctx, emailable.GetBatchRequest{}))
		},
		"WaitForVerification": func(ctx context.Context, c *emailable.Client) error {
			return noValueWith(c.WaitForVerification(ctx, emailable.WaitForVerificationRequest{}))
		},
	}
	cases := opCases()
	out := make([]cliMethod, 0, len(cases))
	for _, o := range cases {
		out = append(out, cliMethod{opCase: o, invalid: invalid[o.name]})
	}
	return out
}

// cliCalls returns m's valid call and, when it has one, its invalid one,
// each named.
func cliCalls(m cliMethod) map[string]func(context.Context, *emailable.Client) error {
	calls := map[string]func(context.Context, *emailable.Client) error{"valid": m.call}
	if m.invalid != nil {
		calls["invalid"] = m.invalid
	}
	return calls
}

// TestClientMethodsNotInitialized proves that a nil or zero Client fails
// every method with ErrInvalidConfig before the request is validated, and
// that a nil context is reported first.
func TestClientMethodsNotInitialized(t *testing.T) {
	for _, m := range cliMethods() {
		for clientName, client := range cliUninitialized() {
			for callName, call := range cliCalls(m) {
				t.Run(m.name+"/"+clientName+"/"+callName, func(t *testing.T) {
					err := call(t.Context(), client)
					assertValidation(t, err, emailable.ErrInvalidConfig, "Client", reasonNotInitialized)
				})
			}
		}
	}
}

// cliUninitialized returns the Clients not built by NewClient: nil and the
// zero value.
func cliUninitialized() map[string]*emailable.Client {
	var nilClient *emailable.Client
	return map[string]*emailable.Client{"nil client": nilClient, "zero client": {}}
}

// TestClientMethodsNilContextFirst proves that a nil context is reported
// before an uninitialized Client and an invalid request.
func TestClientMethodsNilContextFirst(t *testing.T) {
	var nilCtx context.Context
	for _, m := range cliMethods() {
		for clientName, client := range cliUninitialized() {
			for callName, call := range cliCalls(m) {
				t.Run(m.name+"/"+clientName+"/"+callName, func(t *testing.T) {
					assertValidation(t, call(nilCtx, client), emailable.ErrInvalidRequest, "ctx", reasonNil)
				})
			}
		}
	}
}

func TestClientMethodsNilContext(t *testing.T) {
	var nilCtx context.Context
	for _, m := range cliMethods() {
		for callName, call := range cliCalls(m) {
			t.Run(m.name+"/"+callName, func(t *testing.T) {
				c, ct := newOfflineClient(t)
				err := call(nilCtx, c)
				assertValidation(t, err, emailable.ErrInvalidRequest, "ctx", reasonNil)
				assert.Zero(t, ct.Attempts())
			})
		}
	}
}

// cliDoneContexts returns a canceled context and an expired one, with the
// failure kind each must produce.
func cliDoneContexts(tb testing.TB) map[string]struct {
	ctx  context.Context //nolint:containedctx // each case runs with its own context
	kind emailable.FailureKind
	is   error
} {
	tb.Helper()
	canceled, cancel := context.WithCancel(tb.Context())
	cancel()
	expired, cancelExpired := context.WithDeadline(tb.Context(), time.Now().Add(-time.Second))
	tb.Cleanup(cancelExpired)
	return map[string]struct {
		ctx  context.Context //nolint:containedctx // each case runs with its own context
		kind emailable.FailureKind
		is   error
	}{
		"canceled": {ctx: canceled, kind: emailable.FailureCanceled, is: context.Canceled},
		"expired":  {ctx: expired, kind: emailable.FailureDeadlineExceeded, is: context.DeadlineExceeded},
	}
}

func TestClientMethodsDoneContextSendNothing(t *testing.T) {
	for _, m := range cliMethods() {
		for name, dc := range cliDoneContexts(t) {
			t.Run(m.name+"/"+name, func(t *testing.T) {
				c, ct := newOfflineClient(t)
				err := m.call(dc.ctx, c)
				ce := assertCallError(t, err, m.want(wantCall{Kind: dc.kind}))
				assert.Equal(t, dc.is, errors.Unwrap(ce), "the cause is the context error")
				assert.Zero(t, ct.Attempts(), "a done context sends nothing")
			})
		}
	}
}

// TestClientMethodsValidateBeforeContext proves that the request is
// validated before the context's state is looked at: an invalid request with
// a done context is a validation error, and nothing is sent.
func TestClientMethodsValidateBeforeContext(t *testing.T) {
	for _, m := range cliMethods() {
		if m.invalid == nil {
			continue
		}
		for name, dc := range cliDoneContexts(t) {
			t.Run(m.name+"/"+name, func(t *testing.T) {
				c, ct := newOfflineClient(t)
				err := m.invalid(dc.ctx, c)
				require.ErrorIs(t, err, emailable.ErrInvalidRequest)
				require.NotErrorIs(t, err, dc.is)
				assert.Zero(t, ct.Attempts())
			})
		}
	}
}
