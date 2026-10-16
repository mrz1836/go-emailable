package emailable_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-emailable"
	"github.com/mrz1836/go-emailable/internal/testutil"
)

// Shared values of the GetBatch tests.
const (
	// getBatchFixtureID is the batch the batch_* fixtures describe.
	getBatchFixtureID = "65f0c1d2e3a4b5c6d7e8f901"

	// getBatchSigMarker is the signature of every fixture download URL: no
	// formatted value may ever contain it.
	getBatchSigMarker = "sigmarker4e8a1f" // gitleaks:allow (synthetic test marker)

	// getBatchSigned is the download URL of the batch_* fixtures.
	getBatchSigned = "https://downloads.example.com/sdk-fixture/" + getBatchFixtureID +
		".csv?expires=1767225600&signature=" + getBatchSigMarker

	// getBatchLeak is planted inside rejected batch IDs: no error may ever
	// contain it.
	getBatchLeak = "gbleakmarker8c3b07" // gitleaks:allow (synthetic test marker)

	// getBatchMessage is the message of the answers the tests build.
	getBatchMessage = "SDK fixture: batch snapshot."
)

// errGetBatchTransport is a transport failure whose text holds a leak marker.
var errGetBatchTransport = errors.New("read tcp: connection reset by peer " + getBatchLeak)

// getBatchAnswer returns a GetBatch answer for testBatchID with members
// appended, such as `,"total":3`.
func getBatchAnswer(members string) string {
	return `{"id":"` + testBatchID + `"` + members + `}`
}

// getBatchFailure returns the CallError of a GetBatch answer for testBatchID,
// received with HTTP 200, that fails to decode as kind at field: a free read,
// so the outcome is always known.
func getBatchFailure(kind emailable.FailureKind, field string) wantCall {
	return wantCall{
		Kind: kind, Op: emailable.OpGetBatch, Status: http.StatusOK, BatchID: testBatchID, Attempts: 1, Field: field,
	}
}

// getBatchInlineRows are the rows of batch_complete_inline.json: every field
// of a row is optional, as response_fields can limit them.
func getBatchInlineRows() []emailable.Verification {
	return []emailable.Verification{
		{
			Email: "avery.example+news@example.com", State: emailable.StateDeliverable,
			Reason: emailable.ReasonAcceptedEmail, Score: new(96), AcceptAll: new(false), Disposable: new(false),
			Free: new(true), MailboxFull: new(false), NoReply: new(false), Role: new(false), BirthYear: new(1990),
			Domain: new("example.com"), User: new("avery.example+news"), Tag: new("news"),
			MXRecord: new("mx.example.com"), SMTPProvider: new("SDK Fixture Mail"), FirstName: new("Avery"),
			LastName: new("Example"), FullName: new("Avery Example"), Gender: new("unknown"),
		},
		{Email: "blake.example@mail.example.com", State: emailable.StateUndeliverable, Reason: emailable.ReasonRejectedEmail},
		{
			Email: "casey.example@lists.example.com", State: emailable.StateRisky, Reason: emailable.ReasonLowDeliverability,
			AcceptAll: new(true), DidYouMean: new("casey.example@example.com"),
		},
		{Email: "avery.example+news@example.com", State: emailable.StateDuplicate},
		{State: emailable.StateUnknown, Reason: emailable.ReasonNoConnect},
		{},
	}
}

// getBatchDownloadReasons are the reason counts of
// batch_complete_download.json: 1,001 addresses.
func getBatchDownloadReasons() map[emailable.Reason]int64 {
	return map[emailable.Reason]int64{
		"accepted_email": 700, "low_quality": 50, "low_deliverability": 40, "invalid_email": 60,
		"invalid_domain": 30, "rejected_email": 70, "invalid_smtp": 11, "no_connect": 15, "timeout": 10,
		"unavailable_smtp": 9, "unexpected_error": 6,
	}
}

// getBatchFixtureCase is one batch fixture and the Batch it decodes to,
// DownloadFile aside.
type getBatchFixtureCase struct {
	name     string
	file     string
	partial  bool
	query    string
	download string
	want     *emailable.Batch
}

