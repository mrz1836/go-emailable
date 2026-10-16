package emailable

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-emailable/internal/testutil"
)

// fzdMaxInput bounds every fuzz input of this file: 64 KiB.
const fzdMaxInput = 64 << 10

// fzdSeeds returns representative bodies of every operation, and the strict
// corpora.
func fzdSeeds() []string {
	esc := testutil.Esc
	ver, _ := dcdFullVerification()
	seeds := []string{
		ver,
		`{"email":"jane.doe@example.com","state":"deliverable","reason":"accepted_email"}`,
		`{"email":"a@example.com","state":"unknown","reason":"no_connect","score":0,"duration":-0}`,
		`{"email":"a@example.com","state":"risky","score":101}`, `{"email":"a@example.com","state":"Risky"}`, `{"email":"","state":"x"}`,
		`{"email":"a@example.com","state":"risky","duration":1e400}`, `{"email":"a@example.com","state":"risky","birth_year":9999}`,
		esc(`{"email":"~ud800<&>~u2028","state":"risky","tag":"","user":"~u0000"}`),
		`{"email":"a@example.com","state":"risky","gender":` + dcdString("<", maxTextBytes) + `}`,
		`{"id":"b1","message":"Batch verification completed.","total_counts":{"processed":1,"total":1}}`,
		`{"message":"Your batch is being processed.","processed":1,"total":3}`,
		`{"id":"b1","emails":[` + ver + `,{"email":"b@example.com","state":"duplicate"},{}],"reason_counts":{"accepted_email":1}}`,
		`{"id":"b1","download_file":"https://files.example.com/b1.csv?sig=x","total_counts":{"total":5000}}`,
		`{"id":"b1","emails":[],"reason_counts":{},"total_counts":{}}`,
		`{"id":"b2"}`, `{}`, `{"id":null}`, `{"emails":[null]}`, `{"emails":[{"score":101}]}`,
		`{"reason_counts":{"timeout":null}}`, `{"total_counts":{"X":1}}`, `{"id":"b 1"}`, `{"ID":"b1","message":"m"}`,
		`{"id":"b1","message":"m"} x`, "{\"id\":\"b1\",\"m\":\"\xff\"}", `{"id":"b1","id":"b1"}`, `{"id":"b1","ID":"b1"}`,
		`{"owner_email":"o@example.com","available_credits":1000}`, `{"message":"Batch successfully created.","id":"b1"}`,
	}
	for _, s := range append(strictValidCorpus(), strictInvalidCorpus()...) {
		if len(s) <= fzdMaxInput {
			seeds = append(seeds, s)
		}
	}
	return seeds
}

// fzdPatterns are the shapes decoded values and failure fields must have.
type fzdPatterns struct {
	token *regexp.Regexp // states, reasons and count keys
	field *regexp.Regexp // the Field of a failure
}

func fzdNewPatterns() fzdPatterns {
	names := verificationNames()
	member := "(" + strings.Join(names[:], "|") + ")"
	return fzdPatterns{
		token: regexp.MustCompile(`^[a-z0-9_]{1,64}$`),
		field: regexp.MustCompile(`^(` + member + `|emails(\[\d+\](\.` + member + `)?)?|id|message|processed|total|` +
			`download_file|(reason|total)_counts(\.[a-z0-9_]{1,64})?)?$`),
	}
}

// fzdDecodeKinds reports whether kind is one a body that passed checkStrict
// may fail decoding with.
func fzdDecodeKinds(kind FailureKind) bool {
	return kind == FailureMalformedResponse || kind == FailureUnexpectedResponse || kind == FailureResponseTooLarge
}

// fzdCheckText asserts the bounds of decoded provider text.
func fzdCheckText(t *testing.T, s string, limit int) {
	t.Helper()
	assert.LessOrEqual(t, len(s), limit)
	assert.True(t, utf8.ValidString(s), "decoded text is valid UTF-8")
}

