package emailable

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-emailable/internal/testutil"
)

// vldHook is the callback URL the validation tests repeat.
const vldHook = "https://hooks.example.com/emailable?token=vldmarker"

// vldEmailOracle returns the reason an address breaks the documented rules,
// written apart from emailProblem: 1 to 254 bytes of valid UTF-8 without
// control characters or leading or trailing whitespace.
func vldEmailOracle(s string) string {
	switch {
	case s == "":
		return reasonRequired
	case len(s) > maxEmailBytes:
		return reasonTooLong
	case !utf8.ValidString(s):
		return reasonInvalidUTF8
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return reasonControlCharacter
		}
	}
	runes := []rune(s)
	if unicode.IsSpace(runes[0]) || unicode.IsSpace(runes[len(runes)-1]) {
		return reasonSurroundingSpace
	}
	return ""
}

func TestValidateEmailProblem(t *testing.T) {
	exact := strings.Repeat("a", maxEmailBytes-len("@example.com")) + "@example.com"
	tests := []struct {
		name  string
		email string
		want  string
	}{
		{name: "address", email: internalEmail},
		{name: "no at sign is the provider's call", email: "not-an-address"},
		{name: "one byte", email: "x"},
		{name: "quotes and backslashes", email: `"a\"b"@example.com`},
		{name: "a comma is allowed here", email: "a,b@example.com"},
		{name: "inner space", email: "jane doe@example.com"},
		{name: "inner no-break space", email: "jane\u00a0doe@example.com"},
		{name: "inner ideographic space", email: "jane\u3000doe@example.com"},
		{name: "inner line separator", email: "jane\u2028doe@example.com"},
		{name: "non-ASCII", email: "zoë@bücher.example.com"},
		{name: "emoji", email: strictEmoji + "@example.com"},
		{name: "leading byte order mark is not whitespace", email: "\ufeffjane@example.com"},
		{name: "leading zero-width space is not whitespace", email: "\u200bjane@example.com"},
		{name: "Mongolian vowel separator is not whitespace", email: "\u180ejane@example.com"},
		{name: "254 bytes", email: exact},
		{name: "empty", email: "", want: reasonRequired},
		{name: "255 bytes", email: exact + "m", want: reasonTooLong},
		{name: "too long before invalid UTF-8", email: strings.Repeat("\xff", maxEmailBytes+1), want: reasonTooLong},
		{name: "invalid UTF-8", email: "\xff@example.com", want: reasonInvalidUTF8},
		{name: "truncated rune", email: "a@example.com\xe2\x82", want: reasonInvalidUTF8},
		{name: "encoded surrogate", email: "a\xed\xa0\x80@example.com", want: reasonInvalidUTF8},
		{name: "invalid UTF-8 before a control", email: "\x00\xff", want: reasonInvalidUTF8},
		{name: "NUL", email: "a\x00b@example.com", want: reasonControlCharacter},
		{name: "inner tab", email: "a\tb@example.com", want: reasonControlCharacter},
		{name: "trailing newline", email: "a@example.com\n", want: reasonControlCharacter},
		{name: "carriage return", email: "a\r@example.com", want: reasonControlCharacter},
		{name: "DEL", email: "a\x7f@example.com", want: reasonControlCharacter},
		{name: "C1 control", email: "a\u0085@example.com", want: reasonControlCharacter},
		{name: "leading next line is a control first", email: "\u0085a@example.com", want: reasonControlCharacter},
		{name: "leading space", email: " " + internalEmail, want: reasonSurroundingSpace},
		{name: "trailing space", email: internalEmail + " ", want: reasonSurroundingSpace},
		{name: "only a space", email: " ", want: reasonSurroundingSpace},
		{name: "leading no-break space", email: "\u00a0" + internalEmail, want: reasonSurroundingSpace},
		{name: "trailing ideographic space", email: internalEmail + "\u3000", want: reasonSurroundingSpace},
		{name: "leading ogham space", email: "\u1680" + internalEmail, want: reasonSurroundingSpace},
		{name: "trailing en quad", email: internalEmail + "\u2000", want: reasonSurroundingSpace},
		{name: "trailing hair space", email: internalEmail + "\u200a", want: reasonSurroundingSpace},
		{name: "leading line separator", email: "\u2028" + internalEmail, want: reasonSurroundingSpace},
		{name: "trailing paragraph separator", email: internalEmail + "\u2029", want: reasonSurroundingSpace},
		{name: "trailing narrow no-break space", email: internalEmail + "\u202f", want: reasonSurroundingSpace},
		{name: "leading medium mathematical space", email: "\u205f" + internalEmail, want: reasonSurroundingSpace},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, emailProblem(tt.email))
			assert.Equal(t, tt.want, vldEmailOracle(tt.email), "the oracle agrees")
		})
	}
}

