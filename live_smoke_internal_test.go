//go:build live

package emailable

// The live smoke test runs every operation of the SDK against
// https://api.emailable.com, the provider's production API (there is no
// sandbox), with a test key: one that starts with "test_", for which the
// provider uses no credits, makes no real verification and answers with
// simulated results. It checks that each call works end to end against the
// real server, and records what the v1 documentation leaves open: the status
// and body of each error, the HTTP 249 answer, the batch snapshot shapes, and
// what each simulation returns. Only the repository owner runs it, by hand.
// CI and automation never run it, and the live build tag keeps it out of every
// normal build.
//
// It refuses every other key, twice. The key is checked before any client is
// built, and the test fails, having sent nothing, unless it starts with
// "test_". The recording transport under every client then checks each
// request again and refuses, unsent, one whose key is not a test key or that
// would exceed the call budget.
//
// What each call costs: nothing, because every call uses a test key.
// TestLiveSmoke makes at most 17 calls, in this order:
//   - account with a made-up test key, which the provider refuses (HTTP 403,
//     unauthorized, expected); any other answer stops the run;
//   - account with the key; a failure stops the run;
//   - verify of the provider's deliverable test address (state deliverable
//     expected), and of its accept-all test address with smtp, accept_all and
//     timeout set;
//   - WaitForVerification of the provider's slow test address, which always
//     answers HTTP 249: 2 attempts, 1 second apart (budget_exhausted
//     expected);
//   - batch creation of 2 example.com addresses. The batch persists until the
//     provider deletes it, as it does every test-key batch, 24 hours later;
//   - 6 reads of that batch: plain, partial, and with each GetBatch
//     simulation (importing, verifying and paused expected to succeed,
//     generic_error to fail);
//   - a read of a made-up batch ID (not_found expected);
//   - batch creation with each CreateBatch simulation: generic_error (a
//     failure expected), insufficient_credits_error and payment_error
//     (payment_required expected). A batch the provider creates anyway is
//     deleted 24 hours later too.
//
// It skips unless all of these hold:
//
//	EMAILABLE_LIVE=1
//	EMAILABLE_LIVE_KEY=<a private test key, test_...>
//	CI is unset
//
// and fails, before anything is sent, when EMAILABLE_LIVE_KEY is not a valid
// key or does not start with "test_". It never reads EMAILABLE_API_KEY, which
// may hold a live key. Run it by hand:
//
//	EMAILABLE_LIVE=1 EMAILABLE_LIVE_KEY=test_... go test -tags live -count=1 -p 1 -timeout 5m -run 'TestLiveSmoke$' -v .
//
// The guard tests make no call and need no key:
//
//	go test -tags live -count=1 -run 'TestLiveSmokeGuard' -v .
//
// Calls are never retried, start at least half a second apart (the provider
// allows 5 batch or account calls a second), and a rate-limit answer stops the
// remaining calls. The output is shape only, recorded by a transport wrapped
// around the SDK's own: per response, the method and path (never the query),
// the protocol, the HTTP status, the media type, the header names
// (testutil.HeaderNames), the JSON type tree with safe key names
// (testutil.JSONShape), and the SDK's outcome: its error text, which never
// holds values, value-free counts, and documented enum values such as a
// verification's state. No key, address, batch ID, header value, provider
// message, download URL or other response value is ever printed or written.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"maps"
	"mime"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-emailable/internal/testutil"
)

// Live smoke settings.
const (
	// lsmTestKeyPrefix starts every key the smoke test accepts: the
	// provider's test keys, which use no credits.
	lsmTestKeyPrefix = "test_"

	// lsmInvalidKey is a well-formed test key no account has.
	lsmInvalidKey = "test_livesmokeinvalidkey000000000000000" // gitleaks:allow (synthetic test marker)

	// lsmMissingBatchID is a made-up batch ID in the provider's form, 24
	// hexadecimal digits, that no batch has.
	lsmMissingBatchID = "000000000000000000000000"

	// The provider's test addresses, which answer as documented on verify
	// only, and the two rows of the smoke batch, on the reserved domain.
	lsmDeliverable = "deliverable@example.com"
	lsmAcceptAll   = "accept-all@example.com"
	lsmSlow        = "slow@example.com"
	lsmBatchFirst  = "live-smoke.one@example.com"
	lsmBatchSecond = "live-smoke.two@example.com"

	// lsmMaxCalls is the most requests one run sends, the count the comment
	// above states; the recorder refuses any more. TestLiveSmokeGuardPlan
	// ties it to the steps.
	lsmMaxCalls = 17

	// lsmShapeBytes is how much of a response the recorder reads to take its
	// shape; anything longer is passed on unread and unshaped.
	lsmShapeBytes = 64 << 10

	lsmTimeout         = 20 * time.Second // at least lsmProviderTimeout + 5s
	lsmProviderTimeout = 5 * time.Second
	lsmSpacing         = 500 * time.Millisecond // before each call
	lsmWaitInterval    = time.Second
	lsmWaitAttempts    = 2
	lsmWaitMax         = 5 * time.Second
)

// Errors of the smoke test's own guards. Their text never quotes a value.
var (
	// errLsmNotTestKey refuses a key that does not start with lsmTestKeyPrefix.
	errLsmNotTestKey = errors.New("not a test key: it must start with test_")

	// errLsmRefused is what the recorder returns, having sent nothing, for a
	// request without a test key or past the call budget.
	errLsmRefused = errors.New("live smoke: request refused before sending")
)

// lsmKey is an API key lsmCheckKey accepted: the only kind lsmNewClient
// takes, and only lsmCheckKey builds one. The key sits behind a function
// value, as in Client, so no fmt verb or dumper prints it.
type lsmKey struct{ raw func() string }