// fzdCheckVerification asserts the invariants of a decoded verification.
func fzdCheckVerification(t *testing.T, p fzdPatterns, v *Verification, required bool) {
	t.Helper()
	if required {
		assert.NotEmpty(t, v.Email)
		assert.NotEmpty(t, v.State)
	}
	fzdCheckText(t, v.Email, maxTextBytes)
	for _, tok := range []string{string(v.State), string(v.Reason)} {
		if tok != "" {
			assert.Regexp(t, p.token, tok)
		}
	}
	if v.Score != nil {
		assert.True(t, *v.Score >= 0 && *v.Score <= maxScore, "score %d", *v.Score)
	}
	if v.BirthYear != nil {
		assert.True(t, *v.BirthYear >= 0 && *v.BirthYear <= maxBirthYear, "birth year %d", *v.BirthYear)
	}
	for _, s := range []*string{
		v.DidYouMean, v.Domain, v.User, v.Tag, v.MXRecord, v.SMTPProvider, v.FirstName, v.LastName, v.FullName, v.Gender,
	} {
		if s != nil {
			fzdCheckText(t, *s, maxTextBytes)
		}
	}
	if d := v.Duration; d != nil {
		assert.False(t, math.IsNaN(*d) || math.IsInf(*d, 0), "a finite duration")
		assert.GreaterOrEqual(t, *d, 0.0)
		assert.False(t, math.Signbit(*d), "-0 decodes as +0")
	}
}

// FuzzDecodeVerification checks decodeVerification on any body checkStrict
// accepts, as an answer and as a row: it never panics, a failure has a
// decoding kind and a member's path, and a result keeps its invariants and
// survives a round trip: encoding/json marshals it into a body the SDK
// decodes back to the same value, and that encoding/json decodes back too.
func FuzzDecodeVerification(f *testing.F) {
	for _, seed := range fzdSeeds() {
		f.Add([]byte(seed), true)
		f.Add([]byte(seed), false)
	}
	p := fzdNewPatterns()
	f.Fuzz(func(t *testing.T, body []byte, required bool) {
		if len(body) > fzdMaxInput {
			return
		}
		if _, cause := checkStrict(body); cause != nil {
			return
		}
		v, derr := decodeVerification(body, path{}, required)
		row, rowErr := decodeVerification(body, path{base: dcdEmails, index: 4}, required)
		if derr != nil {
			require.NotNil(t, rowErr)
			assert.True(t, fzdDecodeKinds(derr.kind), "kind %q", derr.kind)
			assert.Regexp(t, p.field, derr.field)
			assert.Equal(t, "emails[4]."+derr.field, rowErr.field, "a row's failure is at the row's path")
			assert.Equal(t, Verification{}, v)
			return
		}
		require.Nil(t, rowErr)
		require.Equal(t, v, row, "the path never changes the result")
		fzdCheckVerification(t, p, &v, required)

		data, err := json.Marshal(v)
		require.NoError(t, err)
		_, cause := checkStrict(data)
		require.NoError(t, cause, "a marshaled verification is strict JSON")
		again, derr := decodeVerification(data, path{}, required)
		require.Nil(t, derr, "the SDK decodes what encoding/json marshals: %s", data)
		require.Equal(t, v, again, "the round trip through the SDK keeps the value")
		var std Verification
		require.NoError(t, json.Unmarshal(data, &std))
		require.Equal(t, v, std, "the round trip through encoding/json keeps the value")
	})
}

// fzdComparable returns a copy of b that reflect-based comparison can use:
// DownloadFile, held behind a function, is cleared, and Partial is cleared,
// as neither is serialized.
func fzdComparable(b *Batch) Batch {
	c := *b
	c.DownloadFile, c.Partial = DownloadURL{}, false
	return c
}

// fzdCheckBatch asserts the invariants of a decoded batch snapshot.
func fzdCheckBatch(t *testing.T, p fzdPatterns, b *Batch) {
	t.Helper()
	assert.True(t, validBatchID(b.ID), "the batch has a well-formed ID")
	fzdCheckText(t, b.Message, maxTextBytes)
	fzdCheckText(t, b.DownloadFile.Reveal(), maxDownloadFileBytes)
	for _, n := range []*int64{b.Processed, b.Total} {
		assert.True(t, n == nil || *n >= 0, "counts are never negative")
	}
	for i := range b.Emails {
		fzdCheckVerification(t, p, &b.Emails[i], false)
	}
	assert.LessOrEqual(t, len(b.ReasonCounts), maxCountKeys)
	for k, n := range b.ReasonCounts {
		assert.Regexp(t, p.token, string(k))
		assert.GreaterOrEqual(t, n, int64(0))
	}
	assert.LessOrEqual(t, len(b.TotalCounts), maxCountKeys)
	for k, n := range b.TotalCounts {
		assert.Regexp(t, p.token, k)
		assert.GreaterOrEqual(t, n, int64(0))
	}
}

