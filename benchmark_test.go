package emailable_test

import (
	"bytes"
	"io"
	"net/http"
	"strconv"
	"testing"

	"github.com/mrz1836/go-emailable"
	"github.com/mrz1836/go-emailable/internal/testutil"
)

// These benchmarks measure local CPU and allocations through in-memory
// transports and readers. They say nothing about network or provider
// performance.

// benchInlineRows is the number of rows of an inline batch answer: the most
// the provider sends inline.
const benchInlineRows = 1000

// benchBatchBody returns a completed batch answer for id with rows inline
// rows, each with pad bytes of first_name.
func benchBatchBody(id string, rows, pad int) []byte {
	n := strconv.Itoa(rows)
	b := make([]byte, 0, rows*(400+pad))
	b = append(b, `{"id":"`+id+`","message":"Batch verification completed.","emails":[`...)
	for i := range rows {
		if i > 0 {
			b = append(b, ',')
		}
		b = testutil.AppendBatchRow(b, i+1, pad)
	}
	b = append(b, `],"reason_counts":{"accepted_email":`+n+`},"total_counts":{"deliverable":`+n+
		`,"processed":`+n+`,"total":`+n+`}}`...)
	return b
}

// benchBodyTransport answers every request in memory with body on HTTP 200,
// declared with its length, after draining the request.
func benchBodyTransport(body []byte) testutil.RoundTripFunc {
	return testutil.Transport(http.StatusOK, int64(len(body)), func() io.ReadCloser {
		return io.NopCloser(bytes.NewReader(body))
	})
}

func BenchmarkVerifyEmailEndToEnd(b *testing.B) {
	client := newTransportClient(b, testutil.StaticTransport(http.StatusOK, testutil.FullVerification))
	req := emailable.VerifyEmailRequest{Email: testEmail, SMTP: new(true), AcceptAll: new(true)}
	ctx := b.Context()
	b.ReportAllocs()
	for b.Loop() {
		v, err := client.VerifyEmail(ctx, req)
		if err != nil {
			b.Fatal(err)
		}
		if v.State != emailable.StateDeliverable {
			b.Fatalf("state %q, want deliverable", v.State)
		}
	}
}

func BenchmarkGetBatchInline1000(b *testing.B) {
	body := benchBatchBody(testBatchID, benchInlineRows, 16)
	client := newTransportClient(b, benchBodyTransport(body))
	req := emailable.GetBatchRequest{BatchID: testBatchID}
	ctx := b.Context()
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	for b.Loop() {
		batch, err := client.GetBatch(ctx, req)
		if err != nil {
			b.Fatal(err)
		}
		if len(batch.Emails) != benchInlineRows {
			b.Fatalf("%d rows, want %d", len(batch.Emails), benchInlineRows)
		}
	}
}

func BenchmarkDecodeBatchCallback1000(b *testing.B) {
	body := benchBatchBody(testBatchID, benchInlineRows, 16)
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	for b.Loop() {
		batch, err := emailable.DecodeBatchCallback(bytes.NewReader(body), emailable.DefaultMaxResponseBytes)
		if err != nil {
			b.Fatal(err)
		}
		if batch.ID != testBatchID || len(batch.Emails) != benchInlineRows {
			b.Fatalf("batch %q with %d rows", batch.ID, len(batch.Emails))
		}
	}
}