// lsmCheckKey returns key as an lsmKey when it is a valid API key that starts
// with "test_", and otherwise an error that never quotes it.
func lsmCheckKey(key string) (lsmKey, error) {
	if err := checkKey(key); err != nil {
		return lsmKey{}, err
	}
	if !strings.HasPrefix(key, lsmTestKeyPrefix) {
		return lsmKey{}, errLsmNotTestKey
	}
	return lsmKey{raw: func() string { return key }}, nil
}

// lsmReadSettings applies the gates to lookup (os.LookupEnv in a real run):
// a skip reason unless the owner enabled the run, an error when the key must
// be refused, and the key otherwise. Nothing it returns quotes a value, and it
// builds no client.
func lsmReadSettings(lookup func(string) (string, bool)) (key lsmKey, skip string, err error) {
	if _, ci := lookup("CI"); ci {
		return lsmKey{}, "the live smoke test never runs in CI", nil
	}
	if v, _ := lookup("EMAILABLE_LIVE"); v != "1" {
		return lsmKey{}, "the live smoke test is opt-in; see the comment at the top of live_smoke_internal_test.go", nil
	}
	raw, _ := lookup("EMAILABLE_LIVE_KEY")
	if raw == "" {
		return lsmKey{}, "EMAILABLE_LIVE_KEY is not set", nil
	}
	if key, err = lsmCheckKey(raw); err != nil {
		return lsmKey{}, "", fmt.Errorf("live smoke: EMAILABLE_LIVE_KEY refused, nothing was sent: %w", err)
	}
	return key, "", nil
}

// lsmEnv returns the key, or skips t unless the owner enabled the run. It
// fails t, before any client is built or anything is sent, on a key it
// refuses.
func lsmEnv(t *testing.T) lsmKey {
	t.Helper()
	key, skip, err := lsmReadSettings(os.LookupEnv)
	if skip != "" {
		t.Skip(skip)
	}
	require.NoError(t, err, "no client was built and nothing was sent")
	return key
}

func TestLiveSmoke(t *testing.T) {
	lsmRun(t, lsmEnv(t), lsmNewRecorder(t))
}

// lsmRun runs the plan with key over rec, on the production origin unless
// opts say otherwise.
func lsmRun(tb testing.TB, key lsmKey, rec *lsmRecorder, opts ...Option) {
	tb.Helper()
	invalid, err := lsmCheckKey(lsmInvalidKey)
	require.NoError(tb, err, "the made-up key passes the same guard; nothing was sent")
	defer func() { tb.Logf("requests sent: %d of at most %d", rec.sentCount(), lsmMaxCalls) }()

	if lsmNewClient(tb, rec, invalid, opts...).run(tb, lsmAuthSteps()) {
		return
	}
	lc := lsmNewClient(tb, rec, key, opts...)
	if lc.run(tb, lsmReadSteps()) || lsmBatchPhase(tb, lc) {
		return
	}
	lc.run(tb, lsmErrorSteps())
}

// lsmBatchPhase creates the smoke batch, then reads it with every GetBatch
// variant. It reports whether the run must stop.
func lsmBatchPhase(tb testing.TB, lc *lsmClient) bool {
	tb.Helper()
	var id string // never printed
	if lc.run(tb, []lsmStep{lsmCreateStep(&id)}) {
		return true
	}
	if id == "" {
		tb.Log("create batch: no batch ID, so its reads are skipped; a batch may exist anyway, " +
			"and the provider deletes test-key batches after 24 hours")
		return false
	}
	return lc.run(tb, lsmGetSteps(id))
}

// lsmWant is the outcome a step expects: success, or a failure that matches
// err with errors.Is (any failure when err is nil).
type lsmWant struct {
	ok  bool
	err error
}

// lsmStep is one call of the run.
type lsmStep struct {
	label string // the operation and the case, never a value
	calls int    // the requests it sends at most
	gate  bool   // an unexpected outcome stops the run
	want  lsmWant

	// call makes the call with tb.Context() and returns a value-free summary
	// of a success.
	call func(tb testing.TB, c *Client) (summary string, err error)
}

// lsmAuthSteps is the call with the made-up key.
func lsmAuthSteps() []lsmStep {
	return []lsmStep{{
		label: "account, made-up test key", calls: 1, gate: true, want: lsmWant{err: ErrUnauthorized}, call: lsmAccount,
	}}
}

// lsmReadSteps reads the account and makes the verifications.
func lsmReadSteps() []lsmStep {
	return []lsmStep{
		{label: "account", calls: 1, gate: true, want: lsmWant{ok: true}, call: lsmAccount},
		{label: "verify, deliverable test address", calls: 1, want: lsmWant{ok: true}, call: lsmVerifyDeliverable},
		{label: "verify, accept-all test address, every option", calls: 1, want: lsmWant{ok: true}, call: lsmVerifyOptions},
		{
			label: "WaitForVerification, slow test address", calls: lsmWaitAttempts,
			want: lsmWant{err: ErrBudgetExhausted}, call: lsmWaitSlow,
		},
	}
}

// lsmCreateStep creates the smoke batch and keeps its ID in *id.
func lsmCreateStep(id *string) lsmStep {
	return lsmStep{
		label: "create batch, 2 example.com addresses", calls: 1, want: lsmWant{ok: true},
		call: func(tb testing.TB, c *Client) (string, error) {
			tb.Helper()
			res, err := c.CreateBatch(tb.Context(), CreateBatchRequest{Emails: lsmBatchEmails()})
			if err != nil {
				return "", err
			}
			*id = res.BatchID
			return "batch ID received (not printed), message " + setOrUnset(res.Message != ""), nil
		},
	}
}

