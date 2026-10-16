package emailable_test

import (
	"context"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-emailable"
	"github.com/mrz1836/go-emailable/internal/testutil"
)

// vfyEmailQuery is the encoded email of the minimal request.
const vfyEmailQuery = "email=jane.doe%40example.com"

// vfyAnswer returns a verify answer with the required members, followed by
// members when it is not empty.
func vfyAnswer(members string) string {
	head := `{"email":"` + testEmail + `","state":"deliverable"`
	if members == "" {
		return head + `}`
	}
	return head + `,` + members + `}`
}

// vfyMember returns a verify answer with the required members and one more,
// name, holding the raw JSON value.
func vfyMember(name, value string) string {
	return vfyAnswer(`"` + name + `":` + value)
}

// vfyMinimal returns the Verification of vfyAnswer("").
func vfyMinimal() *emailable.Verification {
	return &emailable.Verification{Email: testEmail, State: emailable.StateDeliverable}
}

// vfyDecoded makes one VerifyEmail call answered with body and returns the
// result, failing t when the call fails.
func vfyDecoded(t *testing.T, body string) *emailable.Verification {
	t.Helper()
	res, err := callOnce(t, pathVerify, body, callVerify)
	require.NoError(t, err)
	require.NotNil(t, res)
	return res
}

// vfyAssertDecodeFailure asserts that body, as the answer to a VerifyEmail,
// fails as kind at field, with the fixed cause.
func vfyAssertDecodeFailure(t *testing.T, body string, kind emailable.FailureKind, field, cause string) {
	t.Helper()
	_, err := callOnce(t, pathVerify, body, callVerify)
	_ = assertCallError(t, err, wantCall{
		Kind: kind, Op: emailable.OpVerifyEmail, Status: http.StatusOK, OutcomeUnknown: true, Attempts: 1, Field: field,
	})
	assert.Equal(t, cause, causeText(t, err))
}

func TestVerifyEmailWireQuery(t *testing.T) {
	tests := []struct {
		name string
		req  emailable.VerifyEmailRequest
		want string // the raw query
	}{
		{name: "minimal", req: verifyRequest(), want: vfyEmailQuery},
		{name: "smtp false", req: emailable.VerifyEmailRequest{Email: testEmail, SMTP: new(false)}, want: vfyEmailQuery + "&smtp=false"},
		{name: "smtp true", req: emailable.VerifyEmailRequest{Email: testEmail, SMTP: new(true)}, want: vfyEmailQuery + "&smtp=true"},
		{
			name: "accept_all true", req: emailable.VerifyEmailRequest{Email: testEmail, AcceptAll: new(true)},
			want: "accept_all=true&" + vfyEmailQuery,
		},
		{
			name: "accept_all false", req: emailable.VerifyEmailRequest{Email: testEmail, AcceptAll: new(false)},
			want: "accept_all=false&" + vfyEmailQuery,
		},
		{
			name: "shortest provider timeout", req: emailable.VerifyEmailRequest{Email: testEmail, ProviderTimeout: 2 * time.Second},
			want: vfyEmailQuery + "&timeout=2",
		},
		{
			name: "longest provider timeout", req: emailable.VerifyEmailRequest{Email: testEmail, ProviderTimeout: 10 * time.Second},
			want: vfyEmailQuery + "&timeout=10",
		},
		{
			name: "captcha response", req: emailable.VerifyEmailRequest{Email: testEmail, CaptchaResponse: testCaptcha},
			want: "captcha_response=" + testCaptcha + "&" + vfyEmailQuery,
		},
		{
			name: "captcha response escaped", req: emailable.VerifyEmailRequest{Email: testEmail, CaptchaResponse: "a b+c/=&d"},
			want: "captcha_response=a+b%2Bc%2F%3D%26d&" + vfyEmailQuery,
		},
		{
			name: "every member, keys sorted",
			req: emailable.VerifyEmailRequest{
				Email: testEmail, SMTP: new(false), AcceptAll: new(true), ProviderTimeout: 8 * time.Second,
				CaptchaResponse: testCaptcha,
			},
			want: "accept_all=true&captcha_response=" + testCaptcha + "&" + vfyEmailQuery + "&smtp=false&timeout=8",
		},
		{name: "inner space", req: emailable.VerifyEmailRequest{Email: "jane doe@example.com"}, want: "email=jane+doe%40example.com"},
		{name: "no at sign", req: emailable.VerifyEmailRequest{Email: "janedoe"}, want: "email=janedoe"},
		{name: "two at signs", req: emailable.VerifyEmailRequest{Email: "a@b@example.com"}, want: "email=a%40b%40example.com"},
		{name: "plus tag", req: emailable.VerifyEmailRequest{Email: "jane+tag@example.com"}, want: "email=jane%2Btag%40example.com"},
		{name: "non-ASCII", req: emailable.VerifyEmailRequest{Email: "zoë@example.com"}, want: "email=zo%C3%AB%40example.com"},
		{
			name: "query metacharacters", req: emailable.VerifyEmailRequest{Email: "a&b=c?d#e;f@example.com"},
			want: "email=a%26b%3Dc%3Fd%23e%3Bf%40example.com",
		},
		{name: "percent", req: emailable.VerifyEmailRequest{Email: "100%@example.com"}, want: "email=100%25%40example.com"},
		{
			name: "quoted local part", req: emailable.VerifyEmailRequest{Email: `"jane doe"@example.com`},
			want: "email=%22jane+doe%22%40example.com",
		},
		{
			name: "inner no-break space", req: emailable.VerifyEmailRequest{Email: "jane\u00a0doe@example.com"},
			want: "email=jane%C2%A0doe%40example.com",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, s := newScriptClient(t, testutil.Step{Body: verifyOK})
			_, err := c.VerifyEmail(t.Context(), tt.req)
			require.NoError(t, err)
			r := testutil.OnlyRequest(t, s)
			assertWireRequest(t, r, http.MethodGet, pathVerify)
			assertNoReplay(t, r)
			assert.Equal(t, tt.want, rawQuery(t, r))
			assert.Equal(t, loopbackOrigin+pathVerify+"?"+tt.want, r.URL)
			assert.Equal(t, tt.req.Email, query(t, r).Get("email"), "the address is sent verbatim")
		})
	}
}

