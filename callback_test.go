package emailable_test

import (
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-emailable"
	"github.com/mrz1836/go-emailable/internal/testutil"
)

// Shared values of the callback tests.
const (
	// callbackFixtureID is the batch the callback_* fixtures describe.
	callbackFixtureID = "65f0c1d2e3a4b5c6d7e8f902"

	// callbackSigMarker is the signature of the fixture download URL.
	callbackSigMarker = "sigmarker4e8a1f" // gitleaks:allow (synthetic test marker)

	// callbackSigned is the download URL of callback_download.json.
	callbackSigned = "https://downloads.example.com/sdk-fixture/" + callbackFixtureID +
		".csv?expires=1767225600&signature=" + callbackSigMarker

	// callbackLeak is planted in callback bodies and reader errors: no error
	// may ever contain it.
	callbackLeak = "cbkleakmarker6a2d94" // gitleaks:allow (synthetic test marker)

	// callbackOK is the smallest callback body the tests decode.
	callbackOK = `{"id":"` + callbackFixtureID + `","message":"` + callbackLeak + `"}`

	// The documented range of maxBytes: 1 KiB to 64 MiB.
	callbackMinBytes = 1 << 10
	callbackMaxBytes = 64 << 20
)

// errCallbackReader is a reader failure whose text holds a leak marker.
var errCallbackReader = errors.New("callback body read failed " + callbackLeak)

// callbackDecode decodes body with the default limit.
func callbackDecode(body string) (*emailable.Batch, error) {
	return emailable.DecodeBatchCallback(strings.NewReader(body), emailable.DefaultMaxResponseBytes)
}

// callbackFailure returns the CallError of a callback body that fails as
// kind at field: no HTTP status and no attempt, since no request was made.
func callbackFailure(kind emailable.FailureKind, batchID, field string) wantCall {
	return wantCall{Kind: kind, Op: emailable.OpBatchCallback, BatchID: batchID, Field: field}
}

// callbackSized returns a callback body of exactly n bytes: callbackOK
// followed by JSON whitespace.
func callbackSized(n int) string {
	return callbackOK + strings.Repeat(" ", n-len(callbackOK))
}

func TestDecodeBatchCallbackDecodesFixtures(t *testing.T) {
	tests := []struct {
		name     string
		file     string
		download string
		want     *emailable.Batch
	}{
		{
			name: "inline rows", file: "callback_complete.json",
			want: &emailable.Batch{
				ID:      callbackFixtureID,
				Message: "SDK fixture: completion callback of a synthetic batch.",
				Emails: []emailable.Verification{
					{
						Email: "avery.example@example.com", State: emailable.StateDeliverable,
						Reason: emailable.ReasonAcceptedEmail, Score: new(95), Free: new(false), Role: new(false),
					},
					{
						Email: "blake.example@mail.example.com", State: emailable.StateUndeliverable,
						Reason: emailable.ReasonInvalidDomain, Score: new(0), Domain: new("mail.example.com"),
					},
					{
						Email: "support@lists.example.com", State: emailable.StateRisky, Reason: emailable.ReasonLowQuality,
						Role: new(true),
					},
				},
				ReasonCounts: map[emailable.Reason]int64{"accepted_email": 1, "invalid_domain": 1, "low_quality": 1},
				TotalCounts: map[string]int64{
					"deliverable": 1, "undeliverable": 1, "risky": 1, "unknown": 0, "duplicate": 0, "processed": 3,
					"total": 3,
				},
			},
		},
		{
			name: "download file", file: "callback_download.json", download: callbackSigned,
			want: &emailable.Batch{
				ID: callbackFixtureID,
				Message: "SDK fixture: completion callback of a synthetic batch whose rows are in the download " +
					"file.",
				ReasonCounts: map[emailable.Reason]int64{"accepted_email": 1500, "rejected_email": 500},
				TotalCounts:  map[string]int64{"deliverable": 1500, "undeliverable": 500, "processed": 2000, "total": 2000},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, err := emailable.DecodeBatchCallback(strings.NewReader(fixture(t, tt.file)),
				emailable.DefaultMaxResponseBytes)
			require.NoError(t, err)
			assert.False(t, b.Partial, "a callback is never a partial read")
			assert.Equal(t, tt.download, b.DownloadFile.Reveal())
			testutil.AssertShapesNeverPrint(t, b, callbackSigned, callbackSigMarker)
			b.DownloadFile = emailable.DownloadURL{}
			assert.Equal(t, tt.want, b)
		})
	}
}