// lsmGetSteps reads batch id: plain, partial, and with each GetBatch
// simulation.
func lsmGetSteps(id string) []lsmStep {
	return []lsmStep{
		lsmGetStep("get batch", GetBatchRequest{BatchID: id}, lsmWant{ok: true}),
		lsmGetStep("get batch, partial", GetBatchRequest{BatchID: id, Partial: true}, lsmWant{ok: true}),
		lsmGetStep("get batch, simulate importing", GetBatchRequest{BatchID: id, Simulate: SimulateImporting},
			lsmWant{ok: true}),
		lsmGetStep("get batch, simulate verifying", GetBatchRequest{BatchID: id, Simulate: SimulateVerifying},
			lsmWant{ok: true}),
		lsmGetStep("get batch, simulate paused", GetBatchRequest{BatchID: id, Simulate: SimulatePaused},
			lsmWant{ok: true}),
		lsmGetStep("get batch, simulate generic_error", GetBatchRequest{BatchID: id, Simulate: SimulateGenericError},
			lsmWant{}),
	}
}

// lsmErrorSteps expect refusals: a batch that does not exist, and each
// CreateBatch simulation.
func lsmErrorSteps() []lsmStep {
	return []lsmStep{
		lsmGetStep("get batch, made-up ID", GetBatchRequest{BatchID: lsmMissingBatchID}, lsmWant{err: ErrNotFound}),
		lsmCreateSimStep(SimulateGenericError, lsmWant{}),
		lsmCreateSimStep(SimulateInsufficientCreditsError, lsmWant{err: ErrPaymentRequired}),
		lsmCreateSimStep(SimulatePaymentError, lsmWant{err: ErrPaymentRequired}),
	}
}

// lsmGetStep reads a batch with req.
func lsmGetStep(label string, req GetBatchRequest, want lsmWant) lsmStep {
	return lsmStep{label: label, calls: 1, want: want, call: func(tb testing.TB, c *Client) (string, error) {
		tb.Helper()
		b, err := c.GetBatch(tb.Context(), req)
		if err != nil {
			return "", err
		}
		return lsmBatchSummary(b), nil
	}}
}

// lsmCreateSimStep creates a batch of the smoke addresses with simulation
// sim.
func lsmCreateSimStep(sim Simulation, want lsmWant) lsmStep {
	return lsmStep{
		label: "create batch, simulate " + string(sim), calls: 1, want: want,
		call: func(tb testing.TB, c *Client) (string, error) {
			tb.Helper()
			res, err := c.CreateBatch(tb.Context(), CreateBatchRequest{Emails: lsmBatchEmails(), Simulate: sim})
			if err != nil {
				return "", err
			}
			return "a batch was created anyway (ID not printed, message " + setOrUnset(res.Message != "") +
				"); the provider deletes test-key batches after 24 hours", nil
		},
	}
}

// lsmBatchEmails returns the two addresses of every smoke batch.
func lsmBatchEmails() []string { return []string{lsmBatchFirst, lsmBatchSecond} }

// lsmAccount reads the account.
func lsmAccount(tb testing.TB, c *Client) (string, error) {
	tb.Helper()
	info, err := c.AccountInfo(tb.Context(), AccountInfoRequest{})
	if err != nil {
		return "", err
	}
	return "owner email " + setOrUnset(info.OwnerEmail != "") + ", credits read (not printed)", nil
}

// lsmVerifyDeliverable verifies the deliverable test address.
func lsmVerifyDeliverable(tb testing.TB, c *Client) (string, error) {
	tb.Helper()
	v, err := c.VerifyEmail(tb.Context(), VerifyEmailRequest{Email: lsmDeliverable})
	if err != nil {
		return "", err
	}
	if v.State != StateDeliverable {
		tb.Errorf("verify, deliverable test address: state %s, want deliverable", lsmEnum(string(v.State), v.State.Known()))
	}
	return lsmVerdict(v), nil
}

// lsmVerifyOptions verifies the accept-all test address with every optional
// query member set.
func lsmVerifyOptions(tb testing.TB, c *Client) (string, error) {
	tb.Helper()
	v, err := c.VerifyEmail(tb.Context(), VerifyEmailRequest{
		Email: lsmAcceptAll, SMTP: new(false), AcceptAll: new(true), ProviderTimeout: lsmProviderTimeout,
	})
	if err != nil {
		return "", err
	}
	if v.AcceptAll == nil || !*v.AcceptAll {
		tb.Errorf("verify, accept-all test address: accept_all %s, want true", boolOrUnset(v.AcceptAll))
	}
	return lsmVerdict(v) + ", accept_all " + boolOrUnset(v.AcceptAll), nil
}

// lsmWaitSlow waits on the slow test address, which answers HTTP 249 until
// the budget runs out.
func lsmWaitSlow(tb testing.TB, c *Client) (string, error) {
	tb.Helper()
	v, err := c.WaitForVerification(tb.Context(), WaitForVerificationRequest{
		Verify:   VerifyEmailRequest{Email: lsmSlow},
		Interval: lsmWaitInterval, MaxAttempts: lsmWaitAttempts, MaxWait: lsmWaitMax,
	})
	if ce, ok := errors.AsType[*CallError](err); ok && ce.Kind == FailureBudgetExhausted && ce.Attempts != lsmWaitAttempts {
		tb.Errorf("WaitForVerification: %d attempts, want %d", ce.Attempts, lsmWaitAttempts)
	}
	if err != nil {
		return "", err
	}
	return lsmVerdict(v), nil
}