func TestVerifyEmailDecodesTheFullFixture(t *testing.T) {
	body := fixture(t, "verify_full.json")
	res := vfyDecoded(t, body)
	assert.Equal(t, &emailable.Verification{
		Email:        "jordan.fixture+news@example.com",
		State:        emailable.StateDeliverable,
		Reason:       emailable.ReasonAcceptedEmail,
		Score:        new(96),
		AcceptAll:    new(false),
		Disposable:   new(false),
		Free:         new(true),
		MailboxFull:  new(false),
		NoReply:      new(false),
		Role:         new(false),
		BirthYear:    new(1988),
		DidYouMean:   new("jordan.fixture@example.com"),
		Domain:       new("example.com"),
		User:         new("jordan.fixture+news"),
		Tag:          new("news"),
		MXRecord:     new("mx.example.com"),
		SMTPProvider: new("SDK Fixture Mail"),
		FirstName:    new("Jordan"),
		LastName:     new("Fixture"),
		FullName:     new("Jordan Fixture"),
		Gender:       new("unknown"),
		Duration:     new(0.493),
	}, res)
	assert.True(t, res.State.Known())
	assert.True(t, res.Reason.Known())

	// Re-encoded with encoding/json, under the provider's names in field
	// order, the result survives a round trip unchanged.
	assertRoundTrip(t, res, `{"email":"jordan.fixture+news@example.com","state":"deliverable",`+
		`"reason":"accepted_email","score":96,"accept_all":false,"disposable":false,"free":true,`+
		`"mailbox_full":false,"no_reply":false,"role":false,"birth_year":1988,`+
		`"did_you_mean":"jordan.fixture@example.com","domain":"example.com","user":"jordan.fixture+news",`+
		`"tag":"news","mx_record":"mx.example.com","smtp_provider":"SDK Fixture Mail","first_name":"Jordan",`+
		`"last_name":"Fixture","full_name":"Jordan Fixture","gender":"unknown","duration":0.493}`)
}

func TestVerifyEmailMinimalAnswer(t *testing.T) {
	res := vfyDecoded(t, verifyOK)
	want := vfyMinimal()
	want.Reason = emailable.ReasonAcceptedEmail
	assert.Equal(t, want, res, "every optional member absent is nil")
	assertRoundTrip(t, res, verifyOK)
}

func TestVerifyEmailUnknownIsAnAnswer(t *testing.T) {
	res := vfyDecoded(t, `{"email":"`+testEmail+`","state":"unknown","reason":"no_connect"}`)
	assert.Equal(t, emailable.StateUnknown, res.State)
	assert.Equal(t, emailable.ReasonNoConnect, res.Reason)
}

func TestVerifyEmailEchoKeptAsSent(t *testing.T) {
	tests := []struct{ name, email, want string }{
		{name: "another address", email: `"other.person@example.com"`, want: "other.person@example.com"},
		{name: "upper case", email: `"JANE.DOE@EXAMPLE.COM"`, want: "JANE.DOE@EXAMPLE.COM"},
		{name: "escaped", email: testutil.Esc(`"jane.doe~u0040example.com"`), want: testEmail},
		{name: "surrounding spaces", email: `" jane.doe@example.com "`, want: " jane.doe@example.com "},
		{name: "exactly 4 KiB", email: `"` + strings.Repeat("e", 4096) + `"`, want: strings.Repeat("e", 4096)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := vfyDecoded(t, `{"email":`+tt.email+`,"state":"risky"}`)
			assert.Equal(t, tt.want, res.Email, "never compared to the request or normalized")
		})
	}
}

func TestVerifyEmailEveryDocumentedState(t *testing.T) {
	states := map[string]emailable.State{
		"deliverable":   emailable.StateDeliverable,
		"undeliverable": emailable.StateUndeliverable,
		"risky":         emailable.StateRisky,
		"unknown":       emailable.StateUnknown,
		"duplicate":     emailable.StateDuplicate,
	}
	for token, state := range states {
		t.Run(token, func(t *testing.T) {
			assert.Equal(t, token, string(state))
			res := vfyDecoded(t, `{"email":"`+testEmail+`","state":"`+token+`"}`)
			assert.Equal(t, state, res.State)
			assert.True(t, res.State.Known())
		})
	}
	t.Run("escaped spelling", func(t *testing.T) {
		res := vfyDecoded(t, testutil.Esc(`{"email":"jane.doe@example.com","state":"~u0064eliverable"}`))
		assert.Equal(t, emailable.StateDeliverable, res.State)
	})
	for _, token := range []string{"catch_all", "deliverable2", "a", "_", "0", strings.Repeat("z", 64)} {
		t.Run("undocumented "+token[:min(len(token), 12)], func(t *testing.T) {
			res := vfyDecoded(t, `{"email":"`+testEmail+`","state":"`+token+`"}`)
			assert.Equal(t, emailable.State(token), res.State, "a well-formed token is kept as sent")
			assert.False(t, res.State.Known())
		})
	}
	assert.False(t, emailable.State("").Known())
}

