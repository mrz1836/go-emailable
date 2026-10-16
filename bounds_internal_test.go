package emailable

import (
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bndDomain ends every address the bounds tests build.
const bndDomain = "@example.com"

// bndHookPrefix starts every callback URL the bounds tests build.
const bndHookPrefix = "https://hooks.example.com/"

// bndEmail returns an address of exactly n bytes.
func bndEmail(n int) string { return strings.Repeat("a", n-len(bndDomain)) + bndDomain }

// bndHook returns a valid callback URL of exactly n bytes.
func bndHook(n int) string { return bndHookPrefix + strings.Repeat("p", n-len(bndHookPrefix)) }

// bndBatch returns addresses whose POST /v1/batch body, with nothing else
// set, is exactly n bytes: {"emails":[]} costs 13 bytes, and each address
// without escapes its length plus 3 (quotes and separator), one less in all.
// Every address but the last two is the same 254-byte string, so the list
// costs little memory.
func bndBatch(n int) []string {
	const per = maxEmailBytes + 3
	rest := n - (len(`{"emails":[]}`) - 1) // the sum of len(e)+3 over the addresses
	count := rest/per - 1
	rest -= count * per // from per to 2*per-1: two addresses of 125 to 254 bytes
	emails := make([]string, count, count+2)
	long := bndEmail(maxEmailBytes)
	for i := range emails {
		emails[i] = long
	}
	return append(emails, bndEmail(rest/2-3), bndEmail(rest-rest/2-3))
}

// bndCase is one bound: the constant, its documented value, and a check of
// a value of n bytes (or items), which must be accepted unless over is set,
// and then fail as documented.
type bndCase struct {
	name  string
	bound int
	want  int
	check func(t *testing.T, n int, over bool)
}

// bndDecodeCheck returns a check that decodes body(n) with decode and wants
// success, or a failure of kind at field.
func bndDecodeCheck(body func(n int) string, decode func(body []byte) *decodeError, kind FailureKind, field string) func(*testing.T, int, bool) {
	return func(t *testing.T, n int, over bool) {
		t.Helper()
		data := dcdStrict(t, body(n))
		derr := decode(data)
		if !over {
			assert.Nil(t, derr)
			return
		}
		assertDecodeError(t, derr, kind, field)
	}
}

// bndDecodeCases returns the bounds on decoded fields.
func bndDecodeCases() []bndCase {
	return []bndCase{
		{
			name: "maxTextBytes", bound: maxTextBytes, want: 4 << 10,
			check: bndDecodeCheck(func(n int) string { return `{"emails":[{"user":` + dcdString("u", n) + `}]}` },
				func(b []byte) *decodeError { _, derr := decodeBatch(b, dcdBatchID); return derr },
				FailureResponseTooLarge, "emails[0].user"),
		},
		{
			name: "maxDownloadFileBytes", bound: maxDownloadFileBytes, want: 8 << 10,
			check: bndDecodeCheck(func(n int) string { return `{"download_file":` + dcdString("f", n) + `}` },
				func(b []byte) *decodeError { _, derr := decodeBatch(b, dcdBatchID); return derr },
				FailureResponseTooLarge, "download_file"),
		},
		{
			name: "maxTokenBytes", bound: maxTokenBytes, want: 64,
			check: bndDecodeCheck(func(n int) string { return `{"email":"a@example.com","state":` + dcdString("s", n) + `}` },
				func(b []byte) *decodeError { _, derr := decodeVerification(b, path{}, true); return derr },
				FailureUnexpectedResponse, "state"),
		},
		{
			name: "maxTokenBytes in a count key", bound: maxTokenBytes, want: 64,
			check: bndDecodeCheck(func(n int) string { return `{"reason_counts":{` + dcdString("r", n) + `:1}}` },
				func(b []byte) *decodeError { _, derr := decodeBatch(b, dcdBatchID); return derr },
				FailureUnexpectedResponse, dcdReasons),
		},
		{
			name: "maxBatchIDBytes", bound: maxBatchIDBytes, want: 256,
			check: bndDecodeCheck(func(n int) string { return `{"id":` + dcdString("b", n) + `}` },
				func(b []byte) *decodeError { _, derr := decodeCreateBatch(b); return derr },
				FailureUnexpectedResponse, dcdIDField),
		},
		{
			name: "maxCountKeys", bound: maxCountKeys, want: 256,
			check: bndDecodeCheck(func(n int) string { return `{"total_counts":` + dcdCounts(n, "") + `}` },
				func(b []byte) *decodeError { _, derr := decodeBatch(b, dcdBatchID); return derr },
				FailureResponseTooLarge, dcdTotals),
		},
	}
}

// bndStrictCheck returns a check that runs checkStrict on body(n).
func bndStrictCheck(body func(n int) []byte) func(*testing.T, int, bool) {
	return func(t *testing.T, n int, over bool) {
		t.Helper()
		kind, cause := checkStrict(body(n))
		if !over {
			require.NoError(t, cause)
			assert.Empty(t, kind)
			return
		}
		assert.Equal(t, FailureResponseTooLarge, kind)
		require.ErrorIs(t, cause, errTooManyValues)
	}
}

// bndBodyCases returns the whole-body bounds.
func bndBodyCases() []bndCase {
	return []bndCase{
		{
			name: "maxContainers", bound: maxContainers, want: 100_000,
			check: bndStrictCheck(func(n int) []byte { return strictFill(`[]`, n-2) }),
		},
		{
			name: "maxValues", bound: maxValues, want: 1_000_000,
			check: bndStrictCheck(func(n int) []byte { return strictFill(`"v"`, n-2) }),
		},
	}
}

// bndRequestCases returns the bounds on request fields.
func bndRequestCases() []bndCase {
	return []bndCase{
		{
			name: "maxEmailBytes", bound: maxEmailBytes, want: 254,
			check: func(t *testing.T, n int, over bool) {
				t.Helper()
				c := newInternalClient(t)
				r, err := VerifyEmailRequest{Email: bndEmail(n)}.request(c)
				b, berr := CreateBatchRequest{Emails: []string{internalEmail, bndEmail(n)}}.request(c)
				if !over {
					require.NoError(t, err)
					require.NoError(t, berr)
					assert.NotNil(t, r)
					assert.NotNil(t, b)
					return
				}
				requireValidation(t, err, ErrInvalidRequest, "Email", reasonTooLong)
				requireValidation(t, berr, ErrInvalidRequest, "Emails[1]", reasonTooLong)
			},
		},
		{
			name: "maxCaptchaBytes", bound: maxCaptchaBytes, want: 8 << 10,
			check: func(t *testing.T, n int, over bool) {
				t.Helper()
				r, err := VerifyEmailRequest{Email: internalEmail, CaptchaResponse: strings.Repeat("c", n)}.request(newInternalClient(t))
				if !over {
					require.NoError(t, err)
					assert.Equal(t, []string{strings.Repeat("c", n)}, r.secrets)
					return
				}
				requireValidation(t, err, ErrInvalidRequest, "CaptchaResponse", reasonTooLong)
			},
		},
		{
			name: "maxCallbackURLBytes", bound: maxCallbackURLBytes, want: 2 << 10,
			check: func(t *testing.T, n int, over bool) {
				t.Helper()
				_, err := CreateBatchRequest{Emails: []string{internalEmail}, CallbackURL: bndHook(n)}.validate(DefaultMaxBatchEmails)
				if !over {
					require.NoError(t, err)
					return
				}
				requireValidation(t, err, ErrInvalidRequest, "CallbackURL", reasonTooLong)
			},
		},
		{
			name: "maxBatchIDBytes in a request", bound: maxBatchIDBytes, want: 256,
			check: func(t *testing.T, n int, over bool) {
				t.Helper()
				r, err := (&GetBatchRequest{BatchID: strings.Repeat("b", n)}).request()
				if !over {
					require.NoError(t, err)
					assert.Equal(t, "id="+strings.Repeat("b", n), r.query)
					return
				}
				requireValidation(t, err, ErrInvalidRequest, "BatchID", reasonInvalidFormat)
			},
		},
		{
			name: "maxKeyBytes", bound: maxKeyBytes, want: 4096,
			check: func(t *testing.T, n int, over bool) {
				t.Helper()
				c, err := NewClient(strings.Repeat("k", n))
				if !over {
					require.NoError(t, err)
					assert.NotNil(t, c)
					return
				}
				assert.Nil(t, c)
				requireValidation(t, err, ErrInvalidConfig, "apiKey", reasonInvalidFormat)
			},
		},
		{
			name: "maxDiagnosticMessageBytes", bound: maxDiagnosticMessageBytes, want: 1 << 10,
			check: func(t *testing.T, n int, over bool) {
				t.Helper()
				c := newInternalClient(t, WithProviderDiagnostics())
				msg := strings.Repeat("m", n)
				got := c.diagnosticsFrom([]byte(`{"message":"`+msg+`"}`), nil).Message
				if !over {
					assert.Equal(t, msg, got, "a message within the bound is kept whole")
					return
				}
				assert.Equal(t, msg[:maxDiagnosticMessageBytes], got, "a longer message is cut to the bound")
			},
		},
	}
}

// bndCases returns every bound checked at its edges.
func bndCases() []bndCase { return slices.Concat(bndDecodeCases(), bndBodyCases(), bndRequestCases()) }

func TestBoundsValues(t *testing.T) {
	cases := append(bndCases(), bndCase{name: "maxBatchRequestBytes", bound: maxBatchRequestBytes, want: 64 << 20})
	for _, tt := range cases {
		assert.Equal(t, tt.want, tt.bound, "%s is documented as %d", tt.name, tt.want)
	}
}

// TestBoundsEdges checks every bound one under, at and one over its value:
// the first two are accepted, the last fails as documented.
func TestBoundsEdges(t *testing.T) {
	for _, tt := range bndCases() {
		t.Run(tt.name, func(t *testing.T) {
			for _, n := range []int{tt.bound - 1, tt.bound, tt.bound + 1} {
				t.Run("n="+strconv.Itoa(n), func(t *testing.T) {
					tt.check(t, n, n > tt.bound)
				})
			}
		})
	}
}

// TestBoundsBatchRequest checks the 64 MiB bound on the encoded POST
// /v1/batch body at its edges: a body one byte under and one exactly at the
// bound is encoded, and its length is validate's size; one byte over fails
// as too_large before anything is encoded.
func TestBoundsBatchRequest(t *testing.T) {
	c := newInternalClient(t, WithMaxBatchEmails(maxBatchEmailsLimit))
	for _, n := range []int{maxBatchRequestBytes - 1, maxBatchRequestBytes, maxBatchRequestBytes + 1} {
		t.Run(strconv.Itoa(n), func(t *testing.T) {
			req := CreateBatchRequest{Emails: bndBatch(n)}
			size, err := req.validate(c.maxBatchEmails)
			require.NoError(t, err)
			require.Equal(t, n, size, "the addresses add up to the body size wanted")
			r, err := req.request(c)
			if n > maxBatchRequestBytes {
				requireValidation(t, err, ErrInvalidRequest, "request", reasonTooLarge)
				assert.Nil(t, r)
				return
			}
			require.NoError(t, err)
			assert.Len(t, r.body, n)
		})
	}
}
