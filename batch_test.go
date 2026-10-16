package emailable_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-emailable"
	"github.com/mrz1836/go-emailable/internal/testutil"
)

// Shared values of the CreateBatch tests.
const (
	// createBatchLeak is planted inside rejected values and transport errors:
	// no error may ever contain it.
	createBatchLeak = "cbleakmarker2f9e61" // gitleaks:allow (synthetic test marker)

	// createBatchMessage is the message of createOK.
	createBatchMessage = "Batch successfully created."

	// createBatchOther is a second valid address.
	createBatchOther = "blake.example@mail.example.com"

	// createBatchMinimal is the body of createRequest().
	createBatchMinimal = `{"emails":["` + testEmail + `"]}`

	// createBatchMaxBody is the documented bound on the encoded body: 64 MiB.
	createBatchMaxBody = 64 << 20

	// createBatchHuge is the largest WithMaxBatchEmails, so that only the
	// encoded size can refuse a list.
	createBatchHuge = 1_000_000

	// createBatchShortest is the shortest address the size tests build, as
	// encoded: `"a@example.com"`.
	createBatchShortest = len(`"a@example.com"`)
)

// errCreateBatchTransport is a transport failure whose text holds a leak
// marker.
var errCreateBatchTransport = errors.New("dial tcp: connection refused " + createBatchLeak)

// createBatchWith returns createRequest() changed by edit.
func createBatchWith(edit func(*emailable.CreateBatchRequest)) emailable.CreateBatchRequest {
	r := createRequest()
	edit(&r)
	return r
}

// createBatchFailure returns the CallError of a CreateBatch call answered
// HTTP 200 with a body that fails as kind at field: one attempt, and an
// unknown outcome, because a batch may exist.
func createBatchFailure(kind emailable.FailureKind, field string) wantCall {
	return wantCall{
		Kind: kind, Op: emailable.OpCreateBatch, Status: http.StatusOK,
		OutcomeUnknown: outcomeUnknownAfter(true, kind), Attempts: 1, Field: field,
	}
}

// createBatchRescued returns the CallError of a CreateBatch answer that is
// not strict JSON but still names the batch: malformed, with the BatchID, and
// a known outcome, because the batch exists.
func createBatchRescued(batchID string) wantCall {
	return wantCall{
		Kind: emailable.FailureMalformedResponse, Op: emailable.OpCreateBatch, Status: http.StatusOK,
		BatchID: batchID, Attempts: 1,
	}
}

// createBatchAllFields returns the 21 documented response fields, in the
// order they are declared.
func createBatchAllFields() []emailable.ResponseField {
	return []emailable.ResponseField{
		emailable.ResponseFieldAcceptAll, emailable.ResponseFieldBirthYear, emailable.ResponseFieldDidYouMean,
		emailable.ResponseFieldDisposable, emailable.ResponseFieldDomain, emailable.ResponseFieldEmail,
		emailable.ResponseFieldFirstName, emailable.ResponseFieldFree, emailable.ResponseFieldFullName,
		emailable.ResponseFieldGender, emailable.ResponseFieldLastName, emailable.ResponseFieldMailboxFull,
		emailable.ResponseFieldMXRecord, emailable.ResponseFieldNoReply, emailable.ResponseFieldReason,
		emailable.ResponseFieldRole, emailable.ResponseFieldScore, emailable.ResponseFieldSMTPProvider,
		emailable.ResponseFieldState, emailable.ResponseFieldTag, emailable.ResponseFieldUser,
	}
}

// createBatchSentEmails decodes the emails member of a sent body with
// encoding/json.
func createBatchSentEmails(tb testing.TB, body []byte) []string {
	tb.Helper()
	var sent struct {
		Emails []string `json:"emails"`
	}
	require.NoError(tb, json.Unmarshal(body, &sent))
	return sent.Emails
}