// lsmVerdict describes a verification by its documented state and reason.
func lsmVerdict(v *Verification) string {
	return "state " + lsmEnum(string(v.State), v.State.Known()) + ", reason " + lsmEnum(string(v.Reason), v.Reason.Known())
}

// lsmEnum prints a documented enum value, and only a documented one.
func lsmEnum(value string, known bool) string {
	switch {
	case value == "":
		return "unset"
	case known:
		return value
	}
	return "<undocumented>"
}

// lsmBatchSummary describes a snapshot by counts and presence only.
func lsmBatchSummary(b *Batch) string {
	_, _, progress := b.Progress()
	return "rows " + lsmRows(b.Emails) +
		", download file " + setOrUnset(!b.DownloadFile.IsZero()) +
		", progress " + lsmPresence(progress) +
		", " + strconv.Itoa(len(b.ReasonCounts)) + " reason-count keys" +
		", " + strconv.Itoa(len(b.TotalCounts)) + " total-count keys" +
		", message " + setOrUnset(b.Message != "")
}

// lsmRows describes the inline rows: "absent", or their count.
func lsmRows(rows []Verification) string {
	if rows == nil {
		return "absent"
	}
	return strconv.Itoa(len(rows))
}

// lsmPresence names whether something is present.
func lsmPresence(present bool) string {
	if present {
		return "present"
	}
	return "absent"
}

// lsmRateLimitShape says which rate-limit headers were read, never their
// values.
func lsmRateLimitShape(r RateLimit) string {
	return "limit " + setOrUnset(r.Limit != nil) + ", remaining " + setOrUnset(r.Remaining != nil) +
		", reset " + setOrUnset(r.Reset != nil)
}

// lsmCheck reports whether err is the outcome s expects, marking t failed,
// and letting the run go on, when it is not.
func lsmCheck(tb testing.TB, s lsmStep, err error) bool {
	tb.Helper()
	switch {
	case s.want.ok && err != nil:
		tb.Errorf("%s: want success", s.label)
	case !s.want.ok && err == nil:
		tb.Errorf("%s: want a failure, got success", s.label)
	case s.want.err != nil && !errors.Is(err, s.want.err):
		tb.Errorf("%s: want a failure matching %v", s.label, s.want.err)
	default:
		return true
	}
	return false
}

// lsmClient is a client whose requests the recorder admits and describes.
type lsmClient struct {
	client *Client
	rec    *lsmRecorder
}

// lsmNewClient returns a client for key, which lsmCheckKey accepted, over rec,
// which wraps the SDK's own transport in a live run, on the production origin
// unless opts say otherwise.
func lsmNewClient(tb testing.TB, rec *lsmRecorder, key lsmKey, opts ...Option) *lsmClient {
	tb.Helper()
	require.NotNil(tb, key.raw, "only a key lsmCheckKey accepted builds a client; nothing was sent")
	opts = append([]Option{WithHTTPClient(&http.Client{Transport: rec}), WithTimeout(lsmTimeout)}, opts...)
	c, err := NewClient(key.raw(), opts...)
	require.NoError(tb, err, "the client cannot be built; nothing was sent")
	return &lsmClient{client: c, rec: rec}
}

// run makes each step in order, lsmSpacing after the previous call, and
// reports whether the run must stop.
func (lc *lsmClient) run(tb testing.TB, steps []lsmStep) (stop bool) {
	tb.Helper()
	for _, s := range steps {
		if perr := pause(tb.Context(), lsmSpacing); perr != nil {
			tb.Log("the test's context ended: stopping; no further calls are made")
			return true
		}
		summary, err := s.call(tb, lc.client)
		lc.report(tb, s.label, summary, err)
		matched := lsmCheck(tb, s, err)
		if lc.stop(tb, err) || (s.gate && !matched) {
			return true
		}
	}
	return false
}

// stop reports whether the run must stop: after a rate limit, so no further
// call adds to it, or after the recorder refused a request.
func (lc *lsmClient) stop(tb testing.TB, err error) bool {
	tb.Helper()
	switch {
	case errors.Is(err, ErrRateLimited):
		tb.Log("rate limited: stopping; no further calls are made")
		return true
	case lc.rec.refusals() > 0:
		tb.Error("the recorder refused a request, unsent: stopping")
		return true
	}
	return false
}

// report logs the shapes recorded since the last report and the SDK's
// outcome: on success the step's summary, on failure the error text, which
// holds no values, and the call error's value-free fields.
func (lc *lsmClient) report(tb testing.TB, label, summary string, err error) {
	tb.Helper()
	lsmLogShapes(tb, label, lc.rec.take())
	if err == nil {
		tb.Logf("%s: SDK outcome: success; %s", label, summary)
		return
	}
	tb.Logf("%s: SDK outcome: %v", label, err)
	ce, ok := errors.AsType[*CallError](err)
	if !ok {
		return
	}
	tb.Logf("%s: attempts %d, outcome unknown %tb, rate limit: %s", label, ce.Attempts, ce.OutcomeUnknown,
		lsmRateLimitShape(ce.RateLimit))
	if ce.Op == OpCreateBatch && ce.BatchID != "" {
		tb.Logf("%s: a batch ID came with the failure (not printed): the batch exists, and the provider deletes "+
			"test-key batches after 24 hours", label)
	}
}

// lsmLogShapes logs shapes, each run of equal shapes once with its count.
func lsmLogShapes(tb testing.TB, label string, shapes []string) {
	tb.Helper()
	if len(shapes) == 0 {
		tb.Logf("%s: no response recorded", label)
	}
	for i := 0; i < len(shapes); {
		j := i + 1
		for j < len(shapes) && shapes[j] == shapes[i] {
			j++
		}
		tb.Logf("%s: response x%d\n%s", label, j-i, shapes[i])
		i = j
	}
}

