package emailable_test

import (
	"math"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-emailable"
	"github.com/mrz1836/go-emailable/internal/testutil"
)

// acctBody returns an account answer with the raw JSON values owner and
// credits; an empty one leaves its member out.
func acctBody(owner, credits string) string {
	var members []string
	if owner != "" {
		members = append(members, `"owner_email":`+owner)
	}
	if credits != "" {
		members = append(members, `"available_credits":`+credits)
	}
	return "{" + strings.Join(members, ",") + "}"
}

func TestAccountInfoDecodesTheFixture(t *testing.T) {
	info, err := callOnce(t, pathAccount, fixture(t, "account.json"), callAccount)
	require.NoError(t, err)
	assert.Equal(t, &emailable.AccountInfo{OwnerEmail: "owner.fixture@example.com", AvailableCredits: 125000}, info)
	assertRoundTrip(t, info, `{"owner_email":"owner.fixture@example.com","available_credits":125000}`)
}

func TestAccountInfoAccepted(t *testing.T) {
	tests := []struct {
		name string
		body string
		want emailable.AccountInfo
	}{
		{name: "minimal", body: accountOK, want: emailable.AccountInfo{OwnerEmail: testOwner, AvailableCredits: 1000}},
		{name: "empty owner", body: acctBody(`""`, "5"), want: emailable.AccountInfo{AvailableCredits: 5}},
		{name: "zero credits", body: acctBody(`"`+testOwner+`"`, "0"), want: emailable.AccountInfo{OwnerEmail: testOwner}},
		{name: "negative zero credits", body: acctBody(`"`+testOwner+`"`, "-0"), want: emailable.AccountInfo{OwnerEmail: testOwner}},
		{
			name: "largest credits", body: acctBody(`"`+testOwner+`"`, "9223372036854775807"),
			want: emailable.AccountInfo{OwnerEmail: testOwner, AvailableCredits: math.MaxInt64},
		},
		{
			name: "escaped owner", body: acctBody(testutil.Esc(`"owner.marker~u0040example.com"`), "1"),
			want: emailable.AccountInfo{OwnerEmail: testOwner, AvailableCredits: 1},
		},
		{
			name: "owner of exactly 4 KiB", body: acctBody(`"`+strings.Repeat("o", 4096)+`"`, "1"),
			want: emailable.AccountInfo{OwnerEmail: strings.Repeat("o", 4096), AvailableCredits: 1},
		},
		{
			name: "members in another order", body: `{"available_credits":7,"owner_email":"` + testOwner + `"}`,
			want: emailable.AccountInfo{OwnerEmail: testOwner, AvailableCredits: 7},
		},
		{
			name: "other members ignored",
			body: `{"owner_email":"` + testOwner + `","available_credits":7,"plan":{"name":"x"},"Credits":"many"}`,
			want: emailable.AccountInfo{OwnerEmail: testOwner, AvailableCredits: 7},
		},
		{
			name: "surrounding whitespace", body: " \n\t" + accountOK + "\r\n ",
			want: emailable.AccountInfo{OwnerEmail: testOwner, AvailableCredits: 1000},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info, err := callOnce(t, pathAccount, tt.body, callAccount)
			require.NoError(t, err)
			assert.Equal(t, &tt.want, info)
		})
	}
}

