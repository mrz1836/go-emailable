package emailable_test

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/mrz1836/go-emailable"
)

// Synthetic provider answers for the examples: SDK fixtures written to the
// documented v1 shapes, with every value made up.
const (
	exVerified = `{"email":"jane.doe@example.com","user":"jane.doe","domain":"example.com",` +
		`"state":"risky","reason":"low_deliverability","score":64,"accept_all":true,"disposable":false,` +
		`"free":false,"role":false,"tag":null,"did_you_mean":null,"mx_record":"mx.example.com","duration":0.94}`
	exDeliverable = `{"email":"jane.doe@example.com","state":"deliverable","reason":"accepted_email",` +
		`"score":96,"duration":1.2}`
	exPending  = `{"message":"Synthetic: still verifying, send the same request again."}`
	exCreated  = `{"message":"Batch successfully created.","id":"example_batch_0001"}`
	exTrailing = exCreated + "\n" + `{"note":"synthetic data after the answer"}`
	exWorking  = `{"message":"Your batch is being processed.","processed":1,"total":3}`
	exComplete = `{"id":"example_batch_0001","message":"Batch verification completed.","emails":[` +
		`{"email":"john.roe@example.com","state":"undeliverable","reason":"rejected_email"},` +
		`{"email":"jane.doe@example.com","state":"deliverable","reason":"accepted_email"},` +
		`{"email":"jane.doe@example.com","state":"duplicate"}],` +
		`"total_counts":{"deliverable":1,"undeliverable":1,"duplicate":1,"processed":3,"total":3},` +
		`"reason_counts":{"accepted_email":1,"rejected_email":1}}`
	exLarge = `{"id":"example_batch_0002","message":"Batch verification completed.",` +
		`"download_file":"https://downloads.example.com/batches/example_batch_0002?signature=synthetic",` +
		`"total_counts":{"deliverable":1100,"undeliverable":250,"processed":1350,"total":1350}}`
	exAccount   = `{"owner_email":"owner@example.com","available_credits":1000}`
	exThrottled = `{"message":"Synthetic: too many requests."}`
	exForbidden = `{"message":"Synthetic: the key test_example_key cannot read this account."}`
)

// exAnswer is one answer of the stand-in server.
type exAnswer struct {
	status int
	body   string
	header http.Header // extra response headers, if any
}

// exOK is an HTTP 200 answer with body.
func exOK(body string) exAnswer { return exAnswer{status: http.StatusOK, body: body} }

// exServer starts a local TLS server that stands in for Emailable, so the
// examples run offline, need no key and cost nothing. It answers each route,
// such as "GET /v1/verify", with the answers given for it in turn, repeating
// the last one, and any other route with HTTP 404.
func exServer(routes map[string][]exAnswer) *httptest.Server {
	var mu sync.Mutex
	calls := map[string]int{}
	return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route := r.Method + " " + r.URL.Path
		answers := routes[route]
		if len(answers) == 0 {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		a := answers[min(calls[route], len(answers)-1)]
		calls[route]++
		mu.Unlock()
		maps.Copy(w.Header(), a.header)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(a.status)
		_, _ = io.WriteString(w, a.body)
	}))
}

// ExampleNewClient builds a client once, to be shared: it is safe for
// concurrent use. NewClient makes no call and reads no environment variable
// or file: the application decides where the key comes from.
func ExampleNewClient() {
	apiKey := "test_example_key"       // from your application's secret store
	sharedHTTPClient := &http.Client{} // your own transport settings, if any

	client, err := emailable.NewClient(apiKey,
		emailable.WithTimeout(20*time.Second),
		emailable.WithHTTPClient(sharedHTTPClient), // copied; redirects refused; never mutated
	)
	if err != nil {
		fmt.Println("config:", err) // names the option, never the key
		return
	}
	fmt.Println(client) // settings only, never the key

	_, err = emailable.NewClient(apiKey, emailable.WithTimeout(time.Hour))
	fmt.Println(err)
	// Output:
	// emailable.Client{timeout: 20s, maxResponseBytes: 8388608, maxBatchEmails: 50000, testBaseURL: false, providerDiagnostics: false}
	// emailable: invalid configuration: Timeout: out_of_range
}