func TestCreateBatchWireBodies(t *testing.T) {
	tests := []struct {
		name string
		req  emailable.CreateBatchRequest
		want string
	}{
		{"minimal", createRequest(), createBatchMinimal},
		{
			"every option",
			emailable.CreateBatchRequest{
				Emails:      []string{testEmail, createBatchOther},
				CallbackURL: testHook,
				ResponseFields: []emailable.ResponseField{
					emailable.ResponseFieldEmail, emailable.ResponseFieldState, emailable.ResponseFieldScore,
				},
				Retries:  new(false),
				Simulate: emailable.SimulateGenericError,
			},
			`{"emails":["` + testEmail + `","` + createBatchOther + `"],"url":"` + testHook +
				`","response_fields":"email,state,score","retries":false,"simulate":"generic_error"}`,
		},
		{"retries nil is not sent", createBatchWith(func(r *emailable.CreateBatchRequest) { r.Retries = nil }), createBatchMinimal},
		{
			"retries true", createBatchWith(func(r *emailable.CreateBatchRequest) { r.Retries = new(true) }),
			`{"emails":["` + testEmail + `"],"retries":true}`,
		},
		{
			"retries false", createBatchWith(func(r *emailable.CreateBatchRequest) { r.Retries = new(false) }),
			`{"emails":["` + testEmail + `"],"retries":false}`,
		},
		{
			"one response field",
			createBatchWith(func(r *emailable.CreateBatchRequest) {
				r.ResponseFields = []emailable.ResponseField{emailable.ResponseFieldReason}
			}),
			`{"emails":["` + testEmail + `"],"response_fields":"reason"}`,
		},
		{
			"response fields in the given order",
			createBatchWith(func(r *emailable.CreateBatchRequest) {
				r.ResponseFields = []emailable.ResponseField{
					emailable.ResponseFieldScore, emailable.ResponseFieldEmail, emailable.ResponseFieldReason,
					emailable.ResponseFieldState,
				}
			}),
			`{"emails":["` + testEmail + `"],"response_fields":"score,email,reason,state"}`,
		},
		{
			"every response field, reversed",
			createBatchWith(func(r *emailable.CreateBatchRequest) {
				r.ResponseFields = createBatchAllFields()
				slices.Reverse(r.ResponseFields)
			}),
			`{"emails":["` + testEmail + `"],"response_fields":"user,tag,state,smtp_provider,score,role,reason,` +
				`no_reply,mx_record,mailbox_full,last_name,gender,full_name,free,first_name,email,domain,disposable,` +
				`did_you_mean,birth_year,accept_all"}`,
		},
		{
			"simulate generic_error",
			createBatchWith(func(r *emailable.CreateBatchRequest) { r.Simulate = emailable.SimulateGenericError }),
			`{"emails":["` + testEmail + `"],"simulate":"generic_error"}`,
		},
		{
			"simulate insufficient_credits_error",
			createBatchWith(func(r *emailable.CreateBatchRequest) { r.Simulate = emailable.SimulateInsufficientCreditsError }),
			`{"emails":["` + testEmail + `"],"simulate":"insufficient_credits_error"}`,
		},
		{
			"simulate payment_error",
			createBatchWith(func(r *emailable.CreateBatchRequest) { r.Simulate = emailable.SimulatePaymentError }),
			`{"emails":["` + testEmail + `"],"simulate":"payment_error"}`,
		},
		{
			"callback url with a query token, sent as given",
			createBatchWith(func(r *emailable.CreateBatchRequest) {
				r.CallbackURL = "https://hooks.example.com/emailable?token=" + createBatchLeak + "&batch=a%20b"
			}),
			`{"emails":["` + testEmail + `"],"url":"https://hooks.example.com/emailable?token=` + createBatchLeak +
				`&batch=a%20b"}`,
		},
		{
			"callback url with an uppercase scheme, a port and characters the encoder escapes",
			createBatchWith(func(r *emailable.CreateBatchRequest) {
				r.CallbackURL = `HTTPS://Hooks.Example.com:8443/cb?a=<b>&c="d"\e`
			}),
			`{"emails":["` + testEmail + `"],"url":"HTTPS://Hooks.Example.com:8443/cb?a=<b>&c=\"d\"\\e"}`,
		},
		{
			"addresses with quotes, backslashes, HTML, line separators and non-ASCII text",
			emailable.CreateBatchRequest{Emails: []string{
				`a"b@example.com`, `a\b@example.com`, `<a>&b@example.com`, "a\u2028b@example.com",
				"a\u2029b@example.com", "jos\u00e9@example.com", "\u03b4\u03bf\u03ba\u03b9\u03bc\u03ae@example.com",
				"mail\U0001F4EC@example.com", "a/b'c@example.com", "a\u00a0b@example.com",
			}},
			`{"emails":["a\"b@example.com","a\\b@example.com","<a>&b@example.com","a\u2028b@example.com",` +
				`"a\u2029b@example.com","` + "jos\u00e9@example.com" + `","` +
				"\u03b4\u03bf\u03ba\u03b9\u03bc\u03ae@example.com" + `","` + "mail\U0001F4EC@example.com" +
				`","a/b'c@example.com","` + "a\u00a0b@example.com" + `"]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, s := newScriptClient(t, testutil.Step{Body: createOK})
			res, err := c.CreateBatch(t.Context(), tt.req)
			require.NoError(t, err)
			assert.Equal(t, &emailable.CreateBatchResult{BatchID: testBatchID, Message: createBatchMessage}, res)
			r := testutil.OnlyRequest(t, s)
			assertWireRequest(t, r, http.MethodPost, pathBatch)
			assertNoReplay(t, r)
			assertBody(t, tt.want, r.Body)
			assert.Equal(t, int64(len(tt.want)), r.ContentLength, "an exact Content-Length")
			assert.Equal(t, tt.req.Emails, createBatchSentEmails(t, r.Body), "encoding/json reads back every address")
		})
	}
}

func TestCreateBatchSendsOrderAndDuplicatesAsGiven(t *testing.T) {
	t.Run("short list", func(t *testing.T) {
		emails := []string{
			createBatchOther, testEmail, createBatchOther, "Jane.Doe@example.com", testEmail, "zed@lists.example.com",
			"aaron@example.com",
		}
		c, s := newScriptClient(t, testutil.Step{Body: createOK})
		_, err := c.CreateBatch(t.Context(), emailable.CreateBatchRequest{Emails: emails})
		require.NoError(t, err)
		assertBody(t, `{"emails":["`+createBatchOther+`","`+testEmail+`","`+createBatchOther+
			`","Jane.Doe@example.com","`+testEmail+`","zed@lists.example.com","aaron@example.com"]}`,
			testutil.OnlyRequest(t, s).Body)
	})

	t.Run("long descending list with repeats", func(t *testing.T) {
		emails := make([]string, 5000)
		for i := range emails {
			emails[i] = "user" + strconv.Itoa(9999-i%1000) + "@example.com"
		}
		given := slices.Clone(emails)
		c, s := newScriptClient(t, testutil.Step{Body: createOK})
		_, err := c.CreateBatch(t.Context(), emailable.CreateBatchRequest{Emails: emails})
		require.NoError(t, err)
		assert.Equal(t, given, createBatchSentEmails(t, testutil.OnlyRequest(t, s).Body), "never sorted or deduplicated")
		assert.Equal(t, given, emails, "the request is never modified")
	})
}

// TestCreateBatchSnapshotsRequestBeforeSend changes the request from inside
// the round trip: the bytes on the wire were fixed before any I/O.
func TestCreateBatchSnapshotsRequestBeforeSend(t *testing.T) {
	req := emailable.CreateBatchRequest{
		Emails:         []string{testEmail, createBatchOther},
		ResponseFields: []emailable.ResponseField{emailable.ResponseFieldEmail},
	}
	var sent []byte
	rt := testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		req.Emails[0] = "changed@lists.example.com"
		req.ResponseFields[0] = emailable.ResponseFieldScore
		sent = testutil.Record(r).Body
		return testutil.Response(r, http.StatusOK, testutil.NewBody(createOK), int64(len(createOK))), nil
	})
	c := newTransportClient(t, rt)
	_, err := c.CreateBatch(t.Context(), req)
	require.NoError(t, err)
	assertBody(t, `{"emails":["`+testEmail+`","`+createBatchOther+`"],"response_fields":"email"}`, sent)
}

// createBatchInvalid is one CreateBatch request that local validation
// refuses.
type createBatchInvalid struct {
	name   string
	req    emailable.CreateBatchRequest
	field  string
	reason string
}

// createBatchWithEmailAt returns a request of valid addresses with bad at
// index i, followed by one more valid address.
func createBatchWithEmailAt(i int, bad string) emailable.CreateBatchRequest {
	emails := slices.Repeat([]string{testEmail}, i+2)
	emails[i] = bad
	return emailable.CreateBatchRequest{Emails: emails}
}

// createBatchWithHook returns a minimal request with the callback URL raw.
func createBatchWithHook(raw string) emailable.CreateBatchRequest {
	return createBatchWith(func(r *emailable.CreateBatchRequest) { r.CallbackURL = raw })
}

// createBatchWithFields returns a minimal request with the response fields.
func createBatchWithFields(fields ...emailable.ResponseField) emailable.CreateBatchRequest {
	return createBatchWith(func(r *emailable.CreateBatchRequest) { r.ResponseFields = fields })
}

// createBatchWithSimulation returns a minimal request with the simulation s.
func createBatchWithSimulation(s emailable.Simulation) emailable.CreateBatchRequest {
	return createBatchWith(func(r *emailable.CreateBatchRequest) { r.Simulate = s })
}

// createBatchLongEmail returns an address of n bytes that holds the leak
// marker.
func createBatchLongEmail(n int) string {
	const domain = "@example.com"
	return createBatchLeak + strings.Repeat("x", n-len(createBatchLeak)-len(domain)) + domain
}

// createBatchLongHook returns an https callback URL of n bytes that holds the
// leak marker.
func createBatchLongHook(n int) string {
	prefix := "https://hooks.example.com/" + createBatchLeak + "/"
	return prefix + strings.Repeat("a", n-len(prefix))
}