// TestValidateEmailWhitespaceClasses checks every rune unicode.IsSpace
// reports, at either end and inside an address: a control character is
// refused anywhere, any other whitespace only at an end.
func TestValidateEmailWhitespaceClasses(t *testing.T) {
	n := 0
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if !unicode.IsSpace(r) {
			continue
		}
		n++
		edge, inner := reasonSurroundingSpace, ""
		if unicode.IsControl(r) {
			edge, inner = reasonControlCharacter, reasonControlCharacter
		}
		ws := string(r)
		assert.Equal(t, edge, emailProblem(ws+internalEmail), "leading %U", r)
		assert.Equal(t, edge, emailProblem(internalEmail+ws), "trailing %U", r)
		assert.Equal(t, edge, emailProblem(ws), "only %U", r)
		assert.Equal(t, inner, emailProblem("jane"+ws+"doe@example.com"), "inner %U", r)
	}
	assert.Equal(t, 25, n, "Unicode's White_Space set, as unicode.IsSpace reports it")
}

func TestValidateTextProblem(t *testing.T) {
	tests := []struct {
		s    string
		want string
	}{
		{"", ""},
		{"captcha-token_0123", ""},
		{"inner space and \u00a0", ""},
		{" surrounding whitespace is fine here ", ""},
		{"\xff", reasonInvalidUTF8},
		{"\x00\xff", reasonInvalidUTF8},
		{"a\xed\xa0\x80", reasonInvalidUTF8},
		{"a\x00", reasonControlCharacter},
		{"a\nb", reasonControlCharacter},
		{"a\x1f", reasonControlCharacter},
		{"a\x7f", reasonControlCharacter},
		{"a\u0080", reasonControlCharacter},
		{"a\u009f", reasonControlCharacter},
		{"a\u00a0", ""},
	}
	for _, tt := range tests {
		t.Run(strconv.Quote(tt.s), func(t *testing.T) {
			assert.Equal(t, tt.want, textProblem(tt.s))
		})
	}
}

func TestValidateCheckOptionalText(t *testing.T) {
	const field = "CaptchaResponse"
	require.NoError(t, checkOptionalText(field, "", 0), `"" is never sent, so it is never checked`)
	require.NoError(t, checkOptionalText(field, "abc", 3))
	require.NoError(t, checkOptionalText(field, "é", 2))
	requireValidation(t, checkOptionalText(field, "abcd", 3), ErrInvalidRequest, field, reasonTooLong)
	requireValidation(t, checkOptionalText(field, "é", 1), ErrInvalidRequest, field, reasonTooLong)
	requireValidation(t, checkOptionalText(field, "\xff\xff\xff\xff", 3), ErrInvalidRequest, field, reasonTooLong)
	requireValidation(t, checkOptionalText(field, "\xff", 3), ErrInvalidRequest, field, reasonInvalidUTF8)
	requireValidation(t, checkOptionalText(field, "a\x00", 3), ErrInvalidRequest, field, reasonControlCharacter)
	requireValidation(t, checkOptionalText("Other", "a\n", 3), ErrInvalidRequest, "Other", reasonControlCharacter)
}