// ExampleClient_VerifyEmail verifies one address. With a live key every
// verification uses one credit; a test key uses none.
func ExampleClient_VerifyEmail() {
	srv := exServer(map[string][]exAnswer{"GET /v1/verify": {exOK(exVerified)}})
	defer srv.Close()

	// A real application passes its own key and leaves out the two test options.
	client, err := emailable.NewClient("test_example_key",
		emailable.WithTestBaseURL(srv.URL),
		emailable.WithHTTPClient(srv.Client()),
	)
	if err != nil {
		fmt.Println("config:", err)
		return
	}

	v, err := client.VerifyEmail(context.Background(), emailable.VerifyEmailRequest{
		Email:           "jane.doe@example.com",
		AcceptAll:       new(true),       // ask for the accept-all check
		ProviderTimeout: 8 * time.Second, // the provider's own timeout: at least 5s below WithTimeout
	})
	var ce *emailable.CallError
	switch {
	case err == nil:
		// A result, unknown included.
	case errors.Is(err, emailable.ErrPending):
		fmt.Println("still verifying: no result yet (WaitForVerification repeats it within a budget)")
		return
	case errors.Is(err, emailable.ErrTimeout): // before context.DeadlineExceeded, which it also matches
		fmt.Println("the SDK's timeout expired; it may be billed:", errors.Is(err, emailable.ErrOutcomeUnknown))
		return
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		fmt.Println("the caller's context ended; it may be billed:", errors.Is(err, emailable.ErrOutcomeUnknown))
		return
	case errors.As(err, &ce):
		fmt.Println("failed:", ce.Kind, ce.StatusCode, "outcome unknown:", ce.OutcomeUnknown)
		return
	default:
		fmt.Println("nothing sent:", err) // a *ValidationError
		return
	}
	fmt.Println("state:", v.State, "documented:", v.State.Known())
	fmt.Println("reason:", v.Reason)
	if v.Score != nil {
		fmt.Println("score:", *v.Score) // an observation: any threshold is your policy
	}
	if v.AcceptAll != nil {
		fmt.Println("accept-all domain:", *v.AcceptAll)
	}
	fmt.Println("tag sent:", v.Tag != nil) // nil: not sent; a pointer to "": an empty tag
	if v.DidYouMean != nil {
		fmt.Println("did you mean:", *v.DidYouMean) // a suggestion: never applied
	}
	// Output:
	// state: risky documented: true
	// reason: low_deliverability
	// score: 64
	// accept-all domain: true
	// tag sent: false
}

// ExampleClient_VerifyEmail_pending meets HTTP 249: the provider is still
// verifying the address. That is not a result, and VerifyEmail never repeats
// the request on its own.
func ExampleClient_VerifyEmail_pending() {
	srv := exServer(map[string][]exAnswer{"GET /v1/verify": {{status: emailable.StatusPending, body: exPending}}})
	defer srv.Close()

	// A real application passes its own key and leaves out the two test options.
	client, err := emailable.NewClient("test_example_key",
		emailable.WithTestBaseURL(srv.URL),
		emailable.WithHTTPClient(srv.Client()),
	)
	if err != nil {
		fmt.Println("config:", err)
		return
	}

	v, err := client.VerifyEmail(context.Background(), emailable.VerifyEmailRequest{Email: "jane.doe@example.com"})
	fmt.Println("result:", v) // never a zero-valued success
	fmt.Println("pending:", errors.Is(err, emailable.ErrPending))
	fmt.Println("outcome unknown:", errors.Is(err, emailable.ErrOutcomeUnknown)) // the provider answered
	fmt.Println("log line:", err)
	// Output:
	// result: <nil>
	// pending: true
	// outcome unknown: false
	// log line: emailable: verify_email: pending (HTTP 249)
}