// createBatchEmailCases are the requests refused for their addresses.
func createBatchEmailCases() []createBatchInvalid {
	const (
		required   = "required"
		control    = "control_character"
		whitespace = "surrounding_whitespace"
		comma      = "contains_comma"
	)
	leaky := func(s string) string { return strings.ReplaceAll(s, "LEAK", createBatchLeak) }
	return []createBatchInvalid{
		{"nil emails", emailable.CreateBatchRequest{}, "Emails", required},
		{"empty emails", emailable.CreateBatchRequest{Emails: []string{}}, "Emails", required},
		{"nil emails with every other member set", emailable.CreateBatchRequest{
			CallbackURL: "http://" + createBatchLeak, ResponseFields: []emailable.ResponseField{"x"}, Simulate: "x",
		}, "Emails", required},
		{"empty address first", createBatchWithEmailAt(0, ""), "Emails[0]", required},
		{"empty address later", createBatchWithEmailAt(3, ""), "Emails[3]", required},
		{"address of 255 bytes", createBatchWithEmailAt(1, createBatchLongEmail(255)), "Emails[1]", "too_long"},
		{"invalid UTF-8", createBatchWithEmailAt(2, leaky("LEAK\xff@example.com")), "Emails[2]", "invalid_utf8"},
		{"truncated UTF-8", createBatchWithEmailAt(0, leaky("LEAK@example.co\xc3")), "Emails[0]", "invalid_utf8"},
		{"NUL", createBatchWithEmailAt(1, leaky("LEAK\x00@example.com")), "Emails[1]", control},
		{"tab inside", createBatchWithEmailAt(1, leaky("LEAK\t@example.com")), "Emails[1]", control},
		{"leading tab", createBatchWithEmailAt(1, leaky("\tLEAK@example.com")), "Emails[1]", control},
		{"line feed at the end", createBatchWithEmailAt(1, leaky("LEAK@example.com\n")), "Emails[1]", control},
		{"carriage return", createBatchWithEmailAt(1, leaky("LEAK\r@example.com")), "Emails[1]", control},
		{"DEL", createBatchWithEmailAt(1, leaky("LEAK\x7f@example.com")), "Emails[1]", control},
		{"escape", createBatchWithEmailAt(1, leaky("LEAK\x1b@example.com")), "Emails[1]", control},
		{"C1 control U+0085", createBatchWithEmailAt(1, leaky("LEAK\u0085@example.com")), "Emails[1]", control},
		{"leading space", createBatchWithEmailAt(4, leaky(" LEAK@example.com")), "Emails[4]", whitespace},
		{"trailing space", createBatchWithEmailAt(4, leaky("LEAK@example.com ")), "Emails[4]", whitespace},
		{"leading no-break space", createBatchWithEmailAt(4, leaky("\u00a0LEAK@example.com")), "Emails[4]", whitespace},
		{"trailing ideographic space", createBatchWithEmailAt(4, leaky("LEAK@example.com\u3000")), "Emails[4]", whitespace},
		{"trailing line separator", createBatchWithEmailAt(4, leaky("LEAK@example.com\u2028")), "Emails[4]", whitespace},
		{"two addresses joined by a comma", createBatchWithEmailAt(2, leaky("LEAK@example.com,"+testEmail)), "Emails[2]", comma},
		{"lone comma", createBatchWithEmailAt(2, ","), "Emails[2]", comma},
		{"trailing comma", createBatchWithEmailAt(2, leaky("LEAK@example.com,")), "Emails[2]", comma},
		{"the first failing address wins", emailable.CreateBatchRequest{Emails: []string{
			testEmail, leaky("LEAK,x"), leaky("LEAK\xff"), "",
		}}, "Emails[1]", comma},
	}
}

// createBatchHookCases are the requests refused for their callback URL.
func createBatchHookCases() []createBatchInvalid {
	const (
		field   = "CallbackURL"
		scheme  = "unsupported_scheme"
		control = "control_character"
	)
	leaky := func(s string) string { return strings.ReplaceAll(s, "LEAK", createBatchLeak) }
	return []createBatchInvalid{
		{"http", createBatchWithHook(leaky("http://hooks.example.com/cb?token=LEAK")), field, scheme},
		{"ftp", createBatchWithHook(leaky("ftp://hooks.example.com/LEAK")), field, scheme},
		{"wss", createBatchWithHook(leaky("wss://hooks.example.com/LEAK")), field, scheme},
		{"scheme-relative", createBatchWithHook(leaky("//hooks.example.com/LEAK")), field, scheme},
		{"userinfo", createBatchWithHook(leaky("https://user:LEAK@hooks.example.com/cb")), field, reasonInvalidURL},
		{"user only", createBatchWithHook(leaky("https://LEAK@hooks.example.com/cb")), field, reasonInvalidURL},
		{"empty userinfo", createBatchWithHook(leaky("https://@hooks.example.com/LEAK")), field, reasonInvalidURL},
		{"http with userinfo", createBatchWithHook(leaky("http://user:LEAK@hooks.example.com/")), field, reasonInvalidURL},
		{"fragment", createBatchWithHook(leaky("https://hooks.example.com/cb?token=LEAK#frag")), field, reasonInvalidURL},
		{"bare #", createBatchWithHook(leaky("https://hooks.example.com/cb?token=LEAK#")), field, reasonInvalidURL},
		{"relative path", createBatchWithHook(leaky("/hooks/LEAK")), field, reasonInvalidURL},
		{"no scheme", createBatchWithHook(leaky("hooks.example.com/LEAK")), field, reasonInvalidURL},
		{"missing host", createBatchWithHook(leaky("https:///LEAK")), field, reasonInvalidURL},
		{"port without a host", createBatchWithHook(leaky("https://:443/LEAK")), field, reasonInvalidURL},
		{"opaque", createBatchWithHook(leaky("https:hooks.example.com/LEAK")), field, reasonInvalidURL},
		{"mailto", createBatchWithHook(leaky("mailto:LEAK@example.com")), field, reasonInvalidURL},
		{"unparsable host", createBatchWithHook(leaky("https://[::1/LEAK")), field, reasonInvalidURL},
		{"bad percent escape", createBatchWithHook(leaky("https://hooks.example.com/%zzLEAK")), field, reasonInvalidURL},
		{"space", createBatchWithHook(leaky("https://hooks.example.com/a LEAK")), field, reasonInvalidURL},
		{"leading space", createBatchWithHook(leaky(" https://hooks.example.com/LEAK")), field, reasonInvalidURL},
		{"trailing no-break space", createBatchWithHook(leaky("https://hooks.example.com/LEAK\u00a0")), field, reasonInvalidURL},
		{"ideographic space", createBatchWithHook(leaky("https://hooks.example.com/\u3000LEAK")), field, reasonInvalidURL},
		{"line separator", createBatchWithHook(leaky("https://hooks.example.com/\u2028LEAK")), field, reasonInvalidURL},
		{"tab", createBatchWithHook(leaky("https://hooks.example.com/LEAK\t")), field, control},
		{"line feed", createBatchWithHook(leaky("https://hooks.example.com/LEAK\n")), field, control},
		{"NUL", createBatchWithHook(leaky("https://hooks.example.com/LEAK\x00")), field, control},
		{"DEL", createBatchWithHook(leaky("https://hooks.example.com/LEAK\x7f")), field, control},
		{"C1 control", createBatchWithHook(leaky("https://hooks.example.com/LEAK\u0085")), field, control},
		{"invalid UTF-8", createBatchWithHook(leaky("https://hooks.example.com/LEAK\xff")), field, "invalid_utf8"},
		{"2049 bytes", createBatchWithHook(createBatchLongHook(2049)), field, "too_long"},
	}
}