func TestAccountInfoRejected(t *testing.T) {
	const (
		malformed = emailable.FailureMalformedResponse
		owner     = "owner_email"
		credits   = "available_credits"
	)
	quoted := `"` + testOwner + `"`
	tests := []struct {
		name  string
		body  string
		kind  emailable.FailureKind
		field string
		cause string
	}{
		{"empty object", `{}`, malformed, owner, causeMissing},
		{"message only", `{"message":"SDK fixture: unavailable"}`, malformed, owner, causeMissing},
		{"owner missing", acctBody("", "1"), malformed, owner, causeMissing},
		{"owner null", acctBody("null", "1"), malformed, owner, causeMissing},
		{"owner number", acctBody("1", "1"), malformed, owner, causeType},
		{"owner boolean", acctBody("true", "1"), malformed, owner, causeType},
		{"owner array", acctBody(`[`+quoted+`]`, "1"), malformed, owner, causeType},
		{"owner object", acctBody(`{"email":`+quoted+`}`, "1"), malformed, owner, causeType},
		{"owner over 4 KiB", acctBody(`"`+strings.Repeat("o", 4097)+`"`, "1"), emailable.FailureResponseTooLarge, owner, causeFieldTooLarge},
		{"owner case variant only", `{"Owner_Email":` + quoted + `,"available_credits":1}`, malformed, owner, causeMissing},
		{"owner checked before credits", acctBody("1", ""), malformed, owner, causeType},
		{"credits missing", acctBody(quoted, ""), malformed, credits, causeMissing},
		{"credits null", acctBody(quoted, "null"), malformed, credits, causeMissing},
		{"credits negative", acctBody(quoted, "-1"), malformed, credits, causeRange},
		{"credits most negative", acctBody(quoted, "-9223372036854775808"), malformed, credits, causeRange},
		{"credits over int64", acctBody(quoted, "9223372036854775808"), malformed, credits, causeRange},
		{"credits fraction", acctBody(quoted, "1.5"), malformed, credits, causeType},
		{"credits whole fraction", acctBody(quoted, "1000.0"), malformed, credits, causeType},
		{"credits exponent", acctBody(quoted, "1e3"), malformed, credits, causeType},
		{"credits negative fraction", acctBody(quoted, "-0.5"), malformed, credits, causeType},
		{"credits string", acctBody(quoted, `"1000"`), malformed, credits, causeType},
		{"credits boolean", acctBody(quoted, "true"), malformed, credits, causeType},
		{"credits array", acctBody(quoted, "[1000]"), malformed, credits, causeType},
		{"credits object", acctBody(quoted, `{"total":1000}`), malformed, credits, causeType},
		{"credits case variant only", `{"owner_email":` + quoted + `,"Available_Credits":1}`, malformed, credits, causeMissing},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info, err := callOnce(t, pathAccount, tt.body, callAccount)
			assert.Nil(t, info)
			// AccountInfo is free: an unreadable answer leaves nothing unknown.
			_ = assertCallError(t, err, wantCall{
				Kind: tt.kind, Op: emailable.OpAccountInfo, Status: http.StatusOK, Attempts: 1, Field: tt.field,
			})
			assert.Equal(t, tt.cause, causeText(t, err))
		})
	}
}

// TestAccountInfoFailuresLeaveNothingUnknown proves that no failure of the
// free, read-only AccountInfo ever sets OutcomeUnknown.
func TestAccountInfoFailuresLeaveNothingUnknown(t *testing.T) {
	steps := map[string]testutil.Step{
		"transport failure": {Err: errUnrelated},
		"server error":      {Status: http.StatusInternalServerError},
		"unavailable":       {Status: http.StatusServiceUnavailable},
		"redirect":          {Status: http.StatusFound},
		"malformed":         {Body: `{"owner_email":`},
		"not an object":     {Body: `[]`},
	}
	for name, step := range steps {
		t.Run(name, func(t *testing.T) {
			c, _ := newScriptClient(t, step)
			_, err := c.AccountInfo(t.Context(), emailable.AccountInfoRequest{})
			ce := callError(t, err)
			assert.False(t, ce.OutcomeUnknown)
			require.NotErrorIs(t, err, emailable.ErrOutcomeUnknown)
			assert.Equal(t, 1, ce.Attempts)
		})
	}
}

func TestAccountInfoOwnerNeverInErrors(t *testing.T) {
	body := `{"owner_email":"` + testOwner + `","available_credits":"` + testOwner + `"}`
	_, err := callOnce(t, pathAccount, body, callAccount)
	_ = assertCallError(t, err, wantCall{
		Kind: emailable.FailureMalformedResponse, Op: emailable.OpAccountInfo, Status: http.StatusOK, Attempts: 1,
		Field: "available_credits",
	}, testOwner)
}
