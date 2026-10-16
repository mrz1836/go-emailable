package emailable

import (
	"bytes"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-emailable/internal/testutil"
)

// dcdAbsent marks a member that is not in the object at all.
const dcdAbsent = "<absent>"

// Literals the decode tests repeat.
const (
	dcdBatchID  = "b1"
	dcdFalse    = "false"
	dcdIDField  = "id"
	dcdEmails   = "emails"
	dcdReasons  = "reason_counts"
	dcdTotals   = "total_counts"
	dcdMessage  = "message"
	dcdOwner    = "owner_email"
	dcdCredits  = "available_credits"
	dcdProgress = "processed"
)

// dcdRaw returns the raw member value v, or nil (absent) for dcdAbsent.
func dcdRaw(v string) []byte {
	if v == dcdAbsent {
		return nil
	}
	return []byte(v)
}

// dcdMember is one member of a JSON object a test builds: a key and its raw
// value, or dcdAbsent to leave it out.
type dcdMember struct{ key, raw string }

// dcdObject returns the JSON object of members, in order, leaving out the
// absent ones.
func dcdObject(members ...dcdMember) string {
	var b strings.Builder
	b.WriteByte('{')
	n := 0
	for _, m := range members {
		if m.raw == dcdAbsent {
			continue
		}
		if n > 0 {
			b.WriteByte(',')
		}
		b.WriteString(testutil.JSONQuote(m.key) + ":" + m.raw)
		n++
	}
	b.WriteByte('}')
	return b.String()
}

// dcdString returns a JSON string of n copies of s.
func dcdString(s string, n int) string { return `"` + strings.Repeat(s, n) + `"` }

// dcdStrict runs checkStrict on body, failing tb unless it accepts it, and
// returns it.
func dcdStrict(tb testing.TB, body string) []byte {
	tb.Helper()
	kind, cause := checkStrict([]byte(body))
	require.NoError(tb, cause, "checkStrict must accept %s", body)
	require.Empty(tb, kind)
	return []byte(body)
}

func TestDecodeErrorConstructors(t *testing.T) {
	tests := []struct {
		derr  *decodeError
		kind  FailureKind
		cause error
	}{
		{missingField("a"), FailureMalformedResponse, errMissingField},
		{wrongType("a"), FailureMalformedResponse, errWrongType},
		{outOfRange("a"), FailureMalformedResponse, errOutOfRange},
		{badToken("a"), FailureUnexpectedResponse, errInvalidToken},
		{fieldTooLarge("a"), FailureResponseTooLarge, errFieldTooLarge},
		{idMismatch("a"), FailureUnexpectedResponse, errIDMismatch},
	}
	for _, tt := range tests {
		t.Run(string(tt.kind)+"/"+tt.cause.Error(), func(t *testing.T) {
			assertDecodeError(t, tt.derr, tt.kind, "a")
			require.ErrorIs(t, tt.derr.cause, tt.cause)

			ce := tt.derr.callError(OpGetBatch)
			assert.Equal(t, tt.kind, ce.Kind)
			assert.Equal(t, OpGetBatch, ce.Op)
			assert.Equal(t, http.StatusOK, ce.StatusCode)
			assert.Equal(t, "a", ce.Field)
			assert.Empty(t, ce.BatchID)
			assert.Zero(t, ce.Attempts)
			assert.False(t, ce.OutcomeUnknown)
			require.ErrorIs(t, ce, tt.cause, "the cause is reachable through Unwrap")
			require.ErrorIs(t, ce, kindSentinel(tt.kind))
			assert.Equal(t, "emailable: get_batch: "+string(tt.kind)+" (HTTP 200): a", ce.Error())
			assert.Empty(t, ce.Diagnostics().Message)
		})
	}

	derr := noMembers()
	assertDecodeError(t, derr, FailureUnexpectedResponse, "")
	requireCause(t, derr, errNoMembers)
	assert.Equal(t, "emailable: batch_callback: unexpected_response (HTTP 200)", derr.callError(OpBatchCallback).Error())

	at := wrongType("")
	assert.Same(t, at, at.at("emails[2].score"), "at sets the field in place")
	assert.Equal(t, "emails[2].score", at.field)
}

// TestDecodeFailureMarksSentRequests proves that a decoding failure counts as
// one attempt, with an unknown outcome only for a paid request.
func TestDecodeFailureMarksSentRequests(t *testing.T) {
	paid := (&request{op: OpCreateBatch, paid: true}).decodeFailure(missingField(dcdIDField))
	assert.Equal(t, 1, paid.Attempts)
	assert.True(t, paid.OutcomeUnknown, "an unreadable answer to a paid call leaves its outcome open")
	require.ErrorIs(t, paid, ErrOutcomeUnknown)
	assert.Equal(t, dcdIDField, paid.Field)

	free := (&request{op: OpGetBatch}).decodeFailure(idMismatch(dcdIDField))
	assert.Equal(t, 1, free.Attempts)
	assert.False(t, free.OutcomeUnknown)
	require.ErrorIs(t, free, ErrUnexpectedResponse)
}

func TestDecodePath(t *testing.T) {
	var top path
	assert.Equal(t, "score", top.member("score"), "the top level renders the member alone")
	assert.Equal(t, "email", top.member("email"))
	p := path{base: dcdEmails, index: 3}
	assert.Equal(t, "emails[3]", p.String())
	assert.Equal(t, "emails[3].score", p.member("score"))
	assert.Equal(t, "emails[0].mx_record", path{base: dcdEmails}.member("mx_record"))
	assert.Equal(t, "emails[12345].state", path{base: dcdEmails, index: 12345}.member("state"))
}

func TestDecodeReadObject(t *testing.T) {
	names := []string{"email", "state", "score"}
	tests := []struct {
		name string
		body string
		want [3]string // raw values; "" when absent
	}{
		{name: "every member", body: `{"email":"a","state":"risky","score":7}`, want: [3]string{`"a"`, `"risky"`, `7`}},
		{name: "order does not matter", body: `{"score":[1],"state":{"a":"b"},"email":null}`, want: [3]string{jsonNull, `{"a":"b"}`, `[1]`}},
		{name: "other cases are ignored", body: `{"Email":"x","STATE":"y","Score":1,"sCORE":2}`},
		{name: "exact case wins over other cases", body: `{"Email":"x","email":"a","SCORE":3}`, want: [3]string{`"a"`}},
		{name: "a Kelvin sign is not a k", body: `{"` + strictKelvin + `ey":1,"email":"a"}`, want: [3]string{`"a"`}},
		{name: "escaped keys match", body: testutil.Esc(`{"~u0065mail":"a","st~u0061te":"b"}`), want: [3]string{`"a"`, `"b"`}},
		{name: "unknown members are ignored", body: `{"x":1,"email":"a","y":{"email":"z"}}`, want: [3]string{`"a"`}},
		{name: "nested members are not flattened", body: `{"data":{"email":"a","state":"risky"}}`},
		{name: "whitespace around members", body: " {\n \"email\" :\t\"a\" , \"score\" : -1 } ", want: [3]string{`"a"`, "", `-1`}},
		{name: "keys with spaces differ", body: `{"email ":"a"," email":"b"}`},
		{name: "not an object", body: `["email","a"]`},
		{name: "empty body", body: ``},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := readObject([]byte(tt.body), names)
			for i := range names {
				if tt.want[i] == "" {
					assert.Nil(t, o.fields[i], names[i])
					continue
				}
				assert.Equal(t, tt.want[i], string(o.fields[i]), names[i])
			}
			for i := len(names); i < maxFields; i++ {
				assert.Nil(t, o.fields[i], "slot %d is unused", i)
			}
		})
	}

	body := []byte(`{"email":"a"}`)
	o := readObject(body, names)
	assert.Same(t, &body[9], &o.fields[0][0], "members alias the body until decoded")
	assert.LessOrEqual(t, int(verCount), maxFields, "a verification fits an object")
	assert.LessOrEqual(t, int(batchCount), maxFields, "a batch fits an object")
}

func TestDecodeObjectPresent(t *testing.T) {
	o := readObject([]byte(`{"a":null,"b":0,"c":null}`), []string{"a", "b", "c", "d"})
	assert.False(t, o.present(1), "null is not present")
	assert.True(t, o.present(2), "0 is present")
	assert.True(t, o.present(4))
	empty := readObject([]byte(`{"a":null,"x":1}`), []string{"a", "b"})
	assert.False(t, empty.present(2), "null and absent members are not present")
	var zero object
	assert.False(t, zero.present(maxFields))
}

func TestDecodeText(t *testing.T) {
	esc := testutil.Esc
	exact := strings.Repeat("a", maxTextBytes)
	tests := []struct {
		name string
		raw  string
		want string
		kind FailureKind
	}{
		{name: "string", raw: `"Batch verification completed."`, want: "Batch verification completed."},
		{name: "empty string", raw: `""`, want: ""},
		{name: "escaped", raw: esc(`"a~"b~nc~u00e9"`), want: "a\"b\ncé"},
		{name: "lone surrogate", raw: esc(`"~ud800"`), want: strictFFFD},
		{name: jsonNull, raw: jsonNull, want: ""},
		{name: dcdAbsent, raw: dcdAbsent, want: ""},
		{name: "exactly 4 KiB", raw: `"` + exact + `"`, want: exact},
		{name: "4 KiB once decoded, at the fast path's edge", raw: dcdString(esc(`~u0061`), maxTextBytes), want: exact},
		{name: "4 KiB of two-byte escapes", raw: dcdString(esc(`~n`), maxTextBytes), want: strings.Repeat("\n", maxTextBytes)},
		{name: "4 KiB of two-byte runes", raw: dcdString("é", maxTextBytes/2), want: strings.Repeat("é", maxTextBytes/2)},
		{name: "4 KiB of three-byte runes and one byte", raw: `"a` + strings.Repeat("€", 1365) + `"`, want: "a" + strings.Repeat("€", 1365)},
		{name: "4 KiB of surrogate pairs", raw: dcdString(esc(`~ud83d~ude00`), maxTextBytes/4), want: strings.Repeat(strictEmoji, maxTextBytes/4)},
		{name: "one byte over 4 KiB", raw: `"` + exact + `b"`, kind: FailureResponseTooLarge},
		{name: "over 4 KiB by a multibyte rune", raw: `"` + strings.Repeat("a", maxTextBytes-1) + "é\"", kind: FailureResponseTooLarge},
		{name: "over 4 KiB once decoded", raw: dcdString(esc(`~u20ac`), 1366), kind: FailureResponseTooLarge},
		{name: "one escape over 4 KiB, past the fast path", raw: dcdString(esc(`~u0061`), maxTextBytes+1), kind: FailureResponseTooLarge},
		{name: "one two-byte escape over", raw: dcdString(esc(`~t`), maxTextBytes+1), kind: FailureResponseTooLarge},
		{name: "number", raw: `12`, kind: FailureMalformedResponse},
		{name: "negative number", raw: `-1`, kind: FailureMalformedResponse},
		{name: strictTrue, raw: strictTrue, kind: FailureMalformedResponse},
		{name: dcdFalse, raw: dcdFalse, kind: FailureMalformedResponse},
		{name: "object", raw: `{"a":"b"}`, kind: FailureMalformedResponse},
		{name: "array", raw: `["a"]`, kind: FailureMalformedResponse},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, derr := decodeText(dcdRaw(tt.raw), maxTextBytes)
			if tt.kind != "" {
				assertDecodeError(t, derr, tt.kind, "")
				assert.Empty(t, got)
				return
			}
			assert.Nil(t, derr)
			assert.Equal(t, tt.want, got)
		})
	}
	_, derr := decodeText([]byte("7"), maxTextBytes)
	requireCause(t, derr, errWrongType)
	_, derr = decodeText([]byte(`"`+exact+`b"`), maxTextBytes)
	requireCause(t, derr, errFieldTooLarge)
}

