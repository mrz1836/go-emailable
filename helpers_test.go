package emailable_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-emailable"
	"github.com/mrz1836/go-emailable/internal/testutil"
)

// Synthetic test values. They double as leak markers: no error, formatted
// value or reflection walk may ever contain them. They are not real secrets.
const (
	testKey     = "test_keymarker7f3a2c91" // gitleaks:allow (synthetic test marker)
	testEmail   = "jane.doe@example.com"
	testBatchID = "batchmarker5d0c7e19a4"
	testCaptcha = "captchamarker0b6e2f"
	testHook    = "https://hooks.example.com/emailable?token=hookmarker93c1d7"
	testOwner   = "owner.marker@example.com"
)

// loopbackOrigin is a test base URL for in-memory transports, which never dial.
const loopbackOrigin = "https://127.0.0.1"

// The path of each operation.
const (
	pathVerify  = "/v1/verify"
	pathBatch   = "/v1/batch"
	pathAccount = "/v1/account"
)

// mediaJSON is the media type the SDK sends and accepts.
const mediaJSON = "application/json"

// Smallest successful answers, synthetic SDK fixtures.
const (
	verifyOK  = `{"email":"` + testEmail + `","state":"deliverable","reason":"accepted_email"}`
	createOK  = `{"message":"Batch successfully created.","id":"` + testBatchID + `"}`
	batchOK   = `{"id":"` + testBatchID + `","message":"Batch verification completed.","total_counts":{"processed":1,"total":1}}`
	accountOK = `{"owner_email":"` + testOwner + `","available_credits":1000}`
	pending   = `{"message":"Your request is taking longer than normal. Please send your request again."}`
)

// Validation reasons, as ValidationError.Reason reports them.
const (
	reasonRequired         = "required"
	reasonNil              = "nil"
	reasonOutOfRange       = "out_of_range"
	reasonNotInitialized   = "not_initialized"
	reasonInvalidFormat    = "invalid_format"
	reasonInvalidURL       = "invalid_url"
	reasonControlCharacter = "control_character"
)

// The fixed causes of decoding failures and of HTTP 249, as a CallError's
// Unwrap returns them.
const (
	causeMissing       = "emailable: response lacks a required field"
	causeType          = "emailable: response field has the wrong JSON type"
	causeRange         = "emailable: response field is out of range"
	causeShape         = "emailable: response field is not a valid token"
	causeFieldTooLarge = "emailable: response field exceeds its size bound"
	causePending       = "emailable: HTTP 249: verification still pending"
	causeStatus        = "emailable: unexpected HTTP status"
)

// errUnrelated is an error no CallError may ever match.
var errUnrelated = errors.New("unrelated test error")

// errBrokenCall reports an operation case whose call broke its own contract,
// such as returning a value alongside an error.
var errBrokenCall = errors.New("the call broke its contract")

// opCase is one public method of Client that calls the provider, called with a
// minimal valid request and reduced to its error. Every suite that runs all
// the methods shares it; a suite that needs more, such as a marker payload or
// an exact wire request, wraps it.
type opCase struct {
	name    string              // the method's name
	op      emailable.Operation // the operation it calls
	method  string              // its HTTP method
	path    string              // its path
	paid    bool                // VerifyEmail, WaitForVerification and CreateBatch
	batchID string              // the BatchID of its CallErrors: testBatchID for GetBatch
	helper  bool                // WaitForVerification, which repeats VerifyEmail
	ok      testutil.Step       // an answer to its first call that ends it successfully
	call    func(ctx context.Context, c *emailable.Client) error
}

// want completes w, the CallError of a failed call of o, with o's Op and
// BatchID.
func (o opCase) want(w wantCall) wantCall {
	w.Op, w.BatchID = o.op, o.batchID
	return w
}