// ExampleClient_WaitForVerification verifies one address and, while the
// provider answers HTTP 249, repeats the identical request within a small
// budget. Call it instead of VerifyEmail, never after one.
func ExampleClient_WaitForVerification() {
	srv := exServer(map[string][]exAnswer{"GET /v1/verify": {
		{status: emailable.StatusPending, body: exPending},
		exOK(exDeliverable),
	}})
	defer srv.Close()

	// A real application passes its own key and leaves out the two test options.
	client, err := emailable.NewClient("test_example_key",
		emailable.WithTestBaseURL(srv.URL),
		emailable.WithHTTPClient(srv.Client()),
	)
	if err != nil {
		fmt.Println("config:", err)
		return
	}

	// Every attempt is a request; the provider documents a repeat within
	// five minutes of a 249 as free.
	v, err := client.WaitForVerification(context.Background(), emailable.WaitForVerificationRequest{
		Verify:      emailable.VerifyEmailRequest{Email: "jane.doe@example.com"},
		Interval:    time.Second, // the pause after each 249: 1s to 30s, 2s when 0
		MaxAttempts: 5,           // requests in all, the first included: 2 to 30
		MaxWait:     time.Minute, // at most MaxPendingWait (4 minutes), by the local clock
	})
	if ce, ok := errors.AsType[*emailable.CallError](err); ok && ce.Kind == emailable.FailureBudgetExhausted {
		fmt.Println("still pending after", ce.Attempts, "requests; try later, or verify it in a batch")
		return
	}
	if err != nil {
		fmt.Println("verify:", err) // never repeated after a failure
		return
	}
	fmt.Println(v.State, v.Reason)
	// Output:
	// deliverable accepted_email
}

// ExampleClient_CreateBatch submits a list. An answer means the provider
// accepted the batch, not that it is verified: persist its ID before
// anything else, because it is the only way to read the batch.
func ExampleClient_CreateBatch() {
	srv := exServer(map[string][]exAnswer{"POST /v1/batch": {exOK(exCreated)}})
	defer srv.Close()

	// A real application passes its own key and leaves out the two test options.
	client, err := emailable.NewClient("test_example_key",
		emailable.WithTestBaseURL(srv.URL),
		emailable.WithHTTPClient(srv.Client()),
	)
	if err != nil {
		fmt.Println("config:", err)
		return
	}
	save := func(batchID string) { fmt.Println("saved batch", batchID) } // your database
	hookToken := "example-hook-token"                                    // your own secret, checked by your callback handler

	// PAID AND STATE-CHANGING: verification starts at once, and nothing ever resubmits it.
	created, err := client.CreateBatch(context.Background(), emailable.CreateBatchRequest{
		Emails:      []string{"jane.doe@example.com", "john.roe@example.com", "jane.doe@example.com"},
		CallbackURL: "https://hooks.example.com/emailable?token=" + hookToken,
		ResponseFields: []emailable.ResponseField{
			emailable.ResponseFieldEmail, // keep it: rows are matched by Email, never by position
			emailable.ResponseFieldState,
			emailable.ResponseFieldReason,
		},
	})
	if err != nil {
		if ce, ok := errors.AsType[*emailable.CallError](err); ok && ce.BatchID != "" {
			save(ce.BatchID) // the batch exists, although its answer was imperfect
		}
		if errors.Is(err, emailable.ErrOutcomeUnknown) {
			fmt.Println("a batch may exist: reconcile it by hand, never resubmit automatically")
		}
		fmt.Println("create:", err)
		return
	}
	save(created.BatchID)
	fmt.Println("accepted, not verified:", created.Message)
	// Output:
	// saved batch example_batch_0001
	// accepted, not verified: Batch successfully created.
}

