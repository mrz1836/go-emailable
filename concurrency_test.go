package emailable_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-emailable"
	"github.com/mrz1836/go-emailable/internal/testutil"
)

// concurrencyGoroutines is the number of goroutines sharing one Client.
const concurrencyGoroutines = 64

// concurrencyRounds is how often each goroutine makes its set of calls.
const concurrencyRounds = 2

// concurrencyOwner is the owner AccountInfo answers with.
const concurrencyOwner = "cc.owner@example.com"

// concurrencyBatchPrefix starts every batch ID the handler issues.
const concurrencyBatchPrefix = "ccb-"

// concurrencyQuote returns s as a JSON string literal.
func concurrencyQuote(s string) string { return testutil.JSONQuote(s) }

// concurrencyHandler answers each operation from what the request itself
// carries, so that an answer delivered to the wrong caller shows up as a
// mismatch: verify echoes the address, its local part as user and its length
// as score; a batch is created with the ID "ccb-<local part of its first
// address>"; reading batch "ccb-X" answers one row for X@example.com and
// progress counts of len(X); the account answers a fixed owner and sets a
// cookie, which no jar may keep.
func concurrencyHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", mediaJSON)
	switch {
	case r.URL.Path == pathVerify:
		email := r.URL.Query().Get("email")
		user, _, _ := strings.Cut(email, "@")
		//nolint:gosec // G705: a test server that echoes the request, so that a misrouted answer shows
		_, _ = fmt.Fprintf(w, `{"email":%s,"state":"deliverable","reason":"accepted_email","user":%s,"score":%d}`,
			concurrencyQuote(email), concurrencyQuote(user), len(email))
	case r.URL.Path == pathBatch && r.Method == http.MethodPost:
		var body struct {
			Emails []string `json:"emails"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Emails) == 0 {
			http.Error(w, "bad batch", http.StatusBadRequest)
			return
		}
		user, _, _ := strings.Cut(body.Emails[0], "@")
		_, _ = fmt.Fprintf(w, `{"message":"Batch successfully created.","id":%s}`,
			concurrencyQuote(concurrencyBatchPrefix+user))
	case r.URL.Path == pathBatch:
		id := r.URL.Query().Get("id")
		user := strings.TrimPrefix(id, concurrencyBatchPrefix)
		//nolint:gosec // G705: a test server that echoes the request, so that a misrouted answer shows
		_, _ = fmt.Fprintf(w, `{"id":%s,"message":"Batch verification completed.",`+
			`"emails":[{"email":%s,"state":"deliverable"}],"total_counts":{"processed":%d,"total":%d}}`,
			concurrencyQuote(id), concurrencyQuote(user+"@example.com"), len(user), len(user))
	case r.URL.Path == pathAccount:
		http.SetCookie(w, &http.Cookie{
			Name: "cc", Value: "never-kept", Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode,
		})
		_, _ = fmt.Fprintf(w, `{"owner_email":%s,"available_credits":1000}`, concurrencyQuote(concurrencyOwner))
	default:
		http.NotFound(w, r)
	}
}

// concurrencyWorker is one goroutine's share of TestConcurrencySharedClient:
// an address of its own, and the requests it expects to have sent.
type concurrencyWorker struct {
	user  string
	email string
	sent  []string
}

// newConcurrencyWorker returns the worker of goroutine g.
func newConcurrencyWorker(g int) *concurrencyWorker {
	user := fmt.Sprintf("cc%02d", g)
	return &concurrencyWorker{user: user, email: user + "@example.com"}
}

// run makes every call concurrencyRounds times, from the one at index first
// on, so that concurrent workers interleave different operations, and checks
// String in between.
func (w *concurrencyWorker) run(ctx context.Context, t *testing.T, c *emailable.Client, first int, str string) {
	t.Helper()
	ops := []func(context.Context, *testing.T, *emailable.Client){w.verify, w.create, w.get, w.account}
	for range concurrencyRounds {
		for i := range ops {
			ops[(first+i)%len(ops)](ctx, t, c)
			assert.Equal(t, str, c.String(), "String is stable")
		}
	}
}

// expect records a request the worker makes, as "METHOD path?query body".
func (w *concurrencyWorker) expect(method, path, query, body string) {
	w.sent = append(w.sent, method+" "+path+"?"+query+" "+body)
}

func (w *concurrencyWorker) verify(ctx context.Context, t *testing.T, c *emailable.Client) {
	t.Helper()
	w.expect(http.MethodGet, pathVerify, url.Values{"email": {w.email}}.Encode(), "")
	v, err := c.VerifyEmail(ctx, emailable.VerifyEmailRequest{Email: w.email})
	if assert.NoError(t, err) {
		assert.Equal(t, w.email, v.Email, "each caller gets the answer to its own address")
		assert.Equal(t, new(w.user), v.User)
		assert.Equal(t, new(len(w.email)), v.Score)
	}
}

func (w *concurrencyWorker) create(ctx context.Context, t *testing.T, c *emailable.Client) {
	t.Helper()
	w.expect(http.MethodPost, pathBatch, "", `{"emails":["`+w.email+`","shared@example.com"]}`)
	res, err := c.CreateBatch(ctx, emailable.CreateBatchRequest{Emails: []string{w.email, "shared@example.com"}})
	if assert.NoError(t, err) {
		assert.Equal(t, concurrencyBatchPrefix+w.user, res.BatchID)
	}
}

func (w *concurrencyWorker) get(ctx context.Context, t *testing.T, c *emailable.Client) {
	t.Helper()
	id := concurrencyBatchPrefix + w.user
	w.expect(http.MethodGet, pathBatch, url.Values{"id": {id}}.Encode(), "")
	b, err := c.GetBatch(ctx, emailable.GetBatchRequest{BatchID: id})
	if assert.NoError(t, err) && assert.Len(t, b.Emails, 1) {
		assert.Equal(t, id, b.ID)
		assert.Equal(t, w.email, b.Emails[0].Email)
		processed, total, ok := b.Progress()
		assert.True(t, ok)
		assert.Equal(t, int64(len(w.user)), processed)
		assert.Equal(t, int64(len(w.user)), total)
	}
}

func (w *concurrencyWorker) account(ctx context.Context, t *testing.T, c *emailable.Client) {
	t.Helper()
	w.expect(http.MethodGet, pathAccount, "", "")
	info, err := c.AccountInfo(ctx, emailable.AccountInfoRequest{})
	if assert.NoError(t, err) {
		assert.Equal(t, &emailable.AccountInfo{OwnerEmail: concurrencyOwner, AvailableCredits: 1000}, info)
	}
}

// concurrencyTransportSettings is the configuration of an *http.Transport that
// a Client built on it must never change.
type concurrencyTransportSettings struct {
	tlsConfig       *tls.Config
	rootCAs         *x509.CertPool
	nextProtos      []string
	insecure        bool
	proxySet        bool
	dialSet         bool
	dialTLSSet      bool
	forceHTTP2      bool
	protocols       string
	keepAlivesOff   bool
	compressionOff  bool
	maxIdle         int
	maxIdlePerHost  int
	maxConnsPerHost int
	idleTimeout     time.Duration
	headerBytes     int64
	tlsNextProto    int
}

// concurrencyClientState is what a Client built on an injected *http.Client
// must never change in it.
type concurrencyClientState struct {
	transport *http.Transport
	redirect  uintptr
	jar       http.CookieJar
	timeout   time.Duration
	settings  concurrencyTransportSettings
}

// concurrencyStateOf captures hc's state. hc must use an *http.Transport.
func concurrencyStateOf(tb testing.TB, hc *http.Client) concurrencyClientState {
	tb.Helper()
	tr, ok := hc.Transport.(*http.Transport)
	require.True(tb, ok, "want an *http.Transport, got %T", hc.Transport)
	// net/http fills in its HTTP/2 defaults (TLSClientConfig.NextProtos and
	// TLSNextProto) once, on the transport's first use, whoever uses it.
	// Clone runs that same step first, so the state holds only what the SDK
	// could change.
	_ = tr.Clone()
	s := concurrencyTransportSettings{
		tlsConfig:       tr.TLSClientConfig,
		proxySet:        tr.Proxy != nil,
		dialSet:         tr.DialContext != nil,
		dialTLSSet:      tr.DialTLSContext != nil,
		forceHTTP2:      tr.ForceAttemptHTTP2,
		keepAlivesOff:   tr.DisableKeepAlives,
		compressionOff:  tr.DisableCompression,
		maxIdle:         tr.MaxIdleConns,
		maxIdlePerHost:  tr.MaxIdleConnsPerHost,
		maxConnsPerHost: tr.MaxConnsPerHost,
		idleTimeout:     tr.IdleConnTimeout,
		headerBytes:     tr.MaxResponseHeaderBytes,
		tlsNextProto:    len(tr.TLSNextProto),
	}
	if tr.Protocols != nil {
		s.protocols = tr.Protocols.String()
	}
	if cfg := tr.TLSClientConfig; cfg != nil {
		s.rootCAs = cfg.RootCAs
		s.nextProtos = slices.Clone(cfg.NextProtos)
		s.insecure = cfg.InsecureSkipVerify
	}
	var redirect uintptr
	if hc.CheckRedirect != nil {
		redirect = reflect.ValueOf(hc.CheckRedirect).Pointer()
	}
	return concurrencyClientState{
		transport: tr, redirect: redirect, jar: hc.Jar, timeout: hc.Timeout, settings: s,
	}
}

// concurrencyAssertSent asserts that srv received exactly the requests the
// workers expect, each once, over proto.
func concurrencyAssertSent(tb testing.TB, srv *testutil.Server, proto testutil.Protocol, workers []*concurrencyWorker) {
	tb.Helper()
	var want []string
	for _, w := range workers {
		want = append(want, w.sent...)
	}
	reqs := srv.Requests()
	got := make([]string, 0, len(reqs))
	for _, r := range reqs {
		u, err := url.Parse(r.URL)
		require.NoError(tb, err)
		got = append(got, r.Method+" "+r.Path+"?"+u.RawQuery+" "+string(r.Body))
		assert.Equal(tb, proto, testutil.Protocol(r.Proto))
		assertWireRequest(tb, r, r.Method, r.Path)
	}
	slices.Sort(want)
	slices.Sort(got)
	assert.Len(tb, got, len(workers)*4*concurrencyRounds, "one request per call")
	assert.Equal(tb, want, got, "each request carries exactly its caller's data, sent once")
}

// TestConcurrencySharedClient shares one Client across 64 goroutines, each
// making every operation with an address of its own, in a rotated order,
// against a loopback TLS server over HTTP/1.1 and HTTP/2 whose answers echo
// each request. Run it with -race. Every caller must get the answer to its
// own request, every request must carry exactly its caller's data, the
// injected *http.Client must be unchanged afterwards (its transport,
// redirect policy, cookie jar and timeout), the jar must keep no cookie, and
// the Client's String must never change.
func TestConcurrencySharedClient(t *testing.T) {
	for _, proto := range testutil.Protocols() {
		t.Run(proto.Name(), func(t *testing.T) {
			srv := testutil.NewServerProto(t, proto, concurrencyHandler)
			jar, err := cookiejar.New(nil)
			require.NoError(t, err)
			var redirects atomic.Int32
			hc := &http.Client{
				Transport: srv.Client().Transport,
				Jar:       jar,
				Timeout:   time.Minute,
				CheckRedirect: func(*http.Request, []*http.Request) error {
					redirects.Add(1)
					return http.ErrUseLastResponse
				},
			}
			before := concurrencyStateOf(t, hc)

			c := newClient(t, emailable.WithTestBaseURL(srv.URL()), emailable.WithHTTPClient(hc))
			str := c.String()
			workers := make([]*concurrencyWorker, concurrencyGoroutines)
			var wg sync.WaitGroup
			for g := range concurrencyGoroutines {
				w := newConcurrencyWorker(g)
				workers[g] = w
				wg.Go(func() { w.run(t.Context(), t, c, g, str) })
			}
			wg.Wait()

			concurrencyAssertSent(t, srv, proto, workers)
			after := concurrencyStateOf(t, hc)
			assert.Same(t, before.transport, hc.Transport, "the transport is wrapped in a copy, never replaced")
			assert.Same(t, before.settings.tlsConfig, after.settings.tlsConfig)
			assert.Same(t, before.settings.rootCAs, after.settings.rootCAs)
			assert.Equal(t, before, after, "the injected client and its transport are untouched")
			assert.Zero(t, redirects.Load(), "the injected redirect policy is never called")
			u, err := url.Parse(srv.URL())
			require.NoError(t, err)
			assert.Empty(t, jar.Cookies(u), "the SDK's copy has no cookie jar")
			assert.Equal(t, str, c.String())
		})
	}
}

// concurrencyRecorder is an in-memory transport that answers each path with
// its smallest successful answer and records every request.
type concurrencyRecorder struct {
	mu   sync.Mutex
	reqs []testutil.Request
}

// RoundTrip records r and answers it.
func (rec *concurrencyRecorder) RoundTrip(r *http.Request) (*http.Response, error) {
	got := testutil.Record(r)
	rec.mu.Lock()
	rec.reqs = append(rec.reqs, got)
	rec.mu.Unlock()
	body := accountOK
	switch {
	case got.Path == pathVerify:
		body = verifyOK
	case got.Path == pathBatch && got.Method == http.MethodPost:
		body = createOK
	case got.Path == pathBatch:
		body = batchOK
	}
	return testutil.Response(r, http.StatusOK, io.NopCloser(strings.NewReader(body)), int64(len(body))), nil
}

// byOperation returns the recorded requests as "METHOD URL body", grouped by
// method and path.
func (rec *concurrencyRecorder) byOperation() map[string][]string {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	out := map[string][]string{}
	for _, r := range rec.reqs {
		key := r.Method + " " + r.Path
		out[key] = append(out[key], r.Method+" "+r.URL+" "+string(r.Body))
	}
	return out
}

// concurrencySharedRequests are request values that every goroutine of
// TestConcurrencySharedRequests reads at once, with every slice and pointer
// set.
type concurrencySharedRequests struct {
	verify emailable.VerifyEmailRequest
	wait   emailable.WaitForVerificationRequest
	create emailable.CreateBatchRequest
	get    emailable.GetBatchRequest
}

// newConcurrencySharedRequests returns the shared requests.
func newConcurrencySharedRequests() *concurrencySharedRequests {
	verify := emailable.VerifyEmailRequest{
		Email: testEmail, SMTP: new(false), AcceptAll: new(true), ProviderTimeout: 7 * time.Second,
		CaptchaResponse: testCaptcha,
	}
	return &concurrencySharedRequests{
		verify: verify,
		wait:   emailable.WaitForVerificationRequest{Verify: verify, Interval: time.Second, MaxAttempts: 2, MaxWait: time.Minute},
		create: emailable.CreateBatchRequest{
			Emails:         []string{testEmail, "blake@mail.example.com", testEmail},
			CallbackURL:    testHook,
			ResponseFields: []emailable.ResponseField{emailable.ResponseFieldEmail, emailable.ResponseFieldState},
			Retries:        new(false),
			Simulate:       emailable.SimulateGenericError,
		},
		get: emailable.GetBatchRequest{BatchID: testBatchID, Partial: true, Simulate: emailable.SimulateVerifying},
	}
}

// call makes every call with the shared requests.
func (s *concurrencySharedRequests) call(ctx context.Context, t *testing.T, c *emailable.Client) {
	t.Helper()
	errs := []error{
		noValueWith(c.VerifyEmail(ctx, s.verify)),
		noValueWith(c.WaitForVerification(ctx, s.wait)),
		noValueWith(c.CreateBatch(ctx, s.create)),
		noValueWith(c.GetBatch(ctx, s.get)),
		noValueWith(c.AccountInfo(ctx, emailable.AccountInfoRequest{})),
	}
	for _, err := range errs {
		if !assert.NoError(t, err) {
			return
		}
	}
}

// TestConcurrencySharedRequests shares one value of every request type, with
// every slice and pointer set, across 64 goroutines that call at once. Run
// it with -race: the SDK only reads requests, so sharing them is safe. Every
// request of an operation is byte-identical, and the shared values are
// unchanged afterwards.
func TestConcurrencySharedRequests(t *testing.T) {
	shared := newConcurrencySharedRequests()
	want := newConcurrencySharedRequests()
	rec := &concurrencyRecorder{}
	c := newTransportClient(t, rec)
	var wg sync.WaitGroup
	for range concurrencyGoroutines {
		wg.Go(func() { shared.call(t.Context(), t, c) })
	}
	wg.Wait()

	assert.Equal(t, want, shared, "requests are never modified")
	ops := rec.byOperation()
	require.Len(t, ops, 4)
	for key, reqs := range ops {
		n := concurrencyGoroutines
		if key == http.MethodGet+" "+pathVerify {
			n = 2 * concurrencyGoroutines // VerifyEmail and WaitForVerification
		}
		require.Len(t, reqs, n, key)
		for _, r := range reqs[1:] {
			assert.Equal(t, reqs[0], r, "%s: every request is identical", key)
		}
	}
	assert.Contains(t, ops[http.MethodPost+" "+pathBatch][0],
		`{"emails":["`+testEmail+`","blake@mail.example.com","`+testEmail+`"],"url":"`+testHook+`",`+
			`"response_fields":"email,state","retries":false,"simulate":"generic_error"}`)
}

// concurrencyPendings is how many HTTP 249 answers the address of goroutine g
// gets before its answer: 0 to 3, or 4, which uses up a budget of 4
// attempts.
func concurrencyPendings(g int) int { return g % 5 }

// concurrencyWaitAttempts is the MaxAttempts of every wait.
const concurrencyWaitAttempts = 4

// concurrencyWaitResult is what one goroutine's wait returned.
type concurrencyWaitResult struct {
	v       *emailable.Verification
	err     error
	elapsed time.Duration
}

// concurrencyPendingTransport answers each address with HTTP 249 as many
// times as concurrencyPendings says for the goroutine whose index ends the
// local part ("ccw<g>@example.com"), then with its verification, and counts
// the requests per address.
type concurrencyPendingTransport struct {
	mu    sync.Mutex
	calls map[string]int
}

// RoundTrip answers r.
func (p *concurrencyPendingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	email := r.URL.Query().Get("email")
	g, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(email, "ccw"), "@example.com"))
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.calls[email]++
	n := p.calls[email]
	p.mu.Unlock()
	status, body := http.StatusOK, `{"email":`+concurrencyQuote(email)+`,"state":"deliverable"}`
	if n <= concurrencyPendings(g) {
		status, body = emailable.StatusPending, pending
	}
	return testutil.Response(r, status, io.NopCloser(strings.NewReader(body)), int64(len(body))), nil
}

// TestConcurrencyWaitForVerificationBubble runs 64 WaitForVerification calls
// at once on one Client inside one synctest bubble, over an in-memory
// transport that answers each address with its own number of HTTP 249
// answers. Every wait gets its own address after exactly its number of
// pauses on the bubble's clock, or uses up its budget, and no attempt is
// lost or repeated.
func TestConcurrencyWaitForVerificationBubble(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rt := &concurrencyPendingTransport{calls: map[string]int{}}
		c := newTransportClient(t, rt)
		results := make([]concurrencyWaitResult, concurrencyGoroutines)
		var wg sync.WaitGroup
		for g := range concurrencyGoroutines {
			wg.Go(func() {
				start := time.Now()
				v, err := c.WaitForVerification(t.Context(), emailable.WaitForVerificationRequest{
					Verify:   emailable.VerifyEmailRequest{Email: fmt.Sprintf("ccw%d@example.com", g)},
					Interval: time.Second, MaxAttempts: concurrencyWaitAttempts, MaxWait: time.Minute,
				})
				results[g] = concurrencyWaitResult{v: v, err: err, elapsed: time.Since(start)}
			})
		}
		wg.Wait()

		for g, res := range results {
			email := fmt.Sprintf("ccw%d@example.com", g)
			pendings := concurrencyPendings(g)
			if pendings < concurrencyWaitAttempts {
				require.NoError(t, res.err, email)
				assert.Equal(t, email, res.v.Email, "each wait gets its own address")
				assert.Equal(t, time.Duration(pendings)*time.Second, res.elapsed, "%s: one pause per 249", email)
				assert.Equal(t, pendings+1, rt.calls[email], email)
				continue
			}
			_ = assertCallError(t, res.err, wantCall{
				Kind: emailable.FailureBudgetExhausted, Op: emailable.OpVerifyEmail, Status: emailable.StatusPending,
				Attempts: concurrencyWaitAttempts,
			})
			assert.Equal(t, (concurrencyWaitAttempts-1)*time.Second, res.elapsed, email)
			assert.Equal(t, concurrencyWaitAttempts, rt.calls[email], email)
		}
	})
}