// lsmRecorder is the transport under every smoke client. It sends a request
// only when it carries a test key and fits the call budget, refusing any
// other unsent, and records a value-free description of each response,
// reading at most lsmShapeBytes of its body and handing the body on unchanged.
type lsmRecorder struct {
	next    http.RoundTripper
	budget  int
	mu      sync.Mutex
	sent    int
	refused int
	shapes  []string
}

// lsmNewRecorder returns a recorder over the SDK's own transport, whose idle
// connections close when t ends.
func lsmNewRecorder(tb testing.TB) *lsmRecorder {
	tb.Helper()
	tr := newTransport()
	tb.Cleanup(tr.CloseIdleConnections)
	return &lsmRecorder{next: tr, budget: lsmMaxCalls}
}

// RoundTrip sends req when admit allows it, and records the response.
func (r *lsmRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	if reason := r.admit(req); reason != "" {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		r.add("  refused before sending: " + reason)
		return nil, errLsmRefused
	}
	resp, err := r.next.RoundTrip(req)
	if err != nil || resp == nil {
		// The error may quote the URL, and so the query: record its absence
		// only.
		r.add("  transport failure (error not printed)")
		return resp, err
	}
	r.add(lsmShape(req, resp))
	return resp, nil
}

// admit counts req against the budget and returns why it must not be sent,
// or "". The key is checked first, so a refused key never uses up the budget.
func (r *lsmRecorder) admit(req *http.Request) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case !strings.HasPrefix(req.Header.Get("Authorization"), "Bearer "+lsmTestKeyPrefix):
		r.refused++
		return "not a test key"
	case r.sent >= r.budget:
		r.refused++
		return "over the call budget"
	}
	r.sent++
	return ""
}

// add records one description.
func (r *lsmRecorder) add(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.shapes = append(r.shapes, s)
}

// take returns the descriptions recorded so far and forgets them.
func (r *lsmRecorder) take() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := slices.Clone(r.shapes)
	r.shapes = r.shapes[:0]
	return out
}

// refusals returns the number of requests refused so far.
func (r *lsmRecorder) refusals() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.refused
}

// sentCount returns the number of requests sent so far.
func (r *lsmRecorder) sentCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sent
}

// lsmShape describes resp without any value: req's method and path (never its
// query), the protocol, the status, the media type, the header names and the
// body's shape. It reads at most lsmShapeBytes+1 bytes of the body and puts
// them back in front of the rest, so the SDK reads the body unchanged.
func lsmShape(req *http.Request, resp *http.Response) string {
	var b strings.Builder
	b.WriteString("  " + req.Method + " " + req.URL.Path + ", " + resp.Proto + " " + strconv.Itoa(resp.StatusCode))
	b.WriteString(", content type " + lsmMediaType(resp.Header.Values("Content-Type")))
	b.WriteString(", headers " + strings.Join(testutil.HeaderNames(resp.Header), " "))
	if resp.StatusCode < http.StatusOK || resp.Body == nil {
		return b.String() // a 1xx body is the raw connection: never read it
	}
	head, rerr := io.ReadAll(io.LimitReader(resp.Body, lsmShapeBytes+1))
	rest := io.Reader(resp.Body)
	if rerr != nil {
		rest = lsmErrReader{err: rerr}
	}
	resp.Body = lsmBody{Reader: io.MultiReader(bytes.NewReader(head), rest), Closer: resp.Body}
	b.WriteString("\n  body: " + lsmBodyShape(head, rerr))
	return b.String()
}

// lsmBodyShape describes a body prefix without any of its values.
func lsmBodyShape(head []byte, rerr error) string {
	switch {
	case rerr != nil:
		return strconv.Itoa(len(head)) + " bytes, then a read error (not printed)"
	case len(head) > lsmShapeBytes:
		return "longer than " + strconv.Itoa(lsmShapeBytes) + " bytes; no shape taken"
	case len(head) == 0:
		return "empty"
	}
	shape, ok := testutil.JSONShape(head)
	if !ok {
		return strconv.Itoa(len(head)) + " bytes, not JSON"
	}
	return strconv.Itoa(len(head)) + " bytes of JSON:\n" + shape
}

// lsmMediaType describes Content-Type values: each media type with its
// parameter names, and a parameter value only when it is a short plain token
// such as a charset name.
func lsmMediaType(values []string) string {
	if len(values) == 0 {
		return "<none>"
	}
	out := make([]string, 0, len(values))
	for _, v := range values {
		media, params, err := mime.ParseMediaType(v)
		if err != nil {
			out = append(out, "<unparsable>")
			continue
		}
		parts := []string{media}
		for _, name := range slices.Sorted(maps.Keys(params)) {
			value := "<value>"
			if lsmPlainToken(params[name]) {
				value = params[name]
			}
			parts = append(parts, name+"="+value)
		}
		out = append(out, strings.Join(parts, "; "))
	}
	return strings.Join(out, " | ")
}

// lsmPlainToken reports whether s is 1 to 32 letters, digits, '.', '_' or
// '-'.
func lsmPlainToken(s string) bool {
	if s == "" || len(s) > 32 {
		return false
	}
	for _, c := range []byte(s) {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '.' && c != '_' && c != '-' {
			return false
		}
	}
	return true
}

// lsmBody is a response body re-assembled after the recorder's read.
type lsmBody struct {
	io.Reader
	io.Closer
}

// lsmErrReader fails every read with err, replaying a read error the
// recorder met.
type lsmErrReader struct{ err error }

// Read returns the recorded error.
func (r lsmErrReader) Read([]byte) (int, error) { return 0, r.err }