// TestDecodeBatchCallbackMatchesGetBatch decodes every GetBatch fixture as a
// callback body: one with an id decodes to what GetBatch returns, and one
// without fails, because a callback names its batch.
func TestDecodeBatchCallbackMatchesGetBatch(t *testing.T) {
	for _, tt := range getBatchFixtureCases() {
		t.Run(tt.name, func(t *testing.T) {
			body := fixture(t, tt.file)
			b, err := callbackDecode(body)
			if !strings.Contains(body, `"id"`) {
				_ = assertCallError(t, err, callbackFailure(emailable.FailureMalformedResponse, "", "id"))
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.download, b.DownloadFile.Reveal())
			b.DownloadFile = emailable.DownloadURL{}
			want := *tt.want
			want.Partial = false
			assert.Equal(t, &want, b)
		})
	}
}

func TestDecodeBatchCallbackArguments(t *testing.T) {
	t.Run("nil reader", func(t *testing.T) {
		b, err := emailable.DecodeBatchCallback(nil, emailable.DefaultMaxResponseBytes)
		assert.Nil(t, b)
		assertValidation(t, err, emailable.ErrInvalidRequest, "r", reasonNil)
	})
	t.Run("nil reader before a bad limit", func(t *testing.T) {
		_, err := emailable.DecodeBatchCallback(nil, 0)
		assertValidation(t, err, emailable.ErrInvalidRequest, "r", reasonNil)
	})

	for _, n := range []int64{math.MinInt64, -1, 0, 1, callbackMinBytes - 1, callbackMaxBytes + 1, math.MaxInt64} {
		t.Run("maxBytes "+strconv.FormatInt(n, 10)+" out of range", func(t *testing.T) {
			r := testutil.NewBody(callbackOK)
			b, err := emailable.DecodeBatchCallback(r, n)
			assert.Nil(t, b)
			assertValidation(t, err, emailable.ErrInvalidRequest, "maxBytes", reasonOutOfRange)
			assert.Zero(t, r.Reads(), "nothing is read")
			assert.Zero(t, r.Closes())
		})
	}

	for _, n := range []int64{callbackMinBytes, emailable.DefaultMaxResponseBytes, callbackMaxBytes} {
		t.Run("maxBytes "+strconv.FormatInt(n, 10)+" in range", func(t *testing.T) {
			b, err := emailable.DecodeBatchCallback(strings.NewReader(callbackOK), n)
			require.NoError(t, err)
			assert.Equal(t, callbackFixtureID, b.ID)
		})
	}
}

func TestDecodeBatchCallbackSizeLimit(t *testing.T) {
	for _, n := range []int{callbackMinBytes, 4096, 1 << 20} {
		limit := int64(n)
		t.Run("a body of exactly "+strconv.Itoa(n)+" bytes", func(t *testing.T) {
			r := testutil.NewBody(callbackSized(n))
			r.Step = 100 // many reads
			b, err := emailable.DecodeBatchCallback(r, limit)
			require.NoError(t, err)
			assert.Equal(t, callbackFixtureID, b.ID)
			assert.Equal(t, limit, r.BytesRead())
			assert.Zero(t, r.Closes(), "the reader is never closed")
		})
		t.Run("one byte over "+strconv.Itoa(n), func(t *testing.T) {
			r := testutil.NewBody(callbackSized(n + 1))
			b, err := emailable.DecodeBatchCallback(r, limit)
			assert.Nil(t, b)
			// The body names its batch, but an oversized body is never read
			// for it.
			_ = assertCallError(t, err, callbackFailure(emailable.FailureResponseTooLarge, "", ""))
			assert.Zero(t, r.Closes(), "the reader is never closed")
		})
	}

	t.Run("an endless body is read only one byte past the limit", func(t *testing.T) {
		r := &testutil.EndlessBody{}
		b, err := emailable.DecodeBatchCallback(r, callbackMinBytes)
		assert.Nil(t, b)
		_ = assertCallError(t, err, callbackFailure(emailable.FailureResponseTooLarge, "", ""))
		assert.LessOrEqual(t, r.BytesRead(), int64(callbackMinBytes+1))
		assert.Zero(t, r.Closes(), "the reader is never closed")
	})
}