// ExampleClient_CreateBatch_imperfectAnswer meets an answer the SDK cannot
// fully read but whose batch ID it can: the error carries the ID, and the
// batch exists.
func ExampleClient_CreateBatch_imperfectAnswer() {
	srv := exServer(map[string][]exAnswer{"POST /v1/batch": {exOK(exTrailing)}})
	defer srv.Close()

	// A real application passes its own key and leaves out the two test options.
	client, err := emailable.NewClient("test_example_key",
		emailable.WithTestBaseURL(srv.URL),
		emailable.WithHTTPClient(srv.Client()),
	)
	if err != nil {
		fmt.Println("config:", err)
		return
	}

	_, err = client.CreateBatch(context.Background(), emailable.CreateBatchRequest{
		Emails: []string{"jane.doe@example.com"},
	})
	if ce, ok := errors.AsType[*emailable.CallError](err); ok {
		fmt.Println("batch ID:", ce.BatchID) // persist it: the batch exists
		fmt.Println("outcome unknown:", ce.OutcomeUnknown)
		fmt.Println("log line:", err)
	}
	// Output:
	// batch ID: example_batch_0001
	// outcome unknown: false
	// log line: emailable: create_batch: malformed_response (HTTP 200)
}

// ExampleClient_GetBatch reads snapshots of a batch: one per call, because
// the SDK never polls. Progress is not completion, and deciding that a batch
// is complete is the caller's call.
func ExampleClient_GetBatch() {
	srv := exServer(map[string][]exAnswer{"GET /v1/batch": {exOK(exWorking), exOK(exComplete)}})
	defer srv.Close()

	// A real application passes its own key and leaves out the two test options.
	client, err := emailable.NewClient("test_example_key",
		emailable.WithTestBaseURL(srv.URL),
		emailable.WithHTTPClient(srv.Client()),
	)
	if err != nil {
		fmt.Println("config:", err)
		return
	}
	ctx := context.Background()
	batchID := "example_batch_0001" // as CreateBatch returned it and you saved it
	mine := []string{"jane.doe@example.com", "john.roe@example.com"}

	b, err := client.GetBatch(ctx, emailable.GetBatchRequest{BatchID: batchID})
	if err != nil {
		fmt.Println("read:", err) // free and read-only: safe to read again later
		return
	}
	if processed, total, ok := b.Progress(); ok {
		fmt.Printf("processed %d of %d: progress, not completion\n", processed, total)
	}

	// Later, on your own schedule or after the callback: another snapshot.
	b, err = client.GetBatch(ctx, emailable.GetBatchRequest{BatchID: batchID})
	if err != nil {
		fmt.Println("read:", err)
		return
	}
	fmt.Println("message:", b.Message) // the provider's text, never interpreted by the SDK

	// Match rows by Email, never by position: an address may have several rows.
	rows := map[string][]emailable.Verification{}
	for _, row := range b.Emails {
		rows[row.Email] = append(rows[row.Email], row)
	}
	for _, email := range mine {
		for _, row := range rows[email] {
			fmt.Printf("%s: %s (reason %q)\n", email, row.State, row.Reason)
		}
	}
	fmt.Println("total counts:", b.TotalCounts)
	fmt.Println("reason counts:", b.ReasonCounts)
	// Output:
	// processed 1 of 3: progress, not completion
	// message: Batch verification completed.
	// jane.doe@example.com: deliverable (reason "accepted_email")
	// jane.doe@example.com: duplicate (reason "")
	// john.roe@example.com: undeliverable (reason "rejected_email")
	// total counts: map[deliverable:1 duplicate:1 processed:3 total:3 undeliverable:1]
	// reason counts: map[accepted_email:1 rejected_email:1]
}

// ExampleClient_GetBatch_downloadFile reads a large batch, whose results come
// as a signed file URL instead of inline rows. The URL prints as
// "[redacted]", and the SDK never fetches it.
func ExampleClient_GetBatch_downloadFile() {
	srv := exServer(map[string][]exAnswer{"GET /v1/batch": {exOK(exLarge)}})
	defer srv.Close()

	// A real application passes its own key and leaves out the two test options.
	client, err := emailable.NewClient("test_example_key",
		emailable.WithTestBaseURL(srv.URL),
		emailable.WithHTTPClient(srv.Client()),
	)
	if err != nil {
		fmt.Println("config:", err)
		return
	}

	b, err := client.GetBatch(context.Background(), emailable.GetBatchRequest{BatchID: "example_batch_0002"})
	if err != nil {
		fmt.Println("read:", err)
		return
	}
	if processed, total, ok := b.Progress(); ok {
		fmt.Printf("processed %d of %d\n", processed, total)
	}
	fmt.Println("inline rows:", b.Emails != nil)
	fmt.Println("results file:", b.DownloadFile) // a bearer credential: prints as [redacted]
	if !b.DownloadFile.IsZero() {
		// Its host, authentication and format are undocumented, and it
		// expires after about five days: hand it, with Reveal, only to a
		// client you trust, and treat the file's contents as untrusted.
		fmt.Println("an https URL:", strings.HasPrefix(b.DownloadFile.Reveal(), "https://"))
	}
	// Output:
	// processed 1350 of 1350
	// inline rows: false
	// results file: [redacted]
	// an https URL: true
}