// getBatchFixtureCases are the documented snapshot shapes, as SDK fixtures.
func getBatchFixtureCases() []getBatchFixtureCase {
	idQuery := "id=" + getBatchFixtureID
	return []getBatchFixtureCase{
		{
			name: "processing, without an id", file: "batch_processing.json", query: idQuery,
			want: &emailable.Batch{
				ID: getBatchFixtureID, Message: "SDK fixture: this synthetic batch is still being verified.",
				Processed: new(int64(120)), Total: new(int64(500)),
			},
		},
		{
			name: "partial rows of a running batch", file: "batch_partial.json", partial: true,
			query: idQuery + "&partial=true",
			want: &emailable.Batch{
				ID: getBatchFixtureID, Partial: true,
				Message:   "SDK fixture: partial rows of a synthetic batch that is still running.",
				Processed: new(int64(2)), Total: new(int64(5)),
				Emails: []emailable.Verification{
					{
						Email: "avery.example@example.com", State: emailable.StateDeliverable,
						Reason: emailable.ReasonAcceptedEmail, Score: new(96),
					},
					{
						Email: "blake.example@mail.example.com", State: emailable.StateUndeliverable,
						Reason: emailable.ReasonRejectedEmail, Score: new(0),
					},
				},
				TotalCounts: map[string]int64{"processed": 2, "total": 5},
			},
		},
		{
			name: "complete with inline rows", file: "batch_complete_inline.json", query: idQuery,
			want: &emailable.Batch{
				ID: getBatchFixtureID, Message: "SDK fixture: this synthetic batch has finished.",
				Emails: getBatchInlineRows(),
				ReasonCounts: map[emailable.Reason]int64{
					"accepted_email": 1, "rejected_email": 1, "low_deliverability": 1, "no_connect": 1,
				},
				TotalCounts: map[string]int64{
					"deliverable": 1, "undeliverable": 1, "risky": 1, "unknown": 1, "duplicate": 1, "processed": 6,
					"total": 6,
				},
			},
		},
		{
			name: "complete with a download file", file: "batch_complete_download.json", query: idQuery,
			download: getBatchSigned,
			want: &emailable.Batch{
				ID: getBatchFixtureID,
				Message: "SDK fixture: this synthetic batch of 1,001 addresses has finished; its rows are in " +
					"the download file.",
				ReasonCounts: getBatchDownloadReasons(),
				TotalCounts: map[string]int64{
					"deliverable": 700, "undeliverable": 171, "risky": 90, "unknown": 40, "duplicate": 0,
					"processed": 1001, "total": 1001,
				},
			},
		},
		{
			name: "expired: counts only", file: "batch_expired_counts.json", query: idQuery,
			want: &emailable.Batch{
				ID: getBatchFixtureID,
				Message: "SDK fixture: the rows of this synthetic batch are no longer kept; only its counts " +
					"remain.",
				ReasonCounts: map[emailable.Reason]int64{"accepted_email": 3, "invalid_domain": 1, "timeout": 1},
				TotalCounts: map[string]int64{
					"deliverable": 3, "undeliverable": 1, "risky": 0, "unknown": 1, "duplicate": 0, "processed": 5,
					"total": 5,
				},
			},
		},
		{
			name: "both inline rows and a download file", file: "batch_emails_and_download.json", query: idQuery,
			download: getBatchSigned,
			want: &emailable.Batch{
				ID:      getBatchFixtureID,
				Message: "SDK fixture: a synthetic snapshot with both inline rows and a download file.",
				Emails: []emailable.Verification{
					{Email: "avery.example@example.com", State: emailable.StateDeliverable, Reason: emailable.ReasonAcceptedEmail},
					{Email: "blake.example@mail.example.com", State: emailable.StateRisky, Reason: emailable.ReasonLowQuality},
				},
				ReasonCounts: map[emailable.Reason]int64{"accepted_email": 1, "low_quality": 1},
				TotalCounts:  map[string]int64{"deliverable": 1, "risky": 1, "processed": 2, "total": 2},
			},
		},
	}
}

func TestGetBatchDecodesFixtures(t *testing.T) {
	for _, tt := range getBatchFixtureCases() {
		t.Run(tt.name, func(t *testing.T) {
			c, s := newScriptClient(t, testutil.Step{Body: fixture(t, tt.file)})
			b, err := c.GetBatch(t.Context(), emailable.GetBatchRequest{BatchID: getBatchFixtureID, Partial: tt.partial})
			require.NoError(t, err)
			r := testutil.OnlyRequest(t, s)
			assertWireRequest(t, r, http.MethodGet, pathBatch)
			assertNoReplay(t, r)
			assert.Equal(t, tt.query, rawQuery(t, r))

			assert.Equal(t, tt.download, b.DownloadFile.Reveal())
			assert.Equal(t, tt.download == "", b.DownloadFile.IsZero())
			b.DownloadFile = emailable.DownloadURL{}
			assert.Equal(t, tt.want, b)
		})
	}
}

// TestGetBatchPartialIsASubset reads a partial snapshot: Partial is set, and
// its rows are fewer than the batch's total, with no cursor to the rest.
func TestGetBatchPartialIsASubset(t *testing.T) {
	c, _ := newScriptClient(t, testutil.Step{Body: fixture(t, "batch_partial.json")})
	b, err := c.GetBatch(t.Context(), emailable.GetBatchRequest{BatchID: getBatchFixtureID, Partial: true})
	require.NoError(t, err)
	assert.True(t, b.Partial)
	processed, total, ok := b.Progress()
	require.True(t, ok)
	assert.Equal(t, int64(2), processed)
	assert.Equal(t, int64(5), total)
	assert.Less(t, int64(len(b.Emails)), total, "a partial snapshot holds a subset of the rows")
}

// getBatchRows returns a GetBatch answer for testBatchID with n generated
// inline rows, and the rows it decodes to.
func getBatchRows(n int) (string, []emailable.Verification) {
	rows := make([]string, n)
	want := make([]emailable.Verification, n)
	for i := range n {
		email := "user" + strconv.Itoa(i) + "@example.com"
		score := i % 101
		rows[i] = `{"email":"` + email + `","state":"deliverable","reason":"accepted_email","score":` + strconv.Itoa(score) + `}`
		want[i] = emailable.Verification{
			Email: email, State: emailable.StateDeliverable, Reason: emailable.ReasonAcceptedEmail, Score: new(score),
		}
	}
	count := strconv.Itoa(n)
	return getBatchAnswer(`,"message":"` + getBatchMessage + `","emails":[` + strings.Join(rows, ",") +
		`],"total_counts":{"processed":` + count + `,"total":` + count + `}`), want
}

// TestGetBatchDecodesByShapeNotSize decodes generated snapshots: the SDK
// reads whatever shape arrives, so inline rows past the documented 1,000 and
// a download file for a tiny batch decode as well.
func TestGetBatchDecodesByShapeNotSize(t *testing.T) {
	for _, n := range []int{1000, 1001} {
		t.Run(strconv.Itoa(n)+" inline rows", func(t *testing.T) {
			body, want := getBatchRows(n)
			b, err := callOnce(t, pathBatch, body, callGet)
			require.NoError(t, err)
			assert.Equal(t, want, b.Emails)
			assert.Equal(t, map[string]int64{"processed": int64(n), "total": int64(n)}, b.TotalCounts)
			assert.True(t, b.DownloadFile.IsZero())
		})
	}

	t.Run("download file for two addresses", func(t *testing.T) {
		b, err := callOnce(t, pathBatch, getBatchAnswer(`,"download_file":"`+getBatchSigned+
			`","total_counts":{"processed":2,"total":2}`), callGet)
		require.NoError(t, err)
		assert.Equal(t, getBatchSigned, b.DownloadFile.Reveal())
		assert.Nil(t, b.Emails)
	})
}