func TestDecodeBatchCallbackReaderFailures(t *testing.T) {
	bodyThen := func(content string, edit func(*testutil.Body)) func() *testutil.Body {
		return func() *testutil.Body {
			b := testutil.NewBody(content)
			edit(b)
			return b
		}
	}
	tests := []struct {
		name string
		body func() *testutil.Body
	}{
		{"error after the whole body", bodyThen(callbackOK, func(b *testutil.Body) { b.End = errCallbackReader })},
		{"error with nothing read", bodyThen("", func(b *testutil.Body) { b.End = errCallbackReader })},
		{"unexpected EOF", bodyThen(callbackOK[:10], func(b *testutil.Body) { b.End = io.ErrUnexpectedEOF })},
		{"stalled with nothing read", bodyThen("", func(b *testutil.Body) { b.Stall = true })},
		{"stalled after the whole body", bodyThen(callbackOK, func(b *testutil.Body) { b.Stall = true })},
		{"100 empty reads in a row", bodyThen(callbackOK, func(b *testutil.Body) { b.Empties = 100 })},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := tt.body()
			b, err := emailable.DecodeBatchCallback(r, emailable.DefaultMaxResponseBytes)
			assert.Nil(t, b)
			_ = assertCallError(t, err, callbackFailure(emailable.FailureTransport, "", ""), callbackLeak)
			require.NotErrorIs(t, err, errCallbackReader, "the reader's own error is never kept")
			assert.Zero(t, r.Closes(), "the reader is never closed")
		})
	}

	readers := []struct {
		name string
		r    io.Reader
	}{
		{"failing reader", iotest.ErrReader(errCallbackReader)},
		{"timeout after one byte", iotest.TimeoutReader(iotest.OneByteReader(strings.NewReader(callbackOK)))},
	}
	for _, tt := range readers {
		t.Run(tt.name, func(t *testing.T) {
			_, err := emailable.DecodeBatchCallback(tt.r, emailable.DefaultMaxResponseBytes)
			_ = assertCallError(t, err, callbackFailure(emailable.FailureTransport, "", ""), callbackLeak)
		})
	}
}

func TestDecodeBatchCallbackReadsAnyChunking(t *testing.T) {
	slow := testutil.NewBody(callbackOK)
	slow.Empties = 99
	slow.Step = 7
	readers := []struct {
		name string
		r    io.Reader
	}{
		{"one byte at a time", iotest.OneByteReader(strings.NewReader(callbackOK))},
		{"half of each read", iotest.HalfReader(strings.NewReader(callbackOK))},
		{"data with EOF", iotest.DataErrReader(strings.NewReader(callbackOK))},
		{"99 empty reads before each chunk", slow},
	}
	for _, tt := range readers {
		t.Run(tt.name, func(t *testing.T) {
			b, err := emailable.DecodeBatchCallback(tt.r, callbackMinBytes)
			require.NoError(t, err)
			assert.Equal(t, &emailable.Batch{ID: callbackFixtureID, Message: callbackLeak}, b)
		})
	}
	assert.Zero(t, slow.Closes(), "the reader is never closed")
}

func TestDecodeBatchCallbackStrictFailures(t *testing.T) {
	const malformed = emailable.FailureMalformedResponse
	id := callbackFixtureID
	tests := []struct {
		name    string
		body    string
		kind    emailable.FailureKind
		batchID string
	}{
		{"empty body", ``, malformed, ""},
		{"invalid JSON", `{"id":"` + id + `",`, malformed, ""},
		{"invalid UTF-8 in the message", "{\"id\":\"" + id + "\",\"message\":\"\xff\"}", malformed, id},
		{"invalid UTF-8 inside the id", "{\"id\":\"b\xff1\",\"message\":\"m\"}", malformed, ""},
		{"duplicate message", `{"id":"` + id + `","message":"a","message":"` + callbackLeak + `"}`, malformed, id},
		{"duplicate keys differing in case", `{"id":"` + id + `","Emails":[],"emails":[]}`, malformed, id},
		{"id twice", `{"id":"` + id + `","id":"` + id + `"}`, malformed, ""},
		{"id and ID", `{"id":"` + id + `","ID":"other"}`, malformed, ""},
		{"trailing data", `{"id":"` + id + `"} {"id":"other"}`, malformed, id},
		{"trailing data after an unreadable id", `{"id":"a b"} x`, malformed, ""},
		{"not an object", `[{"id":"` + id + `"}]`, emailable.FailureUnexpectedResponse, ""},
		{"null", `null`, emailable.FailureUnexpectedResponse, ""},
		{"string", `"` + id + `"`, emailable.FailureUnexpectedResponse, ""},
		{
			"too many containers", `{"id":"` + id + `","pad":[` + strings.TrimSuffix(strings.Repeat("[],", 100_001), ",") + `]}`,
			emailable.FailureResponseTooLarge, id,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, err := callbackDecode(tt.body)
			assert.Nil(t, b)
			_ = assertCallError(t, err, callbackFailure(tt.kind, tt.batchID, ""), callbackLeak)
		})
	}
}

