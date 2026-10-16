// Command examples shows the go-emailable SDK end to end, offline: a local
// TLS server plays a synthetic Emailable, so the demo needs no API key, makes
// no network call and costs nothing.
//
//	go run ./examples
package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mrz1836/go-emailable"
)

// The demo's made-up values. A real application passes its own key, from
// wherever it keeps secrets.
const (
	demoKey     = "test_example_key"
	demoBatchID = "example_batch_0001"
	hookToken   = "example-hook-token" // the application's own secret in its callback URL
)

// The addresses the demo verifies. The fake answers the slow one with HTTP
// 249 on its first request.
const (
	janeEmail = "jane.doe@example.com"
	johnEmail = "john.roe@example.com"
	slowEmail = "sam.slow@lists.example.com"
)

// Synthetic answers: SDK fixtures written to the documented v1 shapes, with
// every value made up.
const (
	accountAnswer = `{"owner_email":"owner@example.com","available_credits":1000}`
	janeAnswer    = `{"email":"jane.doe@example.com","user":"jane.doe","domain":"example.com",` +
		`"state":"deliverable","reason":"accepted_email","score":96,"free":false,"role":false,` +
		`"tag":null,"duration":0.81}`
	slowAnswer = `{"email":"sam.slow@lists.example.com","state":"risky","reason":"low_deliverability",` +
		`"score":55,"accept_all":true,"duration":7.9}`
	pendingAnswer  = `{"message":"Synthetic: still verifying, send the same request again."}`
	createAnswer   = `{"message":"Batch successfully created.","id":"` + demoBatchID + `"}`
	workingFormat  = `{"message":"Your batch is being processed.","processed":1,"total":%d}`
	rejectedAnswer = `{"message":"Synthetic: the request could not be read."}`
	keyAnswer      = `{"message":"Synthetic: missing or invalid API key."}`
	notFoundAnswer = `{"message":"Synthetic: not found."}`
	completed      = "Batch verification completed."
)

// api is what the demo runs against: a handler that plays the provider and
// can report on what it saw, and the body of its completion callback.
type api interface {
	http.Handler
	verifyRequests(email string) int
	callbackBody() string
}

func main() {
	if err := run(context.Background(), os.Stdout, &fakeEmailable{}); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "examples:", err)
		os.Exit(1)
	}
}

// run serves fake locally, walks through every operation against it, and
// writes what came back to w.
func run(ctx context.Context, w io.Writer, fake api) error {
	srv := httptest.NewTLSServer(fake)
	defer srv.Close()

	// A real application passes its own key, from wherever it keeps secrets,
	// and leaves out the two test options.
	client, err := emailable.NewClient(demoKey,
		emailable.WithTimeout(20*time.Second),
		emailable.WithTestBaseURL(srv.URL),
		emailable.WithHTTPClient(srv.Client()),
	)
	if err != nil {
		return describe(w, err)
	}
	if err = showAccount(ctx, w, client); err != nil {
		return describe(w, err)
	}
	if err = verifyOne(ctx, w, client); err != nil {
		return describe(w, err)
	}
	if err = verifySlow(ctx, w, client, fake); err != nil {
		return describe(w, err)
	}
	batchID, err := createBatch(ctx, w, client)
	if err != nil {
		return describe(w, err)
	}
	if err = readBatch(ctx, w, client, batchID); err != nil {
		return describe(w, err)
	}
	if err = receiveCallback(ctx, w, client, fake.callbackBody()); err != nil {
		return describe(w, err)
	}
	return nil
}

// showAccount reads the account's credit balance.
func showAccount(ctx context.Context, w io.Writer, client *emailable.Client) error {
	_, _ = fmt.Fprintln(w, "Account (free)")
	info, err := client.AccountInfo(ctx, emailable.AccountInfoRequest{})
	if err != nil {
		return err
	}
	// OwnerEmail is personal data: the demo does not print it.
	_, _ = fmt.Fprintf(w, "  available credits: %d (a balance, not a reservation)\n", info.AvailableCredits)
	return nil
}