// fzdRoundTrip marshals b with encoding/json, checks that the result is
// strict JSON that decode reads back to the same snapshot, DownloadFile
// aside, and that encoding/json reads back too.
func fzdRoundTrip(t *testing.T, b *Batch, decode func(data []byte) (*Batch, error)) {
	t.Helper()
	data, err := json.Marshal(b)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "download_file", "the download URL is never serialized")
	_, cause := checkStrict(data)
	require.NoError(t, cause, "a marshaled batch is strict JSON")
	again, err := decode(data)
	require.NoError(t, err, "the SDK decodes what encoding/json marshals: %s", data)
	require.Equal(t, fzdComparable(b), fzdComparable(again), "the round trip through the SDK keeps the snapshot")
	assert.True(t, again.DownloadFile.IsZero())
	var std Batch
	require.NoError(t, json.Unmarshal(data, &std))
	require.Equal(t, fzdComparable(b), fzdComparable(&std), "the round trip through encoding/json keeps the snapshot")
}

// FuzzDecodeBatch checks decodeBatch, as GetBatch calls it, on any body
// checkStrict accepts: it never panics, a failure has a decoding kind and a
// documented path, and a snapshot is for the batch requested, keeps its
// invariants and survives a round trip through encoding/json.
func FuzzDecodeBatch(f *testing.F) {
	for _, seed := range fzdSeeds() {
		f.Add([]byte(seed), dcdBatchID)
		f.Add([]byte(seed), "b2")
	}
	p := fzdNewPatterns()
	f.Fuzz(func(t *testing.T, body []byte, wantID string) {
		if len(body) > fzdMaxInput {
			return
		}
		if !validBatchID(wantID) {
			wantID = dcdBatchID // GetBatch refuses any other request
		}
		if _, cause := checkStrict(body); cause != nil {
			return
		}
		b, derr := decodeBatch(body, wantID)
		if derr != nil {
			assert.Nil(t, b)
			assert.True(t, fzdDecodeKinds(derr.kind), "kind %q", derr.kind)
			assert.Regexp(t, p.field, derr.field)
			assert.Equal(t, derr.field == "", isNoMembers(derr), "only a snapshot without members has no field")
			fzdCheckCountPath(t, derr.field)
			return
		}
		require.NotNil(t, b)
		require.Equal(t, wantID, b.ID, "only the batch requested is ever returned")
		fzdCheckBatch(t, p, b)
		fzdRoundTrip(t, b, func(data []byte) (*Batch, error) {
			got, derr := decodeBatch(data, wantID)
			if derr != nil {
				return nil, derr.callError(OpGetBatch)
			}
			return got, nil
		})
	})
}

// fzdCheckCountPath asserts that a failure path inside reason_counts or
// total_counts names only a documented key: any other key is provider data.
func fzdCheckCountPath(t *testing.T, field string) {
	t.Helper()
	if k, ok := strings.CutPrefix(field, "reason_counts."); ok {
		assert.True(t, Reason(k).Known(), "the path names an undocumented reason")
	}
	if k, ok := strings.CutPrefix(field, "total_counts."); ok {
		assert.True(t, State(k).Known() || k == "processed" || k == "total", "the path names an undocumented total")
	}
}

// isNoMembers reports whether derr is the failure of a snapshot without any
// documented member.
func isNoMembers(derr *decodeError) bool {
	return derr.kind == FailureUnexpectedResponse && errors.Is(derr.cause, errNoMembers)
}

// FuzzDecodeBatchCallback checks DecodeBatchCallback on any body: it never
// panics; a failure is a *CallError of Op batch_callback that made no
// request, whose BatchID, when set, is the body's rescued ID; and a snapshot
// has the body's ID, keeps its invariants and survives a round trip.
func FuzzDecodeBatchCallback(f *testing.F) {
	for _, seed := range fzdSeeds() {
		f.Add([]byte(seed), false)
	}
	f.Add(bytes.Repeat([]byte(" "), minResponseBytesLimit+1), true)
	p := fzdNewPatterns()
	f.Fuzz(func(t *testing.T, body []byte, small bool) {
		if len(body) > fzdMaxInput {
			return
		}
		limit := int64(DefaultMaxResponseBytes)
		if small {
			limit = minResponseBytesLimit
		}
		b, err := DecodeBatchCallback(bytes.NewReader(body), limit)
		if err != nil {
			assert.Nil(t, b)
			fzdCheckCallbackError(t, p, body, limit, err)
			return
		}
		require.NotNil(t, b)
		fzdCheckBatch(t, p, b)
		assert.False(t, b.Partial)
		fzdRoundTrip(t, b, func(data []byte) (*Batch, error) {
			return DecodeBatchCallback(bytes.NewReader(data), DefaultMaxResponseBytes)
		})
	})
}