func TestValidateCheckBatchID(t *testing.T) {
	const field = "BatchID"
	visible := dcdVisibleASCII()
	for _, id := range []string{"b1", "5a0e9b5d6f0e6d0012bc", visible, strings.Repeat("q", maxBatchIDBytes), "!", "~"} {
		require.NoError(t, checkBatchID(field, id), id)
	}
	requireValidation(t, checkBatchID(field, ""), ErrInvalidRequest, field, reasonRequired)
	for _, id := range []string{
		strings.Repeat("q", maxBatchIDBytes+1), "b 1", " b1", "b1\n", "b1\x7f", "bé1", "\xff", "b1\x00",
	} {
		requireValidation(t, checkBatchID(field, id), ErrInvalidRequest, field, reasonInvalidFormat)
	}
	requireValidation(t, checkBatchID("Other", "b 1"), ErrInvalidRequest, "Other", reasonInvalidFormat)
}

func TestValidateCheckProviderTimeout(t *testing.T) {
	const field = "ProviderTimeout"
	whole := time.Duration(math.MaxInt64/int64(time.Second)) * time.Second
	tests := []struct {
		name   string
		d      time.Duration
		client time.Duration
		want   string
	}{
		{name: "not sent", d: 0, client: DefaultTimeout},
		{name: "not sent with any client timeout", d: 0, client: time.Nanosecond},
		{name: "lowest", d: 2 * time.Second, client: DefaultTimeout},
		{name: "default", d: 5 * time.Second, client: DefaultTimeout},
		{name: "highest", d: 10 * time.Second, client: DefaultTimeout},
		{name: "one second", d: time.Second, client: DefaultTimeout, want: reasonOutOfRange},
		{name: "eleven seconds", d: 11 * time.Second, client: DefaultTimeout, want: reasonOutOfRange},
		{name: "negative", d: -time.Second, client: DefaultTimeout, want: reasonOutOfRange},
		{name: "negative two seconds", d: -2 * time.Second, client: DefaultTimeout, want: reasonOutOfRange},
		{name: "largest whole seconds", d: whole, client: DefaultTimeout, want: reasonOutOfRange},
		{name: "fraction", d: 1500 * time.Millisecond, client: DefaultTimeout, want: reasonNotWholeSeconds},
		{name: "one nanosecond", d: time.Nanosecond, client: DefaultTimeout, want: reasonNotWholeSeconds},
		{name: "negative nanosecond", d: -time.Nanosecond, client: DefaultTimeout, want: reasonNotWholeSeconds},
		{name: "a fraction past the range", d: 10*time.Second + time.Nanosecond, client: DefaultTimeout, want: reasonNotWholeSeconds},
		{name: "max duration", d: math.MaxInt64, client: DefaultTimeout, want: reasonNotWholeSeconds},
		{name: "min duration", d: math.MinInt64, client: DefaultTimeout, want: reasonNotWholeSeconds},
		{name: "exactly the headroom", d: 10 * time.Second, client: 15 * time.Second},
		{name: "one nanosecond short of the headroom", d: 10 * time.Second, client: 15*time.Second - time.Nanosecond, want: reasonExceedsClientTimeout},
		{name: "a second short of the headroom", d: 10 * time.Second, client: 14 * time.Second, want: reasonExceedsClientTimeout},
		{name: "shorter timeout within headroom", d: 9 * time.Second, client: 14 * time.Second},
		{name: "lowest against a short client", d: 2 * time.Second, client: 7 * time.Second},
		{name: "lowest against a shorter client", d: 2 * time.Second, client: 6 * time.Second, want: reasonExceedsClientTimeout},
		{name: "range before headroom", d: 11 * time.Second, client: time.Second, want: reasonOutOfRange},
		{name: "whole seconds before range", d: 1500 * time.Millisecond, client: time.Second, want: reasonNotWholeSeconds},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkProviderTimeout(tt.d, tt.client)
			if tt.want == "" {
				require.NoError(t, err)
				return
			}
			requireValidation(t, err, ErrInvalidRequest, field, tt.want)
		})
	}
}