func TestVerifyEmailEveryDocumentedReason(t *testing.T) {
	reasons := map[string]emailable.Reason{
		"accepted_email":     emailable.ReasonAcceptedEmail,
		"low_quality":        emailable.ReasonLowQuality,
		"low_deliverability": emailable.ReasonLowDeliverability,
		"invalid_email":      emailable.ReasonInvalidEmail,
		"invalid_domain":     emailable.ReasonInvalidDomain,
		"rejected_email":     emailable.ReasonRejectedEmail,
		"invalid_smtp":       emailable.ReasonInvalidSMTP,
		"no_connect":         emailable.ReasonNoConnect,
		"timeout":            emailable.ReasonTimeout,
		"unavailable_smtp":   emailable.ReasonUnavailableSMTP,
		"unexpected_error":   emailable.ReasonUnexpectedError,
	}
	require.Len(t, reasons, 11)
	for token, reason := range reasons {
		t.Run(token, func(t *testing.T) {
			assert.Equal(t, token, string(reason))
			res := vfyDecoded(t, vfyMember("reason", `"`+token+`"`))
			assert.Equal(t, reason, res.Reason)
			assert.True(t, res.Reason.Known())
		})
	}
	for _, token := range []string{"role_account", "accepted_email_2", "x", strings.Repeat("r", 64)} {
		t.Run("undocumented "+token[:min(len(token), 12)], func(t *testing.T) {
			res := vfyDecoded(t, vfyMember("reason", `"`+token+`"`))
			assert.Equal(t, emailable.Reason(token), res.Reason, "a well-formed token is kept as sent")
			assert.False(t, res.Reason.Known())
		})
	}
	for _, value := range []string{`""`, "null"} {
		t.Run("unset "+value, func(t *testing.T) {
			res := vfyDecoded(t, vfyMember("reason", value))
			assert.Empty(t, res.Reason)
			assert.False(t, res.Reason.Known())
		})
	}
}

// vfyBadTokens returns JSON strings that are not valid tokens: the provider
// sent something, but not in the documented shape.
func vfyBadTokens() map[string]string {
	return map[string]string{
		"capitalized":       `"Deliverable"`,
		"upper case":        `"DELIVERABLE"`,
		"65 bytes":          `"` + strings.Repeat("a", 65) + `"`,
		"far too long":      `"` + strings.Repeat("a", 400) + `"`,
		"hyphen":            `"accept-all"`,
		"space":             `"not valid"`,
		"leading space":     `" risky"`,
		"trailing space":    `"risky "`,
		"dot":               `"a.b"`,
		"non-ASCII":         `"délivrable"`,
		"escaped newline":   testutil.Esc(`"risky~n"`),
		"escaped uppercase": testutil.Esc(`"~u0052isky"`),
		"escaped NUL":       testutil.Esc(`"risky~u0000"`),
	}
}

func TestVerifyEmailInvalidTokens(t *testing.T) {
	for _, field := range []string{"state", "reason"} {
		for name, value := range vfyBadTokens() {
			t.Run(field+"/"+name, func(t *testing.T) {
				body := `{"email":"` + testEmail + `","state":` + value + `}`
				if field == "reason" {
					body = vfyMember("reason", value)
				}
				vfyAssertDecodeFailure(t, body, emailable.FailureUnexpectedResponse, field, causeShape)
			})
		}
		for _, value := range []string{"1", "true", "[]", `["risky"]`, "{}"} {
			t.Run(field+"/wrong type "+value, func(t *testing.T) {
				body := `{"email":"` + testEmail + `","state":` + value + `}`
				if field == "reason" {
					body = vfyMember("reason", value)
				}
				vfyAssertDecodeFailure(t, body, emailable.FailureMalformedResponse, field, causeType)
			})
		}
	}
}

func TestVerifyEmailRequiredMembers(t *testing.T) {
	const malformed = emailable.FailureMalformedResponse
	email := func(v string) string { return `{"email":` + v + `,"state":"deliverable"}` }
	state := func(v string) string { return `{"email":"` + testEmail + `","state":` + v + `}` }
	tests := []struct {
		name  string
		body  string
		kind  emailable.FailureKind
		field string
		cause string
	}{
		{"empty object", `{}`, malformed, "email", causeMissing},
		{"message only", `{"message":"SDK fixture: still verifying"}`, malformed, "email", causeMissing},
		{"email missing", `{"state":"deliverable"}`, malformed, "email", causeMissing},
		{"email null", email("null"), malformed, "email", causeMissing},
		{"email empty", email(`""`), malformed, "email", causeMissing},
		{"email number", email("1"), malformed, "email", causeType},
		{"email boolean", email("false"), malformed, "email", causeType},
		{"email array", email(`["` + testEmail + `"]`), malformed, "email", causeType},
		{"email object", email(`{"address":"` + testEmail + `"}`), malformed, "email", causeType},
		{"email over 4 KiB", email(`"` + strings.Repeat("e", 4097) + `"`), emailable.FailureResponseTooLarge, "email", causeFieldTooLarge},
		{"email case variant only", `{"Email":"` + testEmail + `","state":"deliverable"}`, malformed, "email", causeMissing},
		{"state missing", `{"email":"` + testEmail + `"}`, malformed, "state", causeMissing},
		{"state null", state("null"), malformed, "state", causeMissing},
		{"state empty", state(`""`), malformed, "state", causeMissing},
		{"state empty, escaped", state(testutil.Esc(`""`)), malformed, "state", causeMissing},
		{"state case variant only", `{"email":"` + testEmail + `","State":"deliverable"}`, malformed, "state", causeMissing},
		{"email checked before state", `{"state":"Bad"}`, malformed, "email", causeMissing},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vfyAssertDecodeFailure(t, tt.body, tt.kind, tt.field, tt.cause)
		})
	}
}

