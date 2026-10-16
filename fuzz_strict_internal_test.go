package emailable

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-emailable/internal/testutil"
)

// fzsMaxInput bounds every fuzz input of this file: 64 KiB.
const fzsMaxInput = 64 << 10

// fzsSeeds returns the strict corpora within the input bound, plus bodies of
// the documented shapes.
func fzsSeeds() []string {
	seeds := []string{
		`{"email":"jane.doe@example.com","state":"deliverable","reason":"accepted_email","score":97,"duration":0.42}`,
		`{"id":"b1","message":"Batch verification completed.","emails":[{"email":"a@example.com","state":"risky"}]}`,
		`{"id":"b1","reason_counts":{"accepted_email":1},"total_counts":{"processed":1,"total":1}}`,
		`{"owner_email":"owner@example.com","available_credits":1000}`,
		`{"message":"Batch successfully created.","id":"b1"}`,
		`{"Email":"x","EMAIL":"y"}`, `{"` + strictKelvin + `ey":1,"key":2}`, `{"` + strictLongS + `tate":1,"State":2}`,
		testutil.Esc(`{"~ud801~udc00":1,"~ud801~udc28":2}`), testutil.Esc(`{"~u0069d":"a","ID":"b"}`),
		"2" + strings.Repeat("0", 310), "1" + strings.Repeat("0", 400) + " x", `{"a":1e999} x`,
		"00", "truex", "-0.5e2x", "nullnull",
	}
	for _, s := range append(strictValidCorpus(), strictInvalidCorpus()...) {
		if len(s) <= fzsMaxInput {
			seeds = append(seeds, s)
		}
	}
	return seeds
}

// fzsDecodesFirstValue reports whether encoding/json decodes a first value
// from data, whatever follows it.
func fzsDecodesFirstValue(data []byte) bool {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber() // a number beyond float64 is still a value
	var v any
	return dec.Decode(&v) == nil
}

// FuzzCheckStrict checks checkStrict and its scanners on any input: no
// scanner panics, and checkStrict's verdict is the oracle's. Its validity
// verdict is json.Valid and utf8.Valid, with trailing data told apart from
// invalid syntax; for a valid document the top-level type, the value counts
// and the duplicate-key verdict match the encoding/json token walk with
// independent case folding.
func FuzzCheckStrict(f *testing.F) {
	for _, seed := range fzsSeeds() {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > fzsMaxInput {
			return
		}
		kind, cause := checkStrict(data)
		require.Equal(t, kind == "", cause == nil, "a kind comes with a cause")
		strictSweep(data)

		valid, oracleDup := strictOracle(data)
		require.Equal(t, !valid, strictSyntaxCause(cause), "checkStrict and the oracle agree on validity")
		if !valid {
			fzsCheckInvalid(t, data, kind, cause)
			return
		}
		require.Equal(t, oracleDup, duplicateKey(data), "duplicateKey agrees with the oracle")
		wantContainers, wantValues := strictOracleCount(data)
		containers, values := countValues(data)
		require.Equal(t, wantContainers, containers, "containers")
		require.Equal(t, wantValues, values, "values")
		if data[skipSpace(data, 0)] != '{' {
			require.Equal(t, FailureUnexpectedResponse, kind)
			require.ErrorIs(t, cause, errNotObject)
			return
		}
		switch {
		case containers > maxContainers || values > maxValues:
			require.Equal(t, FailureResponseTooLarge, kind)
			require.ErrorIs(t, cause, errTooManyValues)
		case oracleDup:
			require.Equal(t, FailureMalformedResponse, kind)
			require.ErrorIs(t, cause, errDuplicateKey)
		default:
			require.NoError(t, cause)
			fzsCheckAccepted(t, data)
		}
	})
}

// fzsCheckInvalid checks checkStrict's verdict on data that is not one valid
// JSON value in valid UTF-8.
func fzsCheckInvalid(t *testing.T, data []byte, kind FailureKind, cause error) {
	t.Helper()
	require.Equal(t, FailureMalformedResponse, kind)
	if !utf8.Valid(data) {
		require.ErrorIs(t, cause, errInvalidUTF8)
		return
	}
	if fzsDecodesFirstValue(data) {
		require.ErrorIs(t, cause, errTrailingData)
		return
	}
	require.ErrorIs(t, cause, errInvalidJSON)
}

