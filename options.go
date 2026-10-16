package emailable

import (
	"net/http"
	"time"
)

// Option configures NewClient. Options only record their argument: NewClient
// validates the result. A nil Option is skipped, and when an option is given
// more than once the last one wins.
type Option func(*options)

// options is the configuration options record.
type options struct {
	httpClient       *http.Client
	httpClientSet    bool
	timeout          time.Duration
	maxResponseBytes int64
	maxBatchEmails   int
	testBaseURL      string
	testBaseURLSet   bool
	diagnostics      bool
}

// WithHTTPClient makes calls through a copy of hc. The copy refuses redirects
// (its CheckRedirect returns http.ErrUseLastResponse), has no cookie jar, keeps
// hc.Timeout, and wraps hc's transport, without modifying it, so that every
// redirect comes back as a plain response with its Location removed and is
// never followed. A nil transport means http.DefaultTransport. hc itself is
// never modified.
//
// The transport is trusted: it sees the API key in the Authorization header,
// the email address in every verify URL, and every batch body, and it may
// retry on its own, which the SDK cannot prevent. A nil hc is rejected.
func WithHTTPClient(hc *http.Client) Option {
	return func(o *options) {
		o.httpClient = hc
		o.httpClientSet = true
	}
}

// WithTimeout bounds each HTTP attempt, including reading the whole response
// body. The default is DefaultTimeout (30 seconds); d must be greater than 0
// and at most 10 minutes. The caller's context deadline applies as well.
// Keep it at least 5 seconds above the provider's verification timeout
// (VerifyEmailRequest.ProviderTimeout, 5 seconds by default), or the SDK may
// give up on a verification the provider still bills.
func WithTimeout(d time.Duration) Option {
	return func(o *options) { o.timeout = d }
}

// WithMaxResponseBytes limits every response body, counted after any
// transport decompression. The default is DefaultMaxResponseBytes (8 MiB); n
// must be from 1 KiB to 64 MiB. Only GetBatch can use more than 1 MiB: every
// other operation is capped at the lower of n and 1 MiB. A larger HTTP 200
// body fails with ErrResponseTooLarge, after the provider may already have
// processed the call, so keep n well above a verification answer when you
// lower it.
func WithMaxResponseBytes(n int64) Option {
	return func(o *options) { o.maxResponseBytes = n }
}

// WithMaxBatchEmails raises or lowers the number of addresses CreateBatch
// accepts. The default is DefaultMaxBatchEmails (50,000), the provider's
// standard limit; n must be from 1 to 1,000,000. Raise it only when the
// provider has granted your account a larger limit.
func WithMaxBatchEmails(n int) Option {
	return func(o *options) { o.maxBatchEmails = n }
}

// WithTestBaseURL sends calls to a local test server instead of the provider.
// It exists for tests built on httptest.NewTLSServer, together with
// WithHTTPClient(srv.Client()). The SDK appends /v1/<operation> to it.
//
// The URL must use https and have a host that is a canonical IPv4 address in
// 127.0.0.0/8 or exactly [::1], with an optional port and no userinfo, query,
// fragment or path (a lone trailing "/" is allowed). Names such as "localhost"
// are rejected, because they resolve through the system resolver. Errors never
// quote the URL. There is no option for any other base URL, so the API key
// reaches only the provider or loopback.
func WithTestBaseURL(rawURL string) Option {
	return func(o *options) {
		o.testBaseURL = rawURL
		o.testBaseURLSet = true
	}
}

// WithProviderDiagnostics keeps the provider's "message" on errors, reachable
// only through (*CallError).Diagnostics, cut to 1 KiB and with the API key,
// the request's captcha response, its callback URL and that URL's query
// values of 8 bytes or more replaced by "[redacted]", as written or
// query-escaped. Other spellings, such as a truncated or re-cased value, are
// not recognized.
// It is off by default, because errors are routinely logged and passed on,
// and provider text may echo request data such as an email address. Without
// it, the SDK never reads the body of an error response.
func WithProviderDiagnostics() Option {
	return func(o *options) { o.diagnostics = true }
}

// validate checks the options in a fixed order and returns the API host
// (host[:port]).
func (o *options) validate() (string, error) {
	switch {
	case o.httpClientSet && o.httpClient == nil:
		return "", invalidConfig("HTTPClient", reasonNil)
	case o.timeout <= 0 || o.timeout > maxTimeout:
		return "", invalidConfig("Timeout", reasonOutOfRange)
	case o.maxResponseBytes < minResponseBytesLimit || o.maxResponseBytes > maxResponseBytesLimit:
		return "", invalidConfig("MaxResponseBytes", reasonOutOfRange)
	case o.maxBatchEmails < 1 || o.maxBatchEmails > maxBatchEmailsLimit:
		return "", invalidConfig("MaxBatchEmails", reasonOutOfRange)
	case !o.testBaseURLSet:
		return productionHost, nil
	}
	host, ok := testHost(o.testBaseURL)
	if !ok {
		return "", invalidConfig("TestBaseURL", reasonInvalidURL)
	}
	return host, nil
}