// vfyBools returns each optional boolean member's name and its field.
func vfyBools() map[string]func(*emailable.Verification) **bool {
	return map[string]func(*emailable.Verification) **bool{
		"accept_all":   func(v *emailable.Verification) **bool { return &v.AcceptAll },
		"disposable":   func(v *emailable.Verification) **bool { return &v.Disposable },
		"free":         func(v *emailable.Verification) **bool { return &v.Free },
		"mailbox_full": func(v *emailable.Verification) **bool { return &v.MailboxFull },
		"no_reply":     func(v *emailable.Verification) **bool { return &v.NoReply },
		"role":         func(v *emailable.Verification) **bool { return &v.Role },
	}
}

func TestVerifyEmailOptionalBooleans(t *testing.T) {
	for name, field := range vfyBools() {
		cases := []struct {
			label string
			body  string
			want  *bool
		}{
			{label: "absent", body: vfyAnswer("")},
			{label: "null", body: vfyMember(name, "null")},
			{label: "true", body: vfyMember(name, "true"), want: new(true)},
			{label: "false", body: vfyMember(name, "false"), want: new(false)},
		}
		for _, tt := range cases {
			t.Run(name+"/"+tt.label, func(t *testing.T) {
				want := vfyMinimal()
				*field(want) = tt.want
				assert.Equal(t, want, vfyDecoded(t, tt.body), "false is kept, apart from nil")
			})
		}
		for _, bad := range []string{"0", "1", `"true"`, `""`, "[]", "[true]", "{}"} {
			t.Run(name+"/wrong type "+bad, func(t *testing.T) {
				vfyAssertDecodeFailure(t, vfyMember(name, bad), emailable.FailureMalformedResponse, name, causeType)
			})
		}
	}
}

// vfyTexts returns each optional text member's name and its field.
func vfyTexts() map[string]func(*emailable.Verification) **string {
	return map[string]func(*emailable.Verification) **string{
		"did_you_mean":  func(v *emailable.Verification) **string { return &v.DidYouMean },
		"domain":        func(v *emailable.Verification) **string { return &v.Domain },
		"user":          func(v *emailable.Verification) **string { return &v.User },
		"tag":           func(v *emailable.Verification) **string { return &v.Tag },
		"mx_record":     func(v *emailable.Verification) **string { return &v.MXRecord },
		"smtp_provider": func(v *emailable.Verification) **string { return &v.SMTPProvider },
		"first_name":    func(v *emailable.Verification) **string { return &v.FirstName },
		"last_name":     func(v *emailable.Verification) **string { return &v.LastName },
		"full_name":     func(v *emailable.Verification) **string { return &v.FullName },
		"gender":        func(v *emailable.Verification) **string { return &v.Gender },
	}
}