// opCases returns the operation cases: the four operations in the
// documentation's order, then WaitForVerification.
func opCases() []opCase {
	return []opCase{
		{
			name: "VerifyEmail", op: emailable.OpVerifyEmail, method: http.MethodGet, path: pathVerify, paid: true,
			ok: testutil.Step{Body: verifyOK},
			call: func(ctx context.Context, c *emailable.Client) error {
				return noValueWith(callVerify(ctx, c))
			},
		},
		{
			name: "CreateBatch", op: emailable.OpCreateBatch, method: http.MethodPost, path: pathBatch, paid: true,
			ok: testutil.Step{Body: createOK},
			call: func(ctx context.Context, c *emailable.Client) error {
				return noValueWith(callCreate(ctx, c))
			},
		},
		{
			name: "GetBatch", op: emailable.OpGetBatch, method: http.MethodGet, path: pathBatch, batchID: testBatchID,
			ok: testutil.Step{Body: batchOK},
			call: func(ctx context.Context, c *emailable.Client) error {
				return noValueWith(callGet(ctx, c))
			},
		},
		{
			name: "AccountInfo", op: emailable.OpAccountInfo, method: http.MethodGet, path: pathAccount,
			ok: testutil.Step{Body: accountOK},
			call: func(ctx context.Context, c *emailable.Client) error {
				return noValueWith(callAccount(ctx, c))
			},
		},
		{
			name: "WaitForVerification", op: emailable.OpVerifyEmail, method: http.MethodGet, path: pathVerify,
			paid: true, helper: true, ok: testutil.Step{Body: verifyOK},
			call: func(ctx context.Context, c *emailable.Client) error {
				return noValueWith(c.WaitForVerification(ctx, waitRequest(0, 3, time.Minute)))
			},
		},
	}
}

// opCaseNamed returns the case of the method called name. An unknown name is
// a bug in the test, so it panics with the name.
func opCaseNamed(name string) opCase {
	for _, o := range opCases() {
		if o.name == name {
			return o
		}
	}
	panic("no operation case named " + strconv.Quote(name))
}

// operations returns the cases of the four operations, without the helper.
func operations() []opCase {
	return slices.DeleteFunc(opCases(), func(o opCase) bool { return o.helper })
}

// verifyRequest returns a minimal valid verify request.
func verifyRequest() emailable.VerifyEmailRequest {
	return emailable.VerifyEmailRequest{Email: testEmail}
}

// createRequest returns a minimal valid batch request.
func createRequest() emailable.CreateBatchRequest {
	return emailable.CreateBatchRequest{Emails: []string{testEmail}}
}

// waitRequest returns a WaitForVerification request for verifyRequest().
func waitRequest(interval time.Duration, attempts int, maxWait time.Duration) emailable.WaitForVerificationRequest {
	return emailable.WaitForVerificationRequest{
		Verify: verifyRequest(), Interval: interval, MaxAttempts: attempts, MaxWait: maxWait,
	}
}

// callVerify makes one VerifyEmail call with verifyRequest().
func callVerify(ctx context.Context, c *emailable.Client) (*emailable.Verification, error) {
	return c.VerifyEmail(ctx, verifyRequest())
}

// callCreate makes one CreateBatch call with createRequest().
func callCreate(ctx context.Context, c *emailable.Client) (*emailable.CreateBatchResult, error) {
	return c.CreateBatch(ctx, createRequest())
}

// callGet makes one GetBatch call for testBatchID.
func callGet(ctx context.Context, c *emailable.Client) (*emailable.Batch, error) {
	return c.GetBatch(ctx, emailable.GetBatchRequest{BatchID: testBatchID})
}

// callAccount makes one AccountInfo call.
func callAccount(ctx context.Context, c *emailable.Client) (*emailable.AccountInfo, error) {
	return c.AccountInfo(ctx, emailable.AccountInfoRequest{})
}

// contextFactory makes the context of a call from its parent, with the
// CancelFunc that releases it. Inside a synctest bubble, it must be called in
// the bubble.
type contextFactory func(context.Context) (context.Context, context.CancelFunc)

// cancelable is a contextFactory whose context ends only when canceled.
func cancelable(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithCancel(parent)
}

// alreadyCanceled is a contextFactory whose context is already canceled.
func alreadyCanceled(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	cancel()
	return ctx, cancel
}

// canceledAfter returns a contextFactory whose context is canceled d after it
// is made.
func canceledAfter(d time.Duration) contextFactory {
	return func(parent context.Context) (context.Context, context.CancelFunc) {
		ctx, cancel := context.WithCancel(parent)
		time.AfterFunc(d, cancel)
		return ctx, cancel
	}
}

// expiresAfter returns a contextFactory whose context expires d after it is
// made (already, when d <= 0).
func expiresAfter(d time.Duration) contextFactory {
	return func(parent context.Context) (context.Context, context.CancelFunc) {
		return context.WithTimeout(parent, d)
	}
}