// TestLiveSmokeGuardSettings proves the gates: every case but a test key with
// the run enabled outside CI skips or refuses, and nothing the gates return
// quotes the key.
func TestLiveSmokeGuardSettings(t *testing.T) {
	const marker = "lsmguardmarker7c4e" // gitleaks:allow (synthetic test marker)
	const (
		skip   = "skip"
		refuse = "refuse"
		accept = "accept"
	)
	enabled := func(key string) map[string]string {
		return map[string]string{"EMAILABLE_LIVE": "1", "EMAILABLE_LIVE_KEY": key}
	}
	tests := []struct {
		name string
		env  map[string]string
		want string
		err  error // the refusal's cause
	}{
		{name: "not enabled", env: map[string]string{"EMAILABLE_LIVE_KEY": "test_" + marker}, want: skip},
		{name: "enabled with true", env: map[string]string{"EMAILABLE_LIVE": "true", "EMAILABLE_LIVE_KEY": "test_" + marker}, want: skip},
		{name: "CI present but empty", env: map[string]string{"CI": "", "EMAILABLE_LIVE": "1", "EMAILABLE_LIVE_KEY": "test_" + marker}, want: skip},
		{name: "CI set", env: map[string]string{"CI": "true", "EMAILABLE_LIVE": "1", "EMAILABLE_LIVE_KEY": "test_" + marker}, want: skip},
		{name: "no key", env: map[string]string{"EMAILABLE_LIVE": "1"}, want: skip},
		{name: "empty key", env: enabled(""), want: skip},
		{name: "only the quickstart's key", env: map[string]string{"EMAILABLE_LIVE": "1", "EMAILABLE_API_KEY": "test_" + marker}, want: skip},
		{name: "live key", env: enabled("live_" + marker), want: refuse, err: errLsmNotTestKey},
		{name: "upper-case prefix", env: enabled("TEST_" + marker), want: refuse, err: errLsmNotTestKey},
		{name: "prefix without underscore", env: enabled("test" + marker), want: refuse, err: errLsmNotTestKey},
		{name: "leading space", env: enabled(" test_" + marker), want: refuse, err: ErrInvalidConfig},
		{name: "test key", env: enabled("test_" + marker), want: accept},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key, skipReason, err := lsmReadSettings(func(name string) (string, bool) {
				v, ok := tt.env[name]
				return v, ok
			})
			assert.NotContains(t, skipReason, marker)
			switch tt.want {
			case skip:
				assert.NotEmpty(t, skipReason)
				require.NoError(t, err)
				assert.Nil(t, key.raw)
			case refuse:
				assert.Empty(t, skipReason)
				require.ErrorIs(t, err, tt.err)
				assert.NotContains(t, err.Error(), marker, "the refusal never quotes the key")
				assert.Nil(t, key.raw)
			default:
				assert.Empty(t, skipReason)
				require.NoError(t, err)
				require.NotNil(t, key.raw)
				if key.raw() != "test_"+marker {
					t.Error("the accepted key is not the one set")
				}
				testutil.AssertShapesNeverPrint(t, &key, marker)
			}
		})
	}
}

// TestLiveSmokeGuardRecorder proves the recorder in memory: it refuses,
// unsent, a request without a test key and one past the budget, hands the
// body it shapes on unchanged, and logs no value.
func TestLiveSmokeGuardRecorder(t *testing.T) {
	const marker = "lsmrecmarker3b9d" // gitleaks:allow (synthetic test marker)
	body := `{"owner_email":"` + marker + `@example.com","available_credits":424242}`
	header := http.Header{
		"Content-Type": {`application/json; charset=utf-8; note="has space"`},
		"X-Lsm-Marker": {marker},
	}
	ct := &testutil.CountingTransport{
		Next: testutil.TransportWith(http.StatusOK, header, int64(len(body)), io.NopCloser(strings.NewReader(body))),
	}
	rec := &lsmRecorder{next: ct, budget: 1}
	client := func(key string) *Client {
		c, err := NewClient(key, WithTestBaseURL(internalOrigin), WithHTTPClient(&http.Client{Transport: rec}))
		require.NoError(t, err)
		return c
	}

	_, err := client("live_"+marker).AccountInfo(t.Context(), AccountInfoRequest{})
	require.ErrorIs(t, err, ErrTransport, "a key that is not a test key is refused")
	assert.Zero(t, ct.Attempts(), "nothing was sent")

	c := client("test_" + marker)
	info, err := c.AccountInfo(t.Context(), AccountInfoRequest{})
	require.NoError(t, err)
	if info.OwnerEmail != marker+"@example.com" || info.AvailableCredits != 424242 {
		t.Error("the recorder changed the body it shaped")
	}
	_, err = c.AccountInfo(t.Context(), AccountInfoRequest{})
	require.ErrorIs(t, err, ErrTransport, "a request past the budget is refused")
	assert.Equal(t, 1, ct.Attempts(), "only the request within the budget was sent")
	assert.Equal(t, 2, rec.refusals())
	assert.Equal(t, 1, rec.sentCount())

	log := strings.Join(rec.take(), "\n")
	for _, secret := range []string{marker, "424242", "example.com", "has space"} {
		assert.NotContains(t, log, secret)
	}
	for _, want := range []string{
		"GET /v1/account, HTTP/1.1 200", "content type application/json; charset=utf-8; note=<value>",
		"X-Lsm-Marker", "owner_email: string", "available_credits: number",
		"refused before sending: not a test key", "refused before sending: over the call budget",
	} {
		assert.Contains(t, log, want)
	}
	assert.Empty(t, rec.take(), "take forgets what it returned")

	failing := &lsmRecorder{next: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		testutil.Drain(r)
		return nil, errLsmRefused
	}), budget: 1}
	c, err = NewClient("test_"+marker, WithTestBaseURL(internalOrigin), WithHTTPClient(&http.Client{Transport: failing}))
	require.NoError(t, err)
	_, err = c.AccountInfo(t.Context(), AccountInfoRequest{})
	require.ErrorIs(t, err, ErrTransport)
	assert.Equal(t, []string{"  transport failure (error not printed)"}, failing.take())
}