// TestDecodeTextFastPath proves that the length shortcut never refuses text
// that decodes within the limit: for each small limit, the longest spelling
// of limit bytes (every byte a \u escape, six bytes each) passes, and one
// more escape fails.
func TestDecodeTextFastPath(t *testing.T) {
	esc := testutil.Esc(`~u0061`)
	for limit := range 9 {
		raw := dcdString(esc, limit)
		require.Len(t, raw, 6*limit+2)
		got, derr := decodeText([]byte(raw), limit)
		require.Nil(t, derr, "limit %d", limit)
		assert.Equal(t, strings.Repeat("a", limit), got)

		_, derr = decodeText([]byte(dcdString(esc, limit+1)), limit)
		assertDecodeError(t, derr, FailureResponseTooLarge, "")
		_, derr = decodeText([]byte(dcdString("a", limit+1)), limit)
		assertDecodeError(t, derr, FailureResponseTooLarge, "")
	}
}

func TestDecodeTextPtr(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want *string
		kind FailureKind
	}{
		{name: dcdAbsent, raw: dcdAbsent},
		{name: jsonNull, raw: jsonNull},
		{name: "empty string is kept", raw: `""`, want: new("")},
		{name: "text", raw: `"gmail.com"`, want: new("gmail.com")},
		{name: "escaped", raw: testutil.Esc(`"~u0067mail.com"`), want: new("gmail.com")},
		{name: "exactly 4 KiB", raw: dcdString("x", maxTextBytes), want: new(strings.Repeat("x", maxTextBytes))},
		{name: "over 4 KiB", raw: dcdString("x", maxTextBytes+1), kind: FailureResponseTooLarge},
		{name: "number", raw: `1`, kind: FailureMalformedResponse},
		{name: "boolean", raw: dcdFalse, kind: FailureMalformedResponse},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, derr := decodeTextPtr(dcdRaw(tt.raw))
			if tt.kind != "" {
				assertDecodeError(t, derr, tt.kind, "")
				assert.Nil(t, got)
				return
			}
			assert.Nil(t, derr)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDecodeRequiredText(t *testing.T) {
	tests := []struct {
		name  string
		raw   string
		want  string
		cause error
	}{
		{name: "text", raw: `"` + internalEmail + `"`, want: internalEmail},
		{name: "escaped", raw: testutil.Esc(`"~u0061@example.com"`), want: "a@example.com"},
		{name: "a lone surrogate is text", raw: testutil.Esc(`"~udfff"`), want: strictFFFD},
		{name: dcdAbsent, raw: dcdAbsent, cause: errMissingField},
		{name: jsonNull, raw: jsonNull, cause: errMissingField},
		{name: "empty string", raw: `""`, cause: errMissingField},
		{name: "number", raw: `0`, cause: errWrongType},
		{name: "object", raw: `{}`, cause: errWrongType},
		{name: "over 4 KiB", raw: dcdString("x", maxTextBytes+1), cause: errFieldTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, derr := decodeRequiredText(dcdRaw(tt.raw))
			if tt.cause != nil {
				require.NotNil(t, derr)
				requireCause(t, derr, tt.cause)
				assert.Empty(t, derr.field)
				assert.Empty(t, got)
				return
			}
			assert.Nil(t, derr)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDecodeValidToken(t *testing.T) {
	tests := []struct {
		s    string
		want bool
	}{
		{"deliverable", true},
		{"accepted_email", true},
		{"a", true},
		{"0", true},
		{"_", true},
		{"z9_", true},
		{strings.Repeat("a", maxTokenBytes), true},
		{strings.Repeat("a", maxTokenBytes+1), false},
		{"", false},
		{"Deliverable", false},
		{"RISKY", false},
		{"low-quality", false},
		{"low quality", false},
		{" risky", false},
		{"risky\n", false},
		{"é", false},
		{"a`", false},
		{"a{", false},
		{"a/", false},
		{"a:", false},
		{"a^", false},
		{"a@", false},
		{"a.b", false},
		{"a\x00", false},
		{strictKelvin, false},
	}
	for _, tt := range tests {
		t.Run(strconv.Quote(tt.s), func(t *testing.T) {
			assert.Equal(t, tt.want, validToken(tt.s))
		})
	}
}

func TestDecodeToken(t *testing.T) {
	esc := testutil.Esc
	tests := []struct {
		name     string
		raw      string
		required bool
		want     string
		kind     FailureKind
	}{
		{name: "required token", raw: `"deliverable"`, required: true, want: "deliverable"},
		{name: "required unknown token kept", raw: `"brand_new_state"`, required: true, want: "brand_new_state"},
		{name: "required escaped token", raw: esc(`"~u0072isky"`), required: true, want: "risky"},
		{name: "required 64 bytes", raw: dcdString("x", 64), required: true, want: strings.Repeat("x", 64)},
		{name: "required 64 escaped bytes, at the fast path's edge", raw: dcdString(esc(`~u0078`), 64), required: true, want: strings.Repeat("x", 64)},
		{name: "required absent", raw: dcdAbsent, required: true, kind: FailureMalformedResponse},
		{name: "required null", raw: jsonNull, required: true, kind: FailureMalformedResponse},
		{name: "required empty string is missing", raw: `""`, required: true, kind: FailureMalformedResponse},
		{name: "required number", raw: `1`, required: true, kind: FailureMalformedResponse},
		{name: "required boolean", raw: dcdFalse, required: true, kind: FailureMalformedResponse},
		{name: "required array", raw: `["risky"]`, required: true, kind: FailureMalformedResponse},
		{name: "required uppercase", raw: `"RISKY"`, required: true, kind: FailureUnexpectedResponse},
		{name: "required 65 bytes", raw: dcdString("x", 65), required: true, kind: FailureUnexpectedResponse},
		{name: "required 65 escaped bytes, past the fast path", raw: dcdString(esc(`~u0078`), 65), required: true, kind: FailureUnexpectedResponse},
		{name: "required escaped space", raw: esc(`"risky~u0020"`), required: true, kind: FailureUnexpectedResponse},
		{name: "optional token", raw: `"low_quality"`, want: "low_quality"},
		{name: "optional absent", raw: dcdAbsent, want: ""},
		{name: "optional null", raw: jsonNull, want: ""},
		{name: "optional empty string is unset", raw: `""`, want: ""},
		{name: "optional number", raw: `0`, kind: FailureMalformedResponse},
		{name: "optional object", raw: `{}`, kind: FailureMalformedResponse},
		{name: "optional bad token", raw: `"Low Quality"`, kind: FailureUnexpectedResponse},
		{name: "optional lone surrogate", raw: esc(`"~ud800"`), kind: FailureUnexpectedResponse},
		{name: "optional Kelvin sign", raw: `"` + strictKelvin + `"`, kind: FailureUnexpectedResponse},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, derr := decodeToken(dcdRaw(tt.raw), tt.required)
			if tt.kind != "" {
				assertDecodeError(t, derr, tt.kind, "")
				assert.Empty(t, got)
				return
			}
			assert.Nil(t, derr)
			assert.Equal(t, tt.want, got)
		})
	}
	_, derr := decodeToken(nil, true)
	requireCause(t, derr, errMissingField)
	_, derr = decodeToken([]byte(`""`), true)
	requireCause(t, derr, errMissingField, `a required "" is missing, not an invalid token`)
	_, derr = decodeToken([]byte(`"A"`), true)
	requireCause(t, derr, errInvalidToken)
	_, derr = decodeToken([]byte(`7`), false)
	requireCause(t, derr, errWrongType)
}

func TestDecodeBool(t *testing.T) {
	tests := []struct {
		raw  string
		want *bool
		bad  bool
	}{
		{raw: strictTrue, want: new(true)},
		{raw: dcdFalse, want: new(false)},
		{raw: jsonNull},
		{raw: dcdAbsent},
		{raw: `"true"`, bad: true},
		{raw: `"false"`, bad: true},
		{raw: `1`, bad: true},
		{raw: `0`, bad: true},
		{raw: `[]`, bad: true},
		{raw: `{}`, bad: true},
		{raw: `""`, bad: true},
		{raw: `True`, bad: true},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			got, derr := decodeBool(dcdRaw(tt.raw))
			if tt.bad {
				assertDecodeError(t, derr, FailureMalformedResponse, "")
				requireCause(t, derr, errWrongType)
				assert.Nil(t, got)
				return
			}
			assert.Nil(t, derr)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDecodeParseInt(t *testing.T) {
	tests := []struct {
		lit         string
		n           int64
		ok, inRange bool
	}{
		{"0", 0, true, true},
		{"-0", 0, true, true},
		{"1", 1, true, true},
		{"-1", -1, true, true},
		{"412", 412, true, true},
		{"9223372036854775807", math.MaxInt64, true, true},
		{"-9223372036854775808", math.MinInt64, true, true},
		{"9223372036854775808", 0, true, false},
		{"-9223372036854775809", 0, true, false},
		{"99999999999999999999999999", 0, true, false},
		{"1.0", 0, false, false},
		{"1.5", 0, false, false},
		{"0.0", 0, false, false},
		{"1e3", 0, false, false},
		{"1E3", 0, false, false},
		{"1e+3", 0, false, false},
		{"0e0", 0, false, false},
		{"-1.5e-3", 0, false, false},
		{"99999999999999999999.5", 0, false, false},
		{"1e400", 0, false, false},
		{"1e999999999999999999999", 0, false, false},
		{`"1"`, 0, false, false},
		{strictTrue, 0, false, false},
		{jsonNull, 0, false, false},
		{"[1]", 0, false, false},
		{"+1", 0, false, false},
		{"", 0, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.lit, func(t *testing.T) {
			n, ok, inRange := parseInt([]byte(tt.lit))
			assert.Equal(t, tt.n, n)
			assert.Equal(t, tt.ok, ok, "ok")
			assert.Equal(t, tt.inRange, inRange, "inRange")
		})
	}
}

// TestDecodeLeadingZerosNeverReachParseInt proves that a leading zero, which
// strconv.ParseInt would accept, never reaches it: the strict check already
// rejects the body as invalid JSON.
func TestDecodeLeadingZerosNeverReachParseInt(t *testing.T) {
	for _, body := range []string{`{"score":01}`, `{"score":-01}`, `{"score":00}`, `{"duration":01.5}`} {
		kind, cause := checkStrict([]byte(body))
		assert.Equal(t, FailureMalformedResponse, kind, body)
		require.ErrorIs(t, cause, errInvalidJSON, body)
	}
}

func TestDecodeIntRange(t *testing.T) {
	tests := []struct {
		name   string
		raw    string
		lo, hi int64
		want   *int
		cause  error
	}{
		{name: "score zero", raw: `0`, hi: maxScore, want: new(0)},
		{name: "score negative zero", raw: `-0`, hi: maxScore, want: new(0)},
		{name: "score hundred", raw: `100`, hi: maxScore, want: new(100)},
		{name: "score typical", raw: `97`, hi: maxScore, want: new(97)},
		{name: "score absent", raw: dcdAbsent, hi: maxScore},
		{name: "score null", raw: jsonNull, hi: maxScore},
		{name: "score over", raw: `101`, hi: maxScore, cause: errOutOfRange},
		{name: "score negative", raw: `-1`, hi: maxScore, cause: errOutOfRange},
		{name: "score overflow", raw: `9223372036854775808`, hi: maxScore, cause: errOutOfRange},
		{name: "score fraction", raw: `50.5`, hi: maxScore, cause: errWrongType},
		{name: "score integral fraction", raw: `50.0`, hi: maxScore, cause: errWrongType},
		{name: "score exponent", raw: `1e2`, hi: maxScore, cause: errWrongType},
		{name: "score huge exponent", raw: `1e999999999`, hi: maxScore, cause: errWrongType},
		{name: "score quoted", raw: `"50"`, hi: maxScore, cause: errWrongType},
		{name: "score boolean", raw: strictTrue, hi: maxScore, cause: errWrongType},
		{name: "birth year at the bound", raw: `9999`, hi: maxBirthYear, want: new(9999)},
		{name: "birth year over", raw: `10000`, hi: maxBirthYear, cause: errOutOfRange},
		{name: "birth year zero", raw: `0`, hi: maxBirthYear, want: new(0)},
		{name: "a lower bound", raw: `4`, lo: 5, hi: 9, cause: errOutOfRange},
		{name: "at a lower bound", raw: `5`, lo: 5, hi: 9, want: new(5)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, derr := decodeIntRange(dcdRaw(tt.raw), tt.lo, tt.hi)
			if tt.cause != nil {
				assertDecodeError(t, derr, FailureMalformedResponse, "")
				requireCause(t, derr, tt.cause)
				assert.Nil(t, got)
				return
			}
			assert.Nil(t, derr)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDecodeCount(t *testing.T) {
	tests := []struct {
		name  string
		raw   string
		want  *int64
		cause error
	}{
		{name: dcdAbsent, raw: dcdAbsent},
		{name: jsonNull, raw: jsonNull},
		{name: "zero", raw: `0`, want: new(int64(0))},
		{name: "negative zero", raw: `-0`, want: new(int64(0))},
		{name: "positive", raw: `4321`, want: new(int64(4321))},
		{name: "max int64", raw: `9223372036854775807`, want: new(int64(math.MaxInt64))},
		{name: "quoted", raw: `"1"`, cause: errWrongType},
		{name: "fraction", raw: `1.0`, cause: errWrongType},
		{name: "exponent", raw: `1e3`, cause: errWrongType},
		{name: "huge exponent", raw: `1e400`, cause: errWrongType},
		{name: "boolean", raw: strictTrue, cause: errWrongType},
		{name: "object", raw: `{}`, cause: errWrongType},
		{name: "array", raw: `[1]`, cause: errWrongType},
		{name: "negative", raw: `-1`, cause: errOutOfRange},
		{name: "most negative", raw: `-9223372036854775808`, cause: errOutOfRange},
		{name: "overflow", raw: `9223372036854775808`, cause: errOutOfRange},
		{name: "negative overflow", raw: `-9223372036854775809`, cause: errOutOfRange},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, derr := decodeCount(dcdRaw(tt.raw))
			if tt.cause != nil {
				assertDecodeError(t, derr, FailureMalformedResponse, "")
				requireCause(t, derr, tt.cause)
				assert.Nil(t, got)
				return
			}
			assert.Nil(t, derr)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDecodeRequiredCount(t *testing.T) {
	tests := []struct {
		name  string
		raw   string
		want  int64
		cause error
	}{
		{name: "zero", raw: `0`, want: 0},
		{name: "positive", raw: `3`, want: 3},
		{name: "max int64", raw: `9223372036854775807`, want: math.MaxInt64},
		{name: dcdAbsent, raw: dcdAbsent, cause: errMissingField},
		{name: jsonNull, raw: jsonNull, cause: errMissingField},
		{name: "quoted", raw: `"3"`, cause: errWrongType},
		{name: "fraction", raw: `1.0`, cause: errWrongType},
		{name: "exponent", raw: `1e3`, cause: errWrongType},
		{name: "negative", raw: `-1`, cause: errOutOfRange},
		{name: "overflow", raw: `9223372036854775808`, cause: errOutOfRange},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, derr := decodeRequiredCount(dcdRaw(tt.raw))
			if tt.cause != nil {
				assertDecodeError(t, derr, FailureMalformedResponse, "")
				requireCause(t, derr, tt.cause)
				assert.Zero(t, got)
				return
			}
			assert.Nil(t, derr)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDecodeSeconds(t *testing.T) {
	tests := []struct {
		name  string
		raw   string
		want  float64
		null  bool
		cause error
	}{
		{name: "zero", raw: `0`, want: 0},
		{name: "fraction", raw: `1.25`, want: 1.25},
		{name: "exponent", raw: `1e2`, want: 100},
		{name: "uppercase exponent", raw: `1.5E1`, want: 15},
		{name: "negative exponent", raw: `5e-1`, want: 0.5},
		{name: "integer", raw: `3`, want: 3},
		{name: "large", raw: `1e300`, want: 1e300},
		{name: "negative zero", raw: `-0`, want: 0},
		{name: "negative float zero", raw: `-0.0`, want: 0},
		{name: "negative zero with an exponent", raw: `-0e5`, want: 0},
		{name: "positive underflow", raw: `1e-400`, want: 0},
		{name: "negative underflow", raw: `-1e-400`, want: 0},
		{name: "zero with a huge exponent", raw: `0e999999999`, want: 0},
		{name: dcdAbsent, raw: dcdAbsent, null: true},
		{name: jsonNull, raw: jsonNull, null: true},
		{name: "negative", raw: `-1`, cause: errOutOfRange},
		{name: "slightly negative", raw: `-0.001`, cause: errOutOfRange},
		{name: "infinite", raw: `1e400`, cause: errOutOfRange},
		{name: "huge exponent", raw: `1e999999999999999999`, cause: errOutOfRange},
		{name: "negative infinite", raw: `-1e400`, cause: errOutOfRange},
		{name: "quoted", raw: `"1.25"`, cause: errWrongType},
		{name: "boolean", raw: strictTrue, cause: errWrongType},
		{name: "object", raw: `{"s":1}`, cause: errWrongType},
		{name: "array", raw: `[1]`, cause: errWrongType},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, derr := decodeSeconds(dcdRaw(tt.raw))
			switch {
			case tt.cause != nil:
				assertDecodeError(t, derr, FailureMalformedResponse, "")
				requireCause(t, derr, tt.cause)
				assert.Nil(t, got)
			case tt.null:
				assert.Nil(t, derr)
				assert.Nil(t, got)
			default:
				assert.Nil(t, derr)
				require.NotNil(t, got)
				assert.InDelta(t, tt.want, *got, 0)
				assert.False(t, math.Signbit(*got), "-0 decodes as +0")
			}
		})
	}
}

// dcdVisibleASCII returns every visible ASCII byte, 0x21 to 0x7E, in order.
func dcdVisibleASCII() string {
	b := make([]byte, 0, 0x7e-0x21+1)
	for c := byte(0x21); c <= 0x7e; c++ {
		b = append(b, c)
	}
	return string(b)
}

func TestDecodeBatchID(t *testing.T) {
	esc := testutil.Esc
	visible := dcdVisibleASCII()
	tests := []struct {
		name string
		raw  string
		want string
		kind FailureKind
	}{
		{name: "typical", raw: `"5a0e9b5d6f0e6d0012bc"`, want: "5a0e9b5d6f0e6d0012bc"},
		{name: "one byte", raw: `"q"`, want: "q"},
		{name: "every visible byte", raw: testutil.JSONQuote(visible), want: visible},
		{name: "256 bytes", raw: dcdString("q", maxBatchIDBytes), want: strings.Repeat("q", maxBatchIDBytes)},
		{name: "256 bytes once decoded", raw: dcdString(esc(`~u0071`), maxBatchIDBytes), want: strings.Repeat("q", maxBatchIDBytes)},
		{name: "escaped quote", raw: esc(`"a~"b"`), want: `a"b`},
		{name: "escaped solidus", raw: esc(`"a~/b"`), want: "a/b"},
		{name: "escaped backslash", raw: esc(`"a~~b"`), want: `a\b`},
		{name: dcdAbsent, raw: dcdAbsent, want: ""},
		{name: jsonNull, raw: jsonNull, want: ""},
		{name: "257 bytes", raw: dcdString("q", maxBatchIDBytes+1), kind: FailureUnexpectedResponse},
		{name: "257 escaped bytes, past the fast path", raw: dcdString(esc(`~u0071`), maxBatchIDBytes+1), kind: FailureUnexpectedResponse},
		{name: "empty", raw: `""`, kind: FailureUnexpectedResponse},
		{name: "space", raw: `"a b"`, kind: FailureUnexpectedResponse},
		{name: "escaped space", raw: esc(`"a~u0020"`), kind: FailureUnexpectedResponse},
		{name: "control escape", raw: esc(`"a~u0001"`), kind: FailureUnexpectedResponse},
		{name: "newline escape", raw: esc(`"a~n"`), kind: FailureUnexpectedResponse},
		{name: "DEL", raw: esc(`"a~u007f"`), kind: FailureUnexpectedResponse},
		{name: "non-ASCII", raw: "\"é\"", kind: FailureUnexpectedResponse},
		{name: "escaped non-ASCII", raw: esc(`"~u00e9"`), kind: FailureUnexpectedResponse},
		{name: "lone surrogate", raw: esc(`"~ud800"`), kind: FailureUnexpectedResponse},
		{name: "number", raw: `12345`, kind: FailureMalformedResponse},
		{name: "object", raw: `{"id":"q"}`, kind: FailureMalformedResponse},
		{name: "boolean", raw: strictTrue, kind: FailureMalformedResponse},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, derr := decodeBatchID(dcdRaw(tt.raw))
			if tt.kind != "" {
				assertDecodeError(t, derr, tt.kind, "")
				assert.Empty(t, got)
				return
			}
			assert.Nil(t, derr)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, got != "", validBatchID(got))
		})
	}
}

// dcdCounts returns a JSON object of n counts, k0 to k(n-1), each holding
// its index, plus extra before the closing brace.
func dcdCounts(n int, extra string) string { return strictManyKeys("k", n, extra) }

// dcdOnlyA documents the count key "a" alone.
func dcdOnlyA(k string) bool { return k == "a" }

func TestDecodeCounts(t *testing.T) {
	esc := testutil.Esc
	full, nearFull := map[string]int64{}, map[string]int64{"y": 1}
	for i := range maxCountKeys {
		full["k"+strconv.Itoa(i)] = int64(i)
		if i < maxCountKeys-1 {
			nearFull["k"+strconv.Itoa(i)] = int64(i)
		}
	}
	tests := []struct {
		name  string
		raw   string
		want  map[string]int64
		kind  FailureKind
		field string
	}{
		{name: dcdAbsent, raw: dcdAbsent},
		{name: jsonNull, raw: jsonNull},
		{name: "empty object", raw: `{}`, want: map[string]int64{}},
		{name: "open keys kept as sent", raw: `{"accepted_email":3,"brand_new_reason":0}`, want: map[string]int64{"accepted_email": 3, "brand_new_reason": 0}},
		{name: "escaped key", raw: esc(`{"~u0061":1}`), want: map[string]int64{"a": 1}},
		{name: "max count", raw: `{"total":9223372036854775807}`, want: map[string]int64{"total": math.MaxInt64}},
		{name: "256 keys", raw: dcdCounts(maxCountKeys, ""), want: full},
		{name: "257 keys", raw: dcdCounts(maxCountKeys+1, ""), kind: FailureResponseTooLarge, field: dcdReasons},
		{name: "257th key bad too", raw: dcdCounts(maxCountKeys, `,"BAD":1`), kind: FailureResponseTooLarge, field: dcdReasons},
		{name: "uppercase key", raw: `{"Accepted_email":1}`, kind: FailureUnexpectedResponse, field: dcdReasons},
		{name: "empty key", raw: `{"":1}`, kind: FailureUnexpectedResponse, field: dcdReasons},
		{name: "key with a space", raw: `{"a b":1}`, kind: FailureUnexpectedResponse, field: dcdReasons},
		{name: "65-byte key", raw: `{` + dcdString("k", 65) + `:1}`, kind: FailureUnexpectedResponse, field: dcdReasons},
		{name: "non-ASCII key", raw: "{\"é\":1}", kind: FailureUnexpectedResponse, field: dcdReasons},
		{name: "null value is absent", raw: `{"a":null}`, want: map[string]int64{}},
		{name: "null values are absent", raw: `{"a":1,"b":null,"c":2}`, want: map[string]int64{"a": 1, "c": 2}},
		{name: "a null value's key is still checked", raw: `{"Bad":null}`, kind: FailureUnexpectedResponse, field: dcdReasons},
		{name: "null values do not count toward the bound", raw: dcdCounts(maxCountKeys-1, `,"z":null,"y":1`), want: nearFull},
		{name: "a null value past 256 keys", raw: dcdCounts(maxCountKeys, `,"z":null`), kind: FailureResponseTooLarge, field: dcdReasons},
		{name: "quoted value", raw: `{"a":"1"}`, kind: FailureMalformedResponse, field: "reason_counts.a"},
		{name: "fraction value", raw: `{"a":1.5}`, kind: FailureMalformedResponse, field: "reason_counts.a"},
		{name: "object value", raw: `{"a":{}}`, kind: FailureMalformedResponse, field: "reason_counts.a"},
		{name: "negative value", raw: `{"ok":0,"a":-1}`, kind: FailureMalformedResponse, field: "reason_counts.a"},
		{name: "overflow value", raw: `{"a":9223372036854775808}`, kind: FailureMalformedResponse, field: "reason_counts.a"},
		{name: "first failure wins", raw: `{"a":-1,"B":1}`, kind: FailureMalformedResponse, field: "reason_counts.a"},
		{name: "an undocumented key is not named", raw: `{"zz":-1}`, kind: FailureMalformedResponse, field: dcdReasons},
		{name: "an undocumented key is not named for its type", raw: `{"a":1,"zz":"1"}`, kind: FailureMalformedResponse, field: dcdReasons},
		{name: "array", raw: `[]`, kind: FailureMalformedResponse, field: dcdReasons},
		{name: "number", raw: `1`, kind: FailureMalformedResponse, field: dcdReasons},
		{name: "string", raw: `"a"`, kind: FailureMalformedResponse, field: dcdReasons},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, derr := decodeCounts(dcdRaw(tt.raw), dcdReasons, dcdOnlyA)
			if tt.kind != "" {
				assertDecodeError(t, derr, tt.kind, tt.field)
				assert.Nil(t, got)
				return
			}
			assert.Nil(t, derr)
			assert.Equal(t, tt.want, got)
			if tt.want != nil {
				assert.NotNil(t, got, "{} is an empty map, not nil")
			}
		})
	}

	got, derr := decodeCounts([]byte(`{"a":null}`), dcdTotals, dcdOnlyA)
	assert.Nil(t, derr, "a null count is absent")
	assert.Equal(t, map[string]int64{}, got)
	_, derr = decodeCounts([]byte(`{"a":-5}`), dcdTotals, dcdOnlyA)
	assertDecodeError(t, derr, FailureMalformedResponse, "total_counts.a")
	requireCause(t, derr, errOutOfRange)
	_, derr = decodeCounts([]byte(dcdCounts(maxCountKeys+1, "")), dcdTotals, dcdOnlyA)
	requireCause(t, derr, errFieldTooLarge)
	_, derr = decodeCounts([]byte(`{"X":1}`), dcdTotals, dcdOnlyA)
	requireCause(t, derr, errInvalidToken)
}

// TestDecodeCountsDocumentedKeys proves that a failing count's path names
// its key only when the key is documented for its object: a Reason in
// reason_counts; a State, "processed" or "total" in total_counts. Any other
// key is provider data, which could even have the shape of the API key.
func TestDecodeCountsDocumentedKeys(t *testing.T) {
	reasons := []Reason{
		ReasonAcceptedEmail, ReasonLowQuality, ReasonLowDeliverability, ReasonInvalidEmail, ReasonInvalidDomain,
		ReasonRejectedEmail, ReasonInvalidSMTP, ReasonNoConnect, ReasonTimeout, ReasonUnavailableSMTP,
		ReasonUnexpectedError,
	}
	for _, r := range reasons {
		_, derr := decodeBatch([]byte(`{"reason_counts":{"`+string(r)+`":-1}}`), dcdBatchID)
		assertDecodeError(t, derr, FailureMalformedResponse, "reason_counts."+string(r))
	}
	totals := []string{"deliverable", "undeliverable", "risky", "unknown", "duplicate", dcdProgress, "total"}
	for _, k := range totals {
		assert.True(t, documentedTotal(k), k)
		_, derr := decodeBatch([]byte(`{"total_counts":{"`+k+`":"1"}}`), dcdBatchID)
		assertDecodeError(t, derr, FailureMalformedResponse, "total_counts."+k)
	}
	for _, k := range []string{"brand_new_reason", "deliverable", "processed", internalKey} {
		_, derr := decodeBatch([]byte(`{"reason_counts":{"`+k+`":-1}}`), dcdBatchID)
		assertDecodeError(t, derr, FailureMalformedResponse, dcdReasons)
	}
	for _, k := range []string{"accepted_email", "brand_new_total", "processed_count", internalKey} {
		assert.False(t, documentedTotal(k), k)
		_, derr := decodeBatch([]byte(`{"total_counts":{"`+k+`":1.5}}`), dcdBatchID)
		assertDecodeError(t, derr, FailureMalformedResponse, dcdTotals)
		requireCause(t, derr, errWrongType)
	}
	got, derr := decodeBatch([]byte(`{"reason_counts":{"`+internalKey+`":1},"total_counts":{"brand_new":2}}`), dcdBatchID)
	require.Nil(t, derr, "undocumented keys are kept when their counts are valid")
	assert.Equal(t, map[Reason]int64{internalKey: 1}, got.ReasonCounts)
	assert.Equal(t, map[string]int64{"brand_new": 2}, got.TotalCounts)
}

func TestDecodeASCIIRange(t *testing.T) {
	assert.True(t, asciiRange("", 0x21, 0x7e))
	assert.True(t, asciiRange("!~", 0x21, 0x7e), "both bounds are inclusive")
	assert.False(t, asciiRange(" ", 0x21, 0x7e))
	assert.False(t, asciiRange("\x7f", 0x21, 0x7e))
	assert.False(t, asciiRange("ab\x00", 0x21, 0x7e))
	assert.False(t, asciiRange("é", 0x21, 0x7e))
	assert.True(t, asciiRange("abc", 'a', 'c'))
	assert.False(t, asciiRange("abcd", 'a', 'c'))
}

func TestDecodeTruncateUTF8(t *testing.T) {
	tests := []struct {
		name string
		s    string
		n    int
		want string
	}{
		{name: "shorter", s: "abc", n: 5, want: "abc"},
		{name: "exact", s: "abc", n: 3, want: "abc"},
		{name: "ASCII cut", s: "abcdef", n: 4, want: "abcd"},
		{name: "zero", s: "abc", n: 0, want: ""},
		{name: "before a two-byte rune", s: "abé", n: 2, want: "ab"},
		{name: "inside a two-byte rune", s: "abé", n: 3, want: "ab"},
		{name: "inside a three-byte rune", s: "a€", n: 3, want: "a"},
		{name: "after a three-byte rune", s: "a€b", n: 4, want: "a€"},
		{name: "inside a four-byte rune", s: "a" + strictEmoji, n: 4, want: "a"},
		{name: "four-byte rune at the start", s: strictEmoji, n: 3, want: ""},
		{name: "only continuation bytes", s: "\x80\x80\x80", n: 2, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, truncateUTF8(tt.s, tt.n))
		})
	}

	// For every cut of a mixed string: a prefix, within n, never splitting a
	// rune, and at most three bytes short.
	s := strings.Repeat("aé€"+strictEmoji, 40)
	for n := 0; n <= len(s)+1; n++ {
		got := truncateUTF8(s, n)
		require.True(t, strings.HasPrefix(s, got), "n=%d", n)
		require.LessOrEqual(t, len(got), n)
		require.True(t, utf8.ValidString(got), "n=%d", n)
		require.GreaterOrEqual(t, len(got), min(n, len(s))-3, "n=%d", n)
	}
}

// dcdFullVerification is a verification body with every member set, and the
// Verification it decodes to.
func dcdFullVerification() (string, Verification) {
	body := `{"accept_all":false,"birth_year":1990,"did_you_mean":"jane.doe@mail.example.com","disposable":false,` +
		`"domain":"example.com","duration":0.493,"email":"jane.doe@example.com","first_name":"Jane","free":true,` +
		`"full_name":"Jane Doe","gender":"female","last_name":"Doe","mailbox_full":false,` +
		`"mx_record":"mx.example.com","no_reply":false,"reason":"accepted_email","role":false,"score":97,` +
		`"smtp_provider":"Example Mail","state":"deliverable","tag":"","user":"jane.doe","extra":{"nested":[1]}}`
	return body, Verification{
		Email: internalEmail, State: StateDeliverable, Reason: ReasonAcceptedEmail, Score: new(97),
		AcceptAll: new(false), Disposable: new(false), Free: new(true), MailboxFull: new(false),
		NoReply: new(false), Role: new(false), BirthYear: new(1990), DidYouMean: new("jane.doe@mail.example.com"),
		Domain: new("example.com"), User: new("jane.doe"), Tag: new(""), MXRecord: new("mx.example.com"),
		SMTPProvider: new("Example Mail"), FirstName: new("Jane"), LastName: new("Doe"), FullName: new("Jane Doe"),
		Gender: new("female"), Duration: new(0.493),
	}
}

func TestDecodeVerificationValues(t *testing.T) {
	body, want := dcdFullVerification()
	for _, required := range []bool{true, false} {
		got, derr := decodeVerification(dcdStrict(t, body), path{}, required)
		require.Nil(t, derr)
		assert.Equal(t, want, got)
	}

	got, derr := decodeVerification([]byte(`{"email":"a@example.com","state":"unknown"}`), path{}, true)
	require.Nil(t, derr)
	assert.Equal(t, Verification{Email: "a@example.com", State: StateUnknown}, got, "a minimal answer")

	got, derr = decodeVerification([]byte(`{}`), path{base: dcdEmails, index: 3}, false)
	require.Nil(t, derr)
	assert.Equal(t, Verification{}, got, "a row may hold nothing")

	nulls := `{"email":null,"state":null,"reason":null,"score":null,"accept_all":null,"tag":null,"duration":null}`
	got, derr = decodeVerification([]byte(nulls), path{}, false)
	require.Nil(t, derr)
	assert.Equal(t, Verification{}, got, "null is unset")

	got, derr = decodeVerification([]byte(`{"email":"","state":"","reason":"","duration":-0}`), path{}, false)
	require.Nil(t, derr)
	assert.Equal(t, Verification{Duration: new(0.0)}, got, `"" is unset in a row, and -0 is +0`)
	require.NotNil(t, got.Duration)
	assert.False(t, math.Signbit(*got.Duration))
}

// dcdBadMember is a value one verification member must refuse, and how.
type dcdBadMember struct {
	member string
	raw    string
	kind   FailureKind
	// required limits the case to required (true) or optional (false)
	// decoding; both when nil.
	required *bool
}

// dcdBadMembers returns, for every verification member, values it refuses.
func dcdBadMembers() []dcdBadMember {
	malformed, unexpected, tooLarge := FailureMalformedResponse, FailureUnexpectedResponse, FailureResponseTooLarge
	long := dcdString("x", maxTextBytes+1)
	cases := make([]dcdBadMember, 0, 80)
	cases = append(cases, []dcdBadMember{
		{member: "email", raw: dcdAbsent, kind: malformed, required: new(true)},
		{member: "email", raw: jsonNull, kind: malformed, required: new(true)},
		{member: "email", raw: `""`, kind: malformed, required: new(true)},
		{member: "email", raw: `1`, kind: malformed},
		{member: "email", raw: `["a@example.com"]`, kind: malformed},
		{member: "email", raw: long, kind: tooLarge},
		{member: "state", raw: dcdAbsent, kind: malformed, required: new(true)},
		{member: "state", raw: jsonNull, kind: malformed, required: new(true)},
		{member: "state", raw: `""`, kind: malformed, required: new(true)},
		{member: "state", raw: `"Deliverable"`, kind: unexpected},
		{member: "state", raw: `"safe to send"`, kind: unexpected},
		{member: "state", raw: dcdString("s", 65), kind: unexpected},
		{member: "state", raw: `1`, kind: malformed},
		{member: "reason", raw: `"Accepted"`, kind: unexpected},
		{member: "reason", raw: `false`, kind: malformed},
		{member: "score", raw: `101`, kind: malformed},
		{member: "score", raw: `-1`, kind: malformed},
		{member: "score", raw: `97.5`, kind: malformed},
		{member: "score", raw: `"97"`, kind: malformed},
		{member: "birth_year", raw: `10000`, kind: malformed},
		{member: "birth_year", raw: `-1`, kind: malformed},
		{member: "birth_year", raw: `1990.0`, kind: malformed},
		{member: "duration", raw: `-1`, kind: malformed},
		{member: "duration", raw: `1e400`, kind: malformed},
		{member: "duration", raw: `"0.4"`, kind: malformed},
	}...)
	for _, m := range []string{"accept_all", "disposable", "free", "mailbox_full", "no_reply", "role"} {
		cases = append(cases,
			dcdBadMember{member: m, raw: `"true"`, kind: malformed},
			dcdBadMember{member: m, raw: `1`, kind: malformed},
			dcdBadMember{member: m, raw: `0`, kind: malformed},
		)
	}
	for _, m := range []string{
		"did_you_mean", "domain", "user", "tag", "mx_record", "smtp_provider", "first_name", "last_name",
		"full_name", "gender",
	} {
		cases = append(cases,
			dcdBadMember{member: m, raw: `1`, kind: malformed},
			dcdBadMember{member: m, raw: `{}`, kind: malformed},
			dcdBadMember{member: m, raw: long, kind: tooLarge},
		)
	}
	return cases
}

// dcdVerificationWith returns a valid verification body with member set to
// raw, which may be dcdAbsent.
func dcdVerificationWith(member, raw string) string {
	email, state := dcdMember{"email", `"` + internalEmail + `"`}, dcdMember{"state", `"deliverable"`}
	switch member {
	case email.key:
		email.raw = raw
	case state.key:
		state.raw = raw
	default:
		return dcdObject(email, state, dcdMember{member, raw})
	}
	return dcdObject(email, state)
}

// TestDecodeVerificationMembers proves that every member refuses each bad
// value with the right kind, at the right path, for an answer and for a row.
func TestDecodeVerificationMembers(t *testing.T) {
	names := verificationNames()
	covered := map[string]bool{}
	for _, tt := range dcdBadMembers() {
		covered[tt.member] = true
		for _, required := range []bool{true, false} {
			if tt.required != nil && *tt.required != required {
				continue
			}
			t.Run(tt.member+"="+tt.raw[:min(len(tt.raw), 12)]+"/"+strconv.FormatBool(required), func(t *testing.T) {
				body := dcdVerificationWith(tt.member, tt.raw)
				got, derr := decodeVerification([]byte(body), path{}, required)
				assertDecodeError(t, derr, tt.kind, tt.member)
				assert.Equal(t, Verification{}, got, "nothing alongside a failure")

				_, derr = decodeVerification([]byte(body), path{base: dcdEmails, index: 3}, required)
				assertDecodeError(t, derr, tt.kind, "emails[3]."+tt.member)
			})
		}
	}
	for _, name := range names {
		assert.True(t, covered[name], "member %s has failure cases", name)
	}
}

// TestDecodeVerificationRequiredMembers proves that email and state are
// optional in a row and required in an answer.
func TestDecodeVerificationRequiredMembers(t *testing.T) {
	tests := []struct {
		body  string
		field string
		cause error
	}{
		{body: `{"state":"deliverable"}`, field: "email", cause: errMissingField},
		{body: `{"email":"","state":"deliverable"}`, field: "email", cause: errMissingField},
		{body: `{"email":"a@example.com"}`, field: "state", cause: errMissingField},
		{body: `{"email":"a@example.com","state":""}`, field: "state", cause: errMissingField},
		{body: `{}`, field: "email", cause: errMissingField},
	}
	for _, tt := range tests {
		t.Run(tt.body, func(t *testing.T) {
			_, derr := decodeVerification([]byte(tt.body), path{}, true)
			require.NotNil(t, derr)
			assert.Equal(t, tt.field, derr.field)
			requireCause(t, derr, tt.cause)

			_, derr = decodeVerification([]byte(tt.body), path{base: dcdEmails, index: 0}, false)
			assert.Nil(t, derr, "a row needs neither")
		})
	}
}

// TestDecodeVerificationCheckOrder proves that members are checked in their
// declared order, whatever the body's order: the first failing member in
// that order is the one reported.
func TestDecodeVerificationCheckOrder(t *testing.T) {
	tests := []struct {
		name  string
		body  string
		field string
	}{
		{name: "score before duration", body: `{"duration":-1,"score":101,"email":"a@example.com","state":"risky"}`, field: "score"},
		{name: "email before everything", body: `{"gender":1,"duration":-1,"score":-1,"email":1,"state":2}`, field: "email"},
		{name: "state before reason", body: `{"reason":"X","state":"Y","email":"a@example.com"}`, field: "state"},
		{name: "reason before score", body: `{"score":-1,"reason":"X","email":"a@example.com","state":"risky"}`, field: "reason"},
		{name: "booleans in order", body: `{"role":1,"free":1,"accept_all":1,"email":"a@example.com","state":"risky"}`, field: "accept_all"},
		{name: "free before role", body: `{"role":1,"free":1,"email":"a@example.com","state":"risky"}`, field: "free"},
		{name: "birth year before texts", body: `{"gender":1,"birth_year":-1,"email":"a@example.com","state":"risky"}`, field: "birth_year"},
		{name: "texts in order", body: `{"gender":1,"domain":1,"did_you_mean":1,"email":"a@example.com","state":"risky"}`, field: "did_you_mean"},
		{name: "texts before duration", body: `{"duration":-1,"gender":1,"email":"a@example.com","state":"risky"}`, field: "gender"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, derr := decodeVerification([]byte(tt.body), path{}, true)
			require.NotNil(t, derr)
			assert.Equal(t, tt.field, derr.field)
			_, derr = decodeVerification([]byte(tt.body), path{base: dcdEmails, index: 7}, false)
			require.NotNil(t, derr)
			assert.Equal(t, "emails[7]."+tt.field, derr.field)
		})
	}
}

// TestDecodeVerificationExactKeys proves that only the exact member names
// are read: a case variant alone is ignored (and so missing), and a case
// variant beside the exact name never reaches decoding, because checkStrict
// refuses the body.
func TestDecodeVerificationExactKeys(t *testing.T) {
	_, derr := decodeVerification(dcdStrict(t, `{"Email":"a@example.com","state":"deliverable"}`), path{}, true)
	assertDecodeError(t, derr, FailureMalformedResponse, "email")
	requireCause(t, derr, errMissingField)

	_, derr = decodeVerification(dcdStrict(t, `{"email":"a@example.com","STATE":"deliverable"}`), path{}, true)
	assertDecodeError(t, derr, FailureMalformedResponse, "state")

	row, derr := decodeVerification(dcdStrict(t, `{"Email":"a@example.com","Score":1000,"`+strictLongS+`tate":"X"}`), path{}, false)
	require.Nil(t, derr)
	assert.Equal(t, Verification{}, row, "case variants are not members")

	for _, body := range []string{
		`{"Email":"a@example.com","email":"c@example.com","state":"deliverable"}`,
		`{"email":"a@example.com","state":"deliverable","SCORE":1,"score":2}`,
		`{"email":"a@example.com","state":"deliverable","` + strictLongS + `tate":"risky"}`,
		testutil.Esc(`{"email":"a@example.com","~u0045mail":"c@example.com","state":"deliverable"}`),
	} {
		kind, cause := checkStrict([]byte(body))
		assert.Equal(t, FailureMalformedResponse, kind, body)
		require.ErrorIs(t, cause, errDuplicateKey, body)
	}
}

func TestDecodeVerificationUnknownMembers(t *testing.T) {
	body := `{"email":"a@example.com","state":"risky","extra":{"score":-1,"state":"X"},"Duration":"x","mx":[1,2],` +
		`"status":"ok","score_v2":1000}`
	got, derr := decodeVerification(dcdStrict(t, body), path{}, true)
	require.Nil(t, derr)
	assert.Equal(t, Verification{Email: "a@example.com", State: StateRisky}, got)
}

// dcdBatchCase is one decodeBatch call and what it must return.
type dcdBatchCase struct {
	name   string
	body   string
	wantID string
	want   *Batch
	kind   FailureKind
	field  string
}

// dcdBatchIDCases returns the id rules of GetBatch (wantID set) and of a
// callback (wantID empty).
func dcdBatchIDCases() []dcdBatchCase {
	malformed, unexpected := FailureMalformedResponse, FailureUnexpectedResponse
	msg := `"message":"m"`
	return []dcdBatchCase{
		{name: "get: matching id", body: `{"id":"b1",` + msg + `}`, wantID: dcdBatchID, want: &Batch{ID: dcdBatchID, Message: "m"}},
		{name: "get: escaped matching id", body: testutil.Esc(`{"id":"~u00621",` + msg + `}`), wantID: dcdBatchID, want: &Batch{ID: dcdBatchID, Message: "m"}},
		{name: "get: absent id accepted", body: `{` + msg + `}`, wantID: dcdBatchID, want: &Batch{ID: dcdBatchID, Message: "m"}},
		{name: "get: null id accepted", body: `{"id":null,` + msg + `}`, wantID: dcdBatchID, want: &Batch{ID: dcdBatchID, Message: "m"}},
		{name: "get: id alone", body: `{"id":"b1"}`, wantID: dcdBatchID, want: &Batch{ID: dcdBatchID}},
		{name: "get: another id", body: `{"id":"b2",` + msg + `}`, wantID: dcdBatchID, kind: unexpected, field: dcdIDField},
		{name: "get: id in another case", body: `{"id":"B1"}`, wantID: dcdBatchID, kind: unexpected, field: dcdIDField},
		{name: "get: id with a suffix", body: `{"id":"b1 "}`, wantID: dcdBatchID, kind: unexpected, field: dcdIDField},
		{name: "get: empty id", body: `{"id":"",` + msg + `}`, wantID: dcdBatchID, kind: unexpected, field: dcdIDField},
		{name: "get: number id", body: `{"id":1,` + msg + `}`, wantID: dcdBatchID, kind: malformed, field: dcdIDField},
		{name: "get: id checked first", body: `{"processed":-1,"id":"b2"}`, wantID: dcdBatchID, kind: unexpected, field: dcdIDField},
		{name: "get: ID is not id", body: `{"ID":"b2",` + msg + `}`, wantID: dcdBatchID, want: &Batch{ID: dcdBatchID, Message: "m"}},
		{name: "callback: id", body: `{"id":"b1",` + msg + `}`, want: &Batch{ID: dcdBatchID, Message: "m"}},
		{name: "callback: id alone", body: `{"id":"b9"}`, want: &Batch{ID: "b9"}},
		{name: "callback: absent id", body: `{` + msg + `}`, kind: malformed, field: dcdIDField},
		{name: "callback: null id", body: `{"id":null,` + msg + `}`, kind: malformed, field: dcdIDField},
		{name: "callback: ID only", body: `{"ID":"b1",` + msg + `}`, kind: malformed, field: dcdIDField},
		{name: "callback: empty id", body: `{"id":"",` + msg + `}`, kind: unexpected, field: dcdIDField},
		{name: "callback: id with a space", body: `{"id":"b 1"}`, kind: unexpected, field: dcdIDField},
		{name: "callback: object id", body: `{"id":{"id":"b1"}}`, kind: malformed, field: dcdIDField},
	}
}

// dcdBatchNoMemberCases returns snapshots without any documented member: for
// GetBatch they are unexpected, and for a callback, which must name its
// batch, the missing id is checked first, so they are malformed at "id".
func dcdBatchNoMemberCases() []dcdBatchCase {
	allNull := dcdObject(
		dcdMember{dcdIDField, jsonNull}, dcdMember{dcdMessage, jsonNull}, dcdMember{dcdProgress, jsonNull},
		dcdMember{"total", jsonNull}, dcdMember{dcdEmails, jsonNull}, dcdMember{"download_file", jsonNull},
		dcdMember{dcdReasons, jsonNull}, dcdMember{dcdTotals, jsonNull},
	)
	cases := make([]dcdBatchCase, 0, 8)
	for _, wantID := range []string{dcdBatchID, ""} {
		for _, body := range []string{`{}`, allNull, `{"status":"done","data":{"id":"b1"}}`, `{"ID":"b1","Message":"x"}`} {
			c := dcdBatchCase{
				name: "no member: " + body + " for " + strconv.Quote(wantID), body: body, wantID: wantID,
				kind: FailureUnexpectedResponse,
			}
			if wantID == "" {
				c.kind, c.field = FailureMalformedResponse, dcdIDField
			}
			cases = append(cases, c)
		}
	}
	return cases
}

// dcdBatchMemberCases returns the rules of every other member.
func dcdBatchMemberCases() []dcdBatchCase {
	malformed, tooLarge, unexpected := FailureMalformedResponse, FailureResponseTooLarge, FailureUnexpectedResponse
	file := strings.Repeat("f", maxDownloadFileBytes)
	return []dcdBatchCase{
		{name: "message too long", body: `{"message":` + dcdString("m", maxTextBytes+1) + `}`, wantID: dcdBatchID, kind: tooLarge, field: dcdMessage},
		{name: "message number", body: `{"message":1}`, wantID: dcdBatchID, kind: malformed, field: dcdMessage},
		{name: "message before counts", body: `{"processed":-1,"message":1}`, wantID: dcdBatchID, kind: malformed, field: dcdMessage},
		{name: "progress counts", body: `{"processed":0,"total":5}`, wantID: dcdBatchID, want: &Batch{ID: dcdBatchID, Processed: new(int64(0)), Total: new(int64(5))}},
		{name: "processed negative", body: `{"processed":-1}`, wantID: dcdBatchID, kind: malformed, field: dcdProgress},
		{name: "total quoted", body: `{"total":"5"}`, wantID: dcdBatchID, kind: malformed, field: "total"},
		{name: "total fraction", body: `{"total":5.0}`, wantID: dcdBatchID, kind: malformed, field: "total"},
		{name: "emails null", body: `{"id":"b1","emails":null}`, wantID: dcdBatchID, want: &Batch{ID: dcdBatchID}},
		{name: "emails empty", body: `{"emails":[]}`, wantID: dcdBatchID, want: &Batch{ID: dcdBatchID, Emails: []Verification{}}},
		{
			name: "rows in order, duplicates kept", body: `{"emails":[{"email":"b@example.com"},{"email":"a@example.com","state":"duplicate"},{"email":"b@example.com"},{}]}`,
			wantID: dcdBatchID, want: &Batch{ID: dcdBatchID, Emails: []Verification{
				{Email: "b@example.com"}, {Email: "a@example.com", State: StateDuplicate}, {Email: "b@example.com"}, {},
			}},
		},
		{name: "null row", body: `{"emails":[null]}`, wantID: dcdBatchID, kind: malformed, field: "emails[0]"},
		{name: "number row", body: `{"emails":[{"email":"a@example.com"},1]}`, wantID: dcdBatchID, kind: malformed, field: "emails[1]"},
		{name: "string row", body: `{"emails":[{},{},"a@example.com"]}`, wantID: dcdBatchID, kind: malformed, field: "emails[2]"},
		{name: "array row", body: `{"emails":[[]]}`, wantID: dcdBatchID, kind: malformed, field: "emails[0]"},
		{name: "emails object", body: `{"emails":{}}`, wantID: dcdBatchID, kind: malformed, field: dcdEmails},
		{name: "emails string", body: `{"emails":"a@example.com"}`, wantID: dcdBatchID, kind: malformed, field: dcdEmails},
		{name: "row member failure", body: `{"emails":[{},{},{},{"score":101}]}`, wantID: dcdBatchID, kind: malformed, field: "emails[3].score"},
		{name: "row token failure", body: `{"emails":[{"state":"Risky"}]}`, wantID: dcdBatchID, kind: unexpected, field: "emails[0].state"},
		{name: "row text too long", body: `{"emails":[{"user":` + dcdString("u", maxTextBytes+1) + `}]}`, wantID: dcdBatchID, kind: tooLarge, field: "emails[0].user"},
		{
			name: "download file at 8 KiB", body: `{"download_file":"` + file + `"}`, wantID: dcdBatchID,
			want: &Batch{ID: dcdBatchID, DownloadFile: NewDownloadURL(file)},
		},
		{name: "download file over 8 KiB", body: `{"download_file":"` + file + `f"}`, wantID: dcdBatchID, kind: tooLarge, field: "download_file"},
		{name: "download file number", body: `{"download_file":1}`, wantID: dcdBatchID, kind: malformed, field: "download_file"},
		{name: "download file empty", body: `{"id":"b1","download_file":""}`, wantID: dcdBatchID, want: &Batch{ID: dcdBatchID}},
		{
			name: "reason counts", body: `{"reason_counts":{"accepted_email":2,"brand_new":1}}`, wantID: dcdBatchID,
			want: &Batch{ID: dcdBatchID, ReasonCounts: map[Reason]int64{ReasonAcceptedEmail: 2, "brand_new": 1}},
		},
		{name: "reason counts empty", body: `{"reason_counts":{}}`, wantID: dcdBatchID, want: &Batch{ID: dcdBatchID, ReasonCounts: map[Reason]int64{}}},
		{name: "reason counts bad key", body: `{"reason_counts":{"Bad":1}}`, wantID: dcdBatchID, kind: unexpected, field: dcdReasons},
		{
			name: "reason counts null value is absent", body: `{"reason_counts":{"timeout":null,"no_connect":1}}`, wantID: dcdBatchID,
			want: &Batch{ID: dcdBatchID, ReasonCounts: map[Reason]int64{ReasonNoConnect: 1}},
		},
		{name: "reason counts quoted value", body: `{"reason_counts":{"timeout":"1"}}`, wantID: dcdBatchID, kind: malformed, field: "reason_counts.timeout"},
		{name: "reason counts array", body: `{"reason_counts":[]}`, wantID: dcdBatchID, kind: malformed, field: dcdReasons},
		{
			name: "total counts", body: `{"total_counts":{"processed":3,"total":3,"deliverable":2,"duplicate":1}}`, wantID: dcdBatchID,
			want: &Batch{ID: dcdBatchID, TotalCounts: map[string]int64{dcdProgress: 3, "total": 3, "deliverable": 2, "duplicate": 1}},
		},
		{name: "total counts negative", body: `{"total_counts":{"total":-3}}`, wantID: dcdBatchID, kind: malformed, field: "total_counts.total"},
		{name: "total counts undocumented key", body: `{"total_counts":{"test_x":-3}}`, wantID: dcdBatchID, kind: malformed, field: dcdTotals},
		{name: "reason counts undocumented key", body: `{"reason_counts":{"brand_new":"1"}}`, wantID: dcdBatchID, kind: malformed, field: dcdReasons},
		{name: "total counts too many keys", body: `{"total_counts":` + dcdCounts(maxCountKeys+1, "") + `}`, wantID: dcdBatchID, kind: tooLarge, field: dcdTotals},
	}
}

func TestDecodeBatch(t *testing.T) {
	cases := dcdBatchIDCases()
	cases = append(cases, dcdBatchNoMemberCases()...)
	cases = append(cases, dcdBatchMemberCases()...)
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got, derr := decodeBatch(dcdStrict(t, tt.body), tt.wantID)
			if tt.kind != "" {
				assertDecodeError(t, derr, tt.kind, tt.field)
				assert.Nil(t, got)
				return
			}
			require.Nil(t, derr)
			require.NotNil(t, got)
			assert.True(t, tt.want.DownloadFile.Equal(got.DownloadFile), "download file")
			assert.False(t, got.Partial, "decodeBatch never sets Partial")
			gotCopy, wantCopy := *got, *tt.want
			gotCopy.DownloadFile, wantCopy.DownloadFile = DownloadURL{}, DownloadURL{}
			assert.Equal(t, wantCopy, gotCopy)
		})
	}

	_, derr := decodeBatch([]byte(`{}`), dcdBatchID)
	requireCause(t, derr, errNoMembers)
	_, derr = decodeBatch([]byte(`{"id":"b2"}`), dcdBatchID)
	requireCause(t, derr, errIDMismatch)
	_, derr = decodeBatch([]byte(`{"message":"m"}`), "")
	requireCause(t, derr, errMissingField)
}