func TestGetBatchWireQuery(t *testing.T) {
	id := "id=" + testBatchID
	tests := []struct {
		name string
		req  emailable.GetBatchRequest
		want string
	}{
		{"id only", emailable.GetBatchRequest{BatchID: testBatchID}, id},
		{"partial false is never sent", emailable.GetBatchRequest{BatchID: testBatchID, Partial: false}, id},
		{"partial", emailable.GetBatchRequest{BatchID: testBatchID, Partial: true}, id + "&partial=true"},
		{
			"simulate generic_error",
			emailable.GetBatchRequest{BatchID: testBatchID, Simulate: emailable.SimulateGenericError},
			id + "&simulate=generic_error",
		},
		{
			"simulate importing",
			emailable.GetBatchRequest{BatchID: testBatchID, Simulate: emailable.SimulateImporting},
			id + "&simulate=importing",
		},
		{
			"simulate verifying",
			emailable.GetBatchRequest{BatchID: testBatchID, Simulate: emailable.SimulateVerifying},
			id + "&simulate=verifying",
		},
		{
			"simulate paused",
			emailable.GetBatchRequest{BatchID: testBatchID, Simulate: emailable.SimulatePaused},
			id + "&simulate=paused",
		},
		{
			"partial and simulate",
			emailable.GetBatchRequest{BatchID: testBatchID, Partial: true, Simulate: emailable.SimulatePaused},
			id + "&partial=true&simulate=paused",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, s := newScriptClient(t, testutil.Step{Body: `{"message":"` + getBatchMessage + `"}`})
			b, err := c.GetBatch(t.Context(), tt.req)
			require.NoError(t, err)
			assert.Equal(t, tt.req.Partial, b.Partial)
			r := testutil.OnlyRequest(t, s)
			assertWireRequest(t, r, http.MethodGet, pathBatch)
			assertNoReplay(t, r)
			assert.Equal(t, tt.want, rawQuery(t, r))
			if !tt.req.Partial {
				assert.NotContains(t, query(t, r), "partial")
			}
		})
	}
}

func TestGetBatchIDsNeedingQueryEscapes(t *testing.T) {
	printable := ""
	for c := byte(0x21); c <= 0x7e; c++ {
		printable += string(rune(c))
	}
	tests := []struct {
		id   string
		want string
	}{
		{"a&b=c", "id=a%26b%3Dc"},
		{"x+y", "id=x%2By"},
		{"%", "id=%25"},
		{"100%25", "id=100%2525"},
		{"a/b?c#d", "id=a%2Fb%3Fc%23d"},
		{`"<q>"`, "id=%22%3Cq%3E%22"},
		{printable, "id=" + url.QueryEscape(printable)},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			// encoding/json escapes HTML characters in the echoed id: the SDK
			// compares the decoded ID.
			c, s := newScriptClient(t, testutil.Step{Body: `{"id":` + testutil.JSONQuote(tt.id) + `}`})
			b, err := c.GetBatch(t.Context(), emailable.GetBatchRequest{BatchID: tt.id})
			require.NoError(t, err)
			assert.Equal(t, tt.id, b.ID)
			r := testutil.OnlyRequest(t, s)
			assert.Equal(t, tt.want, rawQuery(t, r))
			assert.Equal(t, url.Values{"id": {tt.id}}, query(t, r), "the query round-trips to exactly the ID")
		})
	}
}

func TestGetBatchRejectsInvalidRequests(t *testing.T) {
	const field = "BatchID"
	leaky := func(s string) string { return strings.ReplaceAll(s, "LEAK", getBatchLeak) }
	tests := []struct {
		name   string
		req    emailable.GetBatchRequest
		field  string
		reason string
	}{
		{"empty id", emailable.GetBatchRequest{}, field, reasonRequired},
		{"empty id with partial and a bad simulation", emailable.GetBatchRequest{Partial: true, Simulate: "x"}, field, reasonRequired},
		{"id of 257 bytes", emailable.GetBatchRequest{BatchID: getBatchLeak + strings.Repeat("i", 257-len(getBatchLeak))}, field, reasonInvalidFormat},
		{"space", emailable.GetBatchRequest{BatchID: leaky("LEAK 1")}, field, reasonInvalidFormat},
		{"leading space", emailable.GetBatchRequest{BatchID: leaky(" LEAK")}, field, reasonInvalidFormat},
		{"non-ASCII", emailable.GetBatchRequest{BatchID: leaky("LEAK\u00e9")}, field, reasonInvalidFormat},
		{"invalid UTF-8", emailable.GetBatchRequest{BatchID: leaky("LEAK\xff")}, field, reasonInvalidFormat},
		{"NUL", emailable.GetBatchRequest{BatchID: leaky("LEAK\x00")}, field, reasonInvalidFormat},
		{"tab", emailable.GetBatchRequest{BatchID: leaky("LEAK\t")}, field, reasonInvalidFormat},
		{"line feed", emailable.GetBatchRequest{BatchID: leaky("LEAK\n")}, field, reasonInvalidFormat},
		{"DEL", emailable.GetBatchRequest{BatchID: leaky("LEAK\x7f")}, field, reasonInvalidFormat},
		{"bad id before a bad simulation", emailable.GetBatchRequest{BatchID: leaky("LEAK "), Simulate: "x"}, field, reasonInvalidFormat},
		{
			"payment_error is for CreateBatch",
			emailable.GetBatchRequest{BatchID: testBatchID, Simulate: emailable.SimulatePaymentError},
			"Simulate", "wrong_operation",
		},
		{
			"insufficient_credits_error is for CreateBatch",
			emailable.GetBatchRequest{BatchID: testBatchID, Simulate: emailable.SimulateInsufficientCreditsError},
			"Simulate", "wrong_operation",
		},
		{"unknown simulation", emailable.GetBatchRequest{BatchID: testBatchID, Simulate: "explode"}, "Simulate", "unknown_value"},
		{"simulation in another case", emailable.GetBatchRequest{BatchID: testBatchID, Simulate: "Paused"}, "Simulate", "unknown_value"},
	}
	c, ct := newOfflineClient(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, err := c.GetBatch(t.Context(), tt.req)
			assert.Nil(t, b)
			assertValidation(t, err, emailable.ErrInvalidRequest, tt.field, tt.reason)
			assertErrorClean(t, err, getBatchLeak, testKey)
		})
	}
	assert.Zero(t, ct.Attempts(), "no invalid request may reach the transport")
}