// verifyOne verifies one address with VerifyEmail.
func verifyOne(ctx context.Context, w io.Writer, client *emailable.Client) error {
	_, _ = fmt.Fprintln(w, "\nSingle verification (1 credit with a live key, none with a test key)")
	v, err := client.VerifyEmail(ctx, emailable.VerifyEmailRequest{Email: janeEmail, ProviderTimeout: 8 * time.Second})
	if err != nil {
		return err // ErrPending (HTTP 249) is not a result; never resend blindly
	}
	_, _ = fmt.Fprintf(w, "  %s: %s\n", v.Email, verdict(v.State, v.Reason))
	_, _ = fmt.Fprintf(w, "  score: %s, free provider: %s, role: %s, tag: %s\n",
		number(v.Score), flag(v.Free), flag(v.Role), text(v.Tag))
	return nil
}

// verifySlow verifies an address the provider is slow to answer: it calls
// WaitForVerification instead of VerifyEmail, which repeats the identical
// request after each HTTP 249, within a small budget.
func verifySlow(ctx context.Context, w io.Writer, client *emailable.Client, fake api) error {
	_, _ = fmt.Fprintln(w, "\nA slow address: HTTP 249, then the identical request again")
	v, err := client.WaitForVerification(ctx, emailable.WaitForVerificationRequest{
		Verify:      emailable.VerifyEmailRequest{Email: slowEmail},
		Interval:    time.Second, // the shortest pause allowed; 0 means 2 seconds
		MaxAttempts: 3,
		MaxWait:     30 * time.Second,
	})
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(w, "  %s: %s after %d requests\n", v.Email, verdict(v.State, v.Reason),
		fake.verifyRequests(slowEmail))
	return nil
}

// createBatch submits a list and returns the new batch's ID, which a real
// application persists before anything else.
func createBatch(ctx context.Context, w io.Writer, client *emailable.Client) (string, error) {
	_, _ = fmt.Fprintln(w, "\nBatch: create, persist the ID, read snapshots")
	created, err := client.CreateBatch(ctx, emailable.CreateBatchRequest{
		Emails:      []string{janeEmail, johnEmail, janeEmail},
		CallbackURL: "https://hooks.example.com/emailable?token=" + hookToken,
		ResponseFields: []emailable.ResponseField{
			emailable.ResponseFieldEmail, emailable.ResponseFieldState, emailable.ResponseFieldReason,
		},
	})
	if err != nil {
		if ce, ok := errors.AsType[*emailable.CallError](err); ok && ce.BatchID != "" {
			// The batch exists although its answer was imperfect.
			_, _ = fmt.Fprintf(w, "  saved batch %s from an imperfect answer\n", ce.BatchID)
		}
		return "", err
	}
	_, _ = fmt.Fprintf(w, "  saved batch %s: accepted, not verified\n", created.BatchID)
	return created.BatchID, nil
}

// readBatch reads two snapshots of the batch and matches the rows of the
// second to the application's addresses.
func readBatch(ctx context.Context, w io.Writer, client *emailable.Client, batchID string) error {
	b, err := client.GetBatch(ctx, emailable.GetBatchRequest{BatchID: batchID})
	if err != nil {
		return err
	}
	if processed, total, ok := b.Progress(); ok {
		_, _ = fmt.Fprintf(w, "  snapshot 1: processed %d of %d: progress, not completion\n", processed, total)
	}
	// One snapshot per call: the SDK never polls. A real application reads
	// again later, on its own schedule or after the callback.
	if b, err = client.GetBatch(ctx, emailable.GetBatchRequest{BatchID: batchID}); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(w, "  snapshot 2: %s\n", b.Message)
	writeRows(w, b, []string{janeEmail, johnEmail})
	return nil
}

// writeRows writes the rows of b for each address in mine, matched by Email,
// never by position: an address may have several rows, or none yet.
func writeRows(w io.Writer, b *emailable.Batch, mine []string) {
	rows := map[string][]emailable.Verification{}
	for _, row := range b.Emails {
		rows[row.Email] = append(rows[row.Email], row)
	}
	for _, email := range mine {
		if len(rows[email]) == 0 {
			_, _ = fmt.Fprintf(w, "    %s: no row yet\n", email)
		}
		for _, row := range rows[email] {
			_, _ = fmt.Fprintf(w, "    %s: %s\n", email, verdict(row.State, row.Reason))
		}
	}
}