// vldCallbackCases returns callback URLs and the reason each one is refused
// with, "" when it is accepted.
func vldCallbackCases() []struct{ name, url, want string } {
	host := "https://hooks.example.com/"
	exact := host + strings.Repeat("p", maxCallbackURLBytes-len(host))
	return []struct{ name, url, want string }{
		{"https with a query token", vldHook, ""},
		{"host only", "https://hooks.example.com", ""},
		{"port", "https://hooks.example.com:8443/x", ""},
		{"IPv6 literal", "https://[2001:db8::1]/x", ""},
		{"loopback is allowed", "https://127.0.0.1/x", ""},
		{"upper-case scheme", "HTTPS://hooks.example.com/x", ""},
		{"mixed-case scheme", "HtTpS://hooks.example.com/x", ""},
		{"escaped query", "https://hooks.example.com/x?token=a&b=c%20d&e=%23", ""},
		{"non-ASCII path", "https://hooks.example.com/café", ""},
		{"exactly 2048 bytes", exact, ""},
		{"http", "http://hooks.example.com/x", reasonUnsupportedScheme},
		{"ftp", "ftp://hooks.example.com/x", reasonUnsupportedScheme},
		{"wss", "wss://hooks.example.com/x", reasonUnsupportedScheme},
		{"scheme-relative has no scheme", "//hooks.example.com/x", reasonUnsupportedScheme},
		{"userinfo", "https://user@hooks.example.com/", reasonInvalidURL},
		{"user and password", "https://user:pass@hooks.example.com/", reasonInvalidURL},
		{"password only", "https://:pass@hooks.example.com/", reasonInvalidURL},
		{"empty userinfo", "https://@hooks.example.com/", reasonInvalidURL},
		{"userinfo before the scheme check", "http://user@hooks.example.com/", reasonInvalidURL},
		{"fragment", "https://hooks.example.com/x#frag", reasonInvalidURL},
		{"bare fragment mark", "https://hooks.example.com/x#", reasonInvalidURL},
		{"fragment mark in the query", "https://hooks.example.com/x?a=#b", reasonInvalidURL},
		{"absolute path only", "/relative/path", reasonInvalidURL},
		{"relative path", "relative/path", reasonInvalidURL},
		{"host without a scheme", "hooks.example.com/x", reasonInvalidURL},
		{"no host", "https://", reasonInvalidURL},
		{"empty host with a path", "https:///x", reasonInvalidURL},
		{"port without a host", "https://:443", reasonInvalidURL},
		{"port without a host, with a path", "https://:443/x", reasonInvalidURL},
		{"opaque", "https:hooks.example.com", reasonInvalidURL},
		{"mailto", "mailto:ops@example.com", reasonInvalidURL},
		{"javascript", "javascript:alert(1)", reasonInvalidURL},
		{"http opaque before the scheme check", "http:opaque", reasonInvalidURL},
		{"empty", "", reasonInvalidURL},
		{"space in the host", "https://hooks example.com/", reasonInvalidURL},
		{"space in the path", "https://hooks.example.com/a b", reasonInvalidURL},
		{"leading space", " " + vldHook, reasonInvalidURL},
		{"trailing space", vldHook + " ", reasonInvalidURL},
		{"no-break space", "https://hooks.example.com/\u00a0", reasonInvalidURL},
		{"ideographic space", "https://hooks.example.com/\u3000", reasonInvalidURL},
		{"invalid escape", "https://hooks.example.com/%zz", reasonInvalidURL},
		{"backslash in the host", "https://hooks.example.com\\x", reasonInvalidURL},
		{"non-numeric port", "https://hooks.example.com:abc/", reasonInvalidURL},
		{"unclosed IPv6 literal", "https://[::1", reasonInvalidURL},
		{"tab", "https://hooks.example.com/\t", reasonControlCharacter},
		{"newline", "https://hooks.example.com/x\n", reasonControlCharacter},
		{"carriage return", "https://hooks.example.com/\rx", reasonControlCharacter},
		{"NUL", "https://hooks.example.com/\x00", reasonControlCharacter},
		{"DEL", "https://hooks.example.com/\x7f", reasonControlCharacter},
		{"C1 control", "https://hooks.example.com/\u0085", reasonControlCharacter},
		{"invalid UTF-8", "https://hooks.example.com/\xff", reasonInvalidUTF8},
		{"2049 bytes", exact + "p", reasonTooLong},
		{"too long before invalid UTF-8", exact + "\xff", reasonTooLong},
	}
}