// createBatchOptionCases are the requests refused for their response
// fields, their simulation, or the order in which members are checked.
func createBatchOptionCases() []createBatchInvalid {
	const unknown = "unknown_value"
	return []createBatchInvalid{
		{"unknown response field", createBatchWithFields(emailable.ResponseFieldEmail, "duration"), "ResponseFields[1]", unknown},
		{"empty response field", createBatchWithFields(""), "ResponseFields[0]", unknown},
		{"uppercase response field", createBatchWithFields("Email"), "ResponseFields[0]", unknown},
		{"response field with a space", createBatchWithFields(" email"), "ResponseFields[0]", unknown},
		{"comma-joined response fields in one value", createBatchWithFields("email,state"), "ResponseFields[0]", unknown},
		{"duplicate response field", createBatchWithFields(
			emailable.ResponseFieldEmail, emailable.ResponseFieldState, emailable.ResponseFieldEmail,
		), "ResponseFields[2]", "duplicate"},
		{"unknown before duplicate", createBatchWithFields("x", "x"), "ResponseFields[0]", unknown},
		{"unknown simulation", createBatchWithSimulation("explode"), "Simulate", unknown},
		{"simulation in another case", createBatchWithSimulation("Generic_Error"), "Simulate", unknown},
		{"importing is for GetBatch", createBatchWithSimulation(emailable.SimulateImporting), "Simulate", "wrong_operation"},
		{"verifying is for GetBatch", createBatchWithSimulation(emailable.SimulateVerifying), "Simulate", "wrong_operation"},
		{"paused is for GetBatch", createBatchWithSimulation(emailable.SimulatePaused), "Simulate", "wrong_operation"},
		{"addresses before the callback", emailable.CreateBatchRequest{
			Emails: []string{""}, CallbackURL: "http://" + createBatchLeak,
		}, "Emails[0]", reasonRequired},
		{"callback before the response fields", emailable.CreateBatchRequest{
			Emails: []string{testEmail}, CallbackURL: "http://" + createBatchLeak, ResponseFields: []emailable.ResponseField{"x"},
		}, "CallbackURL", "unsupported_scheme"},
		{"response fields before the simulation", emailable.CreateBatchRequest{
			Emails: []string{testEmail}, ResponseFields: []emailable.ResponseField{"x"}, Simulate: "x",
		}, "ResponseFields[0]", unknown},
	}
}

func TestCreateBatchRejectsInvalidRequests(t *testing.T) {
	c, ct := newOfflineClient(t)
	cases := slices.Concat(createBatchEmailCases(), createBatchHookCases(), createBatchOptionCases())
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			res, err := c.CreateBatch(t.Context(), tt.req)
			assert.Nil(t, res)
			assertValidation(t, err, emailable.ErrInvalidRequest, tt.field, tt.reason)
			assertErrorClean(t, err, createBatchLeak, testEmail, testHook, "hookmarker93c1d7", testKey)
		})
	}
	assert.Zero(t, ct.Attempts(), "no invalid request may reach the transport")
}

func TestCreateBatchAcceptsBoundaryValues(t *testing.T) {
	tests := []struct {
		name string
		req  emailable.CreateBatchRequest
	}{
		{"address of 254 bytes", createBatchWithEmailAt(0, createBatchLongEmail(254))},
		{"address of one byte", createBatchWithEmailAt(0, "a")},
		{"address without an @", createBatchWithEmailAt(0, "not an address")},
		{"inner no-break space", createBatchWithEmailAt(0, "a\u00a0b@example.com")},
		{"callback url of 2048 bytes", createBatchWithHook(createBatchLongHook(2048))},
		{"callback url with an uppercase scheme", createBatchWithHook("HTTPS://Hooks.Example.com/cb")},
		{"callback url with a port and an empty query", createBatchWithHook("https://hooks.example.com:8443/cb?")},
		{"callback url with an IPv6 host", createBatchWithHook("https://[2001:db8::1]/cb?token=t")},
		{"callback url with only a host", createBatchWithHook("https://hooks.example.com")},
		{"every response field", createBatchWithFields(createBatchAllFields()...)},
		{"no email response field", createBatchWithFields(emailable.ResponseFieldState)},
		{"empty response fields", createBatchWithFields()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, s := newScriptClient(t, testutil.Step{Body: createOK})
			res, err := c.CreateBatch(t.Context(), tt.req)
			require.NoError(t, err)
			assert.Equal(t, testBatchID, res.BatchID)
			assert.Equal(t, 1, s.Calls())
		})
	}
}

func TestCreateBatchEmailCountLimit(t *testing.T) {
	assert.Equal(t, 50_000, emailable.DefaultMaxBatchEmails)
	limit := func(n int) []emailable.Option { return []emailable.Option{emailable.WithMaxBatchEmails(n)} }
	tests := []struct {
		name string
		opts []emailable.Option
		n    int
		ok   bool
	}{
		{"the default accepts 50,000", nil, 50_000, true},
		{"the default refuses 50,001", nil, 50_001, false},
		{"a raised limit accepts 50,001", limit(50_001), 50_001, true},
		{"a raised limit refuses one more", limit(50_001), 50_002, false},
		{"a limit of one accepts one", limit(1), 1, true},
		{"a limit of one refuses two", limit(1), 2, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := emailable.CreateBatchRequest{Emails: slices.Repeat([]string{testEmail}, tt.n)}
			if !tt.ok {
				c, ct := newOfflineClient(t, tt.opts...)
				res, err := c.CreateBatch(t.Context(), req)
				assert.Nil(t, res)
				assertValidation(t, err, emailable.ErrInvalidRequest, "Emails", "too_many")
				assert.Zero(t, ct.Attempts())
				return
			}
			s := testutil.NewScript(testutil.Step{Body: createOK})
			c := newTransportClient(t, s, tt.opts...)
			res, err := c.CreateBatch(t.Context(), req)
			require.NoError(t, err)
			assert.Equal(t, testBatchID, res.BatchID)
			assert.Len(t, createBatchSentEmails(t, testutil.OnlyRequest(t, s).Body), tt.n)
		})
	}
}