// receiveCallback plays the provider's completion callback, a forged and a
// duplicate delivery included, against the application's handler, then
// re-reads each batch the callbacks named.
func receiveCallback(ctx context.Context, w io.Writer, client *emailable.Client, body string) error {
	_, _ = fmt.Fprintln(w, "\nCallback: check your own token, decode, re-read")
	var hints []string // stands in for the application's job queue
	h := callbackHandler(hookToken, func(batchID string) { hints = append(hints, batchID) })
	for _, d := range []struct{ name, token string }{
		{"forged", "guess"}, {"first", hookToken}, {"duplicate", hookToken},
	} {
		code := deliver(ctx, h, "/hooks/emailable?token="+d.token, body)
		_, _ = fmt.Fprintf(w, "  %s delivery: HTTP %d\n", d.name, code)
	}
	for _, batchID := range slices.Compact(hints) {
		b, err := client.GetBatch(ctx, emailable.GetBatchRequest{BatchID: batchID})
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(w, "  re-read %s: %d rows\n", batchID, len(b.Emails))
	}
	return nil
}

// callbackHandler is the application's endpoint for the provider's batch
// callback. It hands each batch ID to enqueue, for a worker that re-reads
// the batch with GetBatch.
func callbackHandler(token string, enqueue func(batchID string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Decoding is not authentication: check your own token first. An
		// empty token would match a request that carries none.
		got := r.URL.Query().Get("token")
		if token == "" || subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		b, err := emailable.DecodeBatchCallback(r.Body, emailable.DefaultMaxResponseBytes)
		if err != nil {
			// Any answer but 200 makes the provider try again later.
			http.Error(w, "unreadable callback", http.StatusBadRequest)
			return
		}
		// A hint: it may be a duplicate or late. Re-read before acting.
		enqueue(b.ID)
		w.WriteHeader(http.StatusOK)
	}
}