func TestValidateCheckCallbackURL(t *testing.T) {
	for _, tt := range vldCallbackCases() {
		t.Run(tt.name, func(t *testing.T) {
			err := checkCallbackURL(tt.url)
			if tt.want == "" {
				require.NoError(t, err)
				return
			}
			requireValidation(t, err, ErrInvalidRequest, "CallbackURL", tt.want)
		})
	}
}

// vldAllResponseFields returns the 21 documented response fields.
func vldAllResponseFields() []ResponseField {
	return []ResponseField{
		ResponseFieldAcceptAll, ResponseFieldBirthYear, ResponseFieldDidYouMean, ResponseFieldDisposable,
		ResponseFieldDomain, ResponseFieldEmail, ResponseFieldFirstName, ResponseFieldFree, ResponseFieldFullName,
		ResponseFieldGender, ResponseFieldLastName, ResponseFieldMailboxFull, ResponseFieldMXRecord,
		ResponseFieldNoReply, ResponseFieldReason, ResponseFieldRole, ResponseFieldScore, ResponseFieldSMTPProvider,
		ResponseFieldState, ResponseFieldTag, ResponseFieldUser,
	}
}

func TestValidateResponseFieldKnown(t *testing.T) {
	all := vldAllResponseFields()
	seen := map[ResponseField]bool{}
	for _, f := range all {
		assert.True(t, f.Known(), f)
		assert.False(t, seen[f], "%s is listed once", f)
		seen[f] = true
	}
	assert.Len(t, seen, 21, "exactly 21 documented response fields")

	names := verificationNames()
	for _, name := range names {
		assert.Equal(t, name != "duration", ResponseField(name).Known(), "every Verification member but duration: %s", name)
		if name != "duration" {
			assert.True(t, seen[ResponseField(name)], "%s has a constant", name)
		}
	}
	for _, f := range []ResponseField{
		"", "duration", "Email", "EMAIL", "email ", " email", " state", "email,state", "emails", "mx", "id", "e-mail",
		"first-name",
	} {
		assert.False(t, f.Known(), "%q", f)
	}
}

func TestValidateCheckResponseFields(t *testing.T) {
	all := vldAllResponseFields()
	joined := make([]string, len(all))
	for i, f := range all {
		joined[i] = string(f)
	}
	tests := []struct {
		name   string
		fields []ResponseField
		want   int
		field  string
		reason string
	}{
		{name: "nil", fields: nil, want: 0},
		{name: "empty", fields: []ResponseField{}, want: 0},
		{name: "one", fields: []ResponseField{ResponseFieldEmail}, want: len("email")},
		{name: "two", fields: []ResponseField{ResponseFieldEmail, ResponseFieldState}, want: len("email,state")},
		{name: "every field", fields: all, want: len(strings.Join(joined, ","))},
		{name: "unknown", fields: []ResponseField{"duration"}, field: "ResponseFields[0]", reason: reasonUnknownValue},
		{name: "empty value", fields: []ResponseField{ResponseFieldEmail, ""}, field: "ResponseFields[1]", reason: reasonUnknownValue},
		{name: "case variant", fields: []ResponseField{ResponseFieldEmail, "Email"}, field: "ResponseFields[1]", reason: reasonUnknownValue},
		{name: "duplicate", fields: []ResponseField{ResponseFieldEmail, ResponseFieldState, ResponseFieldEmail}, field: "ResponseFields[2]", reason: reasonDuplicate},
		{name: "immediate duplicate", fields: []ResponseField{ResponseFieldScore, ResponseFieldScore}, field: "ResponseFields[1]", reason: reasonDuplicate},
		{name: "first failure wins", fields: []ResponseField{ResponseFieldTag, "bogus", ResponseFieldTag}, field: "ResponseFields[1]", reason: reasonUnknownValue},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n, err := checkResponseFields(tt.fields)
			if tt.reason != "" {
				requireValidation(t, err, ErrInvalidRequest, tt.field, tt.reason)
				assert.Zero(t, n)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, n)
		})
	}
}