func TestVerifyEmailOptionalTexts(t *testing.T) {
	for name, field := range vfyTexts() {
		cases := []struct {
			label string
			body  string
			want  *string
		}{
			{label: "absent", body: vfyAnswer("")},
			{label: "null", body: vfyMember(name, "null")},
			{label: "empty", body: vfyMember(name, `""`), want: new("")},
			{label: "text", body: vfyMember(name, `"SDK fixture"`), want: new("SDK fixture")},
			{label: "escaped", body: vfyMember(name, testutil.Esc(`"~u00e9~"~~"`)), want: new(`é"\`)},
			{label: "exactly 4 KiB", body: vfyMember(name, `"`+strings.Repeat("t", 4096)+`"`), want: new(strings.Repeat("t", 4096))},
		}
		for _, tt := range cases {
			t.Run(name+"/"+tt.label, func(t *testing.T) {
				want := vfyMinimal()
				*field(want) = tt.want
				assert.Equal(t, want, vfyDecoded(t, tt.body), `"" is kept, apart from nil`)
			})
		}
		for _, bad := range []string{"0", "true", "[]", `["x"]`, "{}"} {
			t.Run(name+"/wrong type "+bad, func(t *testing.T) {
				vfyAssertDecodeFailure(t, vfyMember(name, bad), emailable.FailureMalformedResponse, name, causeType)
			})
		}
		t.Run(name+"/over 4 KiB", func(t *testing.T) {
			vfyAssertDecodeFailure(t, vfyMember(name, `"`+strings.Repeat("t", 4097)+`"`),
				emailable.FailureResponseTooLarge, name, causeFieldTooLarge)
		})
	}
}

// TestVerifyEmailTextBoundEscaped proves that the 4 KiB bound on provider
// text counts the decoded bytes, however the provider spelled them.
func TestVerifyEmailTextBoundEscaped(t *testing.T) {
	esc := func(n int, unit string) string { return strings.Repeat(testutil.Esc(unit), n) }
	tests := []struct {
		name    string
		literal string // the JSON string's content
		size    int    // its decoded length
	}{
		{name: "4096 ASCII escapes", literal: esc(4096, "~u0061"), size: 4096},
		{name: "4097 ASCII escapes", literal: esc(4097, "~u0061"), size: 4097},
		{name: "2048 two-byte runes", literal: strings.Repeat("é", 2048), size: 4096},
		{name: "2049 two-byte runes", literal: strings.Repeat("é", 2049), size: 4098},
		{name: "2048 escaped two-byte runes", literal: esc(2048, "~u00e9"), size: 4096},
		{name: "4094 bytes and an escaped two-byte rune", literal: strings.Repeat("a", 4094) + esc(1, "~u00e9"), size: 4096},
		{name: "4095 bytes and an escaped two-byte rune", literal: strings.Repeat("a", 4095) + esc(1, "~u00e9"), size: 4097},
		{name: "4092 bytes and an escaped surrogate pair", literal: strings.Repeat("a", 4092) + esc(1, "~ud83d~ude00"), size: 4096},
		{name: "4093 bytes and an escaped surrogate pair", literal: strings.Repeat("a", 4093) + esc(1, "~ud83d~ude00"), size: 4097},
		{name: "4096 escaped quotes", literal: esc(4096, `~"`), size: 4096},
		{name: "4097 escaped backslashes", literal: esc(4097, "~~"), size: 4097},
		{name: "lone surrogate escapes", literal: esc(1365, "~ud800"), size: 1365 * 3},
		{name: "lone surrogate escapes over", literal: esc(1366, "~ud800"), size: 1366 * 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := vfyMember("full_name", `"`+tt.literal+`"`)
			if tt.size > 4096 {
				vfyAssertDecodeFailure(t, body, emailable.FailureResponseTooLarge, "full_name", causeFieldTooLarge)
				return
			}
			res := vfyDecoded(t, body)
			require.NotNil(t, res.FullName)
			assert.Len(t, *res.FullName, tt.size)
		})
	}
}

// vfyNumberCase is one value of a numeric member: accepted as want, or
// refused with cause.
type vfyNumberCase struct {
	value string
	want  any // *int or *float64; nil for a refusal or for null
	cause string
}

// vfyCheckNumber runs each case of a numeric member, name, whose field get
// reads.
func vfyCheckNumber(t *testing.T, name string, cases []vfyNumberCase, get func(*emailable.Verification) any) {
	t.Helper()
	for _, tt := range cases {
		t.Run(name+"/"+tt.value, func(t *testing.T) {
			body := vfyMember(name, tt.value)
			if tt.cause != "" {
				vfyAssertDecodeFailure(t, body, emailable.FailureMalformedResponse, name, tt.cause)
				return
			}
			assert.Equal(t, tt.want, get(vfyDecoded(t, body)))
		})
	}
}

func TestVerifyEmailScore(t *testing.T) {
	vfyCheckNumber(t, "score", []vfyNumberCase{
		{value: "null", want: (*int)(nil)},
		{value: "0", want: new(0)},
		{value: "-0", want: new(0)},
		{value: "50", want: new(50)},
		{value: "100", want: new(100)},
		{value: "101", cause: causeRange},
		{value: "-1", cause: causeRange},
		{value: "1000", cause: causeRange},
		{value: "9223372036854775808", cause: causeRange},
		{value: "-9223372036854775809", cause: causeRange},
		{value: "1.5", cause: causeType},
		{value: "50.0", cause: causeType},
		{value: "1e2", cause: causeType},
		{value: "1E2", cause: causeType},
		{value: `"50"`, cause: causeType},
		{value: "true", cause: causeType},
		{value: "[50]", cause: causeType},
		{value: "{}", cause: causeType},
	}, func(v *emailable.Verification) any { return v.Score })
}

func TestVerifyEmailBirthYear(t *testing.T) {
	vfyCheckNumber(t, "birth_year", []vfyNumberCase{
		{value: "null", want: (*int)(nil)},
		{value: "0", want: new(0)},
		{value: "1988", want: new(1988)},
		{value: "9999", want: new(9999)},
		{value: "10000", cause: causeRange},
		{value: "-1", cause: causeRange},
		{value: "1988.5", cause: causeType},
		{value: "2e3", cause: causeType},
		{value: `"1988"`, cause: causeType},
		{value: "false", cause: causeType},
	}, func(v *emailable.Verification) any { return v.BirthYear })
}

func TestVerifyEmailDuration(t *testing.T) {
	vfyCheckNumber(t, "duration", []vfyNumberCase{
		{value: "null", want: (*float64)(nil)},
		{value: "0", want: new(0.0)},
		{value: "0.0", want: new(0.0)},
		{value: "0.25", want: new(0.25)},
		{value: "12", want: new(12.0)},
		{value: "1e2", want: new(100.0)},
		{value: "1.5E-3", want: new(0.0015)},
		{value: "86400.123", want: new(86400.123)},
		{value: "-1", cause: causeRange},
		{value: "-0.5", cause: causeRange},
		{value: "-1e-9", cause: causeRange},
		{value: "1e400", cause: causeRange},
		{value: `"1.5"`, cause: causeType},
		{value: "true", cause: causeType},
		{value: "[1]", cause: causeType},
		{value: "{}", cause: causeType},
	}, func(v *emailable.Verification) any { return v.Duration })

	t.Run("negative zero becomes zero", func(t *testing.T) {
		res := vfyDecoded(t, vfyMember("duration", "-0"))
		require.NotNil(t, res.Duration)
		assert.False(t, math.Signbit(*res.Duration), "-0 is reported as +0")
		assert.Zero(t, *res.Duration)
	})
}

func TestVerifyEmailIgnoresOtherMembers(t *testing.T) {
	tests := []struct{ name, members string }{
		{name: "unknown members", members: `"message":"SDK fixture","sdk_fixture":{"nested":[1,{"score":"x"}]}`},
		{name: "case variant alone", members: `"Score":"not a number","DURATION":-1`},
		{name: "nested member names", members: `"extra":{"email":7,"state":"Bad","reason":[]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, vfyMinimal(), vfyDecoded(t, vfyAnswer(tt.members)))
		})
	}
}

// TestVerifyEmailFirstFailureInMemberOrder proves that a body failing in
// several members is reported at the first in the documented member order,
// whatever their order in the body.
func TestVerifyEmailFirstFailureInMemberOrder(t *testing.T) {
	tests := []struct {
		name    string
		members string
		field   string
	}{
		{name: "score before duration", members: `"duration":"x","score":"y"`, field: "score"},
		{name: "reason before score", members: `"score":101,"reason":"Bad"`, field: "reason"},
		{name: "booleans before birth_year", members: `"birth_year":-1,"role":0`, field: "role"},
		{name: "birth_year before texts", members: `"gender":1,"birth_year":"1"`, field: "birth_year"},
		{name: "texts in member order", members: `"gender":1,"did_you_mean":2`, field: "did_you_mean"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := callOnce(t, pathVerify, vfyAnswer(tt.members), callVerify)
			assert.Equal(t, tt.field, callError(t, err).Field)
		})
	}
}

func TestVerifyEmailPending(t *testing.T) {
	header := rateHeader(60, 59, 12)
	for _, diagnostics := range []bool{false, true} {
		t.Run("diagnostics="+strconv.FormatBool(diagnostics), func(t *testing.T) {
			body := testutil.NewBody(pending)
			ct := &testutil.CountingTransport{Next: testutil.TransportWith(emailable.StatusPending, header, int64(len(pending)), body)}
			var opts []emailable.Option
			if diagnostics {
				opts = append(opts, emailable.WithProviderDiagnostics())
			}
			v, err := newTransportClient(t, ct, opts...).VerifyEmail(t.Context(), verifyRequest())
			assert.Nil(t, v, "a pending answer is never a result")
			ce := assertCallError(t, err, wantCall{
				Kind: emailable.FailurePending, Op: emailable.OpVerifyEmail, Status: 249, Attempts: 1,
			})
			require.ErrorIs(t, err, emailable.ErrPending)
			require.NotErrorIs(t, err, emailable.ErrOutcomeUnknown, "the provider says it has no result yet")
			assert.Equal(t, causePending, causeText(t, err))
			assert.Equal(t, rateLimit(60, 59, 12), ce.RateLimit, "a 249 exposes the rate limits")
			assert.Equal(t, 1, ct.Attempts(), "never repeated")
			assert.Equal(t, 1, body.Closes())
			if diagnostics {
				assert.NotEmpty(t, ce.Diagnostics().Message)
				return
			}
			assert.Empty(t, ce.Diagnostics().Message)
			assert.Zero(t, body.Reads())
		})
	}
}

func TestVerifyEmailPendingOverTLS(t *testing.T) {
	for _, proto := range testutil.Protocols() {
		t.Run(proto.Name(), func(t *testing.T) {
			c, srv := newServerClientProto(t, proto, testutil.Serve(testutil.Step{Status: emailable.StatusPending, Body: pending}))
			v, err := c.VerifyEmail(t.Context(), verifyRequest())
			assert.Nil(t, v)
			_ = assertCallError(t, err, wantCall{
				Kind: emailable.FailurePending, Op: emailable.OpVerifyEmail, Status: 249, Attempts: 1,
			})
			assert.Equal(t, 1, srv.Hits())
		})
	}
}

// TestVerifyEmailPendingOnlyFromVerify proves that HTTP 249 from any other
// operation is unexpected: only VerifyEmail documents it.
func TestVerifyEmailPendingOnlyFromVerify(t *testing.T) {
	for _, o := range operations() {
		if o.op == emailable.OpVerifyEmail {
			continue
		}
		t.Run(o.name, func(t *testing.T) {
			c, s := newScriptClient(t, testutil.Step{Status: emailable.StatusPending, Body: pending})
			err := o.call(t.Context(), c)
			_ = assertCallError(t, err, o.want(wantCall{
				Kind: emailable.FailureUnexpectedResponse, Status: 249, Attempts: 1,
				OutcomeUnknown: o.op == emailable.OpCreateBatch,
			}))
			require.NotErrorIs(t, err, emailable.ErrPending)
			assert.Equal(t, 1, s.Calls())
		})
	}
}

func TestVerifyEmailInvalidAddress(t *testing.T) {
	long := strings.Repeat("a", 255-len("@example.com")) + "@example.com"
	tests := []struct {
		name   string
		email  string
		reason string
	}{
		{name: "empty", email: "", reason: reasonRequired},
		{name: "255 bytes", email: long, reason: "too_long"},
		{name: "255 bytes with a control character", email: "\n" + long[1:], reason: "too_long"},
		{name: "invalid UTF-8", email: "jane\xff@example.com", reason: "invalid_utf8"},
		{name: "truncated UTF-8", email: "jane@example.com\xc3", reason: "invalid_utf8"},
		{name: "invalid UTF-8 and surrounding space", email: " jane\xff@example.com", reason: "invalid_utf8"},
		{name: "NUL", email: "jane\x00@example.com", reason: reasonControlCharacter},
		{name: "inner tab", email: "jane\tdoe@example.com", reason: reasonControlCharacter},
		{name: "leading tab", email: "\tjane@example.com", reason: reasonControlCharacter},
		{name: "line feed", email: "jane@example.com\n", reason: reasonControlCharacter},
		{name: "carriage return", email: "jane\r@example.com", reason: reasonControlCharacter},
		{name: "vertical tab", email: "jane\v@example.com", reason: reasonControlCharacter},
		{name: "form feed", email: "jane\f@example.com", reason: reasonControlCharacter},
		{name: "escape", email: "jane\x1b@example.com", reason: reasonControlCharacter},
		{name: "unit separator", email: "jane\x1f@example.com", reason: reasonControlCharacter},
		{name: "DEL", email: "jane\x7f@example.com", reason: reasonControlCharacter},
		{name: "C1 first", email: "jane\u0080@example.com", reason: reasonControlCharacter},
		{name: "C1 next line", email: "jane\u0085@example.com", reason: reasonControlCharacter},
		{name: "C1 last", email: "jane\u009f@example.com", reason: reasonControlCharacter},
		{name: "leading space", email: " jane@example.com", reason: "surrounding_whitespace"},
		{name: "trailing space", email: "jane@example.com ", reason: "surrounding_whitespace"},
		{name: "only a space", email: " ", reason: "surrounding_whitespace"},
		{name: "leading no-break space", email: "\u00a0jane@example.com", reason: "surrounding_whitespace"},
		{name: "trailing em space", email: "jane@example.com\u2003", reason: "surrounding_whitespace"},
		{name: "trailing ideographic space", email: "jane@example.com\u3000", reason: "surrounding_whitespace"},
		{name: "leading line separator", email: "\u2028jane@example.com", reason: "surrounding_whitespace"},
		{name: "trailing paragraph separator", email: "jane@example.com\u2029", reason: "surrounding_whitespace"},
		{name: "leading ogham space", email: "\u1680jane@example.com", reason: "surrounding_whitespace"},
		{name: "trailing narrow no-break space", email: "jane@example.com\u202f", reason: "surrounding_whitespace"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, ct := newOfflineClient(t)
			v, err := c.VerifyEmail(t.Context(), emailable.VerifyEmailRequest{Email: tt.email})
			assert.Nil(t, v)
			assertValidation(t, err, emailable.ErrInvalidRequest, "Email", tt.reason)
			assertErrorClean(t, err, append(leakMarkers(), "jane")...)
			assert.Zero(t, ct.Attempts(), "nothing is sent")
		})
	}
}

// TestVerifyEmailAddressSentVerbatim proves that the SDK checks no address
// syntax: anything within the documented rules is sent as it is.
func TestVerifyEmailAddressSentVerbatim(t *testing.T) {
	for _, email := range []string{
		"jane doe@example.com",
		"janedoe",
		"@",
		"a@b@example.com",
		strings.Repeat("a", 254-len("@example.com")) + "@example.com",
		"jane\u00a0doe@example.com",
		"jane\u2028doe@example.com",
		"JANE.DOE@EXAMPLE.COM",
		"zoë.ñandú@example.com",
		`"quoted\back"@example.com`,
	} {
		t.Run(email[:min(len(email), 24)], func(t *testing.T) {
			c, s := newScriptClient(t, testutil.Step{Body: verifyOK})
			_, err := c.VerifyEmail(t.Context(), emailable.VerifyEmailRequest{Email: email})
			require.NoError(t, err)
			assert.Equal(t, email, query(t, testutil.OnlyRequest(t, s)).Get("email"))
		})
	}
}

func TestVerifyEmailProviderTimeoutRejected(t *testing.T) {
	tests := []struct {
		name    string
		timeout time.Duration // the Client's; 0 keeps the default
		value   time.Duration
		reason  string
	}{
		{name: "fraction of a second", value: 1500 * time.Millisecond, reason: "not_whole_seconds"},
		{name: "one millisecond", value: time.Millisecond, reason: "not_whole_seconds"},
		{name: "one nanosecond over 2s", value: 2*time.Second + time.Nanosecond, reason: "not_whole_seconds"},
		{name: "negative fraction", value: -1500 * time.Millisecond, reason: "not_whole_seconds"},
		{name: "one second", value: time.Second, reason: reasonOutOfRange},
		{name: "negative", value: -2 * time.Second, reason: reasonOutOfRange},
		{name: "11 seconds", value: 11 * time.Second, reason: reasonOutOfRange},
		{name: "25 seconds", value: 25 * time.Second, reason: reasonOutOfRange},
		{name: "26 seconds", value: 26 * time.Second, reason: reasonOutOfRange},
		{name: "a minute", value: time.Minute, reason: reasonOutOfRange},
		{name: "range before the client timeout", timeout: 10 * time.Second, value: 11 * time.Second, reason: reasonOutOfRange},
		{name: "6s against a 10s client", timeout: 10 * time.Second, value: 6 * time.Second, reason: "exceeds_client_timeout"},
		{name: "10s against a 10s client", timeout: 10 * time.Second, value: 10 * time.Second, reason: "exceeds_client_timeout"},
		{name: "10s against a client just under 15s", timeout: 15*time.Second - time.Nanosecond, value: 10 * time.Second, reason: "exceeds_client_timeout"},
		{name: "2s against a 6s client", timeout: 6 * time.Second, value: 2 * time.Second, reason: "exceeds_client_timeout"},
		{name: "2s against a 1s client", timeout: time.Second, value: 2 * time.Second, reason: "exceeds_client_timeout"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var opts []emailable.Option
			if tt.timeout > 0 {
				opts = append(opts, emailable.WithTimeout(tt.timeout))
			}
			c, ct := newOfflineClient(t, opts...)
			_, err := c.VerifyEmail(t.Context(), emailable.VerifyEmailRequest{Email: testEmail, ProviderTimeout: tt.value})
			assertValidation(t, err, emailable.ErrInvalidRequest, "ProviderTimeout", tt.reason)
			assertErrorClean(t, err, leakMarkers()...)
			assert.Zero(t, ct.Attempts())
		})
	}
}

func TestVerifyEmailProviderTimeoutAccepted(t *testing.T) {
	tests := []struct {
		name    string
		timeout time.Duration // the Client's; 0 keeps the default
		value   time.Duration
		want    string // the timeout query member
	}{
		{name: "2s", value: 2 * time.Second, want: "2"},
		{name: "5s", value: 5 * time.Second, want: "5"},
		{name: "10s", value: 10 * time.Second, want: "10"},
		{name: "5s against a 10s client", timeout: 10 * time.Second, value: 5 * time.Second, want: "5"},
		{name: "10s against a 15s client", timeout: 15 * time.Second, value: 10 * time.Second, want: "10"},
		{name: "2s against a 7s client", timeout: 7 * time.Second, value: 2 * time.Second, want: "2"},
		{name: "10s against a 10m client", timeout: 10 * time.Minute, value: 10 * time.Second, want: "10"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := testutil.NewScript(testutil.Step{Body: verifyOK})
			opts := []emailable.Option{}
			if tt.timeout > 0 {
				opts = append(opts, emailable.WithTimeout(tt.timeout))
			}
			c := newTransportClient(t, s, opts...)
			_, err := c.VerifyEmail(t.Context(), emailable.VerifyEmailRequest{Email: testEmail, ProviderTimeout: tt.value})
			require.NoError(t, err)
			assert.Equal(t, vfyEmailQuery+"&timeout="+tt.want, rawQuery(t, testutil.OnlyRequest(t, s)))
		})
	}
}

func TestVerifyEmailCaptchaResponse(t *testing.T) {
	const limit = 8 << 10
	tests := []struct {
		name    string
		captcha string
		reason  string // "" when it is accepted
	}{
		{name: "exactly 8 KiB", captcha: strings.Repeat("c", limit)},
		{name: "8 KiB of two-byte runes", captcha: strings.Repeat("é", limit/2)},
		{name: "only a space", captcha: " "},
		{name: "surrounding spaces", captcha: " " + testCaptcha + " "},
		{name: "8 KiB plus one", captcha: strings.Repeat("c", limit+1), reason: "too_long"},
		{name: "too long and a control character", captcha: "\n" + strings.Repeat("c", limit), reason: "too_long"},
		{name: "line feed", captcha: testCaptcha + "\n", reason: reasonControlCharacter},
		{name: "NUL", captcha: "\x00" + testCaptcha, reason: reasonControlCharacter},
		{name: "DEL", captcha: testCaptcha + "\x7f", reason: reasonControlCharacter},
		{name: "C1", captcha: testCaptcha + "\u0085", reason: reasonControlCharacter},
		{name: "invalid UTF-8", captcha: testCaptcha + "\xff", reason: "invalid_utf8"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := testutil.NewScript(testutil.Step{Body: verifyOK})
			c := newTransportClient(t, s)
			_, err := c.VerifyEmail(t.Context(), emailable.VerifyEmailRequest{Email: testEmail, CaptchaResponse: tt.captcha})
			if tt.reason == "" {
				require.NoError(t, err)
				assert.Equal(t, tt.captcha, query(t, testutil.OnlyRequest(t, s)).Get("captcha_response"))
				return
			}
			assertValidation(t, err, emailable.ErrInvalidRequest, "CaptchaResponse", tt.reason)
			assertErrorClean(t, err, leakMarkers()...)
			assert.Zero(t, s.Calls(), "nothing is sent")
		})
	}
}

func TestVerifyEmailValidationOrder(t *testing.T) {
	tests := []struct {
		name  string
		req   emailable.VerifyEmailRequest
		field string
	}{
		{
			name:  "email first",
			req:   emailable.VerifyEmailRequest{ProviderTimeout: time.Millisecond, CaptchaResponse: "\n"},
			field: "Email",
		},
		{
			name:  "provider timeout before captcha",
			req:   emailable.VerifyEmailRequest{Email: testEmail, ProviderTimeout: time.Millisecond, CaptchaResponse: "\n"},
			field: "ProviderTimeout",
		},
		{
			name:  "captcha last",
			req:   emailable.VerifyEmailRequest{Email: testEmail, CaptchaResponse: "\n"},
			field: "CaptchaResponse",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, ct := newOfflineClient(t)
			_, err := c.VerifyEmail(t.Context(), tt.req)
			require.ErrorIs(t, err, emailable.ErrInvalidRequest)
			assert.Equal(t, tt.field, validationError(t, err).Field)
			assert.Zero(t, ct.Attempts())
		})
	}
}

// TestVerifyEmailCaptchaRedactedFromDiagnostics proves that the request's
// captcha response is scrubbed from the provider's message, as the API key
// is, by both VerifyEmail and WaitForVerification.
func TestVerifyEmailCaptchaRedactedFromDiagnostics(t *testing.T) {
	message := `{"message":"captcha ` + testCaptcha + ` refused for key ` + testKey + ` and ` + testEmail + `"}`
	req := emailable.VerifyEmailRequest{Email: testEmail, CaptchaResponse: testCaptcha}
	calls := map[string]func(context.Context, *emailable.Client) error{
		"VerifyEmail": func(ctx context.Context, c *emailable.Client) error {
			return noValueWith(c.VerifyEmail(ctx, req))
		},
		"WaitForVerification": func(ctx context.Context, c *emailable.Client) error {
			return noValueWith(c.WaitForVerification(ctx, emailable.WaitForVerificationRequest{
				Verify: req, MaxAttempts: 2, MaxWait: time.Minute,
			}))
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			c := newTransportClient(t, testutil.StaticTransport(http.StatusForbidden, message),
				emailable.WithProviderDiagnostics())
			err := call(t.Context(), c)
			ce := assertCallError(t, err, wantCall{
				Kind: emailable.FailureUnauthorized, Op: emailable.OpVerifyEmail, Status: http.StatusForbidden, Attempts: 1,
			}, "refused for key")
			assert.Equal(t, "captcha [redacted] refused for key [redacted] and "+testEmail, ce.Diagnostics().Message,
				"the key and the captcha response are scrubbed; the address may be echoed")
		})
	}
}

// TestVerifyEmailCaptchaRedactedInEveryForm proves that the captcha
// response is scrubbed from the provider's message both as sent and in the
// form it took in the query string.
func TestVerifyEmailCaptchaRedactedInEveryForm(t *testing.T) {
	const captcha = "vfy cap+tcha/marker=1&x"
	escaped := url.QueryEscape(captcha)
	require.NotEqual(t, captcha, escaped)
	message := messageBody("captcha " + captcha + " or " + escaped + " refused")
	c := newTransportClient(t, testutil.StaticTransport(http.StatusForbidden, message), emailable.WithProviderDiagnostics())
	_, err := c.VerifyEmail(t.Context(), emailable.VerifyEmailRequest{Email: testEmail, CaptchaResponse: captcha})
	ce := assertCallError(t, err, wantCall{
		Kind: emailable.FailureUnauthorized, Op: emailable.OpVerifyEmail, Status: http.StatusForbidden, Attempts: 1,
	}, captcha, escaped)
	assert.Equal(t, "captcha [redacted] or [redacted] refused", ce.Diagnostics().Message)
}
