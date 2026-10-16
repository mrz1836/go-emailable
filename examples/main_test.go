package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-emailable"
)

// demoOutput is everything the demo prints when every call succeeds.
const demoOutput = `Account (free)
  available credits: 1000 (a balance, not a reservation)

Single verification (1 credit with a live key, none with a test key)
  jane.doe@example.com: deliverable (accepted_email)
  score: 96, free provider: false, role: false, tag: not sent

A slow address: HTTP 249, then the identical request again
  sam.slow@lists.example.com: risky (low_deliverability) after 2 requests

Batch: create, persist the ID, read snapshots
  saved batch example_batch_0001: accepted, not verified
  snapshot 1: processed 1 of 3: progress, not completion
  snapshot 2: Batch verification completed.
    jane.doe@example.com: deliverable (accepted_email)
    jane.doe@example.com: duplicate
    john.roe@example.com: undeliverable (rejected_email)

Callback: check your own token, decode, re-read
  forged delivery: HTTP 403
  first delivery: HTTP 200
  duplicate delivery: HTTP 200
  re-read example_batch_0001: 3 rows
`

// testClient returns a client for a local TLS server running h.
func testClient(t *testing.T, h http.HandlerFunc) *emailable.Client {
	t.Helper()
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	client, err := emailable.NewClient(demoKey,
		emailable.WithTestBaseURL(srv.URL), emailable.WithHTTPClient(srv.Client()))
	require.NoError(t, err)
	return client
}

// answer returns a handler that answers every call with status and a JSON
// body.
func answer(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

// failingAPI plays the fake, except that it answers call number n (from 1)
// to route, such as "GET /v1/verify", with status instead.
type failingAPI struct {
	fakeEmailable

	route  string
	n      int
	status int

	callsMu sync.Mutex
	calls   int
}

// ServeHTTP answers the failing call itself and passes every other one on.
func (f *failingAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method+" "+r.URL.Path == f.route {
		f.callsMu.Lock()
		f.calls++
		fail := f.calls == f.n
		f.callsMu.Unlock()
		if fail {
			answer(f.status, `{"message":"Synthetic failure."}`)(w, r)
			return
		}
	}
	f.fakeEmailable.ServeHTTP(w, r)
}

// serve answers one in-memory request to the fake f.
func serve(t *testing.T, f http.Handler, method, target, auth, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), method, target, strings.NewReader(body))
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()
	f.ServeHTTP(rec, req)
	return rec
}

func TestRunPrintsTheDemo(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, run(t.Context(), &out, &fakeEmailable{}))
	assert.Equal(t, demoOutput, out.String())
}

func TestRunDescribesACanceledCall(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var out bytes.Buffer
	err := run(ctx, &out, &fakeEmailable{})
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, "Account (free)\nstopped by the caller\n", out.String())
}

// TestRunDescribesEachFailedStep fails each step in turn and checks that run
// stops there, describes the failure, and returns it.
func TestRunDescribesEachFailedStep(t *testing.T) {
	tests := []struct {
		name   string
		route  string
		n      int
		status int
		want   error
		last   string // the line before describe's
		says   string // describe's line
	}{
		{
			"account", "GET /v1/account", 1, http.StatusUnauthorized, emailable.ErrUnauthorized,
			"Account (free)", "check the API key",
		},
		{
			"verify", "GET /v1/verify", 1, http.StatusPaymentRequired, emailable.ErrPaymentRequired,
			"Single verification (1 credit with a live key, none with a test key)",
			"out of credits, or auto-refill needs a card charge",
		},
		{
			"slow verify", "GET /v1/verify", 2, http.StatusServiceUnavailable, emailable.ErrUnavailable,
			"A slow address: HTTP 249, then the identical request again",
			"the outcome is unknown: it may already be billed, or a batch created; reconcile, never resend blindly",
		},
		{
			"create", "POST /v1/batch", 1, http.StatusTooManyRequests, emailable.ErrRateLimited,
			"Batch: create, persist the ID, read snapshots", "slow down or try later; the SDK never retries",
		},
		{
			"first read", "GET /v1/batch", 1, http.StatusNotFound, emailable.ErrNotFound,
			"  saved batch example_batch_0001: accepted, not verified", "no such batch",
		},
		{
			"second read", "GET /v1/batch", 2, http.StatusInternalServerError, emailable.ErrUpstream,
			"  snapshot 1: processed 1 of 3: progress, not completion",
			"the call failed; read the error before acting again",
		},
		{
			"re-read", "GET /v1/batch", 3, http.StatusServiceUnavailable, emailable.ErrUnavailable,
			"  duplicate delivery: HTTP 200", "slow down or try later; the SDK never retries",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &failingAPI{route: tt.route, n: tt.n, status: tt.status}
			// The slow address is answered at once, so no case pauses.
			fake.verifies = map[string]int{slowEmail: 1}
			var out bytes.Buffer
			err := run(t.Context(), &out, fake)
			require.ErrorIs(t, err, tt.want)
			assert.True(t, strings.HasSuffix(out.String(), tt.last+"\n"+tt.says+"\n"),
				"run stops at the failed step and describes it:\n%s", out.String())
		})
	}
}