func TestValidateSimulation(t *testing.T) {
	tests := []struct {
		sim                   Simulation
		forCreate, forGet, ok bool
	}{
		{SimulateGenericError, true, true, true},
		{SimulateInsufficientCreditsError, true, false, true},
		{SimulatePaymentError, true, false, true},
		{SimulateImporting, false, true, true},
		{SimulateVerifying, false, true, true},
		{SimulatePaused, false, true, true},
		{"", false, false, false},
		{"Generic_Error", false, false, false},
		{"generic-error", false, false, false},
		{"completed", false, false, false},
		{"generic_error ", false, false, false},
		{"generic", false, false, false},
		{"Paused", false, false, false},
		{" paused", false, false, false},
		{"explode", false, false, false},
	}
	for _, tt := range tests {
		t.Run(strconv.Quote(string(tt.sim)), func(t *testing.T) {
			assert.Equal(t, tt.forCreate, tt.sim.forCreate(), "forCreate")
			assert.Equal(t, tt.forGet, tt.sim.forGet(), "forGet")
			assert.Equal(t, tt.ok, tt.sim.Known(), "Known")
		})
	}
}

func TestValidateEncodedStringSize(t *testing.T) {
	for _, s := range []string{
		"", "a", internalEmail, `"`, `\`, `"\"`, `a"b\c`, "<>&", "<script>&amp;</script>", "\u2028", "\u2029",
		"a\u2028b\u2029c", strictEmoji, "é€" + strictEmoji, strings.Repeat(`"`, 100), strings.Repeat("\u2028", 50),
		"\x7f", "\u0085", "\u00a0", "\ufeff", "\U0010FFFF", "'single'", "/slash/", "%25", vldHook,
	} {
		t.Run(strconv.QuoteToASCII(s), func(t *testing.T) {
			assert.Equal(t, testutil.JSONStringLen(s), encodedStringSize(s))
		})
	}
}

// FuzzEncodedStringSize checks encodedStringSize against json.Encoder without
// HTML escaping, for every string validation lets through: valid UTF-8
// without control characters.
func FuzzEncodedStringSize(f *testing.F) {
	for _, s := range []string{
		"", "a", internalEmail, `"`, `\`, "<>&", "\u2028\u2029", strictEmoji, `a"b\c<d>e&f` + "\u2028" + strictEmoji,
		"é", "\x7f", "\xff", "a\x00b", "\u0085",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if textProblem(s) != "" {
			return // outside the contract: validation refuses it first
		}
		require.Equal(t, testutil.JSONStringLen(s), encodedStringSize(s), "%q", s)
	})
}

// vldBody returns the body request encodes for r, failing tb on any error.
func vldBody(tb testing.TB, c *Client, r CreateBatchRequest) []byte {
	tb.Helper()
	req, err := r.request(c)
	require.NoError(tb, err)
	require.NotNil(tb, req)
	return req.body
}