func TestGetBatchAcceptsAnIDOf256Bytes(t *testing.T) {
	id := strings.Repeat("i", 256)
	b, err := callOnce(t, pathBatch, `{"id":"`+id+`"}`, func(ctx context.Context, c *emailable.Client) (*emailable.Batch, error) {
		return c.GetBatch(ctx, emailable.GetBatchRequest{BatchID: id})
	})
	require.NoError(t, err)
	assert.Equal(t, id, b.ID)
}

func TestGetBatchIDMatch(t *testing.T) {
	accepted := []struct {
		name string
		body string
	}{
		{"same id", getBatchAnswer(``)},
		{"id absent", `{"message":"` + getBatchMessage + `"}`},
		{"id null", `{"id":null,"message":"` + getBatchMessage + `"}`},
		{"same id escaped", testutil.Esc(`{"id":"~u0062atchmarker5d0c7e19a4"}`)},
	}
	for _, tt := range accepted {
		t.Run(tt.name, func(t *testing.T) {
			b, err := callOnce(t, pathBatch, tt.body, callGet)
			require.NoError(t, err)
			assert.Equal(t, testBatchID, b.ID, "the Batch is for the requested ID")
		})
	}

	refused := []struct {
		name string
		body string
		kind emailable.FailureKind
	}{
		{"another batch", `{"id":"otherbatch1","message":"m"}`, emailable.FailureUnexpectedResponse},
		{"same id in another case", `{"id":"` + strings.ToUpper(testBatchID) + `"}`, emailable.FailureUnexpectedResponse},
		{"a prefix of the id", `{"id":"` + testBatchID[:5] + `"}`, emailable.FailureUnexpectedResponse},
		{"empty id", `{"id":"","message":"m"}`, emailable.FailureUnexpectedResponse},
		{"id with a space", `{"id":"batch 1"}`, emailable.FailureUnexpectedResponse},
		{"numeric id", `{"id":5}`, emailable.FailureMalformedResponse},
		{"object id", `{"id":{}}`, emailable.FailureMalformedResponse},
	}
	for _, tt := range refused {
		t.Run(tt.name, func(t *testing.T) {
			_, err := callOnce(t, pathBatch, tt.body, callGet)
			_ = assertCallError(t, err, getBatchFailure(tt.kind, "id"))
		})
	}
}

func TestGetBatchNeedsADocumentedMember(t *testing.T) {
	for _, body := range []string{
		`{}`, `{"message":null}`, `{"id":null}`, `{"ID":"` + testBatchID + `"}`, `{"Message":"m"}`,
		`{"status":"ok","batch":{"id":"` + testBatchID + `"}}`,
		`{"id":null,"message":null,"processed":null,"total":null,"emails":null,"download_file":null,` +
			`"reason_counts":null,"total_counts":null}`,
	} {
		t.Run(body, func(t *testing.T) {
			_, err := callOnce(t, pathBatch, body, callGet)
			_ = assertCallError(t, err, getBatchFailure(emailable.FailureUnexpectedResponse, ""))
		})
	}

	for _, member := range []string{
		`"id":"` + testBatchID + `"`, `"message":""`, `"processed":0`, `"total":0`, `"emails":[]`,
		`"download_file":""`, `"reason_counts":{}`, `"total_counts":{}`,
	} {
		t.Run("only "+member, func(t *testing.T) {
			b, err := callOnce(t, pathBatch, `{`+member+`}`, callGet)
			require.NoError(t, err)
			assert.Equal(t, testBatchID, b.ID)
		})
	}
}