func TestCreateBatchKeepsTheIDOfAnImperfectAnswer(t *testing.T) {
	client := testClient(t, answer(http.StatusOK, createAnswer+" trailing"))
	var out bytes.Buffer
	_, err := createBatch(t.Context(), &out, client)
	require.ErrorIs(t, err, emailable.ErrMalformedResponse)
	require.NotErrorIs(t, err, emailable.ErrOutcomeUnknown, "the batch exists")
	assert.Contains(t, out.String(), "  saved batch example_batch_0001 from an imperfect answer\n")
}

func TestWriteRowsNamesAddressesWithoutARow(t *testing.T) {
	var out bytes.Buffer
	writeRows(&out, &emailable.Batch{}, []string{janeEmail})
	assert.Equal(t, "    jane.doe@example.com: no row yet\n", out.String())
}

func TestCallbackHandlerRefusesEmptyToken(t *testing.T) {
	h := callbackHandler("", func(string) { assert.Fail(t, "a request without a token must never pass") })
	body := (&fakeEmailable{batch: []string{janeEmail}}).callbackBody()
	assert.Equal(t, http.StatusForbidden, deliver(t.Context(), h, "/hooks/emailable", body))
	assert.Equal(t, http.StatusForbidden, deliver(t.Context(), h, "/hooks/emailable?token=", body))
}

func TestCallbackHandler(t *testing.T) {
	var hints []string
	h := callbackHandler(hookToken, func(batchID string) { hints = append(hints, batchID) })
	body := (&fakeEmailable{batch: []string{janeEmail}}).callbackBody()

	assert.Equal(t, http.StatusForbidden, deliver(t.Context(), h, "/hooks/emailable", body), "no token")
	assert.Equal(t, http.StatusForbidden, deliver(t.Context(), h, "/hooks/emailable?token=guess", body))
	assert.Equal(t, http.StatusBadRequest, deliver(t.Context(), h, "/hooks/emailable?token="+hookToken, "{}"))
	assert.Empty(t, hints, "nothing is enqueued without a valid token and body")

	assert.Equal(t, http.StatusOK, deliver(t.Context(), h, "/hooks/emailable?token="+hookToken, body))
	assert.Equal(t, []string{demoBatchID}, hints)
}