// fzdCheckCallbackError asserts the invariants of a failed callback decode.
func fzdCheckCallbackError(t *testing.T, p fzdPatterns, body []byte, limit int64, err error) {
	t.Helper()
	ce := asCallError(t, err)
	assert.Equal(t, OpBatchCallback, ce.Op)
	assert.Zero(t, ce.StatusCode, "a callback has no status")
	assert.Zero(t, ce.Attempts, "a callback makes no request")
	assert.False(t, ce.OutcomeUnknown)
	assert.Regexp(t, p.field, ce.Field)
	fzdCheckCountPath(t, ce.Field)
	if int64(len(body)) > limit {
		assert.Equal(t, FailureResponseTooLarge, ce.Kind)
		assert.Empty(t, ce.BatchID, "an oversized body is never read for an ID")
		return
	}
	if kind, cause := checkStrict(body); cause != nil {
		assert.Equal(t, kind, ce.Kind, "the strict check's kind")
		require.ErrorIs(t, ce, cause)
	} else {
		assert.True(t, fzdDecodeKinds(ce.Kind), "kind %q", ce.Kind)
	}
	id, ok := rescueBatchID(body)
	assert.Equal(t, ok, ce.BatchID != "")
	assert.Equal(t, id, ce.BatchID, "the BatchID is the body's rescued ID")
	if ce.BatchID != "" {
		assert.True(t, validBatchID(ce.BatchID))
	}
}

// FuzzRescueBatchID checks rescueBatchID on any body against an oracle
// written with encoding/json's tokenizer: an ID is rescued exactly when the
// body's first value is an object with one key that folds to "id", that key
// spelled exactly "id" once decoded, holding a string with the batch ID
// shape.
func FuzzRescueBatchID(f *testing.F) {
	for _, seed := range fzdSeeds() {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		if len(body) > fzdMaxInput {
			return
		}
		got, ok := rescueBatchID(body)
		want, wantOK := fzdRescueOracle(body)
		require.Equal(t, wantOK, ok, "rescue of %q", body)
		require.Equal(t, want, got)
		if ok {
			assert.True(t, validBatchID(got))
		}
	})
}

// fzdRescueOracle is rescueBatchID's oracle: it walks the first JSON value of
// body with encoding/json's tokenizer.
func fzdRescueOracle(body []byte) (string, bool) {
	var first json.RawMessage
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&first); err != nil || first[0] != '{' {
		return "", false
	}
	dec := json.NewDecoder(bytes.NewReader(first))
	if _, err := dec.Token(); err != nil {
		return "", false
	}
	n, id, isID := 0, "", false
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return "", false
		}
		var value json.RawMessage
		if err = dec.Decode(&value); err != nil {
			return "", false
		}
		key, _ := tok.(string)
		// encoding/json repairs invalid UTF-8 in a key to U+FFFD, which folds
		// to no ASCII letter, so a key spelled with it never folds to "id".
		if !strings.EqualFold(key, "id") {
			continue
		}
		n++
		if key == "id" {
			isID = true
			id, _ = fzdBatchIDValue(value)
		}
	}
	if n != 1 || !isID || id == "" {
		return "", false
	}
	return id, true
}

// fzdBatchIDValue decodes value as a JSON string with the batch ID shape.
func fzdBatchIDValue(value json.RawMessage) (string, bool) {
	var s string
	if len(value) == 0 || value[0] != '"' || json.Unmarshal(value, &s) != nil {
		return "", false
	}
	if !utf8.Valid(value) || s == "" || len(s) > maxBatchIDBytes {
		return "", false
	}
	for i := range len(s) {
		if s[i] < 0x21 || s[i] > 0x7e {
			return "", false
		}
	}
	return s, true
}