// TestLiveSmokeGuardBodyShapes covers the shapes of the bodies and media
// types the recorder can meet.
func TestLiveSmokeGuardBodyShapes(t *testing.T) {
	assert.Equal(t, "empty", lsmBodyShape(nil, nil))
	assert.Equal(t, "3 bytes, not JSON", lsmBodyShape([]byte("abc"), nil))
	assert.Equal(t, "2 bytes, then a read error (not printed)", lsmBodyShape([]byte("{}"), errLsmRefused))
	assert.Equal(t, "longer than 65536 bytes; no shape taken", lsmBodyShape(make([]byte, lsmShapeBytes+1), nil))
	assert.Equal(t, "2 bytes of JSON:\n{}", lsmBodyShape([]byte("{}"), nil))

	assert.Equal(t, "<none>", lsmMediaType(nil))
	assert.Equal(t, "<unparsable> | text/plain", lsmMediaType([]string{"/", "text/plain"}))
	long := strings.Repeat("a", 33)
	assert.Equal(t, "application/json; boundary=<value>", lsmMediaType([]string{"application/json; boundary=" + long}))

	assert.Equal(t, "unset", lsmEnum("", false))
	assert.Equal(t, "deliverable", lsmEnum("deliverable", true))
	assert.Equal(t, "<undocumented>", lsmEnum("brand_new", false))
	assert.Equal(t, "rows absent, download file unset, progress absent, 0 reason-count keys, 0 total-count keys, "+
		"message unset", lsmBatchSummary(&Batch{}))
	assert.Equal(t, "rows 0, download file set, progress present, 1 reason-count keys, 2 total-count keys, "+
		"message set", lsmBatchSummary(&Batch{
		Emails: []Verification{}, DownloadFile: NewDownloadURL("https://files.example.com/x.csv"), Message: "m",
		ReasonCounts: map[Reason]int64{ReasonAcceptedEmail: 1}, TotalCounts: map[string]int64{"processed": 1, "total": 1},
	}))
	assert.Equal(t, "limit unset, remaining set, reset unset", lsmRateLimitShape(RateLimit{Remaining: new(int64(1))}))
}

// TestLiveSmokeGuardPlan ties the steps to the call budget and checks, in
// memory, that every request of the plan passes the SDK's own validation, so
// that a live run never stops on a local mistake.
func TestLiveSmokeGuardPlan(t *testing.T) {
	steps := slices.Concat(lsmAuthSteps(), lsmReadSteps(), []lsmStep{lsmCreateStep(new(string))},
		lsmGetSteps(lsmMissingBatchID), lsmErrorSteps())
	calls, gates := 0, 0
	for _, s := range steps {
		calls += s.calls
		if s.gate {
			gates++
		}
		assert.NotContains(t, s.label, "@", "a label never holds an address")
		assert.NotNil(t, s.call, s.label)
	}
	assert.Equal(t, lsmMaxCalls, calls, "the steps send at most lsmMaxCalls requests")
	assert.Equal(t, 2, gates, "only the two account calls stop the run")

	_, err := lsmCheckKey(lsmInvalidKey)
	require.NoError(t, err, "the made-up key passes the guard")
	assert.True(t, validBatchID(lsmMissingBatchID))
	require.NoError(t, checkProviderTimeout(lsmProviderTimeout, lsmTimeout))
	_, err = WaitForVerificationRequest{
		Verify: VerifyEmailRequest{Email: lsmSlow}, Interval: lsmWaitInterval, MaxAttempts: lsmWaitAttempts,
		MaxWait: lsmWaitMax,
	}.validate()
	require.NoError(t, err)
	for _, sim := range []Simulation{"", SimulateGenericError, SimulateInsufficientCreditsError, SimulatePaymentError} {
		_, err = CreateBatchRequest{Emails: lsmBatchEmails(), Simulate: sim}.validate(DefaultMaxBatchEmails)
		require.NoError(t, err, "create simulation %q", sim)
	}
	for _, sim := range []Simulation{"", SimulateImporting, SimulateVerifying, SimulatePaused, SimulateGenericError} {
		_, err = GetBatchRequest{BatchID: lsmMissingBatchID, Partial: true, Simulate: sim}.request()
		require.NoError(t, err, "get simulation %q", sim)
	}
}