// ExampleClient_AccountInfo reads the account's credit balance. It is free,
// and needs a private key.
func ExampleClient_AccountInfo() {
	srv := exServer(map[string][]exAnswer{"GET /v1/account": {exOK(exAccount)}})
	defer srv.Close()

	// A real application passes its own key and leaves out the two test options.
	client, err := emailable.NewClient("test_example_key",
		emailable.WithTestBaseURL(srv.URL),
		emailable.WithHTTPClient(srv.Client()),
	)
	if err != nil {
		fmt.Println("config:", err)
		return
	}

	info, err := client.AccountInfo(context.Background(), emailable.AccountInfoRequest{})
	if err != nil {
		fmt.Println("account:", err)
		return
	}
	// A balance, not a reservation or a spending cap: concurrent calls and
	// auto-refill change it at any time. OwnerEmail is personal data.
	fmt.Println("available credits:", info.AvailableCredits)
	// Output:
	// available credits: 1000
}

// callbackHandler is an application's endpoint for the provider's batch
// callback, at the CallbackURL it passed to CreateBatch with a secret token of
// its own in the query. It hands each batch ID to enqueue, for a worker that
// re-reads the batch with GetBatch.
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
			// err holds no addresses or URLs: safe to log. Any answer but
			// 200 makes the provider try again, hourly for up to three days.
			http.Error(w, "unreadable callback", http.StatusBadRequest)
			return
		}
		// A hint, not the truth: it may be a duplicate or arrive late. Answer
		// 200 now, and re-read the batch with GetBatch before acting on it.
		enqueue(b.ID)
		w.WriteHeader(http.StatusOK)
	}
}

// ExampleDecodeBatchCallback receives the provider's completion callback
// behind the application's own token check, tolerates a duplicate delivery,
// and treats each callback as a hint to re-read the batch.
func ExampleDecodeBatchCallback() {
	srv := exServer(map[string][]exAnswer{"GET /v1/batch": {exOK(exComplete)}})
	defer srv.Close()

	// A real application passes its own key and leaves out the two test options.
	client, err := emailable.NewClient("test_example_key",
		emailable.WithTestBaseURL(srv.URL),
		emailable.WithHTTPClient(srv.Client()),
	)
	if err != nil {
		fmt.Println("config:", err)
		return
	}
	var queue []string // stands in for the application's job queue
	h := callbackHandler("example-hook-token", func(batchID string) { queue = append(queue, batchID) })
	// In an application: mux.Handle("POST /hooks/emailable", h)

	deliver := func(target string) int {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, target,
			strings.NewReader(exComplete))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	fmt.Println("delivery:", deliver("/hooks/emailable?token=example-hook-token"))
	fmt.Println("duplicate delivery:", deliver("/hooks/emailable?token=example-hook-token"))
	fmt.Println("forged delivery:", deliver("/hooks/emailable?token=guess"))

	// A worker drains the queue: one free read per batch, however many hints.
	for _, batchID := range slices.Compact(queue) {
		b, err := client.GetBatch(context.Background(), emailable.GetBatchRequest{BatchID: batchID})
		if err != nil {
			fmt.Println("read:", err)
			return
		}
		fmt.Println(batchID, "has", len(b.Emails), "rows")
	}
	// Output:
	// delivery: 200
	// duplicate delivery: 200
	// forged delivery: 403
	// example_batch_0001 has 3 rows
}