// deliver plays one provider POST of body to target on the application's
// handler h, in memory, and returns the status h answered.
func deliver(ctx context.Context, h http.Handler, target, body string) int {
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

// describe writes what a failed call means and returns err. A paid call that
// was sent may already have been processed and billed, so none of these
// branches resends it.
func describe(w io.Writer, err error) error {
	var what string
	switch {
	case errors.Is(err, emailable.ErrInvalidConfig), errors.Is(err, emailable.ErrInvalidRequest):
		what = "fix the request; nothing was sent"
	case errors.Is(err, emailable.ErrOutcomeUnknown):
		what = "the outcome is unknown: it may already be billed, or a batch created; reconcile, never resend blindly"
	case errors.Is(err, emailable.ErrPending), errors.Is(err, emailable.ErrBudgetExhausted):
		what = "still verifying; try later, or verify the address in a batch"
	case errors.Is(err, emailable.ErrTimeout): // before context.DeadlineExceeded, which it also matches
		what = "the SDK's timeout expired"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		what = "stopped by the caller"
	case errors.Is(err, emailable.ErrUnauthorized):
		what = "check the API key"
	case errors.Is(err, emailable.ErrPaymentRequired):
		what = "out of credits, or auto-refill needs a card charge"
	case errors.Is(err, emailable.ErrRateLimited), errors.Is(err, emailable.ErrUnavailable):
		what = "slow down or try later; the SDK never retries"
	case errors.Is(err, emailable.ErrNotFound):
		what = "no such batch"
	default:
		what = "the call failed; read the error before acting again"
	}
	_, _ = fmt.Fprintln(w, what)
	return err
}

// verdict prints a state and its reason, or the state alone without one.
func verdict(state emailable.State, reason emailable.Reason) string {
	if reason == "" {
		return string(state)
	}
	return string(state) + " (" + string(reason) + ")"
}

// flag prints an optional boolean, or "not sent" for nil.
func flag(b *bool) string {
	if b == nil {
		return "not sent"
	}
	return strconv.FormatBool(*b)
}

// number prints an optional integer, or "not sent" for nil.
func number(n *int) string {
	if n == nil {
		return "not sent"
	}
	return strconv.Itoa(*n)
}

// text prints optional text quoted, or "not sent" for nil.
func text(s *string) string {
	if s == nil {
		return "not sent"
	}
	return strconv.Quote(*s)
}

// row is one row of a batch, as the fake writes it.
type row struct {
	Email  string `json:"email"`
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
}

// batchAnswer is a completed batch, as the fake writes it.
type batchAnswer struct {
	ID          string           `json:"id"`
	Message     string           `json:"message"`
	Emails      []row            `json:"emails"`
	TotalCounts map[string]int64 `json:"total_counts"`
}

// fakeEmailable plays the Emailable API v1 with synthetic answers. The slow
// address is pending on its first request; a batch is processing on its first
// read and complete after.
type fakeEmailable struct {
	mu       sync.Mutex
	verifies map[string]int // verify requests, by address
	batch    []string       // the addresses of the batch, once created
	reads    int            // reads of the batch since it was created
}

// ServeHTTP answers one call.
func (f *fakeEmailable) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	status, body := http.StatusUnauthorized, keyAnswer
	if r.Header.Get("Authorization") == "Bearer "+demoKey {
		status, body = f.answer(r)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

// answer returns the status and body for r.
func (f *fakeEmailable) answer(r *http.Request) (int, string) {
	switch r.Method + " " + r.URL.Path {
	case "GET /v1/account":
		return http.StatusOK, accountAnswer
	case "GET /v1/verify":
		return f.verify(r.URL.Query().Get("email"))
	case "POST /v1/batch":
		var req struct {
			Emails []string `json:"emails"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Emails) == 0 {
			return http.StatusBadRequest, rejectedAnswer
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.batch, f.reads = req.Emails, 0
		return http.StatusOK, createAnswer
	case "GET /v1/batch":
		return f.read(r.URL.Query().Get("id"))
	}
	return http.StatusNotFound, notFoundAnswer
}

// verify answers a verification of email.
func (f *fakeEmailable) verify(email string) (int, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.verifies == nil {
		f.verifies = map[string]int{}
	}
	f.verifies[email]++
	switch email {
	case janeEmail:
		return http.StatusOK, janeAnswer
	case slowEmail:
		if f.verifies[email] == 1 {
			return emailable.StatusPending, pendingAnswer
		}
		return http.StatusOK, slowAnswer
	}
	return http.StatusOK, mustJSON(row{Email: email, State: "unknown", Reason: "no_connect"})
}

// read answers a read of the batch batchID.
func (f *fakeEmailable) read(batchID string) (int, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if batchID != demoBatchID || f.batch == nil {
		return http.StatusNotFound, notFoundAnswer
	}
	if f.reads++; f.reads == 1 {
		return http.StatusOK, fmt.Sprintf(workingFormat, len(f.batch))
	}
	return http.StatusOK, f.completedLocked()
}

// completedLocked returns the completed batch: one row per address, in
// order, with repeats marked duplicate. f.mu must be held.
func (f *fakeEmailable) completedLocked() string {
	verdicts := map[string]row{
		janeEmail: {State: "deliverable", Reason: "accepted_email"},
		johnEmail: {State: "undeliverable", Reason: "rejected_email"},
	}
	b := batchAnswer{ID: demoBatchID, Message: completed, TotalCounts: map[string]int64{}}
	seen := map[string]bool{}
	for _, email := range f.batch {
		r, ok := verdicts[email]
		switch {
		case seen[email]:
			r = row{State: "duplicate"}
		case !ok:
			r = row{State: "unknown", Reason: "no_connect"}
		}
		seen[email] = true
		r.Email = email
		b.Emails = append(b.Emails, r)
		b.TotalCounts[r.State]++
	}
	b.TotalCounts["processed"] = int64(len(f.batch))
	b.TotalCounts["total"] = int64(len(f.batch))
	return mustJSON(b)
}

// verifyRequests returns the number of verify requests the fake saw for
// email.
func (f *fakeEmailable) verifyRequests(email string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.verifies[email]
}

// callbackBody returns the body of the completion callback the provider
// would POST: the completed batch.
func (f *fakeEmailable) callbackBody() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.completedLocked()
}

// mustJSON encodes v, which is one of the fake's own types and always
// encodes.
func mustJSON(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		return rejectedAnswer
	}
	return string(data)
}