// createBatchHeavy returns a valid 233-byte address that encoding/json grows
// to 435 bytes: '"' and '\' take two bytes each once escaped, and U+2028 and
// U+2029 six instead of three.
func createBatchHeavy() string {
	return "a" + strings.Repeat(`"`, 40) + strings.Repeat(`\`, 40) + strings.Repeat("\u2028", 20) +
		strings.Repeat("\u2029", 20) + strings.Repeat("\u00e9", 10) + "@example.com"
}

// createBatchFiller returns an address whose JSON encoding is exactly n bytes
// long, for n from 15 to 497.
func createBatchFiller(tb testing.TB, n int) string {
	tb.Helper()
	require.GreaterOrEqual(tb, n, createBatchShortest)
	quotes, odd := (n-createBatchShortest)/2, (n-createBatchShortest)%2
	s := "a" + strings.Repeat(`"`, quotes) + strings.Repeat("b", odd) + "@example.com"
	require.LessOrEqual(tb, len(s), 254)
	require.Equal(tb, n, testutil.JSONStringLen(s))
	return s
}

// createBatchFill returns addresses whose POST /v1/batch body, with the
// members tail before its closing brace, is exactly size bytes long: copies
// of createBatchHeavy, then one address that takes up the rest.
func createBatchFill(tb testing.TB, size int, tail string) []string {
	tb.Helper()
	heavy := createBatchHeavy()
	unit := testutil.JSONStringLen(heavy) + len(",")
	rest := size - len(`{"emails":[]}`) - len(tail)
	n := (rest - createBatchShortest) / unit
	emails := make([]string, n, n+1)
	for i := range emails {
		emails[i] = heavy
	}
	return append(emails, createBatchFiller(tb, rest-n*unit))
}

// createBatchTail is the members every option of createBatchOptioned adds
// after the emails, without the closing brace.
func createBatchTail(retries bool) string {
	return `,"url":"` + testHook + `","response_fields":"email,state","retries":` + strconv.FormatBool(retries) +
		`,"simulate":"payment_error"`
}

// createBatchOptioned returns a request for emails with every option set.
func createBatchOptioned(emails []string, retries bool) emailable.CreateBatchRequest {
	return emailable.CreateBatchRequest{
		Emails:         emails,
		CallbackURL:    testHook,
		ResponseFields: []emailable.ResponseField{emailable.ResponseFieldEmail, emailable.ResponseFieldState},
		Retries:        new(retries),
		Simulate:       emailable.SimulatePaymentError,
	}
}

// createBatchSink records what a request body puts on the wire without
// keeping it: its length and its first and last 64 bytes.
type createBatchSink struct {
	n          int64
	head, tail []byte
}

// Write records p.
func (s *createBatchSink) Write(p []byte) (int, error) {
	const keep = 64
	s.n += int64(len(p))
	if room := keep - len(s.head); room > 0 {
		s.head = append(s.head, p[:min(room, len(p))]...)
	}
	s.tail = append(s.tail, p[max(0, len(p)-keep):]...)
	s.tail = slices.Clone(s.tail[max(0, len(s.tail)-keep):])
	return len(p), nil
}

func TestCreateBatchRequestSizeLimit(t *testing.T) {
	t.Run("a body of exactly 64 MiB is sent once", func(t *testing.T) {
		req := createBatchOptioned(createBatchFill(t, createBatchMaxBody, createBatchTail(true)), true)
		sink := &createBatchSink{}
		var declared int64
		ct := &testutil.CountingTransport{Next: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
			declared = r.ContentLength
			_, _ = io.Copy(sink, r.Body)
			_ = r.Body.Close()
			return testutil.Response(r, http.StatusOK, testutil.NewBody(createOK), int64(len(createOK))), nil
		})}
		c := newTransportClient(t, ct, emailable.WithMaxBatchEmails(createBatchHuge))
		res, err := c.CreateBatch(t.Context(), req)
		require.NoError(t, err)
		assert.Equal(t, testBatchID, res.BatchID)
		assert.Equal(t, 1, ct.Attempts())
		assert.Equal(t, int64(createBatchMaxBody), declared)
		assert.Equal(t, int64(createBatchMaxBody), sink.n)
		assert.True(t, bytes.HasPrefix(sink.head, []byte(`{"emails":["a\"\"\"`)), "head %q", sink.head)
		assert.True(t, bytes.HasSuffix(sink.tail, []byte(`"email,state","retries":true,"simulate":"payment_error"}`)),
			"tail %q", sink.tail)
	})

	over := []struct {
		name string
		req  func(t *testing.T) emailable.CreateBatchRequest
	}{
		{"one byte over in the addresses alone", func(t *testing.T) emailable.CreateBatchRequest {
			t.Helper()
			return emailable.CreateBatchRequest{Emails: createBatchFill(t, createBatchMaxBody+1, "")}
		}},
		{"one byte over with every option", func(t *testing.T) emailable.CreateBatchRequest {
			t.Helper()
			return createBatchOptioned(createBatchFill(t, createBatchMaxBody+1, createBatchTail(true)), true)
		}},
		{"retries false is one byte longer than true", func(t *testing.T) emailable.CreateBatchRequest {
			t.Helper()
			return createBatchOptioned(createBatchFill(t, createBatchMaxBody, createBatchTail(true)), false)
		}},
	}
	for _, tt := range over {
		t.Run(tt.name, func(t *testing.T) {
			req := tt.req(t)
			raw := len(`{"emails":[]}`) + len(req.Emails) - 1
			for _, e := range req.Emails {
				raw += len(e)
			}
			require.Less(t, raw, createBatchMaxBody, "unescaped, the addresses would fit: only escaping takes them over")
			c, ct := newOfflineClient(t, emailable.WithMaxBatchEmails(createBatchHuge))
			res, err := c.CreateBatch(t.Context(), req)
			assert.Nil(t, res)
			assertValidation(t, err, emailable.ErrInvalidRequest, "request", "too_large")
			assertErrorClean(t, err, testHook, "hookmarker93c1d7")
			assert.Zero(t, ct.Attempts())
		})
	}
}

// TestCreateBatchFarOverSizeLimitEncodesNothing refuses a list that would
// encode to about 87 MB, and shows that refusing it allocates next to
// nothing: the size is computed, never found by encoding.
func TestCreateBatchFarOverSizeLimitEncodesNothing(t *testing.T) {
	req := emailable.CreateBatchRequest{Emails: slices.Repeat([]string{createBatchHeavy()}, 200_000), CallbackURL: testHook}
	c, ct := newOfflineClient(t, emailable.WithMaxBatchEmails(createBatchHuge))
	res, err := c.CreateBatch(t.Context(), req)
	assert.Nil(t, res)
	assertValidation(t, err, emailable.ErrInvalidRequest, "request", "too_large")
	assert.Zero(t, ct.Attempts())

	t.Run("allocation stays far below the body size", func(t *testing.T) {
		testutil.SkipUnderRace(t)
		grown := testutil.HeapGrowth(func() {
			_, _ = c.CreateBatch(t.Context(), req) // the outcome is asserted above
		})
		t.Logf("one refused call allocated %d bytes for a body of about 87 MB", grown)
		assert.Less(t, grown, uint64(1<<20))
	})
}