func TestGetBatchCountFailures(t *testing.T) {
	malformed := []struct {
		name, members, field string
	}{
		{"negative processed", `,"processed":-1`, "processed"},
		{"fractional processed", `,"processed":1.5`, "processed"},
		{"integral fraction", `,"processed":2.0`, "processed"},
		{"exponent", `,"processed":1e3`, "processed"},
		{"quoted processed", `,"processed":"5"`, "processed"},
		{"boolean processed", `,"processed":true`, "processed"},
		{"processed over int64", `,"processed":9223372036854775808`, "processed"},
		{"negative total", `,"total":-3`, "total"},
		{"fractional total", `,"total":0.5`, "total"},
		{"negative state count", `,"total_counts":{"processed":1,"deliverable":-1}`, "total_counts.deliverable"},
		{"fractional state count", `,"total_counts":{"deliverable":1.5}`, "total_counts.deliverable"},
		{"quoted count", `,"total_counts":{"total":"3"}`, "total_counts.total"},
		{"negative reason count", `,"reason_counts":{"accepted_email":-2}`, "reason_counts.accepted_email"},
		{"object reason count", `,"reason_counts":{"timeout":{}}`, "reason_counts.timeout"},
		{"reason counts as an array", `,"reason_counts":[]`, "reason_counts"},
		{"total counts as a number", `,"total_counts":5`, "total_counts"},
		{"total counts as a string", `,"total_counts":"x"`, "total_counts"},
	}
	for _, tt := range malformed {
		t.Run(tt.name, func(t *testing.T) {
			_, err := callOnce(t, pathBatch, getBatchAnswer(tt.members), callGet)
			_ = assertCallError(t, err, getBatchFailure(emailable.FailureMalformedResponse, tt.field))
		})
	}

	t.Run("null counts are absent", func(t *testing.T) {
		b, err := callOnce(t, pathBatch, getBatchAnswer(`,"reason_counts":{"timeout":null},`+
			`"total_counts":{"unknown":null,"total":2}`), callGet)
		require.NoError(t, err)
		assert.Equal(t, map[emailable.Reason]int64{}, b.ReasonCounts, "an empty map, not nil")
		assert.Equal(t, map[string]int64{"total": 2}, b.TotalCounts)
	})

	t.Run("a null count still needs a valid key", func(t *testing.T) {
		_, err := callOnce(t, pathBatch, getBatchAnswer(`,"total_counts":{"Bad Key":null}`), callGet)
		_ = assertCallError(t, err, getBatchFailure(emailable.FailureUnexpectedResponse, "total_counts"))
	})

	t.Run("largest counts", func(t *testing.T) {
		b, err := callOnce(t, pathBatch, getBatchAnswer(`,"processed":9223372036854775807,"total":-0,`+
			`"total_counts":{"total":9223372036854775807}`), callGet)
		require.NoError(t, err)
		assert.Equal(t, new(int64(math.MaxInt64)), b.Processed)
		assert.Equal(t, new(int64(0)), b.Total)
		assert.Equal(t, map[string]int64{"total": math.MaxInt64}, b.TotalCounts)
	})
}

func TestGetBatchKeepsOpenCountKeys(t *testing.T) {
	b, err := callOnce(t, pathBatch, getBatchAnswer(testutil.Esc(`,"reason_counts":{"accepted_email":3,`+
		`"brand_new_reason":1,"low~u005fquality":2},"total_counts":{"deliverable":3,"duplicate":1,`+
		`"processed":4,"total":4,"brand_new_total":2,"2fa_required":0}`)), callGet)
	require.NoError(t, err)
	assert.Equal(t, map[emailable.Reason]int64{"accepted_email": 3, "brand_new_reason": 1, "low_quality": 2}, b.ReasonCounts)
	assert.Equal(t, map[string]int64{
		"deliverable": 3, "duplicate": 1, "processed": 4, "total": 4, "brand_new_total": 2, "2fa_required": 0,
	}, b.TotalCounts)
	assert.True(t, emailable.ReasonAcceptedEmail.Known())
	assert.False(t, emailable.Reason("brand_new_reason").Known(), "an undocumented reason is kept, not known")
}

func TestGetBatchCountKeyShape(t *testing.T) {
	refused := []struct {
		name, members, field string
	}{
		{"uppercase total key", `,"total_counts":{"Deliverable":1}`, "total_counts"},
		{"space in a reason key", `,"reason_counts":{"low quality":1}`, "reason_counts"},
		{"empty key", `,"reason_counts":{"":1}`, "reason_counts"},
		{"hyphen", `,"total_counts":{"to-do":1}`, "total_counts"},
		{"non-ASCII key", `,"total_counts":{"d` + "\u00e9" + `liverable":1}`, "total_counts"},
		{"key of 65 bytes", `,"reason_counts":{"` + strings.Repeat("k", 65) + `":1}`, "reason_counts"},
	}
	for _, tt := range refused {
		t.Run(tt.name, func(t *testing.T) {
			_, err := callOnce(t, pathBatch, getBatchAnswer(tt.members), callGet)
			_ = assertCallError(t, err, getBatchFailure(emailable.FailureUnexpectedResponse, tt.field))
		})
	}

	keys := func(n int) string {
		members := make([]string, n)
		for i := range members {
			members[i] = `"k` + strconv.Itoa(i) + `":1`
		}
		return "{" + strings.Join(members, ",") + "}"
	}
	t.Run("256 keys and one of 64 bytes", func(t *testing.T) {
		b, err := callOnce(t, pathBatch, getBatchAnswer(`,"reason_counts":`+keys(256)+
			`,"total_counts":{"`+strings.Repeat("k", 64)+`":1}`), callGet)
		require.NoError(t, err)
		assert.Len(t, b.ReasonCounts, 256)
		assert.Len(t, b.TotalCounts, 1)
	})
	t.Run("257 keys", func(t *testing.T) {
		_, err := callOnce(t, pathBatch, getBatchAnswer(`,"reason_counts":`+keys(257)), callGet)
		_ = assertCallError(t, err, getBatchFailure(emailable.FailureResponseTooLarge, "reason_counts"))
	})
}

func TestGetBatchEmailsPresence(t *testing.T) {
	tests := []struct {
		name    string
		members string
		want    []emailable.Verification
	}{
		{"absent", `,"message":"m"`, nil},
		{"null", `,"emails":null`, nil},
		{"empty", `,"emails":[]`, []emailable.Verification{}},
		{"rows without any field", `,"emails":[{},{"state":"risky"},{"email":"x"}]`, []emailable.Verification{
			{}, {State: emailable.StateRisky}, {Email: "x"},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, err := callOnce(t, pathBatch, getBatchAnswer(tt.members), callGet)
			require.NoError(t, err)
			assert.Equal(t, tt.want, b.Emails)
			assert.Equal(t, tt.want == nil, b.Emails == nil, "nil and empty differ")
		})
	}
}

