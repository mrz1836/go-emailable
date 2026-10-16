package emailable

import (
	"encoding/json"
	"errors"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fzrMaxInput bounds every fuzz input of this file: 64 KiB.
const fzrMaxInput = 64 << 10

// fzrMaxEmails is the address limit of the CreateBatch fuzz client, low
// enough for the fuzzer to cross it.
const fzrMaxEmails = 100

// Bits of the fuzzed request options.
const (
	fzrSMTPSet = 1 << iota
	fzrSMTP
	fzrAcceptAllSet
	fzrAcceptAll
	fzrRetriesSet
	fzrRetries
	fzrCallback
)

// fzrTextOracle returns the reason optional text breaks the rules, written
// apart from checkOptionalText: at most maxBytes of valid UTF-8 without
// control characters, "" being unset.
func fzrTextOracle(s string, maxBytes int) string {
	switch {
	case s == "":
		return ""
	case len(s) > maxBytes:
		return reasonTooLong
	case !utf8.ValidString(s):
		return reasonInvalidUTF8
	case strings.IndexFunc(s, unicode.IsControl) >= 0:
		return reasonControlCharacter
	}
	return ""
}

// fzrTimeoutOracle returns the reason a provider timeout breaks the rules
// against the client timeout, written apart from checkProviderTimeout.
func fzrTimeoutOracle(d, client time.Duration) string {
	switch {
	case d == 0:
		return ""
	case d%time.Second != 0:
		return reasonNotWholeSeconds
	case d < 2*time.Second || d > 10*time.Second:
		return reasonOutOfRange
	case client-d < 5*time.Second:
		return reasonExceedsClientTimeout
	}
	return ""
}

// fzrVerifyOracle returns the field and reason VerifyEmail refuses r with on
// a client whose timeout is client, checked in the documented order, or ""
// when r is valid.
func fzrVerifyOracle(r *VerifyEmailRequest, client time.Duration) (field, reason string) {
	if reason = vldEmailOracle(r.Email); reason != "" {
		return "Email", reason
	}
	if reason = fzrTimeoutOracle(r.ProviderTimeout, client); reason != "" {
		return "ProviderTimeout", reason
	}
	if reason = fzrTextOracle(r.CaptchaResponse, 8<<10); reason != "" {
		return "CaptchaResponse", reason
	}
	return "", ""
}

// fzrOptionalBool returns a pointer to the bit value when the set bit is on.
func fzrOptionalBool(opts uint8, set, value int) *bool {
	if int(opts)&set == 0 {
		return nil
	}
	return new(int(opts)&value != 0)
}

// FuzzVerifyEmailRequest checks the GET /v1/verify encoding on any input:
// the request is either refused with the oracle's *ValidationError, or its
// query decodes back to exactly the address and options given, on its own
// and inside the URL the sender builds.
func FuzzVerifyEmailRequest(f *testing.F) {
	for _, seed := range []struct {
		email, captcha string
		timeout        int64
		opts           uint8
	}{
		{internalEmail, "", 0, 0},
		{"a+b&c=d#e?f%20g;h@example.com", "captcha+/=", int64(5 * time.Second), fzrSMTPSet | fzrAcceptAllSet | fzrAcceptAll},
		{"zoë@bücher.example.com", "é" + strictEmoji, int64(10 * time.Second), fzrSMTPSet | fzrSMTP},
		{" a@example.com", "", 0, 0},
		{"a@example.com\n", "", 0, 0},
		{"\xff", "", 0, 0},
		{"", "", 0, 0},
		{strings.Repeat("a", 255), "", 0, 0},
		{internalEmail, strings.Repeat("c", 8193), 0, 0},
		{internalEmail, "c\x00", 0, 0},
		{internalEmail, "", int64(1500 * time.Millisecond), 0},
		{internalEmail, "", int64(11 * time.Second), 0},
		{internalEmail, "", int64(26 * time.Second), 0},
		{internalEmail, "", -1, 0},
	} {
		f.Add(seed.email, seed.captcha, seed.timeout, seed.opts)
	}
	c := newInternalClient(f)
	f.Fuzz(func(t *testing.T, email, captcha string, timeout int64, opts uint8) {
		if len(email)+len(captcha) > fzrMaxInput {
			return
		}
		req := VerifyEmailRequest{
			Email: email, CaptchaResponse: captcha, ProviderTimeout: time.Duration(timeout),
			SMTP: fzrOptionalBool(opts, fzrSMTPSet, fzrSMTP), AcceptAll: fzrOptionalBool(opts, fzrAcceptAllSet, fzrAcceptAll),
		}
		r, err := req.request(c)
		if field, reason := fzrVerifyOracle(&req, c.timeout); field != "" {
			assert.Nil(t, r)
			requireValidation(t, err, ErrInvalidRequest, field, reason)
			return
		}
		require.NoError(t, err)
		fzrCheckVerifyRequest(t, &req, r)
	})
}

// fzrCheckVerifyRequest checks the encoded request r of the valid req.
func fzrCheckVerifyRequest(t *testing.T, req *VerifyEmailRequest, r *request) {
	t.Helper()
	assert.Equal(t, OpVerifyEmail, r.op)
	assert.Equal(t, "GET", r.method)
	assert.Equal(t, "verify", r.path)
	assert.True(t, r.paid)
	assert.Nil(t, r.body)
	raw := strings.IndexFunc(r.query, func(c rune) bool { return c >= utf8.RuneSelf || strings.ContainsRune(" #\t\r\n", c) })
	require.Equal(t, -1, raw, "the query is ASCII without a raw separator")
	want := url.Values{"email": {req.Email}}
	if req.SMTP != nil {
		want.Set("smtp", strconv.FormatBool(*req.SMTP))
	}
	if req.AcceptAll != nil {
		want.Set("accept_all", strconv.FormatBool(*req.AcceptAll))
	}
	if req.ProviderTimeout != 0 {
		want.Set("timeout", strconv.FormatInt(int64(req.ProviderTimeout/time.Second), 10))
	}
	var secrets []string
	if req.CaptchaResponse != "" {
		want.Set("captcha_response", req.CaptchaResponse)
		secrets = []string{req.CaptchaResponse}
	}
	got, err := url.ParseQuery(r.query)
	require.NoError(t, err)
	require.Equal(t, want, got, "the query decodes back to exactly the request")
	assert.Equal(t, secrets, r.secrets)

	u := url.URL{Scheme: "https", Host: productionHost, Path: apiRoot + r.path, RawQuery: r.query}
	parsed, err := url.Parse(u.String())
	require.NoError(t, err)
	require.Equal(t, want, parsed.Query(), "the URL the sender builds carries the same query")
	assert.Equal(t, "/v1/verify", parsed.Path)
}

// fzrSimulations are the values the CreateBatch fuzz target picks Simulate
// from.
func fzrSimulations() []Simulation {
	return []Simulation{
		"", SimulateGenericError, SimulateInsufficientCreditsError, SimulatePaymentError, SimulateImporting,
		SimulateVerifying, SimulatePaused, "bogus",
	}
}

// fzrResponseFields picks response fields with mask: bit i, for i below 21,
// adds the i-th documented field, and bits 21 and 22 add an unknown field and
// a repeat of the first one picked.
func fzrResponseFields(mask uint32) []ResponseField {
	all := vldAllResponseFields()
	var out []ResponseField
	for i, f := range all {
		if mask&(1<<i) != 0 {
			out = append(out, f)
		}
	}
	if mask&(1<<21) != 0 {
		out = append(out, "duration")
	}
	if mask&(1<<22) != 0 && len(out) > 0 {
		out = append(out, out[0])
	}
	return out
}

// fzrCreateOracle returns the field and reason CreateBatch refuses r with on
// a client limited to maxEmails, checked in the documented order, or ""
// when r is valid. The callback URL's own rules are checkCallbackURL's.
func fzrCreateOracle(r *CreateBatchRequest, maxEmails int) (field, reason string) {
	switch {
	case len(r.Emails) == 0:
		return "Emails", reasonRequired
	case len(r.Emails) > maxEmails:
		return "Emails", reasonTooMany
	}
	for i, e := range r.Emails {
		reason = vldEmailOracle(e)
		if reason == "" && strings.Contains(e, ",") {
			reason = reasonContainsComma
		}
		if reason != "" {
			return "Emails[" + strconv.Itoa(i) + "]", reason
		}
	}
	if r.CallbackURL != "" {
		if err := checkCallbackURL(r.CallbackURL); err != nil {
			if ve, ok := errors.AsType[*ValidationError](err); ok {
				return ve.Field, ve.Reason
			}
			return "CallbackURL", "not a validation error"
		}
	}
	for i, f := range r.ResponseFields {
		switch {
		case !f.Known():
			return "ResponseFields[" + strconv.Itoa(i) + "]", reasonUnknownValue
		case slices.Contains(r.ResponseFields[:i], f):
			return "ResponseFields[" + strconv.Itoa(i) + "]", reasonDuplicate
		}
	}
	switch r.Simulate {
	case "", SimulateGenericError, SimulateInsufficientCreditsError, SimulatePaymentError:
	case SimulateImporting, SimulateVerifying, SimulatePaused:
		return "Simulate", reasonWrongOperation
	default:
		return "Simulate", reasonUnknownValue
	}
	return "", ""
}

// FuzzCreateBatchRequest checks the POST /v1/batch encoding on any input.
// The addresses are the input split at newlines: the request is either
// refused with the oracle's *ValidationError, or its body is strict JSON, as
// long as validate said, that json.Unmarshal decodes back to exactly the
// addresses, in order, duplicates kept, and the other members as set.
func FuzzCreateBatchRequest(f *testing.F) {
	for _, seed := range []struct {
		emails, callback string
		mask             uint32
		opts, sim        uint8
	}{
		{internalEmail, "", 0, 0, 0},
		{"a@example.com\nb@example.com\na@example.com", vldHook, 1<<5 | 1<<18, fzrCallback | fzrRetriesSet, 1},
		{`"q\"u\\o"@example.com` + "\n<a>&b@example.com\n @example.com\nzoë@example.com", vldHook + "&x=<y>", 1<<21 - 1, fzrCallback | fzrRetriesSet | fzrRetries, 3},
		{"a,b@example.com", "", 0, 0, 0},
		{"a@example.com\n\nb@example.com", "", 0, 0, 0},
		{"", "", 0, 0, 0},
		{"a@example.com ", "", 0, 0, 0},
		{"a@example.com\tb", "", 0, 0, 0},
		{"\xff", "", 0, 0, 0},
		{strings.Repeat("a@example.com\n", fzrMaxEmails) + "a@example.com", "", 0, 0, 0},
		{internalEmail, "http://hooks.example.com/", 0, fzrCallback, 0},
		{internalEmail, "https://u@hooks.example.com/#f", 0, fzrCallback, 0},
		{internalEmail, "", 1 << 21, 0, 0},
		{internalEmail, "", 1<<3 | 1<<22, 0, 0},
		{internalEmail, "", 0, 0, 5},
		{internalEmail, "", 0, 0, 7},
	} {
		f.Add(seed.emails, seed.callback, seed.mask, seed.opts, seed.sim)
	}
	c := newInternalClient(f, WithMaxBatchEmails(fzrMaxEmails))
	sims := fzrSimulations()
	f.Fuzz(func(t *testing.T, emails, callback string, mask uint32, opts, sim uint8) {
		if len(emails)+len(callback) > fzrMaxInput {
			return
		}
		req := CreateBatchRequest{
			Emails: strings.Split(emails, "\n"), ResponseFields: fzrResponseFields(mask),
			Retries: fzrOptionalBool(opts, fzrRetriesSet, fzrRetries), Simulate: sims[int(sim)%len(sims)],
		}
		if opts&fzrCallback != 0 {
			req.CallbackURL = callback
		}
		size, verr := req.validate(c.maxBatchEmails)
		r, err := req.request(c)
		if field, reason := fzrCreateOracle(&req, c.maxBatchEmails); field != "" {
			assert.Nil(t, r)
			requireValidation(t, err, ErrInvalidRequest, field, reason)
			requireValidation(t, verr, ErrInvalidRequest, field, reason)
			return
		}
		require.NoError(t, verr)
		require.NoError(t, err)
		require.Len(t, r.body, size, "validate's size is the body's exact length")
		fzrCheckCreateBody(t, &req, r)
	})
}

// fzrCheckCreateBody checks the encoded body of the valid req.
func fzrCheckCreateBody(t *testing.T, req *CreateBatchRequest, r *request) {
	t.Helper()
	assert.Equal(t, OpCreateBatch, r.op)
	assert.True(t, r.paid)
	kind, cause := checkStrict(r.body)
	require.NoError(t, cause, "the SDK's own body passes its strict check")
	require.Empty(t, kind)

	var members map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(r.body, &members))
	want := []string{"emails"}
	if req.CallbackURL != "" {
		want = append(want, "url")
	}
	if len(req.ResponseFields) > 0 {
		want = append(want, "response_fields")
	}
	if req.Retries != nil {
		want = append(want, "retries")
	}
	if req.Simulate != "" {
		want = append(want, "simulate")
	}
	got := make([]string, 0, len(members))
	for k := range members {
		got = append(got, k)
	}
	require.ElementsMatch(t, want, got)

	var back struct {
		Emails         []string `json:"emails"`
		URL            string   `json:"url"`
		ResponseFields string   `json:"response_fields"`
		Retries        *bool    `json:"retries"`
		Simulate       string   `json:"simulate"`
	}
	require.NoError(t, json.Unmarshal(r.body, &back))
	require.Equal(t, req.Emails, back.Emails, "the addresses decode back exactly, in order, duplicates kept")
	assert.Equal(t, req.CallbackURL, back.URL)
	fields := make([]string, len(req.ResponseFields))
	for i, f := range req.ResponseFields {
		fields[i] = string(f)
	}
	assert.Equal(t, strings.Join(fields, ","), back.ResponseFields)
	assert.Equal(t, req.Retries, back.Retries)
	assert.Equal(t, string(req.Simulate), back.Simulate)
	if req.CallbackURL != "" {
		assert.ElementsMatch(t, callbackSecrets(req.CallbackURL), r.secrets)
		assert.Equal(t, req.CallbackURL, r.secrets[0], "the whole URL is a secret")
		for _, s := range r.secrets[1:] {
			assert.GreaterOrEqual(t, len(s), minKeyOverlap, "only long query values are secrets")
		}
	} else {
		assert.Nil(t, r.secrets)
	}
}