func TestCreateBatchDecodesAnswer(t *testing.T) {
	long := func(n int) string { return strings.Repeat("m", n) }
	escaped := func(n int) string { return testutil.Esc(strings.Repeat("~u0041", n)) }
	printable := ""
	for c := byte(0x21); c <= 0x7e; c++ {
		printable += string(rune(c))
	}
	tests := []struct {
		name string
		body string
		want emailable.CreateBatchResult
	}{
		{"documented members", createOK, emailable.CreateBatchResult{BatchID: testBatchID, Message: createBatchMessage}},
		{"id only", `{"id":"` + testBatchID + `"}`, emailable.CreateBatchResult{BatchID: testBatchID}},
		{"message null", `{"id":"b1","message":null}`, emailable.CreateBatchResult{BatchID: "b1"}},
		{"message empty", `{"id":"b1","message":""}`, emailable.CreateBatchResult{BatchID: "b1"}},
		{"message number dropped", `{"id":"b1","message":5}`, emailable.CreateBatchResult{BatchID: "b1"}},
		{"message object dropped", `{"id":"b1","message":{"text":"m"}}`, emailable.CreateBatchResult{BatchID: "b1"}},
		{"message array dropped", `{"id":"b1","message":["m"]}`, emailable.CreateBatchResult{BatchID: "b1"}},
		{"message boolean dropped", `{"id":"b1","message":true}`, emailable.CreateBatchResult{BatchID: "b1"}},
		{"message of 4096 bytes kept", `{"id":"b1","message":"` + long(4096) + `"}`, emailable.CreateBatchResult{BatchID: "b1", Message: long(4096)}},
		{"message of 4097 bytes dropped", `{"id":"b1","message":"` + long(4097) + `"}`, emailable.CreateBatchResult{BatchID: "b1"}},
		{
			"escaped message decoding to 4096 bytes kept", `{"id":"b1","message":"` + escaped(4096) + `"}`,
			emailable.CreateBatchResult{BatchID: "b1", Message: strings.Repeat("A", 4096)},
		},
		{"escaped message decoding to 4097 bytes dropped", `{"id":"b1","message":"` + escaped(4097) + `"}`, emailable.CreateBatchResult{BatchID: "b1"}},
		{
			"message escapes decoded", testutil.Esc(`{"id":"b1","message":"Batch ~"created~" ~u00e9"}`),
			emailable.CreateBatchResult{BatchID: "b1", Message: "Batch \"created\" \u00e9"},
		},
		{"undocumented members ignored", `{"status":"ok","id":"b1","extra":{"id":"b2"},"Message2":[1]}`, emailable.CreateBatchResult{BatchID: "b1"}},
		{"surrounding whitespace", " \n\t{ \"id\" : \"b1\" }\r\n ", emailable.CreateBatchResult{BatchID: "b1"}},
		{"escaped id", testutil.Esc(`{"id":"~u0062atch~/1"}`), emailable.CreateBatchResult{BatchID: "batch/1"}},
		{"id of 256 bytes", `{"id":"` + strings.Repeat("i", 256) + `"}`, emailable.CreateBatchResult{BatchID: strings.Repeat("i", 256)}},
		{"id of every visible ASCII byte", `{"id":` + testutil.JSONQuote(printable) + `}`, emailable.CreateBatchResult{BatchID: printable}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := callOnce(t, pathBatch, tt.body, callCreate)
			require.NoError(t, err)
			assert.Equal(t, &tt.want, res)
		})
	}
}

func TestCreateBatchIDFailures(t *testing.T) {
	const malformed, unexpected = emailable.FailureMalformedResponse, emailable.FailureUnexpectedResponse
	tests := []struct {
		name string
		body string
		kind emailable.FailureKind
	}{
		{"missing", `{"message":"m"}`, malformed},
		{"null", `{"id":null,"message":"m"}`, malformed},
		{"empty object", `{}`, malformed},
		{"other case only", `{"ID":"b1"}`, malformed},
		{"nested only", `{"batch":{"id":"b1"}}`, malformed},
		{"number", `{"id":12345}`, malformed},
		{"boolean", `{"id":true}`, malformed},
		{"object", `{"id":{"id":"b1"}}`, malformed},
		{"array", `{"id":["b1"]}`, malformed},
		{"empty string", `{"id":""}`, unexpected},
		{"space", `{"id":"b 1"}`, unexpected},
		{"leading space", `{"id":" b1"}`, unexpected},
		{"257 bytes", `{"id":"` + strings.Repeat("i", 257) + `"}`, unexpected},
		{"escaped to 257 bytes", `{"id":"` + testutil.Esc(strings.Repeat("~u0069", 257)) + `"}`, unexpected},
		{"non-ASCII", "{\"id\":\"b\u00e91\"}", unexpected},
		{"escaped non-ASCII", testutil.Esc(`{"id":"b~u00e91"}`), unexpected},
		{"escaped control character", testutil.Esc(`{"id":"b~u00071"}`), unexpected},
		{"escaped tab", testutil.Esc(`{"id":"b~t1"}`), unexpected},
		{"escaped DEL", testutil.Esc(`{"id":"b~u007f1"}`), unexpected},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := callOnce(t, pathBatch, tt.body, callCreate)
			_ = assertCallError(t, err, createBatchFailure(tt.kind, "id"))
			require.ErrorIs(t, err, emailable.ErrOutcomeUnknown, "a batch may exist: reconcile by hand")
		})
	}
}

// createBatchContainers returns n empty JSON arrays, comma-separated: enough
// of them exceed the SDK's bound on containers in one body.
func createBatchContainers(n int) string {
	return strings.TrimSuffix(strings.Repeat("[],", n), ",")
}

// TestCreateBatchRescuesIDFromUnreadableAnswer reads answers that are not
// strict JSON but still name exactly one well-formed id: whatever made them
// unreadable, the failure is malformed_response with the BatchID, and the
// outcome is known, because the batch exists.
func TestCreateBatchRescuesIDFromUnreadableAnswer(t *testing.T) {
	tests := []struct {
		name string
		body string
		id   string
	}{
		{"trailing object", `{"id":"b1","message":"m"} {"id":"b2"}`, "b1"},
		{"trailing garbage", `{"id":"b1"}x`, "b1"},
		{"trailing bracket", `{"id":"b1"}]`, "b1"},
		{"leading whitespace and trailing data", " \n{\"id\":\"b1\"}\n{", "b1"},
		{"duplicate message", `{"id":"b1","message":"a","message":"b"}`, "b1"},
		{"duplicate keys differing in case", `{"message":"a","Message":"b","id":"b1"}`, "b1"},
		{"duplicate nested keys", `{"id":"b1","meta":{"a":1,"a":2}}`, "b1"},
		{"invalid UTF-8 in the message", "{\"message\":\"Batch \xff started\",\"id\":\"abc123\"}", "abc123"},
		{"invalid UTF-8 after the object", "{\"id\":\"b1\"} \xff", "b1"},
		{"escaped id key", testutil.Esc(`{"~u0069d":"b1"} x`), "b1"},
		{"escaped id value", testutil.Esc(`{"id":"~u0062atch"} x`), "batch"},
		{"too many containers", `{"id":"b1","pad":[` + createBatchContainers(100_001) + `]}`, "b1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := callOnce(t, pathBatch, tt.body, callCreate)
			_ = assertCallError(t, err, createBatchRescued(tt.id))
			require.NotErrorIs(t, err, emailable.ErrOutcomeUnknown, "the batch exists: persist its ID")
		})
	}
}