// TestValidateCreateBatchSize proves that validate's size is the exact length
// of the encoded body, for requests that use every member and every kind of
// escape.
func TestValidateCreateBatchSize(t *testing.T) {
	c := newInternalClient(t)
	tests := []struct {
		name string
		req  CreateBatchRequest
		body string
	}{
		{name: "one address", req: CreateBatchRequest{Emails: []string{internalEmail}}, body: `{"emails":["` + internalEmail + `"]}`},
		{
			name: "escapes and HTML characters", req: CreateBatchRequest{Emails: []string{`"a\b"@example.com`, "<a>&@example.com", "l\u2028s\u2029@example.com"}},
			body: `{"emails":["\"a\\b\"@example.com","<a>&@example.com","l\u2028s\u2029@example.com"]}`,
		},
		{name: "duplicates kept in order", req: CreateBatchRequest{Emails: []string{"b@example.com", "a@example.com", "b@example.com"}}, body: `{"emails":["b@example.com","a@example.com","b@example.com"]}`},
		{name: "callback", req: CreateBatchRequest{Emails: []string{"a@example.com"}, CallbackURL: vldHook + "&x=<y>"}, body: `{"emails":["a@example.com"],"url":"` + vldHook + `&x=<y>"}`},
		{
			name: "response fields", req: CreateBatchRequest{Emails: []string{"a@example.com"}, ResponseFields: []ResponseField{ResponseFieldEmail, ResponseFieldState, ResponseFieldMXRecord}},
			body: `{"emails":["a@example.com"],"response_fields":"email,state,mx_record"}`,
		},
		{name: "retries false", req: CreateBatchRequest{Emails: []string{"a@example.com"}, Retries: new(false)}, body: `{"emails":["a@example.com"],"retries":false}`},
		{name: "retries true", req: CreateBatchRequest{Emails: []string{"a@example.com"}, Retries: new(true)}, body: `{"emails":["a@example.com"],"retries":true}`},
		{name: "simulate", req: CreateBatchRequest{Emails: []string{"a@example.com"}, Simulate: SimulatePaymentError}, body: `{"emails":["a@example.com"],"simulate":"payment_error"}`},
		{
			name: "every member", req: CreateBatchRequest{
				Emails: []string{"a@example.com", "zoë@example.com"}, CallbackURL: vldHook, ResponseFields: vldAllResponseFields(),
				Retries: new(false), Simulate: SimulateGenericError,
			},
			body: `{"emails":["a@example.com","zo` + "ë" + `@example.com"],"url":"` + vldHook + `","response_fields":"` +
				"accept_all,birth_year,did_you_mean,disposable,domain,email,first_name,free,full_name,gender,last_name," +
				"mailbox_full,mx_record,no_reply,reason,role,score,smtp_provider,state,tag,user" +
				`","retries":false,"simulate":"generic_error"}`,
		},
		{name: "empty response fields are not sent", req: CreateBatchRequest{Emails: []string{"a@example.com"}, ResponseFields: []ResponseField{}}, body: `{"emails":["a@example.com"]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			size, err := tt.req.validate(c.maxBatchEmails)
			require.NoError(t, err)
			body := vldBody(t, c, tt.req)
			assert.Equal(t, []byte(tt.body), body, "the encoded body")
			assert.Len(t, body, size, "validate's size is the body's exact length")
			require.True(t, json.Valid(body))
			var back createBatchBody
			require.NoError(t, json.Unmarshal(body, &back))
			assert.Equal(t, tt.req.Emails, back.Emails, "the addresses survive as sent")
		})
	}
}

func TestValidateCreateBatchErrors(t *testing.T) {
	c := newInternalClient(t, WithMaxBatchEmails(3))
	tests := []struct {
		name          string
		req           CreateBatchRequest
		field, reason string
	}{
		{name: "no addresses", req: CreateBatchRequest{}, field: "Emails", reason: reasonRequired},
		{name: "empty list", req: CreateBatchRequest{Emails: []string{}}, field: "Emails", reason: reasonRequired},
		{name: "over the client's limit", req: CreateBatchRequest{Emails: []string{"a@example.com", "b@example.com", "c@example.com", "d@example.com"}}, field: "Emails", reason: reasonTooMany},
		{name: "empty address", req: CreateBatchRequest{Emails: []string{"a@example.com", ""}}, field: "Emails[1]", reason: reasonRequired},
		{name: "comma", req: CreateBatchRequest{Emails: []string{"a@example.com", "b@example.com", "c,d@example.com"}}, field: "Emails[2]", reason: reasonContainsComma},
		{name: "a lone comma", req: CreateBatchRequest{Emails: []string{","}}, field: "Emails[0]", reason: reasonContainsComma},
		{name: "address rules before the comma", req: CreateBatchRequest{Emails: []string{"c,d@example.com "}}, field: "Emails[0]", reason: reasonSurroundingSpace},
		{name: "control character", req: CreateBatchRequest{Emails: []string{"a\nb@example.com"}}, field: "Emails[0]", reason: reasonControlCharacter},
		{name: "first bad address wins", req: CreateBatchRequest{Emails: []string{"a@example.com", "\xff", ""}}, field: "Emails[1]", reason: reasonInvalidUTF8},
		{name: "count before addresses", req: CreateBatchRequest{Emails: []string{"", "", "", ""}}, field: "Emails", reason: reasonTooMany},
		{name: "callback", req: CreateBatchRequest{Emails: []string{"a@example.com"}, CallbackURL: "http://hooks.example.com/"}, field: "CallbackURL", reason: reasonUnsupportedScheme},
		{name: "response field", req: CreateBatchRequest{Emails: []string{"a@example.com"}, ResponseFields: []ResponseField{"duration"}}, field: "ResponseFields[0]", reason: reasonUnknownValue},
		{name: "unknown simulation", req: CreateBatchRequest{Emails: []string{"a@example.com"}, Simulate: "bogus"}, field: "Simulate", reason: reasonUnknownValue},
		{name: "get-only simulation", req: CreateBatchRequest{Emails: []string{"a@example.com"}, Simulate: SimulateVerifying}, field: "Simulate", reason: reasonWrongOperation},
		{
			name: "addresses before the callback", req: CreateBatchRequest{Emails: []string{""}, CallbackURL: "x", ResponseFields: []ResponseField{"x"}, Simulate: "x"},
			field: "Emails[0]", reason: reasonRequired,
		},
		{
			name: "callback before response fields", req: CreateBatchRequest{Emails: []string{"a@example.com"}, CallbackURL: "x", ResponseFields: []ResponseField{"x"}, Simulate: "x"},
			field: "CallbackURL", reason: reasonInvalidURL,
		},
		{
			name: "response fields before the simulation", req: CreateBatchRequest{Emails: []string{"a@example.com"}, ResponseFields: []ResponseField{"x"}, Simulate: "x"},
			field: "ResponseFields[0]", reason: reasonUnknownValue,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			size, err := tt.req.validate(c.maxBatchEmails)
			requireValidation(t, err, ErrInvalidRequest, tt.field, tt.reason)
			assert.Zero(t, size)
			req, err := tt.req.request(c)
			requireValidation(t, err, ErrInvalidRequest, tt.field, tt.reason)
			assert.Nil(t, req)
		})
	}
}

func TestValidateGetBatchRequest(t *testing.T) {
	tests := []struct {
		name          string
		req           GetBatchRequest
		query         string
		field, reason string
	}{
		{name: "id only", req: GetBatchRequest{BatchID: dcdBatchID}, query: "id=b1"},
		{name: "escaped id", req: GetBatchRequest{BatchID: "a&b=c+d%"}, query: "id=a%26b%3Dc%2Bd%25"},
		{name: "partial", req: GetBatchRequest{BatchID: dcdBatchID, Partial: true}, query: "id=b1&partial=true"},
		{name: "simulation", req: GetBatchRequest{BatchID: dcdBatchID, Simulate: SimulatePaused}, query: "id=b1&simulate=paused"},
		{
			name: "every member", req: GetBatchRequest{BatchID: dcdBatchID, Partial: true, Simulate: SimulateGenericError},
			query: "id=b1&partial=true&simulate=generic_error",
		},
		{name: "no id", req: GetBatchRequest{}, field: "BatchID", reason: reasonRequired},
		{name: "bad id", req: GetBatchRequest{BatchID: "b 1"}, field: "BatchID", reason: reasonInvalidFormat},
		{name: "unknown simulation", req: GetBatchRequest{BatchID: dcdBatchID, Simulate: "bogus"}, field: "Simulate", reason: reasonUnknownValue},
		{name: "create-only simulation", req: GetBatchRequest{BatchID: dcdBatchID, Simulate: SimulatePaymentError}, field: "Simulate", reason: reasonWrongOperation},
		{name: "id before simulation", req: GetBatchRequest{Simulate: "bogus"}, field: "BatchID", reason: reasonRequired},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := tt.req.request()
			if tt.reason != "" {
				requireValidation(t, err, ErrInvalidRequest, tt.field, tt.reason)
				assert.Nil(t, r)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.query, r.query)
			assert.Equal(t, OpGetBatch, r.op)
			assert.Equal(t, "GET", r.method)
			assert.Equal(t, "batch", r.path)
			assert.False(t, r.paid, "reading a batch is free")
			assert.Nil(t, r.secrets)
		})
	}
}