// fzrLoopback reports whether hostport, a URL host, names a loopback address
// on a valid port, decided apart from loopbackHostPort.
func fzrLoopback(tb testing.TB, hostport string) bool {
	tb.Helper()
	u, err := url.Parse("https://" + hostport + "/")
	if err != nil || u.Host != hostport {
		return false
	}
	addr, err := netip.ParseAddr(u.Hostname())
	if err != nil || !addr.IsLoopback() || addr.Zone() != "" {
		return false
	}
	if port := u.Port(); port != "" {
		n, perr := strconv.Atoi(port)
		if perr != nil || n < 1 || n > 65535 {
			return false
		}
	}
	return true
}

// FuzzTestHost checks that testHost never accepts a host that is not
// loopback: whatever URL it accepts names 127.0.0.0/8 or ::1, on a valid
// port, both as parsed and as the sender rebuilds it, and NewClient agrees.
func FuzzTestHost(f *testing.F) {
	for _, seed := range []string{
		"https://127.0.0.1", "https://127.0.0.1/", "https://127.0.0.1:8443", "https://[::1]", "https://[::1]:443/",
		"https://127.255.255.254:65535", "https://localhost", "https://localhost:443", "https://127.0.0.1.nip.io",
		"https://127.1", "https://0x7f.0.0.1", "https://0177.0.0.1", "https://2130706433", "https://[::ffff:127.0.0.1]",
		"https://[0:0:0:0:0:0:0:1]", "https://[::1%25lo0]", "http://127.0.0.1", "HTTPS://127.0.0.1",
		"https://127.0.0.1:0", "https://127.0.0.1:65536", "https://127.0.0.1:080", "https://127.0.0.1:+80",
		"https://127.0.0.1:", "https://user@127.0.0.1", "https://127.0.0.1/x", "https://127.0.0.1?x",
		"https://127.0.0.1#x", "https://128.0.0.1", "https://126.255.255.255", "https://10.0.0.1",
		"https://api.emailable.com", "https:// 127.0.0.1", "https://127.0.0.1%2f", "https://127.0.0.1\\@evil.example.com",
		"https://evil.example.com#@127.0.0.1", "https://127.0.0.1:80:80", "https://[::1]:80:80", "https://[::1]x",
		"https:127.0.0.1", "https:///127.0.0.1", "", "127.0.0.1",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > fzrMaxInput {
			return
		}
		host, ok := testHost(raw)
		c, err := NewClient(internalKey, WithTestBaseURL(raw))
		if !ok {
			assert.Empty(t, host)
			assert.Nil(t, c)
			requireValidation(t, err, ErrInvalidConfig, "TestBaseURL", reasonInvalidURL)
			return
		}
		require.NoError(t, err)
		require.NotNil(t, c)
		u, perr := url.Parse(raw)
		require.NoError(t, perr)
		require.Equal(t, u.Host, host, "the host is the URL's own")
		require.True(t, fzrLoopback(t, host), "testHost accepted %q, host %q, which is not loopback", raw, host)

		built := url.URL{Scheme: "https", Host: host, Path: apiRoot + "verify", RawQuery: "email=a%40b"}
		again, perr := url.Parse(built.String())
		require.NoError(t, perr)
		require.Equal(t, host, again.Host, "the sender's URL keeps the host")
		addr, aerr := netip.ParseAddr(again.Hostname())
		require.NoError(t, aerr)
		require.True(t, addr.IsLoopback())
		require.True(t, addr.Is4() || addr == netip.IPv6Loopback(), "only IPv4 loopback or ::1")
	})
}