// TestLiveSmokeGuardDryRun runs the whole plan in memory, against a fake that
// answers as the provider documents, inside a synctest bubble so that the
// spacing takes no time. Every expectation holds, every request of the budget
// is sent and none is refused, and nothing the run logs or reports holds a
// key, an address, a batch ID or a header value.
func TestLiveSmokeGuardDryRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const marker = "lsmdrymarker5e1a" // gitleaks:allow (synthetic test marker)
		key, err := lsmCheckKey("test_" + marker)
		require.NoError(t, err)
		fake := &testutil.CountingTransport{Next: lsmFakeProvider()}
		rec := &lsmRecorder{next: fake, budget: lsmMaxCalls}
		log := &lsmLog{TB: t}
		lsmRun(log, key, rec, WithTestBaseURL(internalOrigin))

		assert.Equal(t, lsmMaxCalls, fake.Attempts(), "every request of the budget is sent")
		assert.Zero(t, rec.refusals())
		text := log.text()
		for _, secret := range []string{
			marker, lsmInvalidKey, lsmDeliverable, lsmAcceptAll, lsmSlow, lsmBatchFirst, lsmBatchSecond,
			lsmFakeBatchID, lsmMissingBatchID, "owner@", lsmFakeHeaderValue,
		} {
			assert.NotContains(t, text, secret)
		}
		for _, want := range []string{
			"account, made-up test key: SDK outcome: emailable: account_info: unauthorized (HTTP 403)",
			"GET /v1/verify, HTTP/1.1 249", "POST /v1/batch, HTTP/1.1 200", "Ratelimit-Remaining",
			"verify, deliverable test address: SDK outcome: success; state deliverable, reason accepted_email",
			"accept_all true", "budget_exhausted (HTTP 249)", "attempts 2", "batch ID received (not printed)",
			"get batch, made-up ID: SDK outcome: emailable: get_batch: not_found (HTTP 404)",
			"payment_required (HTTP 402)", "requests sent: 17 of at most 17",
		} {
			assert.Contains(t, text, want)
		}
		assert.NotContains(t, text, "a batch ID came with the failure", "no CreateBatch failure carried an ID")
	})
}

// lsmFakeBatchID is the batch the fake provider creates.
const lsmFakeBatchID = "lsmfakebatch5c0d2e7a19b4"

// lsmFakeHeaderValue is the value of the fake provider's rate-limit header,
// which the run must never log.
const lsmFakeHeaderValue = "98765"

// lsmFakeProvider answers as the provider documents for test keys: HTTP 403
// for the made-up key, 249 for the slow address, 404 for a batch that does
// not exist, 402 for the payment simulations, 400 for generic_error, and
// otherwise the smallest successful answer, which echoes the request.
func lsmFakeProvider() testutil.RoundTripFunc {
	return func(r *http.Request) (*http.Response, error) {
		q := r.URL.Query()
		status, body := lsmFakeAnswer(testutil.Record(r), q)
		header := http.Header{"Content-Type": {"application/json; charset=utf-8"}, "Ratelimit-Remaining": {lsmFakeHeaderValue}}
		return testutil.TransportWith(status, header, int64(len(body)), io.NopCloser(strings.NewReader(body)))(r)
	}
}

// lsmFakeAnswer returns the fake provider's status and body for req, whose
// query is q.
func lsmFakeAnswer(req testutil.Request, q url.Values) (int, string) {
	switch {
	case req.Header.Get("Authorization") == "Bearer "+lsmInvalidKey:
		return http.StatusForbidden, `{"message":"Invalid API key"}`
	case req.Path == "/v1/account":
		return http.StatusOK, `{"owner_email":"owner@example.com","available_credits":250}`
	case req.Path == "/v1/verify" && q.Get("email") == lsmSlow:
		return StatusPending, `{"message":"Your request is taking longer than normal."}`
	case req.Path == "/v1/verify":
		return http.StatusOK, `{"email":` + testutil.JSONQuote(q.Get("email")) +
			`,"state":"deliverable","reason":"accepted_email","accept_all":true}`
	case req.Method == http.MethodPost:
		return lsmFakeCreate(req.Body)
	}
	return lsmFakeGet(q)
}

// lsmFakeCreate answers a POST /v1/batch with body.
func lsmFakeCreate(body []byte) (int, string) {
	switch {
	case bytes.Contains(body, []byte(`"simulate":"generic_error"`)):
		return http.StatusBadRequest, `{"message":"Simulated error."}`
	case bytes.Contains(body, []byte(`"simulate"`)):
		return http.StatusPaymentRequired, `{"message":"Simulated payment error."}`
	}
	return http.StatusOK, `{"message":"Batch successfully created.","id":"` + lsmFakeBatchID + `"}`
}

// lsmFakeGet answers a GET /v1/batch with query q.
func lsmFakeGet(q url.Values) (int, string) {
	switch {
	case q.Get("id") != lsmFakeBatchID:
		return http.StatusNotFound, `{"message":"Batch not found."}`
	case q.Get("simulate") == string(SimulateGenericError):
		return http.StatusBadRequest, `{"message":"Simulated error."}`
	}
	return http.StatusOK, `{"id":"` + lsmFakeBatchID + `","message":"Batch verification completed.",` +
		`"emails":[{"email":"` + lsmBatchFirst + `","state":"deliverable"}],"total_counts":{"processed":2,"total":2}}`
}

// lsmLog is a testing.TB that keeps every line a run logs or reports as a
// failure, for TestLiveSmokeGuardDryRun to check, and passes each on.
type lsmLog struct {
	testing.TB

	mu    sync.Mutex
	lines []string
}

// Log keeps and logs args.
func (l *lsmLog) Log(args ...any) {
	l.keep(fmt.Sprint(args...))
	l.TB.Log(args...)
}

// Logf keeps and logs the formatted line.
func (l *lsmLog) Logf(format string, args ...any) {
	l.keep(fmt.Sprintf(format, args...))
	l.TB.Logf(format, args...)
}

// Error keeps args and reports them as a failure.
func (l *lsmLog) Error(args ...any) {
	l.keep(fmt.Sprint(args...))
	l.TB.Error(args...)
}

// Errorf keeps the formatted line and reports it as a failure.
func (l *lsmLog) Errorf(format string, args ...any) {
	l.keep(fmt.Sprintf(format, args...))
	l.TB.Errorf(format, args...)
}

// keep records one line.
func (l *lsmLog) keep(s string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, s)
}

// text returns every kept line.
func (l *lsmLog) text() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}