func TestGetBatchRowFailures(t *testing.T) {
	const malformed, unexpected = emailable.FailureMalformedResponse, emailable.FailureUnexpectedResponse
	tests := []struct {
		name   string
		emails string
		kind   emailable.FailureKind
		field  string
	}{
		{"emails as an object", `{}`, malformed, "emails"},
		{"emails as a string", `"x"`, malformed, "emails"},
		{"emails as a number", `5`, malformed, "emails"},
		{"row that is a number", `[{"email":"a@example.com"},{"email":"b@example.com"},5]`, malformed, "emails[2]"},
		{"row that is null", `[{},{},null]`, malformed, "emails[2]"},
		{"row that is a string", `[{},{},"x"]`, malformed, "emails[2]"},
		{"row that is an array", `[[]]`, malformed, "emails[0]"},
		{"score over 100", `[{},{},{},{"score":101}]`, malformed, "emails[3].score"},
		{"negative score", `[{"score":-1}]`, malformed, "emails[0].score"},
		{"quoted score", `[{"score":"5"}]`, malformed, "emails[0].score"},
		{"fractional score", `[{"score":1.5}]`, malformed, "emails[0].score"},
		{"email as a number", `[{"email":5}]`, malformed, "emails[0].email"},
		{"email over 4 KiB", `[{"email":"` + strings.Repeat("e", 4097) + `"}]`, emailable.FailureResponseTooLarge, "emails[0].email"},
		{"state not a token", `[{"state":"Deliverable"}]`, unexpected, "emails[0].state"},
		{"reason not a token", `[{},{"reason":"no connect"}]`, unexpected, "emails[1].reason"},
		{"quoted boolean", `[{"accept_all":"true"}]`, malformed, "emails[0].accept_all"},
		{"birth year out of range", `[{"birth_year":10000}]`, malformed, "emails[0].birth_year"},
		{"domain as a number", `[{"domain":5}]`, malformed, "emails[0].domain"},
		{"negative duration", `[{"duration":-1}]`, malformed, "emails[0].duration"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := callOnce(t, pathBatch, getBatchAnswer(`,"emails":`+tt.emails), callGet)
			_ = assertCallError(t, err, getBatchFailure(tt.kind, tt.field))
		})
	}
}

func TestGetBatchTextBounds(t *testing.T) {
	file := func(n int) string {
		return "https://downloads.example.com/" + strings.Repeat("f", n-len("https://downloads.example.com/"))
	}
	t.Run("download file of 8 KiB", func(t *testing.T) {
		b, err := callOnce(t, pathBatch, getBatchAnswer(`,"download_file":"`+file(8192)+`"`), callGet)
		require.NoError(t, err)
		assert.Equal(t, file(8192), b.DownloadFile.Reveal())
	})
	for _, v := range []string{`""`, `null`} {
		t.Run("download file "+v, func(t *testing.T) {
			b, err := callOnce(t, pathBatch, getBatchAnswer(`,"download_file":`+v), callGet)
			require.NoError(t, err)
			assert.True(t, b.DownloadFile.IsZero())
		})
	}
	t.Run("message of 4 KiB", func(t *testing.T) {
		b, err := callOnce(t, pathBatch, getBatchAnswer(`,"message":"`+strings.Repeat("m", 4096)+`"`), callGet)
		require.NoError(t, err)
		assert.Len(t, b.Message, 4096)
	})

	refused := []struct {
		name, members, field string
		kind                 emailable.FailureKind
	}{
		{"download file over 8 KiB", `,"download_file":"` + file(8193) + `"`, "download_file", emailable.FailureResponseTooLarge},
		{
			"escaped download file over 8 KiB", `,"download_file":"` + testutil.Esc(strings.Repeat("~u0066", 8193)) + `"`,
			"download_file", emailable.FailureResponseTooLarge,
		},
		{"download file as a number", `,"download_file":5`, "download_file", emailable.FailureMalformedResponse},
		{"download file as an object", `,"download_file":{"url":"x"}`, "download_file", emailable.FailureMalformedResponse},
		{"message over 4 KiB", `,"message":"` + strings.Repeat("m", 4097) + `"`, "message", emailable.FailureResponseTooLarge},
		{"message as a number", `,"message":5`, "message", emailable.FailureMalformedResponse},
	}
	for _, tt := range refused {
		t.Run(tt.name, func(t *testing.T) {
			_, err := callOnce(t, pathBatch, getBatchAnswer(tt.members), callGet)
			_ = assertCallError(t, err, getBatchFailure(tt.kind, tt.field), getBatchSigMarker)
		})
	}
}

func TestGetBatchAcceptsCountsAsSent(t *testing.T) {
	b, err := callOnce(t, pathBatch, getBatchAnswer(`,"processed":10,"total":5,`+
		`"reason_counts":{"accepted_email":7},"total_counts":{"deliverable":5,"processed":1,"total":2}`), callGet)
	require.NoError(t, err)
	processed, total, ok := b.Progress()
	require.True(t, ok)
	assert.Equal(t, int64(10), processed, "more processed than total is kept as sent")
	assert.Equal(t, int64(5), total)
	assert.Equal(t, map[emailable.Reason]int64{"accepted_email": 7}, b.ReasonCounts, "counts that do not sum are kept")
	assert.Equal(t, map[string]int64{"deliverable": 5, "processed": 1, "total": 2}, b.TotalCounts)
}

// getBatchClass is one way a GetBatch call can fail after it was sent.
type getBatchClass struct {
	name   string
	step   testutil.Step
	kind   emailable.FailureKind
	status int
	field  string
}