func TestDecodeBatchCallbackDecodeFailures(t *testing.T) {
	const malformed, unexpected = emailable.FailureMalformedResponse, emailable.FailureUnexpectedResponse
	id := callbackFixtureID
	withID := func(members string) string { return `{"id":"` + id + `"` + members + `}` }
	tests := []struct {
		name    string
		body    string
		kind    emailable.FailureKind
		batchID string
		field   string
	}{
		{"no documented member", `{"status":"done"}`, malformed, "", "id"},
		{"empty object", `{}`, malformed, "", "id"},
		{"missing id", `{"message":"` + callbackLeak + `","total_counts":{"processed":1,"total":1}}`, malformed, "", "id"},
		{"null id", `{"id":null,"message":"m"}`, malformed, "", "id"},
		{"other case only", `{"ID":"` + id + `","message":"m"}`, malformed, "", "id"},
		{"numeric id", `{"id":5}`, malformed, "", "id"},
		{"empty id", `{"id":"","message":"m"}`, unexpected, "", "id"},
		{"id with a space", `{"id":"a b"}`, unexpected, "", "id"},
		{"id of 257 bytes", `{"id":"` + strings.Repeat("i", 257) + `"}`, unexpected, "", "id"},
		{"bad row field", withID(`,"emails":[{"email":"a@example.com"},{"score":101}]`), malformed, id, "emails[1].score"},
		{"row that is not an object", withID(`,"emails":[{},5]`), malformed, id, "emails[1]"},
		{"bad count", withID(`,"total_counts":{"total":-1}`), malformed, id, "total_counts.total"},
		{"count key not a token", withID(`,"reason_counts":{"No Connect":1}`), unexpected, id, "reason_counts"},
		{"message as a number", withID(`,"message":5`), malformed, id, "message"},
		{
			"download file over 8 KiB", withID(`,"download_file":"https://` + strings.Repeat("d", 8192) + `"`),
			emailable.FailureResponseTooLarge, id, "download_file",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, err := callbackDecode(tt.body)
			assert.Nil(t, b)
			_ = assertCallError(t, err, callbackFailure(tt.kind, tt.batchID, tt.field), callbackLeak)
		})
	}
}

func TestDecodeBatchCallbackAcceptsAnyID(t *testing.T) {
	for _, id := range []string{"b", strings.Repeat("i", 256), "a&b=c", `"<q>"`} {
		t.Run(id, func(t *testing.T) {
			b, err := callbackDecode(`{"id":` + testutil.JSONQuote(id) + `}`)
			require.NoError(t, err)
			assert.Equal(t, &emailable.Batch{ID: id}, b, "the Batch's ID is the body's")
		})
	}
}

// TestDecodeBatchCallbackMakesNoRequest decodes bodies, good and bad, with
// http.DefaultTransport replaced by one that fails the test: the decoder
// needs no Client and touches no network.
func TestDecodeBatchCallbackMakesNoRequest(t *testing.T) {
	ct := &testutil.CountingTransport{Next: offlineTransport(t)}
	saved := http.DefaultTransport
	http.DefaultTransport = ct
	t.Cleanup(func() { http.DefaultTransport = saved })

	for _, body := range []string{
		fixture(t, "callback_complete.json"), fixture(t, "callback_download.json"), `{}`, `{"id":5}`, `{`,
	} {
		_, _ = callbackDecode(body) // the outcomes are asserted elsewhere
	}
	_, _ = emailable.DecodeBatchCallback(iotest.ErrReader(errCallbackReader), callbackMinBytes)
	assert.Zero(t, ct.Attempts())
}

// TestCallbackHandlerExampleRefusesEmptyToken proves that the documented
// callback handler fails closed: built with an empty token, it refuses every
// delivery, a request that carries no token included.
func TestCallbackHandlerExampleRefusesEmptyToken(t *testing.T) {
	h := callbackHandler("", func(string) { assert.Fail(t, "a request without a token must never pass") })
	for _, target := range []string{"/hooks/emailable", "/hooks/emailable?token="} {
		w := httptest.NewRecorder()
		h(w, httptest.NewRequestWithContext(t.Context(), http.MethodPost, target, strings.NewReader(`{"id":"b1"}`)))
		assert.Equal(t, http.StatusForbidden, w.Code, target)
	}
}
