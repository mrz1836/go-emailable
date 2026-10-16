package emailable

import (
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Defaults and limits.
const (
	// DefaultTimeout bounds each HTTP attempt, including reading the whole
	// response body. WithTimeout accepts more than 0 and at most 10 minutes.
	DefaultTimeout = 30 * time.Second

	// DefaultMaxResponseBytes limits every response body.
	// WithMaxResponseBytes accepts 1 KiB to 64 MiB. Only GetBatch can use more
	// than 1 MiB: every other operation is capped at the lower of the limit
	// and 1 MiB, far above any documented answer. DecodeBatchCallback takes
	// its own limit in the same range.
	DefaultMaxResponseBytes = 8 << 20

	// DefaultMaxBatchEmails is the provider's standard limit on the addresses
	// in one batch. WithMaxBatchEmails accepts 1 to 1,000,000, for accounts
	// the provider has granted more.
	DefaultMaxBatchEmails = 50_000

	// DefaultPendingInterval is WaitForVerification's pause after each HTTP
	// 249 when WaitForVerificationRequest.Interval is 0.
	DefaultPendingInterval = 2 * time.Second

	// MaxPendingWait is the largest WaitForVerificationRequest.MaxWait: one
	// minute inside the provider's documented five-minute window in which a
	// repeat of a pending verification is free.
	MaxPendingWait = 4 * time.Minute

	// StatusPending is the HTTP status (249) with which the provider answers
	// a verification that is still running.
	StatusPending = 249
)

// Internal bounds.
const (
	// productionHost is the only server the provider documents. Calls to it
	// are live and may be billed, unless the key is a test key.
	productionHost = "api.emailable.com"

	// apiRoot is the path prefix of every operation: API version 1 only.
	apiRoot = "/v1/"

	maxTimeout            = 10 * time.Minute
	minResponseBytesLimit = 1 << 10
	maxResponseBytesLimit = 64 << 20
	smallResponseCap      = 1 << 20
	maxBatchEmailsLimit   = 1_000_000
	maxKeyBytes           = 4096

	// minKeyOverlap is the shortest piece of the API key, or of a callback
	// URL's query value, that the SDK treats as a secret of its own: shorter
	// ones would match ordinary text.
	minKeyOverlap = 8
)

// Client calls the Emailable API v1. Create it with NewClient; the zero Client
// and a nil *Client return ErrInvalidConfig without any I/O. A Client is
// immutable and safe for concurrent use.
//
// Its fields are unexported and the API key is held only inside function
// values, so formatting a Client, or a struct holding one, with any fmt verb or
// a reflection-based dumper never prints it.
type Client struct {
	send  sender
	scrub func(s string, secrets []string) string

	// overlapsKey reports whether s holds the API key, or is a piece of it
	// of at least minKeyOverlap bytes.
	overlapsKey func(s string) bool

	timeout          time.Duration
	maxResponseBytes int64
	maxBatchEmails   int
	testBaseURL      bool
	diagnostics      bool
}

// NewClient returns a Client that authenticates with an Emailable API key.
//
// Parameters:
//   - apiKey: a private or public key from the Emailable dashboard, sent as
//     "Authorization: Bearer <key>" on every request, never in a URL or body.
//     A public key works only for VerifyEmail (and WaitForVerification),
//     within the domain allowlist and daily cap the provider applies; the
//     other operations need a private key
//   - opts: optional settings; see the With functions
//
// The key must be 1 to 4096 bytes of visible ASCII (0x21 to 0x7E). Its prefix
// (such as "test_") is never inspected: a test key is the provider's way to
// make calls that use no credits. The application decides where the key comes
// from: NewClient reads no environment variables or files and performs no I/O.
//
// Errors are *ValidationError values matching ErrInvalidConfig. They name the
// argument or option, never its value.
func NewClient(apiKey string, opts ...Option) (*Client, error) {
	if err := checkKey(apiKey); err != nil {
		return nil, err
	}
	o := options{
		timeout:          DefaultTimeout,
		maxResponseBytes: DefaultMaxResponseBytes,
		maxBatchEmails:   DefaultMaxBatchEmails,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	host, err := o.validate()
	if err != nil {
		return nil, err
	}
	return &Client{
		send:  newSender(o.client(), host, "Bearer "+apiKey),
		scrub: func(s string, secrets []string) string { return scrubSecrets(s, apiKey, secrets) },
		overlapsKey: func(s string) bool {
			return strings.Contains(s, apiKey) || (len(s) >= minKeyOverlap && strings.Contains(apiKey, s))
		},
		timeout:          o.timeout,
		maxResponseBytes: o.maxResponseBytes,
		maxBatchEmails:   o.maxBatchEmails,
		testBaseURL:      o.testBaseURLSet,
		diagnostics:      o.diagnostics,
	}, nil
}

// String describes the Client's settings, such as
// "emailable.Client{timeout: 30s, maxResponseBytes: 8388608, ...}". It never
// includes the API key or the base URL.
func (c *Client) String() string {
	if c == nil {
		return "emailable.Client(nil)"
	}
	return describe("emailable.Client",
		field{"timeout", c.timeout.String()},
		field{"maxResponseBytes", strconv.FormatInt(c.maxResponseBytes, 10)},
		field{"maxBatchEmails", strconv.Itoa(c.maxBatchEmails)},
		field{"testBaseURL", strconv.FormatBool(c.testBaseURL)},
		field{"providerDiagnostics", strconv.FormatBool(c.diagnostics)},
	)
}

// GoString returns the same text as String, so that %#v prints settings only.
func (c *Client) GoString() string { return c.String() }

// ready reports ErrInvalidConfig for a nil Client or one not built by
// NewClient.
func (c *Client) ready() error {
	if c == nil || c.send == nil || c.scrub == nil || c.overlapsKey == nil {
		return invalidConfig("Client", reasonNotInitialized)
	}
	return nil
}

// responseLimit returns the body limit for op: the configured limit for
// GetBatch, and the lower of it and 1 MiB for every other operation.
func (c *Client) responseLimit(op Operation) int64 {
	if op == OpGetBatch {
		return c.maxResponseBytes
	}
	return min(c.maxResponseBytes, smallResponseCap)
}

// scrubSecrets replaces every occurrence of key and of each secret, raw and in
// the form url.QueryEscape gives it in a query string, with "[redacted]". It
// replaces in one pass, longest secret first, so that a short secret never
// matches inside a longer one or inside a "[redacted]" already written.
func scrubSecrets(s, key string, secrets []string) string {
	var olds []string
	for _, secret := range append([]string{key}, secrets...) {
		if secret == "" {
			continue
		}
		olds = append(olds, secret)
		if escaped := url.QueryEscape(secret); escaped != secret {
			olds = append(olds, escaped)
		}
	}
	slices.SortStableFunc(olds, func(a, b string) int { return len(b) - len(a) })
	pairs := make([]string, 0, 2*len(olds))
	for _, old := range olds {
		pairs = append(pairs, old, redacted)
	}
	return strings.NewReplacer(pairs...).Replace(s)
}

// checkKey validates the API key: 1 to 4096 bytes of visible ASCII, so that
// it is a valid header value as it is. The error names the argument, never
// its value.
func checkKey(key string) error {
	if key == "" {
		return invalidConfig("apiKey", reasonRequired)
	}
	if len(key) > maxKeyBytes || !asciiRange(key, 0x21, 0x7e) {
		return invalidConfig("apiKey", reasonInvalidFormat)
	}
	return nil
}

// client returns the HTTP client calls use: a copy of the injected client, or a
// new one on a fresh transport, that never follows a redirect and keeps no
// cookies.
func (o *options) client() *http.Client {
	var c http.Client
	if o.httpClientSet {
		c = *o.httpClient
	} else {
		c.Transport = newTransport()
	}
	next := c.Transport
	if next == nil {
		next = http.DefaultTransport
	}
	c.Transport = redirectGuard{next: next}
	c.CheckRedirect = refuseRedirect
	c.Jar = nil
	return &c
}

// newTransport returns a fresh transport with the standard library's default
// settings, a bound on response headers, and its own connection pool. It uses
// http.ProxyFromEnvironment, as http.DefaultTransport does, so HTTPS_PROXY and
// NO_PROXY apply.
func newTransport() *http.Transport {
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	return &http.Transport{
		Proxy:                  http.ProxyFromEnvironment,
		DialContext:            dialer.DialContext,
		ForceAttemptHTTP2:      true,
		MaxIdleConns:           100,
		IdleConnTimeout:        90 * time.Second,
		TLSHandshakeTimeout:    10 * time.Second,
		ExpectContinueTimeout:  1 * time.Second,
		MaxResponseHeaderBytes: maxResponseHeaderBytes,
	}
}

// refuseRedirect stops the client at the first response, so the API key is
// never sent to another URL.
func refuseRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// redirectGuard removes the Location header from every 3xx response before the
// http.Client sees it. The client then returns the response as it is: it
// neither follows the redirect, which would resend the request, API key
// included, to the new URL, nor fails on an unparsable Location with an error
// that quotes it.
type redirectGuard struct {
	next http.RoundTripper
}

// RoundTrip passes r to the wrapped transport and strips Location from a 3xx.
func (g redirectGuard) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := g.next.RoundTrip(r)
	if resp != nil && resp.StatusCode >= http.StatusMultipleChoices && resp.StatusCode < http.StatusBadRequest {
		resp.Header.Del("Location")
	}
	return resp, err
}

// CloseIdleConnections closes the wrapped transport's idle connections, if it
// has any, so that (*http.Client).CloseIdleConnections still reaches it.
func (g redirectGuard) CloseIdleConnections() {
	if c, ok := g.next.(interface{ CloseIdleConnections() }); ok {
		c.CloseIdleConnections()
	}
}

// testHost validates a loopback test URL and returns its "host[:port]".
func testHost(raw string) (string, bool) {
	if raw == "" || strings.ContainsAny(raw, "?#@\\") {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Opaque != "" || u.User != nil ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" ||
		(u.Path != "" && u.Path != "/") {
		return "", false
	}
	if !loopbackHostPort(u.Host) {
		return "", false
	}
	return u.Host, true
}

// loopbackHostPort accepts "ip[:port]" for a canonical IPv4 address in
// 127.0.0.0/8, or "[::1][:port]", with a canonical port from 1 to 65535.
func loopbackHostPort(hostport string) bool {
	host, port := hostport, ""
	if end := strings.LastIndexByte(hostport, ']'); end >= 0 {
		host, port = hostport[:end+1], hostport[end+1:]
	} else if i := strings.LastIndexByte(hostport, ':'); i >= 0 {
		host, port = hostport[:i], hostport[i:]
	}
	if host != "[::1]" {
		ip, err := netip.ParseAddr(host)
		if err != nil || !ip.Is4() || !ip.IsLoopback() || ip.String() != host {
			return false
		}
	}
	if port == "" {
		return true
	}
	digits, ok := strings.CutPrefix(port, ":")
	if !ok {
		return false
	}
	n, err := strconv.ParseUint(digits, 10, 16)
	return err == nil && n != 0 && strconv.FormatUint(n, 10) == digits
}