// ExampleDownloadURL handles the signed URL of a batch's result file. It is a
// bearer credential: every fmt verb prints "[redacted]", encoding/json never
// writes it, and only Reveal returns it.
func ExampleDownloadURL() {
	// NewDownloadURL wraps a URL you persisted yourself, or one for a fake.
	u := emailable.NewDownloadURL("https://downloads.example.com/batches/example_batch_0002?signature=synthetic")
	fmt.Println(u)
	fmt.Printf("%v %s %q %+v %#v\n", u, u, u, u, u)

	stored, err := json.Marshal(emailable.Batch{ID: "example_batch_0002", DownloadFile: u})
	if err != nil {
		fmt.Println("store:", err)
		return
	}
	fmt.Println(string(stored)) // never serialized: persist Reveal() yourself, as a secret
	fmt.Println("same URL:", u.Equal(emailable.NewDownloadURL(u.Reveal())))
	fmt.Printf("zero value: %q, empty: %t\n", emailable.DownloadURL{}, emailable.DownloadURL{}.IsZero())
	// Output:
	// [redacted]
	// [redacted] [redacted] "[redacted]" [redacted] [redacted]
	// {"id":"example_batch_0002"}
	// same URL: true
	// zero value: "", empty: true
}

// ExampleCallError reads a failed call. Its Error text holds only the
// operation, the kind, the HTTP status and a field path, so it is safe to
// log. RateLimit keeps the provider's rate-limit headers as raw integers.
func ExampleCallError() {
	srv := exServer(map[string][]exAnswer{"GET /v1/account": {{
		status: http.StatusTooManyRequests,
		body:   exThrottled,
		header: http.Header{"Ratelimit-Limit": {"25"}, "Ratelimit-Remaining": {"0"}, "Ratelimit-Reset": {"1"}},
	}}})
	defer srv.Close()

	// A real application passes its own key and leaves out the two test options.
	client, err := emailable.NewClient("test_example_key",
		emailable.WithTestBaseURL(srv.URL),
		emailable.WithHTTPClient(srv.Client()),
	)
	if err != nil {
		fmt.Println("config:", err)
		return
	}

	_, err = client.AccountInfo(context.Background(), emailable.AccountInfoRequest{})
	fmt.Println("log line:", err)
	fmt.Println("rate limited:", errors.Is(err, emailable.ErrRateLimited)) // the SDK never sleeps or retries
	if ce, ok := errors.AsType[*emailable.CallError](err); ok {
		fmt.Println(ce.Op, ce.Kind, ce.StatusCode, "attempts:", ce.Attempts, "outcome unknown:", ce.OutcomeUnknown)
		if rl := ce.RateLimit; rl.Remaining != nil && rl.Reset != nil {
			// "A timestamp, in seconds": an epoch time or a delay? The provider does not say.
			fmt.Println("remaining:", *rl.Remaining, "reset, raw:", *rl.Reset)
		}
	}
	// Output:
	// log line: emailable: account_info: rate_limited (HTTP 429)
	// rate limited: true
	// account_info rate_limited 429 attempts: 1 outcome unknown: false
	// remaining: 0 reset, raw: 1
}

// ExampleWithProviderDiagnostics keeps the provider's message on errors,
// reachable only through Diagnostics, with the API key scrubbed. It is off
// by default, because provider text may echo request data.
func ExampleWithProviderDiagnostics() {
	srv := exServer(map[string][]exAnswer{"GET /v1/account": {{status: http.StatusForbidden, body: exForbidden}}})
	defer srv.Close()

	// A real application passes its own key and leaves out the two test options.
	client, err := emailable.NewClient("test_example_key",
		emailable.WithTestBaseURL(srv.URL),
		emailable.WithHTTPClient(srv.Client()),
		emailable.WithProviderDiagnostics(),
	)
	if err != nil {
		fmt.Println("config:", err)
		return
	}

	_, err = client.AccountInfo(context.Background(), emailable.AccountInfoRequest{})
	fmt.Println("log line:", err) // never the provider's text
	if ce, ok := errors.AsType[*emailable.CallError](err); ok {
		fmt.Println("provider says:", ce.Diagnostics().Message) // may echo request data: think before logging
	}
	// Output:
	// log line: emailable: account_info: unauthorized (HTTP 403)
	// provider says: Synthetic: the key [redacted] cannot read this account.
}