// TestCreateBatchRescueDropsSecretIDs answers with an unreadable body whose
// id echoes the API key, or any part of an address, the callback URL or its
// token: such an ID is never kept, because GoString prints BatchID, so the
// outcome stays unknown.
func TestCreateBatchRescueDropsSecretIDs(t *testing.T) {
	req := createBatchWithHook(testHook)
	req.Emails = append(req.Emails, `q"uote@example.com`)
	submit := func(ctx context.Context, c *emailable.Client) (*emailable.CreateBatchResult, error) {
		return c.CreateBatch(ctx, req)
	}
	for _, tt := range []struct{ name, body string }{
		{"the callback url", `{"id":"` + testHook + `"} x`},
		{"the callback url, escaped", `{"id":"` + strings.ReplaceAll(testHook, "/", `\/`) + `"} x`},
		{"the API key", `{"id":"` + testKey + `"} x`},
		{"the API key inside", `{"id":"batch-` + testKey + `-1","message":"a","message":"b"}`},
		{"an address", `{"id":"` + testEmail + `"} x`},
		{"part of an address", `{"id":"jane.doe"} x`},
		{"an address that JSON escapes", `{"id":"q"uote@example.com"} x`},
		{"the callback token", `{"id":"hookmarker93c1d7"} x`},
		{"the key without its prefix", `{"id":"` + testKey[5:] + `"} x`},
		{"the key without its last byte", `{"id":"` + testKey[:len(testKey)-1] + `"} x`},
		{"an 8-byte piece of the key", `{"id":"` + testKey[5:13] + `"} x`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := callOnce(t, pathBatch, tt.body, submit)
			_ = assertCallError(t, err, createBatchFailure(emailable.FailureMalformedResponse, ""))
		})
	}

	t.Run("an ordinary id is still kept", func(t *testing.T) {
		_, err := callOnce(t, pathBatch, `{"id":"b1"} x`, submit)
		_ = assertCallError(t, err, createBatchRescued("b1"))
	})

	t.Run("a piece of the key shorter than 8 bytes is still kept", func(t *testing.T) {
		piece := testKey[5:12]
		_, err := callOnce(t, pathBatch, `{"id":"`+piece+`"} x`, submit)
		_ = assertCallError(t, err, createBatchRescued(piece))
	})
}

func TestCreateBatchDoesNotRescueDoubtfulID(t *testing.T) {
	const malformed, unexpected = emailable.FailureMalformedResponse, emailable.FailureUnexpectedResponse
	tests := []struct {
		name string
		body string
		kind emailable.FailureKind
	}{
		{"empty body", ``, malformed},
		{"whitespace only", " \n ", malformed},
		{"invalid JSON", `{"id":"b1",`, malformed},
		{"unterminated object", `{"id":"b1"`, malformed},
		{"invalid UTF-8 inside the id", "{\"id\":\"b\xff1\"}", malformed},
		{"invalid UTF-8 inside the id with trailing data", "{\"id\":\"b\xff1\"} x", malformed},
		{"invalid UTF-8 inside the id key", "{\"i\xffd\":\"b1\"} x", malformed},
		{"id twice", `{"id":"b1","id":"b1"}`, malformed},
		{"two different ids", `{"id":"b1","id":"b2"}`, malformed},
		{"id and ID", `{"id":"b1","ID":"b1"}`, malformed},
		{"id and Id", `{"Id":"b2","id":"b1"}`, malformed},
		{"id and an escaped ID", testutil.Esc(`{"id":"b1","~u0049D":"b1"}`), malformed},
		{"only ID with trailing data", `{"ID":"b1"} x`, malformed},
		{"id of the wrong shape with trailing data", `{"id":"b 1"} x`, malformed},
		{"numeric id with trailing data", `{"id":5} x`, malformed},
		{"null id with trailing data", `{"id":null} x`, malformed},
		{"empty id with trailing data", `{"id":""} x`, malformed},
		{"nested id with trailing data", `{"batch":{"id":"b1"}} x`, malformed},
		{"array with trailing data", `[{"id":"b1"}] x`, malformed},
		{"top-level array", `[{"id":"b1"}]`, unexpected},
		{"top-level string", `"b1"`, unexpected},
		{"top-level null", `null`, unexpected},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := callOnce(t, pathBatch, tt.body, callCreate)
			_ = assertCallError(t, err, createBatchFailure(tt.kind, ""))
		})
	}
}

// createBatchClass is one way a CreateBatch call can fail after it was sent.
type createBatchClass struct {
	name   string
	step   testutil.Step
	kind   emailable.FailureKind
	status int
	field  string
}

// createBatchAnswer answers HTTP 200 with the body newBody returns, declared
// with length (-1 for unknown).
func createBatchAnswer(length int64, newBody func() io.ReadCloser) testutil.Step {
	return testutil.Step{Respond: testutil.Transport(http.StatusOK, length, newBody)}
}

// createBatchStatusClasses answers every status other than 200 with a body
// that names a batch: none is ever read for its ID.
func createBatchStatusClasses() []createBatchClass {
	codes := []int{
		201, 202, 204, 249, 300, 301, 302, 303, 304, 307, 308, 400, 401, 402, 403, 404, 405, 408, 409, 410, 413,
		422, 429, 451, 500, 501, 502, 503, 504, 599,
	}
	out := make([]createBatchClass, 0, len(codes))
	for _, code := range codes {
		h := http.Header{"Content-Type": {mediaJSON}}
		if code >= 300 && code < 400 {
			h.Set("Location", "https://attacker.lists.example.com/v1/batch?token="+createBatchLeak)
		}
		out = append(out, createBatchClass{
			name: "HTTP " + strconv.Itoa(code), step: testutil.Step{Status: code, Header: h, Body: createOK},
			kind: statusKind(emailable.OpCreateBatch, code), status: code,
		})
	}
	return out
}