// fzsCheckAccepted checks how the decoders see an object checkStrict accepted:
// members yields each member once, as encoding/json decodes it, and readObject
// finds each one by its exact key.
func fzsCheckAccepted(t *testing.T, data []byte) {
	t.Helper()
	var decoded map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &decoded))
	got := strictMembers(data)
	require.Len(t, got, len(decoded), "members sees every member once")
	for _, m := range got {
		want, ok := decoded[m.key]
		require.True(t, ok, "member %q", m.key)
		require.Equal(t, []byte(want), []byte(m.value), "the raw value of %q", m.key)
	}
	if len(got) > 0 && len(got) <= maxFields {
		names := make([]string, len(got))
		for i, m := range got {
			names[i] = m.key
		}
		o := readObject(data, names)
		for i, m := range got {
			require.Equal(t, []byte(m.value), o.fields[i], "readObject finds %q", m.key)
		}
	}
}

// fzsEscapeAll spells every rune of s as a JSON escape.
func fzsEscapeAll(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		b.WriteString(strictEscapeRune(r))
	}
	b.WriteByte('"')
	return b.String()
}

// FuzzDecodeKey checks key decoding and folding against encoding/json: for a
// JSON string literal, decodeKey decodes as json.Unmarshal does, appendFolded
// folds as the oracle does, and foldsToID agrees. The input is tried as a
// literal, as content quoted by encoding/json, and as content with every rune
// escaped. Nothing panics on any input.
func FuzzDecodeKey(f *testing.F) {
	esc := testutil.Esc
	for _, seed := range []string{
		`"email"`, `"EMAIL"`, esc(`"~u0065mail"`), esc(`"~ud83d~ude00"`), esc(`"~ud800"`), esc(`"~ud800~u0041"`),
		esc(`"~"~~~/~b~f~n~r~t"`), `"` + strictKelvin + `"`, `"` + strictLongS + `"`, "\"ς\"", `"` + strictDotI + `"`,
		`"id"`, `"ID"`, esc(`"~u0049d"`), esc(`"~ud801~udc28"`), "\"\xff\"", `"`, ``, esc(`"~`), esc(`"~u12`),
		esc(`"~"~"~/" `), ` "a"`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > fzsMaxInput {
			return
		}
		// Malformed literals decode to a prefix and never panic.
		_ = appendUnquoted(nil, raw)
		_ = appendFolded(nil, raw)
		_ = keyBytes(raw)
		_ = foldsToID(raw)
		fzsCheckLiteral(t, raw)
		if utf8.Valid(raw) {
			fzsCheckLiteral(t, []byte(testutil.JSONQuote(string(raw))))
			fzsCheckLiteral(t, []byte(fzsEscapeAll(string(raw))))
		}
	})
}

// fzsCheckLiteral checks the decoders on lit when it is one JSON string in
// valid UTF-8.
func fzsCheckLiteral(t *testing.T, lit []byte) {
	t.Helper()
	if !utf8.Valid(lit) || !json.Valid(lit) || lit[0] != '"' || stringEnd(lit, 0) != len(lit) {
		return // not a bare literal: json.Valid also accepts surrounding whitespace
	}
	var want string
	require.NoError(t, json.Unmarshal(lit, &want))
	require.Equal(t, want, decodeKey(lit), "decodeKey(%s)", lit)
	require.Equal(t, want, string(appendUnquoted([]byte(nil), lit)))
	got := keyBytes(lit)
	require.Equal(t, want, string(got))
	folded := strictFold(want)
	require.Equal(t, folded, string(appendFolded(nil, lit)), "appendFolded(%s)", lit)
	require.Equal(t, folded == "ID", foldsToID(lit))
	doc := []byte(`{` + string(lit) + `:1,` + testutil.JSONQuote(want) + `:2}`)
	require.True(t, duplicateKey(doc), "a key and its decoded spelling are the same key")
	kind, cause := checkStrict(doc)
	require.Equal(t, FailureMalformedResponse, kind)
	require.ErrorIs(t, cause, errDuplicateKey)
	assert.Equal(t, !bytes.Contains(lit, []byte(`\`)), unescaped(lit))
}