// getBatchFailureClasses are failures of every kind: none leaves the outcome
// of a free read open, and each keeps the requested ID.
func getBatchFailureClasses() []getBatchClass {
	codes := []int{201, 249, 301, 302, 304, 307, 400, 401, 402, 403, 404, 410, 429, 500, 502, 503, 504}
	out := make([]getBatchClass, 0, len(codes)+12)
	for _, code := range codes {
		out = append(out, getBatchClass{
			name: "HTTP " + strconv.Itoa(code), step: testutil.Step{Status: code, Body: batchOK},
			kind: statusKind(emailable.OpGetBatch, code), status: code,
		})
	}
	ok := http.StatusOK
	return append(out, []getBatchClass{
		{name: "transport error", step: testutil.Step{Err: errGetBatchTransport}, kind: emailable.FailureTransport},
		{
			name: "network timeout", step: testutil.Step{Err: testutil.TimeoutError{Msg: "timeout " + getBatchLeak}},
			kind: emailable.FailureTimeout,
		},
		{name: "body over the limit", step: testutil.Step{Body: getBatchAnswer(`,"message":"` + strings.Repeat("m", 1100) + `"`)}, kind: emailable.FailureResponseTooLarge, status: ok},
		{name: "endless body", step: testutil.Step{Respond: testutil.Transport(ok, -1, func() io.ReadCloser {
			return &testutil.EndlessBody{}
		})}, kind: emailable.FailureResponseTooLarge, status: ok},
		{name: "invalid JSON", step: testutil.Step{Body: `{"id":"` + testBatchID + `",`}, kind: emailable.FailureMalformedResponse, status: ok},
		{name: "trailing data", step: testutil.Step{Body: batchOK + ` x`}, kind: emailable.FailureMalformedResponse, status: ok},
		{name: "duplicate key", step: testutil.Step{Body: `{"id":"x","ID":"y"}`}, kind: emailable.FailureMalformedResponse, status: ok},
		{name: "not an object", step: testutil.Step{Body: `[` + batchOK + `]`}, kind: emailable.FailureUnexpectedResponse, status: ok},
		{name: "another batch", step: testutil.Step{Body: `{"id":"other"}`}, kind: emailable.FailureUnexpectedResponse, status: ok, field: "id"},
		{name: "bad row", step: testutil.Step{Body: getBatchAnswer(`,"emails":[{"score":101}]`)}, kind: emailable.FailureMalformedResponse, status: ok, field: "emails[0].score"},
	}...)
}

func TestGetBatchFailuresKeepTheRequestedID(t *testing.T) {
	for _, tt := range getBatchFailureClasses() {
		t.Run(tt.name, func(t *testing.T) {
			s := testutil.NewScript(tt.step, testutil.Step{Body: batchOK})
			c := newTransportClient(t, s, emailable.WithMaxResponseBytes(1024))
			b, err := callGet(t.Context(), c)
			assert.Nil(t, b)
			_ = assertCallError(t, err, wantCall{
				Kind: tt.kind, Op: emailable.OpGetBatch, Status: tt.status, BatchID: testBatchID, Attempts: 1,
				Field: tt.field,
			}, getBatchLeak)
			require.NotErrorIs(t, err, emailable.ErrOutcomeUnknown, "a read is free: its outcome is never open")
			assert.Equal(t, 1, s.Calls(), "GetBatch never retries")
		})
	}
}

func TestGetBatchProgress(t *testing.T) {
	tests := []struct {
		name            string
		batch           *emailable.Batch
		processed, want int64
		ok              bool
	}{
		{"nil batch", nil, 0, 0, false},
		{"zero batch", &emailable.Batch{}, 0, 0, false},
		{"top-level pair", &emailable.Batch{Processed: new(int64(3)), Total: new(int64(9))}, 3, 9, true},
		{"top-level zeros", &emailable.Batch{Processed: new(int64(0)), Total: new(int64(0))}, 0, 0, true},
		{"top-level pair wins over total_counts", &emailable.Batch{
			Processed: new(int64(3)), Total: new(int64(9)), TotalCounts: map[string]int64{"processed": 7, "total": 8},
		}, 3, 9, true},
		{"only processed at the top falls back", &emailable.Batch{
			Processed: new(int64(3)), TotalCounts: map[string]int64{"processed": 7, "total": 8},
		}, 7, 8, true},
		{"only total at the top falls back", &emailable.Batch{
			Total: new(int64(9)), TotalCounts: map[string]int64{"processed": 7, "total": 8},
		}, 7, 8, true},
		{"total_counts pair", &emailable.Batch{TotalCounts: map[string]int64{"processed": 7, "total": 8, "deliverable": 1}}, 7, 8, true},
		{"total_counts without total", &emailable.Batch{TotalCounts: map[string]int64{"processed": 7}}, 0, 0, false},
		{"total_counts without processed", &emailable.Batch{TotalCounts: map[string]int64{"total": 8}}, 0, 0, false},
		{"incomplete pairs everywhere", &emailable.Batch{Processed: new(int64(3)), TotalCounts: map[string]int64{"total": 8}}, 0, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			processed, total, ok := tt.batch.Progress()
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.processed, processed)
			assert.Equal(t, tt.want, total)
		})
	}
}

func TestGetBatchDownloadURLZeroValue(t *testing.T) {
	var zero emailable.DownloadURL
	for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
		assert.Empty(t, fmt.Sprintf(verb, zero), verb)
		assert.Empty(t, fmt.Sprintf(verb, &zero), verb+" on a pointer")
	}
	assert.Equal(t, `""`, fmt.Sprintf("%q", zero))
	assert.Empty(t, zero.String())
	assert.Empty(t, zero.GoString())
	assert.Empty(t, zero.Reveal())
	assert.True(t, zero.IsZero())
	assert.Contains(t, fmt.Sprintf("%+v", emailable.Batch{}), "DownloadFile: ")

	empty := emailable.NewDownloadURL("")
	assert.True(t, empty.IsZero(), `NewDownloadURL("") is the zero value`)
	assert.Empty(t, empty.Reveal())
	assert.True(t, empty.Equal(zero))
	assert.Equal(t, zero, empty)
}

