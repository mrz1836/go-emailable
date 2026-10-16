// Package emailable is a small, typed, standard-library-only Go client for the
// Emailable API v1 (https://api.emailable.com/v1): single-email verification,
// native asynchronous batch verification, and account information.
//
// Create one Client with NewClient and your API key, share it, and call one
// method per operation. Every method takes a context and a request struct,
// and makes exactly one HTTP attempt; WaitForVerification makes one per
// pending answer, within a budget you set.
//
//	client, err := emailable.NewClient(apiKey)
//	if err != nil {
//		return err
//	}
//	v, err := client.VerifyEmail(ctx, emailable.VerifyEmailRequest{Email: "jane.doe@example.com"})
//	if err != nil {
//		return err // ErrPending (HTTP 249) is not a result; never resend blindly
//	}
//	if v.State == emailable.StateUnknown {
//		// a successful answer, not an error
//	}
//
// # Operations and billing
//
// Nothing paid or state-changing happens unless you call the method that does
// it:
//
//	Method               Operation         Effect
//	VerifyEmail          GET /v1/verify    PAID: one credit per verification
//	WaitForVerification  GET /v1/verify    PAID: VerifyEmail, repeated identically after HTTP 249 only
//	CreateBatch          POST /v1/batch    PAID and state-changing: verification starts at once
//	GetBatch             GET /v1/batch     free, read-only, one snapshot
//	AccountInfo          GET /v1/account   free, read-only
//	DecodeBatchCallback  (no request)      decodes a callback body you received
//
// A test key (the provider's "test_" keys) uses no credits. The SDK never
// retries, hedges, follows a redirect, polls a batch, or resubmits anything,
// and it disables the standard library's automatic resends. It never buys
// credits or changes billing; with auto-refill on, the provider may charge a
// card, and HTTP 402 can mean a charge is needed. A credit balance is not a
// reservation or a spending cap. After a timeout, a cancellation, a transport
// failure, a 5xx, an unexpected status (such as a redirect, a 2xx other than
// 200, or a 249 from anything but VerifyEmail) or an unreadable answer, a
// VerifyEmail or CreateBatch error matches ErrOutcomeUnknown: the provider may
// already have billed the verification or created the batch. Requests that
// fail local validation, and calls whose context is already done, send
// nothing.
//
// A public key works only for VerifyEmail and WaitForVerification; the other
// operations need a private key. The provider documents 25 verify requests a
// second and 5 batch or account requests a second; the SDK enforces no rate
// of its own.
//
// # Verifying a list
//
// CreateBatch returns the new batch's ID as soon as the provider accepts it.
// Persist the ID first, then read snapshots with GetBatch, or receive the
// completion callback at your CallbackURL and decode it with
// DecodeBatchCallback. A Batch snapshot may hold progress counts, inline rows
// (Emails, for batches of up to 1,000 addresses), a signed DownloadFile URL
// (larger batches), counts, or several of them. The provider signals
// completion only in free text, so deciding that a batch is complete is
// yours. Match rows to addresses by Verification.Email, never by position.
//
// The SDK never fetches DownloadFile, whose host, authentication and format
// are undocumented: it prints as "[redacted]" and Reveal returns it.
//
// # Errors
//
// Validation failures are *ValidationError values matching ErrInvalidConfig or
// ErrInvalidRequest: nothing was sent. Every other failure is a *CallError,
// whose Kind matches its sentinel (or context.Canceled or
// context.DeadlineExceeded) with errors.Is, and whose Op, StatusCode,
// OutcomeUnknown, BatchID, Attempts, Field and RateLimit say what happened.
// The HTTP status decides the kind: 249 from VerifyEmail is ErrPending, 401
// and 403 ErrUnauthorized, 402 ErrPaymentRequired, 404 ErrNotFound, 429
// ErrRateLimited, other 4xx ErrRejected, 503 ErrUnavailable, other 5xx
// ErrUpstream, and any other status ErrUnexpectedResponse. ErrTimeout also
// matches context.DeadlineExceeded, so check it first.
//
// Error strings hold only the operation, the kind, the HTTP status and a JSON
// field path: never the API key, email addresses, captcha responses, callback
// or download URLs, header values, response bodies or provider messages. The
// provider's message is kept only with WithProviderDiagnostics, behind
// (*CallError).Diagnostics, with the key and the call's secrets scrubbed.
//
// # Personal data
//
// Requests and results hold contact data. The SDK never logs, prints, stores or
// caches anything, and never reads credentials or configuration from the
// environment, but its result types have no redacting formatter: access
// control, logging, retention, security and lawful use are the caller's
// responsibility, and serializing or hashing an address does not anonymize
// it. A verification result is the provider's observation: it does not
// establish consent, identity, ownership or future deliverability.
//
// VerifyEmail sends the address in the URL query, as the provider's reference
// documents, so an injected transport, a TLS-intercepting proxy or an
// httptrace hook can see it, as they can see the Authorization header. So can
// the standard library's own HTTP/2 debug logging, which GODEBUG=http2debug=1
// or 2 turns on: it prints request headers, the API key included, to
// standard error.
//
// VerifyEmailRequest, WaitForVerificationRequest and CreateBatchRequest print
// only their shape for every fmt verb, except %p on a value and a request held
// in an unexported struct field, which fmt prints without calling their
// methods. encoding/json, and any logger that serializes values with it (such
// as log/slog's JSON handler), still writes every field, addresses, captcha
// responses and callback URLs included: log a request's String, never the
// request.
//
// # Proxies
//
// The transport the SDK builds honors HTTPS_PROXY and NO_PROXY through
// http.ProxyFromEnvironment, as the standard library's default transport does.
// That is the standard library reading the environment, not the SDK. To bypass
// proxies, inject an http.Client whose transport sets no Proxy.
//
// # Where the documentation is ambiguous
//
// Emailable's v1 documentation leaves some points open or contradicts itself.
// The SDK sends verify as GET with a query (the endpoint reference), not POST;
// sends a batch's emails as a JSON array (the documentation), not a
// comma-joined string; never decides completion from a batch's message; keeps
// the rate-limit headers as raw integers; and never assumes a callback is
// authentic, ordered or delivered once. The provider's pages disagree on
// whether unknown and duplicate results are not charged or refunded, and on
// whether a public key's daily cap is per user or per IP. The completion
// signal of a batch, the shapes of the importing and paused simulations, a
// simulation's effect with a live key, whether ratelimit-reset is an epoch
// time or a delay, where the free-repeat window after a 249 starts, the
// retention clock's origin, row order and duplicate correlation, the
// download file's format, host and authentication, callback authentication,
// and batch idempotency are undocumented: the SDK assumes none of them and
// never retries or replays a request.
//
// # Scope and extension
//
// Implemented: verify, batch create and read, account, and the callback
// decoder. Not implemented: OAuth, the widget, batch listing, cancellation or
// deletion, file uploads, a callback server, and fetching or parsing the
// download file. Each operation would be added later as one more method on
// Client with its own request and result types, without breaking existing
// callers (see docs/adding-an-operation.md in the repository).
package emailable
