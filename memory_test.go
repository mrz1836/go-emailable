package emailable_test

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-emailable"
	"github.com/mrz1836/go-emailable/internal/testutil"
)

// memoryDownload is a signed download URL.
const memoryDownload = "https://files.example.com/batch/memory.csv?signature=memsig"

// memorySmallGrowth bounds the bytes a call that reads no body may allocate.
const memorySmallGrowth = 64 << 10

// TestMemoryAllocationCeilings guards the allocation counts of the common
// paths, in-memory transport included. The ceilings sit about 1.5 times
// above the counts measured with Go 1.27.2 on darwin/arm64; formatting a
// DownloadURL and String on the zero value allocate nothing.
func TestMemoryAllocationCeilings(t *testing.T) {
	testutil.SkipUnderRace(t)
	verify := newTransportClient(t, testutil.StaticTransport(http.StatusOK, testutil.FullVerification))
	get := newTransportClient(t, testutil.StaticTransport(http.StatusOK, batchOK))
	account := newTransportClient(t, testutil.StaticTransport(http.StatusOK, accountOK))
	ctx := t.Context()
	verifyReq := emailable.VerifyEmailRequest{Email: testEmail, SMTP: new(true)}
	v, err := verify.VerifyEmail(ctx, verifyReq)
	require.NoError(t, err)
	require.NotNil(t, v.Duration, "the full answer decodes every member")
	getReq := emailable.GetBatchRequest{BatchID: testBatchID}
	_, err = get.GetBatch(ctx, getReq)
	require.NoError(t, err)
	_, err = account.AccountInfo(ctx, emailable.AccountInfoRequest{})
	require.NoError(t, err)

	zero := emailable.DownloadURL{}
	set := emailable.NewDownloadURL(memoryDownload)
	client := newClient(t, emailable.WithProviderDiagnostics())
	var sink string
	testutil.CheckCeilings(t, []testutil.Ceiling{
		// Measured: 85.
		{Name: "VerifyEmail end to end", Max: 126, Run: func() { _, _ = verify.VerifyEmail(ctx, verifyReq) }},
		// Measured: 58.
		{Name: "GetBatch end to end", Max: 86, Run: func() { _, _ = get.GetBatch(ctx, getReq) }},
		// Measured: 46.
		{Name: "AccountInfo end to end", Max: 68, Run: func() {
			_, _ = account.AccountInfo(ctx, emailable.AccountInfoRequest{})
		}},
		{Name: "DownloadURL String zero", Max: 0, Run: func() { sink = zero.String() }},
		{Name: "DownloadURL String", Max: 0, Run: func() { sink = set.String() }},
		{Name: "DownloadURL GoString", Max: 0, Run: func() { sink = set.GoString() }},
		{Name: "DownloadURL formatted", Max: 0, Run: func() { _, _ = fmt.Fprintf(io.Discard, "%v %s %d", set, zero, set) }},
		{Name: "DownloadURL Reveal", Max: 0, Run: func() { sink = set.Reveal() }},
		// Measured: 5.
		{Name: "Client String", Max: 8, Run: func() { sink = client.String() }},
	})
	_ = sink
}

// TestMemoryLargeBatchBody reads and decodes a GetBatch answer of 1,000 rows
// and about 4 MiB, and checks that the call allocates a small multiple of
// the body.
func TestMemoryLargeBatchBody(t *testing.T) {
	testutil.SkipUnderRace(t)
	body := benchBatchBody(testBatchID, benchInlineRows, 4000)
	c := newTransportClient(t, benchBodyTransport(body))
	call := func() {
		b, err := c.GetBatch(t.Context(), emailable.GetBatchRequest{BatchID: testBatchID})
		if assert.NoError(t, err) {
			assert.Len(t, b.Emails, benchInlineRows)
		}
	}
	growth := testutil.HeapGrowth(call)
	ratio := float64(growth) / float64(len(body))
	t.Logf("%d-byte body: %d bytes allocated (%.2fx the body)", len(body), growth, ratio)
	// Measured: 3.49x (reading a body of that size about 2x, the decoded
	// rows the rest).
	assert.LessOrEqual(t, ratio, 5.25)
}

// TestMemoryDeclaredLengthNeverPreallocated answers with bodies that declare
// a large length and deliver little or nothing. A declared length over the
// limit fails before any read, and one within it sizes the buffer exactly
// only up to 1 MiB; a larger one is read as bytes arrive. None of these calls
// allocates anything near what was declared.
func TestMemoryDeclaredLengthNeverPreallocated(t *testing.T) {
	testutil.SkipUnderRace(t)
	short := func() io.ReadCloser { return io.NopCloser(strings.NewReader(`{"id":"`)) }
	endless := func() io.ReadCloser { return &testutil.EndlessBody{} }
	tests := []struct {
		name     string
		declared int64
		body     func() io.ReadCloser
		opts     []emailable.Option
		kind     emailable.FailureKind
		max      uint64
	}{
		{"1 TiB declared", 1 << 40, endless, nil, emailable.FailureResponseTooLarge, memorySmallGrowth},
		{
			"one byte over the default limit", emailable.DefaultMaxResponseBytes + 1, endless, nil,
			emailable.FailureResponseTooLarge, memorySmallGrowth,
		},
		{
			"64 MiB declared over a raised limit", 64<<20 + 1, endless,
			[]emailable.Option{emailable.WithMaxResponseBytes(64 << 20)},
			emailable.FailureResponseTooLarge,
			memorySmallGrowth,
		},
		{
			"8 MiB declared, 7 bytes sent", emailable.DefaultMaxResponseBytes, short, nil, emailable.FailureTransport,
			memorySmallGrowth,
		},
		{
			"64 MiB declared, 7 bytes sent", 64 << 20, short,
			[]emailable.Option{emailable.WithMaxResponseBytes(64 << 20)},
			emailable.FailureTransport,
			memorySmallGrowth,
		},
		// The largest length sized exactly: one buffer of 1 MiB and a byte.
		{
			"1 MiB declared, 7 bytes sent", 1 << 20, short, nil, emailable.FailureTransport,
			1<<20 + memorySmallGrowth,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTransportClient(t, testutil.Transport(http.StatusOK, tt.declared, tt.body), tt.opts...)
			call := func() {
				_, err := c.GetBatch(t.Context(), emailable.GetBatchRequest{BatchID: testBatchID})
				assert.ErrorIs(t, err, kindSentinels()[tt.kind])
			}
			growth := testutil.HeapGrowth(call)
			t.Logf("%d bytes declared: %d bytes allocated", tt.declared, growth)
			assert.Less(t, growth, tt.max, "nothing near the declared length")
		})
	}
}