func TestGetBatchDownloadURLMethods(t *testing.T) {
	u := emailable.NewDownloadURL(getBatchSigned)
	same := emailable.NewDownloadURL(getBatchSigned)
	other := emailable.NewDownloadURL(getBatchSigned + "&v=2")
	var zero emailable.DownloadURL

	assert.Equal(t, getBatchSigned, u.Reveal())
	assert.False(t, u.IsZero())
	assert.True(t, u.Equal(u))
	assert.True(t, u.Equal(same), "two values of the same URL are Equal")
	assert.False(t, u.Equal(other))
	assert.False(t, u.Equal(zero))
	assert.False(t, zero.Equal(u))
	assert.True(t, zero.Equal(emailable.DownloadURL{}))
}

func TestGetBatchJSONSkipsDownloadFileAndPartial(t *testing.T) {
	b := &emailable.Batch{
		ID: testBatchID, Partial: true, Message: getBatchMessage, DownloadFile: emailable.NewDownloadURL(getBatchSigned),
	}
	data, err := json.Marshal(b)
	require.NoError(t, err)
	assertBody(t, `{"id":"`+testBatchID+`","message":"`+getBatchMessage+`"}`, data)
	testutil.AssertRedacted(t, string(data), []string{getBatchSigned, getBatchSigMarker})
	for _, key := range []string{"download", "Download", "partial", "Partial"} {
		assert.NotContains(t, string(data), key)
	}

	var back emailable.Batch
	require.NoError(t, json.Unmarshal([]byte(`{"id":"`+testBatchID+`","partial":true,"Partial":true,`+
		`"download_file":"`+getBatchSigned+`","DownloadFile":"`+getBatchSigned+`"}`), &back))
	assert.Equal(t, emailable.Batch{ID: testBatchID}, back, "neither member is ever read back")
}

func TestGetBatchJSONRoundTrip(t *testing.T) {
	t.Run("rows, counts and a download file", func(t *testing.T) {
		c, _ := newScriptClient(t, testutil.Step{Body: fixture(t, "batch_emails_and_download.json")})
		b, err := c.GetBatch(t.Context(), emailable.GetBatchRequest{BatchID: getBatchFixtureID, Partial: true})
		require.NoError(t, err)
		b.DownloadFile, b.Partial = emailable.DownloadURL{}, false
		assertRoundTrip(t, b, `{"id":"`+getBatchFixtureID+`","message":"SDK fixture: a synthetic snapshot with both `+
			`inline rows and a download file.","emails":[{"email":"avery.example@example.com","state":"deliverable",`+
			`"reason":"accepted_email"},{"email":"blake.example@mail.example.com","state":"risky","reason":"low_quality"}],`+
			`"reason_counts":{"accepted_email":1,"low_quality":1},"total_counts":{"deliverable":1,"processed":2,`+
			`"risky":1,"total":2}}`)
	})

	t.Run("progress and partial rows", func(t *testing.T) {
		c, _ := newScriptClient(t, testutil.Step{Body: fixture(t, "batch_partial.json")})
		b, err := c.GetBatch(t.Context(), emailable.GetBatchRequest{BatchID: getBatchFixtureID, Partial: true})
		require.NoError(t, err)
		b.Partial = false
		assertRoundTrip(t, b, `{"id":"`+getBatchFixtureID+`","message":"SDK fixture: partial rows of a synthetic `+
			`batch that is still running.","processed":2,"total":5,"emails":[{"email":"avery.example@example.com",`+
			`"state":"deliverable","reason":"accepted_email","score":96},{"email":"blake.example@mail.example.com",`+
			`"state":"undeliverable","reason":"rejected_email","score":0}],"total_counts":{"processed":2,"total":5}}`)
	})

	t.Run("every fixture survives a round trip", func(t *testing.T) {
		for _, tt := range getBatchFixtureCases() {
			want := *tt.want
			want.Partial = false
			data, err := json.Marshal(&want)
			require.NoError(t, err)
			var back emailable.Batch
			require.NoError(t, json.Unmarshal(data, &back))
			assert.Equal(t, want, back, tt.name)
		}
	})
}

func TestGetBatchJSONOmitzero(t *testing.T) {
	assertRoundTrip(t, &emailable.Batch{}, `{"id":""}`)
	assertRoundTrip(t, &emailable.Batch{ID: testBatchID}, `{"id":"`+testBatchID+`"}`)
	back := assertRoundTrip(t, &emailable.Batch{
		ID: testBatchID, Emails: []emailable.Verification{}, ReasonCounts: map[emailable.Reason]int64{},
		TotalCounts: map[string]int64{},
	}, `{"id":"`+testBatchID+`","emails":[],"reason_counts":{},"total_counts":{}}`)
	assert.NotNil(t, back.Emails, "an empty array stays empty, not nil")
	assert.NotNil(t, back.ReasonCounts)
	assert.NotNil(t, back.TotalCounts)
	assertRoundTrip(t, &emailable.Batch{ID: testBatchID, Processed: new(int64(0)), Total: new(int64(0))},
		`{"id":"`+testBatchID+`","processed":0,"total":0}`)
	assertRoundTrip(t, &emailable.Batch{ID: testBatchID, Emails: []emailable.Verification{{}}},
		`{"id":"`+testBatchID+`","emails":[{}]}`)
}