func TestDescribe(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			"invalid config", &emailable.ValidationError{Err: emailable.ErrInvalidConfig, Field: "apiKey"},
			"fix the request; nothing was sent",
		},
		{
			"invalid request", &emailable.ValidationError{Err: emailable.ErrInvalidRequest, Field: "Email"},
			"fix the request; nothing was sent",
		},
		{
			"outcome unknown", &emailable.CallError{Kind: emailable.FailureTimeout, OutcomeUnknown: true},
			"the outcome is unknown: it may already be billed, or a batch created; reconcile, never resend blindly",
		},
		{
			"pending", &emailable.CallError{Kind: emailable.FailurePending},
			"still verifying; try later, or verify the address in a batch",
		},
		{
			"budget exhausted", &emailable.CallError{Kind: emailable.FailureBudgetExhausted},
			"still verifying; try later, or verify the address in a batch",
		},
		{"sdk timeout", &emailable.CallError{Kind: emailable.FailureTimeout}, "the SDK's timeout expired"},
		{"caller deadline", &emailable.CallError{Kind: emailable.FailureDeadlineExceeded}, "stopped by the caller"},
		{"canceled", &emailable.CallError{Kind: emailable.FailureCanceled}, "stopped by the caller"},
		{"unauthorized", &emailable.CallError{Kind: emailable.FailureUnauthorized}, "check the API key"},
		{
			"payment required", &emailable.CallError{Kind: emailable.FailurePaymentRequired},
			"out of credits, or auto-refill needs a card charge",
		},
		{
			"rate limited", &emailable.CallError{Kind: emailable.FailureRateLimited},
			"slow down or try later; the SDK never retries",
		},
		{
			"unavailable", &emailable.CallError{Kind: emailable.FailureUnavailable},
			"slow down or try later; the SDK never retries",
		},
		{"not found", &emailable.CallError{Kind: emailable.FailureNotFound}, "no such batch"},
		{
			"rejected", &emailable.CallError{Kind: emailable.FailureRejected},
			"the call failed; read the error before acting again",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			got := describe(&out, tt.err)
			assert.Same(t, tt.err, got, "describe returns the error unchanged")
			assert.Equal(t, tt.want+"\n", out.String())
		})
	}
}

func TestFakeEmailableRefusesWhatItDoesNotPlay(t *testing.T) {
	const auth = "Bearer " + demoKey
	tests := []struct {
		name   string
		method string
		target string
		auth   string
		body   string
		status int
	}{
		{"no key", http.MethodGet, "/v1/account", "", "", http.StatusUnauthorized},
		{"another key", http.MethodGet, "/v1/account", "Bearer test_another_key", "", http.StatusUnauthorized},
		{"unknown path", http.MethodGet, "/v1/widget", auth, "", http.StatusNotFound},
		{"batch body not JSON", http.MethodPost, "/v1/batch", auth, "emails=x", http.StatusBadRequest},
		{"batch without emails", http.MethodPost, "/v1/batch", auth, `{"emails":[]}`, http.StatusBadRequest},
		{"batch never created", http.MethodGet, "/v1/batch?id=" + demoBatchID, auth, "", http.StatusNotFound},
		{"another batch", http.MethodGet, "/v1/batch?id=example_batch_0404", auth, "", http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := serve(t, &fakeEmailable{}, tt.method, tt.target, tt.auth, tt.body)
			assert.Equal(t, tt.status, rec.Code)
		})
	}
}

func TestFakeEmailableAnswersAnyAddress(t *testing.T) {
	rec := serve(t, &fakeEmailable{}, http.MethodGet, "/v1/verify?email=casey.example%40mail.example.com",
		"Bearer "+demoKey, "")
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"email":"casey.example@mail.example.com","state":"unknown","reason":"no_connect"}`, rec.Body.String())
}

func TestFakeEmailableCompletesAnyBatch(t *testing.T) {
	f := &fakeEmailable{batch: []string{"casey.example@mail.example.com", johnEmail, johnEmail}}
	assert.JSONEq(t, `{"id":"example_batch_0001","message":"Batch verification completed.","emails":[
		{"email":"casey.example@mail.example.com","state":"unknown","reason":"no_connect"},
		{"email":"john.roe@example.com","state":"undeliverable","reason":"rejected_email"},
		{"email":"john.roe@example.com","state":"duplicate"}],
		"total_counts":{"unknown":1,"undeliverable":1,"duplicate":1,"processed":3,"total":3}}`, f.callbackBody())
}

func TestFormatHelpers(t *testing.T) {
	assert.Equal(t, "duplicate", verdict(emailable.StateDuplicate, ""))
	assert.Equal(t, "risky (low_quality)", verdict(emailable.StateRisky, emailable.ReasonLowQuality))
	assert.Equal(t, "not sent", flag(nil))
	assert.Equal(t, "false", flag(new(false)))
	assert.Equal(t, "not sent", number(nil))
	assert.Equal(t, "0", number(new(0)))
	assert.Equal(t, "not sent", text(nil))
	assert.Equal(t, `""`, text(new("")))
	assert.JSONEq(t, rejectedAnswer, mustJSON(make(chan int)), "a value that cannot be encoded")
}
