package emailable

import (
	"bytes"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-emailable/internal/testutil"
)

// These benchmarks measure local CPU and allocations only: strict checking,
// decoding and encoding over in-memory bytes. They say nothing about network
// or provider performance.

// benchStrictHead and benchStrictTail open and close a batch answer.
const (
	benchStrictHead = `{"id":"bench-batch","message":"Batch verification completed.","emails":[`
	benchStrictTail = `],"total_counts":{"processed":1,"total":1}}`
)

// benchStrictBody returns a batch answer of exactly size bytes: as many rows
// as fit, then spaces before the closing brace. Each row holds one container
// and 19 values, so the body stays within the container and value bounds.
func benchStrictBody(tb testing.TB, size int) []byte {
	tb.Helper()
	const pad = 128
	b := make([]byte, 0, size)
	b = append(b, benchStrictHead...)
	row := make([]byte, 0, 1024)
	for i := 1; ; i++ {
		row = testutil.AppendBatchRow(row[:0], i, pad)
		if len(b)+1+len(row)+len(benchStrictTail) > size {
			break
		}
		if i > 1 {
			b = append(b, ',')
		}
		b = append(b, row...)
	}
	b = append(b, benchStrictTail[:len(benchStrictTail)-1]...)
	b = append(b, bytes.Repeat([]byte{' '}, size-len(b)-1)...)
	b = append(b, '}')
	require.Len(tb, b, size, "strict body size")
	kind, cause := checkStrict(b)
	require.NoError(tb, cause, "the strict body fails the check: %s", kind)
	return b
}

// benchCheckStrict benchmarks checkStrict on a body of size bytes.
func benchCheckStrict(b *testing.B, size int) {
	b.Helper()
	body := benchStrictBody(b, size)
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := checkStrict(body); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCheckStrict1MiB(b *testing.B) { benchCheckStrict(b, 1<<20) }

func BenchmarkCheckStrict8MiB(b *testing.B) { benchCheckStrict(b, DefaultMaxResponseBytes) }

func BenchmarkDecodeVerification(b *testing.B) {
	body := []byte(testutil.FullVerification)
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	for b.Loop() {
		v, derr := decodeVerification(body, path{}, true)
		if derr != nil {
			b.Fatal(derr.cause)
		}
		if v.State != StateDeliverable || v.Duration == nil {
			b.Fatal("the full verification must decode every member")
		}
	}
}

// benchWideObject returns one object of n distinct keys.
func benchWideObject(n int) []byte {
	b := []byte{'{'}
	for i := range n {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, `"key_`...)
		b = strconv.AppendInt(b, int64(i), 10)
		b = append(b, `":`...)
		b = strconv.AppendInt(b, int64(i), 10)
	}
	return append(b, '}')
}

// benchDeepObject returns objects nested depth levels deep, each with keys
// distinct keys and the next level under "next".
func benchDeepObject(depth, keys int) []byte {
	var b []byte
	for range depth {
		b = append(b, '{')
		for k := range keys {
			b = append(b, `"k`...)
			b = strconv.AppendInt(b, int64(k), 10)
			b = append(b, `":true,`...)
		}
		b = append(b, `"next":`...)
	}
	b = append(b, "null"...)
	return append(b, bytes.Repeat([]byte{'}'}, depth)...)
}

// benchDuplicateKey benchmarks duplicateKey on body, which repeats no key.
func benchDuplicateKey(b *testing.B, body []byte) {
	b.Helper()
	require.False(b, duplicateKey(body))
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	for b.Loop() {
		if duplicateKey(body) {
			b.Fatal("no key repeats")
		}
	}
}

func BenchmarkDuplicateKeyWide4096(b *testing.B) { benchDuplicateKey(b, benchWideObject(4096)) }

func BenchmarkDuplicateKeyDeep4096(b *testing.B) { benchDuplicateKey(b, benchDeepObject(4096, 4)) }

func BenchmarkCreateBatchEncode50k(b *testing.B) {
	emails := make([]string, DefaultMaxBatchEmails)
	for i := range emails {
		emails[i] = "user." + strconv.Itoa(i+1) + "@example.com"
	}
	req := CreateBatchRequest{
		Emails: emails, CallbackURL: "https://hooks.example.com/emailable?token=benchmark",
		ResponseFields: []ResponseField{ResponseFieldEmail, ResponseFieldState, ResponseFieldReason},
	}
	c := newInternalClient(b)
	r, err := req.request(c)
	require.NoError(b, err)
	b.SetBytes(int64(len(r.body)))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := req.request(c); err != nil {
			b.Fatal(err)
		}
	}
}
