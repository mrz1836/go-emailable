package emailable

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Single-verification bounds.
const (
	minProviderTimeout      = 2 * time.Second
	maxProviderTimeout      = 10 * time.Second
	providerTimeoutHeadroom = 5 * time.Second
	maxCaptchaBytes         = 8 << 10
)

// VerifyEmailRequest is one GET /v1/verify call. It is read only while the
// call encodes it, before any I/O, and is never kept or modified.
type VerifyEmailRequest struct {
	// Email is the address to verify, sent verbatim in the URL query: never
	// trimmed, normalized or checked against an RFC. It must be 1 to 254
	// bytes of valid UTF-8 without control characters or leading or trailing
	// whitespace. A malformed address is a legitimate input: the provider
	// answers it (and bills it) like any other.
	Email string

	// SMTP, when non-nil, is sent as "smtp": false skips the SMTP check. nil
	// is not sent (the provider's default is true).
	SMTP *bool

	// AcceptAll, when non-nil, is sent as "accept_all": true asks for the
	// accept-all check. nil is not sent (the provider's default is false).
	AcceptAll *bool

	// ProviderTimeout is the provider's own verification timeout, sent as
	// "timeout" in whole seconds. 0 means not sent (the provider's default,
	// 5 seconds). Otherwise it must be whole seconds from 2s to 10s, and at
	// least 5 seconds shorter than the Client's WithTimeout, so that the SDK
	// does not give up on a verification the provider will still bill. The
	// check applies only to a value you set: with 0, keep WithTimeout at
	// 10 seconds or more for the same margin (the default is 30 seconds).
	ProviderTimeout time.Duration

	// CaptchaResponse is sent as "captcha_response", for public keys that
	// require a captcha: at most 8 KiB of valid UTF-8 without control
	// characters. "" is not sent. A captcha token is likely single-use.
	CaptchaResponse string
}

// String describes the request's shape only, such as
// "emailable.VerifyEmailRequest{email: set, smtp: unset, acceptAll: true,
// providerTimeout: 8s, captchaResponse: unset}": never the address or the
// captcha response.
func (r VerifyEmailRequest) String() string {
	timeout := "unset"
	if r.ProviderTimeout != 0 {
		timeout = r.ProviderTimeout.String()
	}
	return describe("emailable.VerifyEmailRequest",
		field{"email", setOrUnset(r.Email != "")},
		field{"smtp", boolOrUnset(r.SMTP)},
		field{"acceptAll", boolOrUnset(r.AcceptAll)},
		field{"providerTimeout", timeout},
		field{"captchaResponse", setOrUnset(r.CaptchaResponse != "")},
	)
}

// GoString returns the same text as String, so that %#v prints the shape only.
func (r VerifyEmailRequest) GoString() string { return r.String() }

// Format prints the same text as String for every fmt verb (quoted for %q),
// so that no verb, %d and %x included, reaches the address or the captcha
// response by reflection. fmt cannot call it for %p on a value, nor for a
// request held in an unexported struct field: keep requests out of formatted
// output there.
func (r VerifyEmailRequest) Format(f fmt.State, verb rune) { formatAs(f, verb, r.String()) }

// VerifyEmail verifies one address: GET https://api.emailable.com/v1/verify.
//
// PAID: a verification uses one credit. The provider's pages say both that an
// unknown result is not charged and that it is refunded, and document a
// repeat within five minutes of an HTTP 249 as free, without saying where
// the window starts. It makes exactly one attempt and never retries. HTTP 249,
// the provider's "still verifying", returns (nil, a *CallError matching
// ErrPending): no result, never a zero-valued success and never repeated; see
// WaitForVerification. After a timeout, cancellation, transport failure, 5xx,
// unexpected status or unreadable answer the verification may already be
// billed (the error matches ErrOutcomeUnknown): do not resend it blindly.
//
// The address travels in the URL query, as the provider's reference
// documents, so it is visible to anything that sees request URLs, such as an
// injected transport, a TLS-intercepting proxy or an httptrace hook. Both
// private and public keys may call it; a public key is limited to the domains
// and daily cap set in the provider's dashboard.
//
// Errors are a *ValidationError (nothing sent) or a *CallError.
func (c *Client) VerifyEmail(ctx context.Context, req VerifyEmailRequest) (*Verification, error) {
	if err := c.prepare(ctx); err != nil {
		return nil, err
	}
	r, err := req.request(c)
	if err != nil {
		return nil, err
	}
	v, cerr := c.verify(ctx, r)
	if cerr != nil {
		return nil, cerr
	}
	return v, nil
}