// Example_errors classifies failures. A *ValidationError means nothing was
// sent. Any other failure of a paid call may follow a request the provider
// already processed, so none of these branches resends blindly.
func Example_errors() {
	// A loopback address with no server: even a bug could not reach the provider.
	client, err := emailable.NewClient("test_example_key", emailable.WithTestBaseURL("https://127.0.0.1:9"))
	if err != nil {
		fmt.Println("config:", err)
		return
	}
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	_, invalid := client.VerifyEmail(context.Background(), emailable.VerifyEmailRequest{Email: " jane.doe@example.com"})
	_, deadline := client.VerifyEmail(expired, emailable.VerifyEmailRequest{Email: "jane.doe@example.com"})

	describe := func(err error) string {
		if ve, ok := errors.AsType[*emailable.ValidationError](err); ok {
			return "nothing sent; fix " + ve.Field + " (" + ve.Reason + ")"
		}
		var what string
		switch {
		case errors.Is(err, emailable.ErrPending):
			what = "still verifying: no result yet"
		case errors.Is(err, emailable.ErrTimeout): // before context.DeadlineExceeded, which it also matches
			what = "the SDK's timeout expired"
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			what = "the caller's context ended"
		case errors.Is(err, emailable.ErrPaymentRequired):
			what = "out of credits, or auto-refill needs a card charge"
		case errors.Is(err, emailable.ErrUnauthorized), errors.Is(err, emailable.ErrRateLimited):
			what = "refused; check the key or slow down"
		default:
			what = "failed"
		}
		if errors.Is(err, emailable.ErrOutcomeUnknown) {
			what += "; it may already be billed: reconcile, never resend blindly"
		}
		return what
	}
	for _, err := range []error{
		invalid,
		deadline,
		// Errors as the SDK reports them, built by hand here.
		&emailable.CallError{
			Kind: emailable.FailurePending, Op: emailable.OpVerifyEmail, StatusCode: emailable.StatusPending, Attempts: 1,
		},
		&emailable.CallError{Kind: emailable.FailureTimeout, Op: emailable.OpVerifyEmail, OutcomeUnknown: true, Attempts: 1},
		&emailable.CallError{
			Kind: emailable.FailurePaymentRequired, Op: emailable.OpCreateBatch,
			StatusCode: http.StatusPaymentRequired, Attempts: 1,
		},
		&emailable.CallError{
			Kind: emailable.FailureUpstream, Op: emailable.OpCreateBatch,
			StatusCode: http.StatusBadGateway, OutcomeUnknown: true, Attempts: 1,
		},
	} {
		fmt.Println(describe(err))
		fmt.Println("  log line:", err)
	}
	// Output:
	// nothing sent; fix Email (surrounding_whitespace)
	//   log line: emailable: invalid request: Email: surrounding_whitespace
	// the caller's context ended
	//   log line: emailable: verify_email: deadline_exceeded
	// still verifying: no result yet
	//   log line: emailable: verify_email: pending (HTTP 249)
	// the SDK's timeout expired; it may already be billed: reconcile, never resend blindly
	//   log line: emailable: verify_email: timeout
	// out of credits, or auto-refill needs a card charge
	//   log line: emailable: create_batch: payment_required (HTTP 402)
	// failed; it may already be billed: reconcile, never resend blindly
	//   log line: emailable: create_batch: upstream (HTTP 502)
}

// batchReader is the one method an application's report needs. The real
// Client satisfies it, and the application's tests pass a fake.
type batchReader interface {
	GetBatch(ctx context.Context, req emailable.GetBatchRequest) (*emailable.Batch, error)
}

// fakeBatches is an application's test fake: canned snapshots by batch ID.
type fakeBatches map[string]*emailable.Batch

