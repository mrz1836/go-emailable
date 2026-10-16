# Adding an operation

go-emailable implements four Emailable API v1 operations, single verification (`GET /v1/verify`), batch
creation (`POST /v1/batch`), batch reads (`GET /v1/batch`) and account information (`GET /v1/account`),
plus two helpers: `WaitForVerification` and `DecodeBatchCallback`. This note describes how a future
operation, such as one of those the [README](../README.md#what-is-not-covered) lists as not covered, would
be added without breaking existing callers. Each one needs the owner's approval first: it is a new surface
with its own data, costs and contract evidence. An operation that deletes or cancels anything must never
be used for automatic cleanup.

## The pattern

An operation is **one method on `*Client`** with **its own request and result types**, named after the
method. There is no registry, generic executor, service object or subpackage.

```go
// ListBatches would list the account's batches: GET /v1/batches (hypothetical).
func (c *Client) ListBatches(ctx context.Context, req ListBatchesRequest) (*BatchList, error)
```

Even an operation without parameters takes a request struct, so fields can be added later without
breaking callers ([`AccountInfoRequest`](../account.go) is one). The method follows the same steps as
`AccountInfo` in [`account.go`](../account.go) and `GetBatch` in [`batch_get.go`](../batch_get.go):

1. `c.prepare(ctx)`: `ErrInvalidRequest` (`ctx`, `nil`) for a nil context, then `ErrInvalidConfig`
   (`Client`, `not_initialized`) for a nil or zero Client.
2. Validate the request with the operation's own rules, through `invalidRequest(field, reason)`: a
   `*ValidationError` that names the Go field (such as `Emails[17]`) and a fixed reason, never the value.
   Validate everything, and bound the encoded size, before encoding anything: nothing is streamed, so
   validation always ends before the first byte is sent.
3. Build one `request`: its `Operation`, method, path under `/v1/`, an encoded query (`url.Values`) or a
   JSON body, `paid` when the operation may be billed or change state, and `secrets`, the per-call values
   (a captcha response, a callback URL) that diagnostics must scrub. The API key is never part of it: the
   sender adds the `Authorization` header.
4. Call `c.call(ctx, r)`. It returns a `canceled` or `deadline_exceeded` `*CallError` without sending when
   the context is already done, and otherwise makes one attempt: no retries, no redirects, the anti-replay
   body, the timeout over the whole body read, the response limit, the HTTP status mapping, strict JSON,
   `OutcomeUnknown` for a paid request, and sanitized errors all come with it.
5. Decode the answer with `readObject(body, names)`, which matches keys **exactly** (unlike
   `encoding/json`, `Email` is not `email`; `checkStrict` has already refused keys that collide once
   case-folded), and the helpers in [`decode.go`](../decode.go): `decodeText`, `decodeTextPtr`,
   `decodeRequiredText`, `decodeToken`, `decodeBool`, `decodeIntRange`, `decodeCount`,
   `decodeRequiredCount`, `decodeSeconds`, `decodeBatchID` and `decodeCounts`. A failure becomes a `*CallError` through `r.decodeFailure(derr)`, with the JSON path in
   `Field`.
6. Set the operation's own `CallError` fields on **every** `*CallError` the method returns, such as
   `BatchID` for an operation about one batch.

Two limits apply to a new operation's answer: `readObject` reads at most `maxFields` names (`verCount`,
the members of a `Verification`, the largest object decoded), so raise it for an object with more
members; and every operation except `GetBatch` is capped at 1 MiB (`Client.responseLimit`).

A new `Operation` constant names the operation, in the `snake_case` of the existing ones. Add a new
`FailureKind` only when no existing kind fits, and then give it a sentinel in [`errors.go`](../errors.go),
a case in `kindSentinel`, and a decision in `outcomeOpen` on whether it leaves a paid request's outcome
open.

## Rules for the new types

- **Request.** A plain struct passed by value, read only while it is encoded, never kept or modified.
  Values are sent verbatim after validation; limits the provider does not document are labelled as SDK
  limits. A request that carries contact data or a secret gets `String`, `GoString` and `Format` methods
  that print its shape only (set or unset, counts), as `VerifyEmailRequest`, `WaitForVerificationRequest` and `CreateBatchRequest` do.
- **Result.** Required members are checked and fail as `malformed_response` when missing. Optional
  numbers, booleans and text are pointers, so absent stays apart from zero. Provider text is never parsed
  or interpreted. Open vocabularies are string types with `Known()`, kept as sent when they have the token
  shape. Every field has a bound, and one over it fails as `response_too_large`: the SDK never truncates
  what it returns. JSON tags use the provider's snake_case names and round-trip through `encoding/json`
  unchanged. A bearer credential, such as a signed URL, is a `DownloadURL`-style type that prints as
  `[redacted]` and is never serialized.
- **Fixtures.** Write fixtures from scratch to the documented shape with synthetic values (`example.com`
  addresses, made-up IDs), under `testdata/synthetic/` with the operation's prefix, labelled as SDK
  fixtures; never copy the provider's examples or commit a real response.
- **Tests.** The same evidence as the existing operations: an `opCase` in `helpers_test.go`, so that the
  suites that run every operation cover the new one; the exact wire request (method, path, query or body
  bytes, headers, no banned header) with `assertWireRequest` and `assertNoReplay`; the HTTP status table;
  zero calls on invalid input or a done context (`newOfflineClient`); exactly one attempt on every
  failure; `assertCallError` for every error, which checks the error text, the sentinels and the leak
  markers across `fmt` verbs and error chains; `assertRoundTrip` for the result; and fuzzing of the
  decoder. Add a step to the live smoke test (`live_smoke_internal_test.go`), raise `lsmMaxCalls` and
  the call list at the top of the file to match, and teach its fake provider the new path:
  `TestLiveSmokeGuardPlan` and `TestLiveSmokeGuardDryRun` fail until the three agree. Never run the
  live test itself; only the owner does.

## Documentation to update

- [`doc.go`](../doc.go): the operations-and-billing table and the scope section. It is the source of truth
  for wording.
- [README.md](../README.md): the operations table, a section for the operation with a Go block quoted
  verbatim from a new `Example` in `example_test.go`, its request fields, the limits table, the error
  reference, and the list of what is not covered.
- `readme_internal_test.go`: one `TestReadmeLimitsMatchCode` row for each new limit the README states,
  tying the claim to the constant that enforces it.

## What must not change

- **Exactly one HTTP attempt per call.** No retries, redirects, hedging, fallbacks, polling, pagination or
  follow-on calls inside a method. Every request keeps a non-rewindable body (`GetBody` nil, never
  `http.NoBody`), and no idempotency key is sent. A helper that repeats (like `WaitForVerification`) takes
  an explicit, required budget, repeats only an answer that means "not done yet", and never repeats after
  a failure.
- **Nothing paid or state-changing without a method call.** A paid operation sets `paid`, so that a
  failure after sending matches `ErrOutcomeUnknown`; nothing is ever resubmitted.
- **No values in errors.** `Error()` holds only the operation, the kind, the HTTP status and a JSON field
  path: never the key, an address, a captcha response, a URL, a header value, a body or provider text.
  Provider messages stay behind the opt-in `WithProviderDiagnostics`, scrubbed.
- **The key reaches only the provider or loopback,** in the `Authorization` header only. There is no
  general base URL option.
- The library stays standard-library only, starts no background goroutine (only the `context.AfterFunc`
  that closes a response body on timeout or cancellation runs apart from the caller), has no globals but
  sentinel errors and no `init`, never sleeps, and never logs, prints, stores or caches data, or reads
  credentials or configuration from the environment.