// noValueWith returns err, or errBrokenCall when a value came with it: a
// method that fails returns nil.
func noValueWith[T any](v *T, err error) error {
	if err != nil && v != nil {
		return errBrokenCall
	}
	return err
}

// newClient returns a client built with the test key and opts, failing tb when
// NewClient fails.
func newClient(tb testing.TB, opts ...emailable.Option) *emailable.Client {
	tb.Helper()
	c, err := emailable.NewClient(testKey, opts...)
	require.NoError(tb, err)
	return c
}

// okHandler answers each operation's path and method with its smallest
// successful answer, and anything else with HTTP 404.
func okHandler() http.HandlerFunc {
	answers := map[string]testutil.Step{}
	for _, o := range opCases() {
		answers[o.method+" "+o.path] = o.ok
	}
	return func(w http.ResponseWriter, r *http.Request) {
		step, ok := answers[r.Method+" "+r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		testutil.Serve(step)(w, r)
	}
}

// newServerClient starts a recording HTTP/1.1 TLS server running h and returns
// a client pointed at it.
func newServerClient(tb testing.TB, h http.HandlerFunc) (*emailable.Client, *testutil.Server) {
	tb.Helper()
	return newServerClientProto(tb, testutil.HTTP1, h)
}

// newServerClientProto is newServerClient for a server speaking proto.
func newServerClientProto(tb testing.TB, proto testutil.Protocol, h http.HandlerFunc,
	opts ...emailable.Option,
) (*emailable.Client, *testutil.Server) {
	tb.Helper()
	srv := testutil.NewServerProto(tb, proto, h)
	all := append([]emailable.Option{
		emailable.WithTestBaseURL(srv.URL()),
		emailable.WithHTTPClient(srv.Client()),
	}, opts...)
	return newClient(tb, all...), srv
}

// newTransportClient returns a client whose calls go through rt, in memory,
// with opts applied after the transport options.
func newTransportClient(tb testing.TB, rt http.RoundTripper, opts ...emailable.Option) *emailable.Client {
	tb.Helper()
	all := append([]emailable.Option{
		emailable.WithTestBaseURL(loopbackOrigin),
		emailable.WithHTTPClient(&http.Client{Transport: rt}),
	}, opts...)
	return newClient(tb, all...)
}

// newScriptClient returns a client whose calls go, in memory, through a new
// Script answering with steps.
func newScriptClient(tb testing.TB, steps ...testutil.Step) (*emailable.Client, *testutil.Script) {
	tb.Helper()
	s := testutil.NewScript(steps...)
	return newTransportClient(tb, s), s
}

// offlineTransport is a transport that fails tb if anything uses it.
func offlineTransport(tb testing.TB) testutil.RoundTripFunc {
	tb.Helper()
	return func(r *http.Request) (*http.Response, error) {
		assert.Fail(tb, "unexpected round trip to "+r.URL.Path+": this call must send nothing")
		return nil, errUnrelated
	}
}

// newOfflineClient returns a client whose transport fails tb if it is ever
// called, and the transport, whose Attempts must stay 0: for tests of calls
// that must send nothing.
func newOfflineClient(tb testing.TB, opts ...emailable.Option) (*emailable.Client, *testutil.CountingTransport) {
	tb.Helper()
	ct := &testutil.CountingTransport{Next: offlineTransport(tb)}
	return newTransportClient(tb, ct, opts...), ct
}

// callOnce makes one call answered in memory with body on HTTP 200, asserts
// that it made exactly one attempt, to path, and returned nothing alongside
// an error, and returns its outcome.
func callOnce[T any](tb testing.TB, path, body string,
	call func(context.Context, *emailable.Client) (*T, error),
) (*T, error) {
	tb.Helper()
	c, s := newScriptClient(tb, testutil.Step{Body: body})
	v, err := call(tb.Context(), c)
	assert.Equal(tb, []string{path}, s.Paths(), "exactly one attempt, to the operation's path")
	if err != nil {
		assert.Nil(tb, v, "nothing alongside an error")
	}
	return v, err
}

// causeText returns the text of the cause of err's CallError, failing tb when
// there is none.
func causeText(tb testing.TB, err error) string {
	tb.Helper()
	cause := errors.Unwrap(callError(tb, err))
	require.Error(tb, cause)
	return cause.Error()
}

// callError returns the *emailable.CallError in err's chain, failing tb when
// there is none.
func callError(tb testing.TB, err error) *emailable.CallError {
	tb.Helper()
	return testutil.RequireErrorAs[*emailable.CallError](tb, err)
}

// validationError returns the *emailable.ValidationError in err's chain,
// failing tb when there is none.
func validationError(tb testing.TB, err error) *emailable.ValidationError {
	tb.Helper()
	return testutil.RequireErrorAs[*emailable.ValidationError](tb, err)
}

// assertValidation asserts that err is the *ValidationError for field and
// reason that matches sentinel, ErrInvalidConfig or ErrInvalidRequest, and
// not the other one, with the fixed error text, which never quotes a value.
// It is never a CallError.
func assertValidation(tb testing.TB, err, sentinel error, field, reason string) {
	tb.Helper()
	require.ErrorIs(tb, err, sentinel)
	ve := validationError(tb, err)
	assert.Equal(tb, emailable.ValidationError{Err: sentinel, Field: field, Reason: reason}, *ve)
	for _, other := range []error{emailable.ErrInvalidConfig, emailable.ErrInvalidRequest} {
		assert.Equal(tb, errors.Is(other, sentinel), errors.Is(err, other), "matches %v", other)
	}
	want := sentinel.Error()
	for _, part := range []string{field, reason} {
		if part != "" {
			want += ": " + part
		}
	}
	assert.Equal(tb, want, err.Error())
	assert.NotErrorAs(tb, err, new(*emailable.CallError), "a validation failure is never a CallError")
}

// assertErrorClean fails tb when a secret appears however a caller might print
// or walk err: any fmt verb on each error of its Unwrap chain, or on err
// wrapped or joined, or any string reflection reaches. The chain must also hold
// no *url.Error, whose text quotes the URL.
func assertErrorClean(tb testing.TB, err error, secrets ...string) {
	tb.Helper()
	for e := err; e != nil; e = errors.Unwrap(e) {
		testutil.AssertNeverPrints(tb, e, secrets...)
	}
	testutil.AssertNeverPrints(tb, fmt.Errorf("wrapped: %w", err), secrets...)
	testutil.AssertNeverPrints(tb, errors.Join(err, context.Canceled), secrets...)
	testutil.AssertRedacted(tb, strings.Join(testutil.ReflectStrings(err), "\n"), secrets, "reflection walk")
	assert.NotErrorAs(tb, err, new(*url.Error), "the chain must never hold a *url.Error")
}

// leakMarkers returns every synthetic secret and personal value the tests
// plant: none may ever appear in an error.
func leakMarkers() []string {
	return []string{testKey, testEmail, testCaptcha, testHook, "hookmarker93c1d7", testOwner}
}

// verbsButP returns every verb of testutil.FormatVerbs but %p, which fmt
// applies to a value without calling its methods.
func verbsButP() []string {
	return slices.DeleteFunc(testutil.FormatVerbs(), func(v string) bool { return v == "%p" })
}

// printable returns the ways a caller might hold p whose printing calls its
// methods: fmt cannot call a method on a value held in an unexported struct
// field, so those shapes are left out (see TestPrivacyRequestFmtLimits).
func printable[T any](p *T) []testutil.Shape {
	return slices.DeleteFunc(testutil.Shapes(p), func(s testutil.Shape) bool {
		return strings.Contains(s.Name, "unexported")
	})
}

// assertShapeOnly asserts that p, a value with a Format method, prints
// exactly want (quoted for %q) with every verb but %p, as a value and as a
// pointer, and that none of its printable shapes prints a leak marker or one
// of secrets.
func assertShapeOnly[T any](tb testing.TB, p *T, want string, secrets ...string) {
	tb.Helper()
	secrets = append(leakMarkers(), secrets...)
	for _, verb := range verbsButP() {
		w := want
		if verb == "%q" {
			w = strconv.Quote(want)
		}
		assert.Equal(tb, w, fmt.Sprintf(verb, *p), "value %s", verb)
		assert.Equal(tb, w, fmt.Sprintf(verb, p), "pointer %s", verb)
		for _, s := range printable(p) {
			testutil.AssertRedacted(tb, fmt.Sprintf(verb, s.Value), secrets, "%s %s", s.Name, verb)
		}
	}
}

// wantCall is the *CallError a test expects: every exported field but
// RateLimit, zero values included.
type wantCall struct {
	Kind           emailable.FailureKind
	Op             emailable.Operation
	Status         int
	OutcomeUnknown bool
	BatchID        string
	Attempts       int
	Field          string
}

// assertCallError asserts that err is the *CallError want describes, field
// for field. The Error text the caller sees must be the documented one, err
// must match exactly the sentinels of its kind (and ErrOutcomeUnknown exactly
// when OutcomeUnknown is set), its cause must be a fixed one, and none of the
// leak markers or secrets may appear however err is printed or walked. It
// returns the CallError, for tests that need more of it, such as its
// RateLimit or Diagnostics.
func assertCallError(tb testing.TB, err error, want wantCall, secrets ...string) *emailable.CallError {
	tb.Helper()
	ce := callError(tb, err)
	assert.Equal(tb, want, wantCall{
		Kind: ce.Kind, Op: ce.Op, Status: ce.StatusCode, OutcomeUnknown: ce.OutcomeUnknown,
		BatchID: ce.BatchID, Attempts: ce.Attempts, Field: ce.Field,
	})
	assert.Equal(tb, errorText(want), err.Error(), "the error the caller sees")
	assertOnlySentinel(tb, err, want.Kind, want.OutcomeUnknown)
	assertFixedCause(tb, err)
	assertErrorClean(tb, err, append(leakMarkers(), secrets...)...)
	return ce
}

// errorText renders the documented Error grammar of a CallError,
// "emailable: <op>: <kind>[ (HTTP <n>)][: <field>]", independently of the
// library.
func errorText(w wantCall) string {
	s := "emailable: " + string(w.Op) + ": " + string(w.Kind)
	if w.Status != 0 {
		s += " (HTTP " + strconv.Itoa(w.Status) + ")"
	}
	if w.Field != "" {
		s += ": " + w.Field
	}
	return s
}

// allSentinels returns every error a CallError could be asked about with
// errors.Is: the package's sentinels, both context errors, and an unrelated
// error.
func allSentinels() []error {
	return []error{
		emailable.ErrInvalidConfig, emailable.ErrInvalidRequest, emailable.ErrPending, emailable.ErrUnauthorized,
		emailable.ErrPaymentRequired, emailable.ErrNotFound, emailable.ErrRateLimited, emailable.ErrRejected,
		emailable.ErrUpstream, emailable.ErrUnavailable, emailable.ErrUnexpectedResponse,
		emailable.ErrMalformedResponse, emailable.ErrResponseTooLarge, emailable.ErrTransport, emailable.ErrTimeout,
		emailable.ErrBudgetExhausted, emailable.ErrOutcomeUnknown,
		context.Canceled, context.DeadlineExceeded, errUnrelated,
	}
}

// wantMatches returns the targets a CallError of kind must match with
// errors.Is: its own sentinel, plus context.DeadlineExceeded for a timeout,
// plus ErrOutcomeUnknown when the outcome is unknown.
func wantMatches(kind emailable.FailureKind, outcomeUnknown bool) []error {
	want := []error{kindSentinels()[kind]}
	if kind == emailable.FailureTimeout {
		want = append(want, context.DeadlineExceeded)
	}
	if outcomeUnknown {
		want = append(want, emailable.ErrOutcomeUnknown)
	}
	return want
}

// assertOnlySentinel asserts that err, a CallError of kind or anything
// wrapping one, matches exactly the targets documented for kind and no other
// sentinel or context error.
func assertOnlySentinel(tb testing.TB, err error, kind emailable.FailureKind, outcomeUnknown bool) {
	tb.Helper()
	want := wantMatches(kind, outcomeUnknown)
	for _, target := range allSentinels() {
		assert.Equal(tb, slices.Contains(want, target), errors.Is(err, target), "kind %s vs %v", kind, target)
	}
}

// assertFixedCause asserts that the cause of err's *CallError is a context
// error or a fixed error of the package that wraps nothing: never a reader's
// or transport's own error.
func assertFixedCause(tb testing.TB, err error) {
	tb.Helper()
	cause := callError(tb, err).Unwrap()
	require.Error(tb, cause, "a CallError the SDK returns keeps a cause")
	if slices.Contains([]error{context.Canceled, context.DeadlineExceeded}, cause) {
		return
	}
	assert.True(tb, strings.HasPrefix(cause.Error(), "emailable: "), "cause %q is a fixed package error", cause)
	require.NoError(tb, errors.Unwrap(cause), "a fixed cause wraps nothing")
}

// statusKind is the documented failure kind of an HTTP status other than 200
// for op, written out independently of the library.
func statusKind(op emailable.Operation, code int) emailable.FailureKind {
	switch {
	case code == 249 && op == emailable.OpVerifyEmail:
		return emailable.FailurePending
	case code == 401 || code == 403:
		return emailable.FailureUnauthorized
	case code == 402:
		return emailable.FailurePaymentRequired
	case code == 404:
		return emailable.FailureNotFound
	case code == 429:
		return emailable.FailureRateLimited
	case code >= 400 && code <= 499:
		return emailable.FailureRejected
	case code == 503:
		return emailable.FailureUnavailable
	case code >= 500 && code <= 599:
		return emailable.FailureUpstream
	default:
		return emailable.FailureUnexpectedResponse
	}
}

// outcomeUnknownAfter is the documented OutcomeUnknown of a failure of kind
// after a paid request was sent, written out independently of the library:
// set for everything but the provider's own refusals and HTTP 249.
func outcomeUnknownAfter(paid bool, kind emailable.FailureKind) bool {
	if !paid {
		return false
	}
	switch kind {
	case emailable.FailurePending, emailable.FailureUnauthorized, emailable.FailurePaymentRequired,
		emailable.FailureNotFound, emailable.FailureRateLimited, emailable.FailureRejected,
		emailable.FailureBudgetExhausted:
		return false
	case emailable.FailureUpstream, emailable.FailureUnavailable, emailable.FailureUnexpectedResponse,
		emailable.FailureMalformedResponse, emailable.FailureResponseTooLarge, emailable.FailureTransport,
		emailable.FailureTimeout, emailable.FailureCanceled, emailable.FailureDeadlineExceeded:
		return true
	}
	return true
}

// kindSentinels is the documented mapping from each failure kind to the
// sentinel, or context error, that it matches with errors.Is, written out
// independently of the library's. FailureTimeout also matches
// context.DeadlineExceeded.
func kindSentinels() map[emailable.FailureKind]error {
	return map[emailable.FailureKind]error{
		emailable.FailurePending:            emailable.ErrPending,
		emailable.FailureUnauthorized:       emailable.ErrUnauthorized,
		emailable.FailurePaymentRequired:    emailable.ErrPaymentRequired,
		emailable.FailureNotFound:           emailable.ErrNotFound,
		emailable.FailureRateLimited:        emailable.ErrRateLimited,
		emailable.FailureRejected:           emailable.ErrRejected,
		emailable.FailureUpstream:           emailable.ErrUpstream,
		emailable.FailureUnavailable:        emailable.ErrUnavailable,
		emailable.FailureUnexpectedResponse: emailable.ErrUnexpectedResponse,
		emailable.FailureMalformedResponse:  emailable.ErrMalformedResponse,
		emailable.FailureResponseTooLarge:   emailable.ErrResponseTooLarge,
		emailable.FailureTransport:          emailable.ErrTransport,
		emailable.FailureTimeout:            emailable.ErrTimeout,
		emailable.FailureCanceled:           context.Canceled,
		emailable.FailureDeadlineExceeded:   context.DeadlineExceeded,
		emailable.FailureBudgetExhausted:    emailable.ErrBudgetExhausted,
	}
}

// fixture returns the SDK fixture name, under testdata/synthetic.
func fixture(tb testing.TB, name string) string {
	tb.Helper()
	return string(testutil.Fixture(tb, "synthetic/"+name))
}

// messageBody returns a provider error body whose message is text.
func messageBody(text string) string {
	return `{"message":` + testutil.JSONQuote(text) + `}`
}

// padded returns body, a JSON object, with spaces before its closing brace,
// so that it is exactly n bytes long.
func padded(tb testing.TB, body string, n int) string {
	tb.Helper()
	require.GreaterOrEqual(tb, n, len(body))
	return body[:len(body)-1] + strings.Repeat(" ", n-len(body)) + "}"
}

// rateLimit returns the RateLimit holding the three values.
func rateLimit(limit, remaining, reset int64) emailable.RateLimit {
	return emailable.RateLimit{Limit: &limit, Remaining: &remaining, Reset: &reset}
}

// rateHeader returns a JSON response header carrying the three rate-limit
// headers.
func rateHeader(limit, remaining, reset int64) http.Header {
	return http.Header{
		"Content-Type":        {mediaJSON},
		"Ratelimit-Limit":     {strconv.FormatInt(limit, 10)},
		"Ratelimit-Remaining": {strconv.FormatInt(remaining, 10)},
		"Ratelimit-Reset":     {strconv.FormatInt(reset, 10)},
	}
}

// assertBody asserts that got is exactly the JSON text want, byte for byte:
// member order and escaping are part of the contract, so a semantic JSON
// comparison would be too lenient.
func assertBody(tb testing.TB, want string, got []byte) {
	tb.Helper()
	assert.Equal(tb, []byte(want), got, "JSON text\n got: %s\nwant: %s", got, want)
}

// assertRoundTrip marshals v, checks the JSON is exactly want, unmarshals it
// into a new T, and checks the copy is reflect.DeepEqual to v. It returns the
// copy.
func assertRoundTrip[T any](tb testing.TB, v *T, want string) *T {
	tb.Helper()
	data, err := json.Marshal(v)
	require.NoError(tb, err)
	assertBody(tb, want, data)
	back := new(T)
	require.NoError(tb, json.Unmarshal(data, back))
	assert.Equal(tb, v, back, "round trip changed the value")
	return back
}

// bannedHeaders are the request headers the SDK must never send: proxy
// credentials, cookies, and the idempotency keys with which the standard
// library may replay a request.
func bannedHeaders() []string {
	return []string{"Proxy-Authorization", "Cookie", "Idempotency-Key", "X-Idempotency-Key", "Range", "Expect"}
}

// assertWireRequest asserts the wire policy every operation shares, on a
// request recorded by a server or a transport: method and path, the SDK's
// fixed headers (Content-Type only on a POST), the bearer key, none of the
// banned headers, and an exact Content-Length on a POST and no body bytes on
// a GET. A server sees a GET's empty body as http.NoBody with length 0 on
// HTTP/1 and as an unknown length (-1) on HTTP/2, where the SDK sends an
// empty END_STREAM DATA frame.
func assertWireRequest(tb testing.TB, r testutil.Request, method, path string) {
	tb.Helper()
	assert.Equal(tb, method, r.Method)
	assert.Equal(tb, path, r.Path)
	assert.Equal(tb, []string{mediaJSON}, r.Header.Values("Accept"))
	assert.Equal(tb, []string{"go-emailable"}, r.Header.Values("User-Agent"))
	assert.Equal(tb, []string{"Bearer " + testKey}, r.Header.Values("Authorization"))
	for _, h := range bannedHeaders() {
		assert.Empty(tb, r.Header.Values(h), "header %s must never be sent", h)
	}
	if method == http.MethodPost {
		assert.Equal(tb, []string{mediaJSON}, r.Header.Values("Content-Type"))
		assert.Equal(tb, int64(len(r.Body)), r.ContentLength)
		return
	}
	assert.Empty(tb, r.Header.Values("Content-Type"), "a GET has no Content-Type")
	assert.Empty(tb, r.Body, "a GET has no body bytes")
	assert.Empty(tb, r.Header.Values("Transfer-Encoding"), "a GET is never chunked")
}

// assertNoReplay asserts, on a request recorded by a transport (a Script or a
// CountingTransport's Next, never a server), that the standard library could
// not resend it: its body is neither nil nor http.NoBody and GetBody is nil.
// A server always sees a bodiless request as http.NoBody, so this check
// means nothing there.
func assertNoReplay(tb testing.TB, r testutil.Request) {
	tb.Helper()
	assert.False(tb, r.GetBody, "GetBody must be nil, so nothing can be resent")
	assert.False(tb, r.NoBody, "the body is never http.NoBody")
	if r.Method == http.MethodGet {
		assert.Equal(tb, int64(0), r.ContentLength, "a GET declares no length")
	}
}

// query parses the raw query of a recorded request's URL, failing tb when it
// does not parse.
func query(tb testing.TB, r testutil.Request) url.Values {
	tb.Helper()
	u, err := url.Parse(r.URL)
	require.NoError(tb, err)
	q, err := url.ParseQuery(u.RawQuery)
	require.NoError(tb, err)
	return q
}

// rawQuery returns the raw query of a recorded request's URL.
func rawQuery(tb testing.TB, r testutil.Request) string {
	tb.Helper()
	u, err := url.Parse(r.URL)
	require.NoError(tb, err)
	return u.RawQuery
}