// createBatchOtherClasses are the failures that are not an HTTP status.
func createBatchOtherClasses() []createBatchClass {
	ok := http.StatusOK
	tooLarge := `{"id":"` + testBatchID + `","pad":"` + strings.Repeat("p", 1<<20) + `"}`
	return []createBatchClass{
		{name: "transport error", step: testutil.Step{Err: errCreateBatchTransport}, kind: emailable.FailureTransport},
		{name: "unexpected EOF", step: testutil.Step{Err: io.ErrUnexpectedEOF}, kind: emailable.FailureTransport},
		{
			name: "network timeout", step: testutil.Step{Err: testutil.TimeoutError{Msg: "i/o timeout " + createBatchLeak}},
			kind: emailable.FailureTimeout,
		},
		{name: "body read failure", step: createBatchAnswer(-1, func() io.ReadCloser {
			b := testutil.NewBody(createOK)
			b.End = errCreateBatchTransport
			return b
		}), kind: emailable.FailureTransport, status: ok},
		{name: "truncated body", step: createBatchAnswer(int64(len(createOK)+10), func() io.ReadCloser {
			return testutil.NewBody(createOK)
		}), kind: emailable.FailureTransport, status: ok},
		{name: "stalled body", step: createBatchAnswer(-1, func() io.ReadCloser {
			b := testutil.NewBody(createOK)
			b.Stall = true
			return b
		}), kind: emailable.FailureTransport, status: ok},
		{name: "declared body over 1 MiB", step: testutil.Step{Body: tooLarge}, kind: emailable.FailureResponseTooLarge, status: ok},
		{name: "endless body", step: createBatchAnswer(-1, func() io.ReadCloser {
			return &testutil.EndlessBody{}
		}), kind: emailable.FailureResponseTooLarge, status: ok},
		{name: "undecoded gzip", step: testutil.Step{
			Header: http.Header{"Content-Type": {mediaJSON}, "Content-Encoding": {"gzip"}}, Body: createOK,
		}, kind: emailable.FailureUnexpectedResponse, status: ok},
		{name: "invalid JSON", step: testutil.Step{Body: `{`}, kind: emailable.FailureMalformedResponse, status: ok},
		{name: "not an object", step: testutil.Step{Body: `[]`}, kind: emailable.FailureUnexpectedResponse, status: ok},
		{name: "missing id", step: testutil.Step{Body: `{"message":"m"}`}, kind: emailable.FailureMalformedResponse, status: ok, field: "id"},
		{name: "id of the wrong shape", step: testutil.Step{Body: `{"id":"b 1"}`}, kind: emailable.FailureUnexpectedResponse, status: ok, field: "id"},
	}
}

// TestCreateBatchNeverResubmits fails the one attempt in every way, with a
// success queued behind it: CreateBatch must never take it.
func TestCreateBatchNeverResubmits(t *testing.T) {
	for _, tt := range slices.Concat(createBatchStatusClasses(), createBatchOtherClasses()) {
		t.Run(tt.name, func(t *testing.T) {
			s := testutil.NewScript(tt.step, testutil.Step{Body: createOK})
			c := newTransportClient(t, s)
			res, err := c.CreateBatch(t.Context(), createRequest())
			assert.Nil(t, res)
			_ = assertCallError(t, err, wantCall{
				Kind: tt.kind, Op: emailable.OpCreateBatch, Status: tt.status,
				OutcomeUnknown: outcomeUnknownAfter(true, tt.kind), Attempts: 1, Field: tt.field,
			}, createBatchLeak)
			r := testutil.OnlyRequest(t, s) // the batch is never resubmitted
			assertBody(t, createBatchMinimal, r.Body)
		})
	}
}

func TestCreateBatchTimeoutIsNotResubmitted(t *testing.T) {
	tests := []struct {
		name   string
		first  testutil.Step
		status int
	}{
		{"no answer", testutil.Step{Respond: testutil.BlockUntilDone}, 0},
		{"a body that stops", createBatchAnswer(-1, func() io.ReadCloser {
			return testutil.NewBlockingBody(`{"id":"` + testBatchID)
		}), http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s := testutil.NewScript(tt.first, testutil.Step{Body: createOK})
				c := newTransportClient(t, s, emailable.WithTimeout(time.Second))
				start := time.Now()
				res, err := c.CreateBatch(t.Context(), createRequest())
				assert.Nil(t, res)
				assert.Equal(t, time.Second, time.Since(start), "the SDK's timeout ends the attempt")
				_ = assertCallError(t, err, wantCall{
					Kind: emailable.FailureTimeout, Op: emailable.OpCreateBatch, Status: tt.status,
					OutcomeUnknown: true, Attempts: 1,
				})
				assert.Equal(t, 1, s.Calls())
			})
		})
	}
}

func TestCreateBatchCancellationAfterSending(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	s := testutil.NewScript(testutil.Step{Respond: func(r *http.Request) (*http.Response, error) {
		cancel()
		return testutil.BlockUntilDone(r)
	}}, testutil.Step{Body: createOK})
	c := newTransportClient(t, s)
	res, err := c.CreateBatch(ctx, createRequest())
	assert.Nil(t, res)
	_ = assertCallError(t, err, wantCall{
		Kind: emailable.FailureCanceled, Op: emailable.OpCreateBatch, OutcomeUnknown: true, Attempts: 1,
	})
	assert.Equal(t, 1, s.Calls())
}

func TestCreateBatchDoneContextSendsNothing(t *testing.T) {
	c, ct := newOfflineClient(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	res, err := c.CreateBatch(ctx, createRequest())
	assert.Nil(t, res)
	_ = assertCallError(t, err, wantCall{Kind: emailable.FailureCanceled, Op: emailable.OpCreateBatch})
	assert.Zero(t, ct.Attempts())
}

// TestCreateBatchRequestNeverPrintsDataWithAnyVerb formats a request with
// every verb and every way of holding it that fmt reaches through methods.
// The documented exceptions are %p on a value, which fmt reports as a bad
// verb by reflection, and a request in an unexported struct field, whose
// methods fmt cannot call.
func TestCreateBatchRequestNeverPrintsDataWithAnyVerb(t *testing.T) {
	req := emailable.CreateBatchRequest{
		Emails:         []string{testEmail, createBatchLeak + "@example.com"},
		CallbackURL:    testHook,
		ResponseFields: []emailable.ResponseField{emailable.ResponseFieldEmail},
		Retries:        new(true),
		Simulate:       emailable.SimulateGenericError,
	}
	secrets := []string{testEmail, testHook, "hookmarker93c1d7", createBatchLeak}
	testutil.AssertNeverPrints(t, &req, secrets...)
	for _, sh := range testutil.Shapes(&req) {
		if strings.Contains(sh.Name, "unexported") {
			continue
		}
		for _, verb := range testutil.FormatVerbs() {
			if verb == "%p" && (sh.Name == "value" || sh.Name == "exported fields") {
				continue
			}
			testutil.AssertRedacted(t, fmt.Sprintf(verb, sh.Value), secrets, "%s %s", sh.Name, verb)
		}
	}
}

func TestCreateBatchResultJSON(t *testing.T) {
	assertRoundTrip(t, &emailable.CreateBatchResult{BatchID: testBatchID, Message: createBatchMessage},
		`{"id":"`+testBatchID+`","message":"`+createBatchMessage+`"}`)
	assertRoundTrip(t, &emailable.CreateBatchResult{BatchID: testBatchID}, `{"id":"`+testBatchID+`"}`)
	assertRoundTrip(t, &emailable.CreateBatchResult{}, `{"id":""}`)
}