// verify makes the one attempt of an encoded verify request and decodes its
// answer.
func (c *Client) verify(ctx context.Context, r *request) (*Verification, *CallError) {
	body, cerr := c.call(ctx, r)
	if cerr != nil {
		return nil, cerr
	}
	v, derr := decodeVerification(body, path{}, true)
	if derr != nil {
		return nil, r.decodeFailure(derr)
	}
	return &v, nil
}

// request validates r and encodes it, once, as the GET /v1/verify request:
// the query holds email and the members that are set, in url.Values order.
func (r VerifyEmailRequest) request(c *Client) (*request, error) {
	if reason := emailProblem(r.Email); reason != "" {
		return nil, invalidRequest("Email", reason)
	}
	if err := checkProviderTimeout(r.ProviderTimeout, c.timeout); err != nil {
		return nil, err
	}
	if err := checkOptionalText("CaptchaResponse", r.CaptchaResponse, maxCaptchaBytes); err != nil {
		return nil, err
	}
	q := url.Values{"email": {r.Email}}
	if r.SMTP != nil {
		q.Set("smtp", strconv.FormatBool(*r.SMTP))
	}
	if r.AcceptAll != nil {
		q.Set("accept_all", strconv.FormatBool(*r.AcceptAll))
	}
	if r.ProviderTimeout != 0 {
		q.Set("timeout", strconv.FormatInt(int64(r.ProviderTimeout/time.Second), 10))
	}
	var secrets []string
	if r.CaptchaResponse != "" {
		q.Set("captcha_response", r.CaptchaResponse)
		secrets = []string{r.CaptchaResponse}
	}
	return &request{
		op:      OpVerifyEmail,
		method:  http.MethodGet,
		path:    "verify",
		query:   q.Encode(),
		paid:    true,
		secrets: secrets,
	}, nil
}

// checkProviderTimeout validates VerifyEmailRequest.ProviderTimeout against
// the Client's own timeout.
func checkProviderTimeout(d, clientTimeout time.Duration) error {
	const field = "ProviderTimeout"
	switch {
	case d == 0:
		return nil
	case d%time.Second != 0:
		return invalidRequest(field, reasonNotWholeSeconds)
	case d < minProviderTimeout || d > maxProviderTimeout:
		return invalidRequest(field, reasonOutOfRange)
	case d+providerTimeoutHeadroom > clientTimeout:
		return invalidRequest(field, reasonExceedsClientTimeout)
	}
	return nil
}

// field is one name and printed value of a description.
type field struct{ name, value string }

// describe prints a value's description as "typ{name: value, ...}".
func describe(typ string, fields ...field) string {
	n := len(typ) + 2
	for _, f := range fields {
		n += len(f.name) + len(f.value) + 4
	}
	var b strings.Builder
	b.Grow(n)
	b.WriteString(typ)
	b.WriteByte('{')
	for i, f := range fields {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(f.name)
		b.WriteString(": ")
		b.WriteString(f.value)
	}
	b.WriteByte('}')
	return b.String()
}

// setOrUnset names whether a field is filled in.
func setOrUnset(set bool) string {
	if set {
		return "set"
	}
	return "unset"
}

// boolOrUnset prints an optional boolean: "unset", "true" or "false".
func boolOrUnset(b *bool) string {
	if b == nil {
		return "unset"
	}
	return strconv.FormatBool(*b)
}

// formatAs prints s for every fmt verb, quoted for %q.
func formatAs(f fmt.State, verb rune, s string) {
	if verb == 'q' {
		s = strconv.Quote(s)
	}
	_, _ = io.WriteString(f, s)
}