func TestDecodeRows(t *testing.T) {
	rows, derr := decodeRows(nil)
	assert.Nil(t, derr)
	assert.Nil(t, rows, "absent rows are nil")
	rows, derr = decodeRows([]byte(jsonNull))
	assert.Nil(t, derr)
	assert.Nil(t, rows, "null rows are nil")
	rows, derr = decodeRows([]byte(`[ ]`))
	assert.Nil(t, derr)
	assert.NotNil(t, rows, "[] is an empty slice")
	assert.Empty(t, rows)

	rows, derr = decodeRows([]byte(`[{"email":"a@example.com","score":1},{"Email":"ignored"}]`))
	require.Nil(t, derr)
	assert.Equal(t, []Verification{{Email: "a@example.com", Score: new(1)}, {}}, rows)

	_, derr = decodeRows([]byte(`[{}, true]`))
	assertDecodeError(t, derr, FailureMalformedResponse, "emails[1]")
	_, derr = decodeRows([]byte(`7`))
	assertDecodeError(t, derr, FailureMalformedResponse, dcdEmails)
}

func TestDecodeCreateBatch(t *testing.T) {
	id256 := strings.Repeat("q", maxBatchIDBytes)
	tests := []struct {
		name  string
		body  string
		want  *CreateBatchResult
		kind  FailureKind
		cause error
	}{
		{name: "documented answer", body: `{"message":"Batch successfully created.","id":"b1"}`, want: &CreateBatchResult{BatchID: dcdBatchID, Message: "Batch successfully created."}},
		{name: "id alone", body: `{"id":"b1"}`, want: &CreateBatchResult{BatchID: dcdBatchID}},
		{name: "escaped id", body: testutil.Esc(`{"id":"b~u0031"}`), want: &CreateBatchResult{BatchID: dcdBatchID}},
		{name: "256-byte id", body: `{"id":"` + id256 + `"}`, want: &CreateBatchResult{BatchID: id256}},
		{name: "number message dropped", body: `{"id":"b1","message":1}`, want: &CreateBatchResult{BatchID: dcdBatchID}},
		{name: "object message dropped", body: `{"id":"b1","message":{"text":"x"}}`, want: &CreateBatchResult{BatchID: dcdBatchID}},
		{name: "oversized message dropped", body: `{"id":"b1","message":` + dcdString("m", maxTextBytes+1) + `}`, want: &CreateBatchResult{BatchID: dcdBatchID}},
		{name: "null message", body: `{"id":"b1","message":null}`, want: &CreateBatchResult{BatchID: dcdBatchID}},
		{name: "unknown members ignored", body: `{"id":"b1","status":"queued","Message":"x"}`, want: &CreateBatchResult{BatchID: dcdBatchID}},
		{name: "absent id", body: `{"message":"Batch successfully created."}`, kind: FailureMalformedResponse, cause: errMissingField},
		{name: "null id", body: `{"id":null}`, kind: FailureMalformedResponse, cause: errMissingField},
		{name: "ID is not id", body: `{"ID":"b1"}`, kind: FailureMalformedResponse, cause: errMissingField},
		{name: "empty id", body: `{"id":""}`, kind: FailureUnexpectedResponse, cause: errInvalidToken},
		{name: "id with a space", body: `{"id":"b 1"}`, kind: FailureUnexpectedResponse, cause: errInvalidToken},
		{name: "non-ASCII id", body: "{\"id\":\"bé\"}", kind: FailureUnexpectedResponse, cause: errInvalidToken},
		{name: "257-byte id", body: `{"id":"` + id256 + `q"}`, kind: FailureUnexpectedResponse, cause: errInvalidToken},
		{name: "number id", body: `{"id":17}`, kind: FailureMalformedResponse, cause: errWrongType},
		{name: "array id", body: `{"id":["b1"]}`, kind: FailureMalformedResponse, cause: errWrongType},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, derr := decodeCreateBatch(dcdStrict(t, tt.body))
			if tt.kind != "" {
				assertDecodeError(t, derr, tt.kind, dcdIDField)
				requireCause(t, derr, tt.cause)
				assert.Nil(t, got)
				return
			}
			require.Nil(t, derr)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDecodeRescueBatchID(t *testing.T) {
	esc := testutil.Esc
	id256 := strings.Repeat("r", maxBatchIDBytes)
	tests := []struct {
		name string
		body string
		want string // "" when nothing may be rescued
	}{
		{name: "a strict body", body: `{"message":"Batch successfully created.","id":"b1"}`, want: dcdBatchID},
		{name: "trailing data", body: `{"id":"b1"} x`, want: dcdBatchID},
		{name: "a second object", body: `{"id":"b1"}{"id":"b2"}`, want: dcdBatchID},
		{name: "an invalid second value", body: `{"id":"b1"}}`, want: dcdBatchID},
		{name: "leading whitespace", body: " \n {\"id\":\"b1\"}garbage", want: dcdBatchID},
		{name: "a duplicate elsewhere", body: `{"id":"b1","message":"a","Message":"b"}`, want: dcdBatchID},
		{name: "a nested id is not the id", body: `{"data":{"id":"x","ID":"y"},"id":"b1"}`, want: dcdBatchID},
		{name: "escaped id key", body: esc(`{"~u0069d":"b1"} x`), want: dcdBatchID},
		{name: "fully escaped id key", body: esc(`{"~u0069~u0064":"b1"}`), want: dcdBatchID},
		{name: "escaped id value", body: esc(`{"id":"b~u0031"}`), want: dcdBatchID},
		{name: "256-byte id", body: `{"id":"` + id256 + `"} x`, want: id256},
		{name: "invalid JSON", body: `{"id":"b1"`},
		{name: "invalid JSON after the id", body: `{"id":"b1",}`},
		{name: "invalid JSON inside", body: `{"id":"b1","x":[1,]}`},
		{name: "invalid UTF-8 in another member", body: "{\"id\":\"b1\",\"m\":\"\xff\"}", want: dcdBatchID},
		{name: "invalid UTF-8 in another key", body: "{\"\xffid\":1,\"id\":\"b1\"}", want: dcdBatchID},
		{name: "invalid UTF-8 after the object", body: "{\"id\":\"b1\"}\xff", want: dcdBatchID},
		{name: "invalid UTF-8 in the id", body: "{\"id\":\"b\xff1\"}"},
		{name: "invalid UTF-8 in the id key", body: "{\"i\xffd\":\"b1\"}"},
		{name: "invalid UTF-8 beside an ID", body: "{\"id\":\"b1\",\"ID\":\"\xff\"}"},
		{name: "an array", body: `[{"id":"b1"}]`},
		{name: "a string", body: `"b1"`},
		{name: "empty body", body: ``},
		{name: "empty object", body: `{}`},
		{name: "no id", body: `{"message":"created"}`},
		{name: "id twice", body: `{"id":"b1","id":"b1"}`},
		{name: "id and ID", body: `{"id":"b1","ID":"b1"}`},
		{name: "ID and id", body: `{"ID":"b2","id":"b1"}`},
		{name: "id and escaped Id", body: esc(`{"id":"b1","~u0049d":"b1"}`)},
		{name: "only ID", body: `{"ID":"b1"}`},
		{name: "only Id", body: `{"Id":"b1"}`},
		{name: "only iD", body: `{"iD":"b1"}`},
		{name: "null id", body: `{"id":null}`},
		{name: "number id", body: `{"id":1}`},
		{name: "object id", body: `{"id":{"id":"b1"}}`},
		{name: "empty id", body: `{"id":""}`},
		{name: "id with a space", body: `{"id":"b 1"}`},
		{name: "non-ASCII id", body: "{\"id\":\"bé\"}"},
		{name: "257-byte id", body: `{"id":"` + id256 + `r"}`},
		{name: "dotless i key", body: `{"` + strictDotlessI + `d":"b1"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := rescueBatchID([]byte(tt.body))
			assert.Equal(t, tt.want != "", ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDecodeAccountInfo(t *testing.T) {
	tests := []struct {
		name  string
		body  string
		want  *AccountInfo
		kind  FailureKind
		field string
	}{
		{name: "documented answer", body: `{"owner_email":"owner@example.com","available_credits":1000}`, want: &AccountInfo{OwnerEmail: "owner@example.com", AvailableCredits: 1000}},
		{name: "zero credits", body: `{"available_credits":0,"owner_email":"o@example.com"}`, want: &AccountInfo{OwnerEmail: "o@example.com"}},
		{name: "max credits", body: `{"owner_email":"o@example.com","available_credits":9223372036854775807}`, want: &AccountInfo{OwnerEmail: "o@example.com", AvailableCredits: math.MaxInt64}},
		{name: "empty owner is a string", body: `{"owner_email":"","available_credits":1}`, want: &AccountInfo{AvailableCredits: 1}},
		{name: "unknown members ignored", body: `{"owner_email":"o@example.com","available_credits":1,"plan":"pro","owner":"y"}`, want: &AccountInfo{OwnerEmail: "o@example.com", AvailableCredits: 1}},
		{name: "absent owner", body: `{"available_credits":1}`, kind: FailureMalformedResponse, field: dcdOwner},
		{name: "null owner", body: `{"owner_email":null,"available_credits":1}`, kind: FailureMalformedResponse, field: dcdOwner},
		{name: "owner in another case", body: `{"Owner_Email":"o@example.com","available_credits":1}`, kind: FailureMalformedResponse, field: dcdOwner},
		{name: "number owner", body: `{"owner_email":1,"available_credits":1}`, kind: FailureMalformedResponse, field: dcdOwner},
		{name: "owner too long", body: `{"owner_email":` + dcdString("o", maxTextBytes+1) + `,"available_credits":1}`, kind: FailureResponseTooLarge, field: dcdOwner},
		{name: "owner checked first", body: `{"owner_email":1,"available_credits":-1}`, kind: FailureMalformedResponse, field: dcdOwner},
		{name: "absent credits", body: `{"owner_email":"o@example.com"}`, kind: FailureMalformedResponse, field: dcdCredits},
		{name: "null credits", body: `{"owner_email":"o@example.com","available_credits":null}`, kind: FailureMalformedResponse, field: dcdCredits},
		{name: "negative credits", body: `{"owner_email":"o@example.com","available_credits":-1}`, kind: FailureMalformedResponse, field: dcdCredits},
		{name: "fractional credits", body: `{"owner_email":"o@example.com","available_credits":1.5}`, kind: FailureMalformedResponse, field: dcdCredits},
		{name: "quoted credits", body: `{"owner_email":"o@example.com","available_credits":"1000"}`, kind: FailureMalformedResponse, field: dcdCredits},
		{name: "overflowing credits", body: `{"owner_email":"o@example.com","available_credits":9223372036854775808}`, kind: FailureMalformedResponse, field: dcdCredits},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, derr := decodeAccountInfo(dcdStrict(t, tt.body))
			if tt.kind != "" {
				assertDecodeError(t, derr, tt.kind, tt.field)
				assert.Nil(t, got)
				return
			}
			require.Nil(t, derr)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestDecodeNeverAliasesBody proves that decoded values are copies: changing
// the body afterwards changes nothing.
func TestDecodeNeverAliasesBody(t *testing.T) {
	ver, want := dcdFullVerification()
	batch := `{"id":"b1","message":"done","download_file":"https://files.example.com/b1.csv",` +
		`"emails":[` + ver + `],"reason_counts":{"accepted_email":1},"total_counts":{"total":1}}`
	tests := []struct {
		name   string
		body   string
		decode func(body []byte) (any, *decodeError)
	}{
		{name: "verification", body: ver, decode: func(b []byte) (any, *decodeError) { return decodeVerification(b, path{}, true) }},
		{name: "batch", body: batch, decode: func(b []byte) (any, *decodeError) {
			got, derr := decodeBatch(b, "")
			if derr != nil {
				return nil, derr
			}
			return []any{got.ID, got.Message, got.DownloadFile.Reveal(), got.Emails, got.ReasonCounts, got.TotalCounts}, nil
		}},
		{name: "create batch", body: `{"id":"b1","message":"created"}`, decode: func(b []byte) (any, *decodeError) { return decodeCreateBatch(b) }},
		{name: "account", body: `{"owner_email":"o@example.com","available_credits":7}`, decode: func(b []byte) (any, *decodeError) { return decodeAccountInfo(b) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := dcdStrict(t, tt.body)
			before, derr := tt.decode(bytes.Clone(body))
			require.Nil(t, derr)
			got, derr := tt.decode(body)
			require.Nil(t, derr)
			require.Equal(t, before, got)
			for i := range body {
				body[i] = 'x'
			}
			assert.Equal(t, before, got, "changing the body changes nothing decoded")
		})
	}
	got, derr := decodeVerification([]byte(ver), path{}, true)
	require.Nil(t, derr)
	assert.Equal(t, want, got)
}

// TestDecodeErrorsNeverQuoteValues proves that a decoding failure names a
// location, never the value: the planted marker appears in no error text.
func TestDecodeErrorsNeverQuoteValues(t *testing.T) {
	const marker = "plantedvaluemarker"
	bodies := []string{
		`{"email":"a@example.com","state":"` + marker + `X"}`,
		`{"emails":[{"state":"` + marker + `X"}]}`,
		`{"reason_counts":{"` + marker + `X":1}}`,
		`{"id":"` + marker + ` "}`,
	}
	for _, body := range bodies {
		var derr *decodeError
		if strings.HasPrefix(body, `{"email"`) {
			_, derr = decodeVerification([]byte(body), path{}, true)
		} else {
			_, derr = decodeBatch([]byte(body), "")
		}
		require.NotNil(t, derr, body)
		ce := derr.callError(OpBatchCallback)
		assert.NotContains(t, ce.Error(), marker)
		assert.NotContains(t, ce.GoString(), marker)
		assert.False(t, errors.Is(ce, ErrMalformedResponse) && errors.Is(ce, ErrUnexpectedResponse), "one kind only")
	}
}