// GetBatch returns the canned snapshot, or the error the SDK returns for an
// unknown batch, built from exported fields.
func (f fakeBatches) GetBatch(_ context.Context, req emailable.GetBatchRequest) (*emailable.Batch, error) {
	if b, ok := f[req.BatchID]; ok {
		return b, nil
	}
	return nil, &emailable.CallError{
		Kind: emailable.FailureNotFound, Op: emailable.OpGetBatch,
		StatusCode: http.StatusNotFound, BatchID: req.BatchID, Attempts: 1,
	}
}

// resultsReport is the application code under test: it says where a batch's
// results are.
func resultsReport(ctx context.Context, batches batchReader, batchID string) string {
	b, err := batches.GetBatch(ctx, emailable.GetBatchRequest{BatchID: batchID})
	switch {
	case errors.Is(err, emailable.ErrNotFound):
		return "no such batch"
	case err != nil:
		return "unavailable"
	case !b.DownloadFile.IsZero():
		return "results file ready: " + b.DownloadFile.String()
	}
	return fmt.Sprintf("%d inline rows", len(b.Emails))
}

// Example_fake tests application code without the provider: the application
// depends on a one-method interface of its own, which the real Client
// satisfies, and its tests pass a fake. A CallError built from its exported
// fields matches errors.Is like one the SDK returns.
func Example_fake() {
	var _ batchReader = (*emailable.Client)(nil)

	fake := fakeBatches{"example_batch_0002": {
		ID:           "example_batch_0002",
		DownloadFile: emailable.NewDownloadURL("https://downloads.example.com/batches/example_batch_0002?signature=synthetic"),
	}}
	fmt.Println(resultsReport(context.Background(), fake, "example_batch_0002"))
	fmt.Println(resultsReport(context.Background(), fake, "example_batch_0404"))

	pending := &emailable.CallError{Kind: emailable.FailurePending, Op: emailable.OpVerifyEmail, StatusCode: emailable.StatusPending}
	fmt.Println(errors.Is(pending, emailable.ErrPending), errors.Is(pending, emailable.ErrOutcomeUnknown))
	fmt.Println(pending)
	// Output:
	// results file ready: [redacted]
	// no such batch
	// true false
	// emailable: verify_email: pending (HTTP 249)
}

// ExampleVerification stores a result as JSON and reads it back unchanged:
// the provider's snake_case names, with a field the provider did not send
// (nil) kept apart from false, 0 or "". Unmarshal only what the SDK
// returned: a raw provider body unmarshaled into these types skips every
// check the SDK makes.
func ExampleVerification() {
	srv := exServer(map[string][]exAnswer{"GET /v1/verify": {exOK(exVerified)}})
	defer srv.Close()

	// A real application passes its own key and leaves out the two test options.
	client, err := emailable.NewClient("test_example_key",
		emailable.WithTestBaseURL(srv.URL),
		emailable.WithHTTPClient(srv.Client()),
	)
	if err != nil {
		fmt.Println("config:", err)
		return
	}
	v, err := client.VerifyEmail(context.Background(), emailable.VerifyEmailRequest{Email: "jane.doe@example.com"})
	if err != nil {
		fmt.Println("verify:", err)
		return
	}

	// Contact data: the address, its parts and any inferred names.
	stored, err := json.Marshal(v)
	if err != nil {
		fmt.Println("store:", err)
		return
	}
	var loaded emailable.Verification
	if err = json.Unmarshal(stored, &loaded); err != nil {
		fmt.Println("load:", err)
		return
	}
	fmt.Println(string(stored))
	fmt.Println("unchanged:", reflect.DeepEqual(*v, loaded))
	fmt.Println("free:", *loaded.Free, "birth year sent:", loaded.BirthYear != nil)
	// Output:
	// {"email":"jane.doe@example.com","state":"risky","reason":"low_deliverability","score":64,"accept_all":true,"disposable":false,"free":false,"role":false,"domain":"example.com","user":"jane.doe","mx_record":"mx.example.com","duration":0.94}
	// unchanged: true
	// free: false birth year sent: false
}
