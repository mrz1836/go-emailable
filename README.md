<div align="center">

# 📧&nbsp;&nbsp;go-emailable

**A small, typed Go SDK for the Emailable API v1: single and batch email verification.**

<br/>

<a href="https://github.com/mrz1836/go-emailable/releases"><img src="https://img.shields.io/github/release-pre/mrz1836/go-emailable.svg?include_prereleases&style=flat-square&logo=github&color=black" alt="Release"></a>
<a href="https://golang.org/"><img src="https://img.shields.io/github/go-mod/go-version/mrz1836/go-emailable?style=flat-square&logo=go&color=00ADD8" alt="Go Version"></a>
<a href="https://github.com/mrz1836/go-emailable/blob/master/LICENSE"><img src="https://img.shields.io/github/license/mrz1836/go-emailable.svg?style=flat-square&color=blue" alt="License"></a>

<br/>

<table align="center" border="0">
  <tr>
    <td align="right">
       <code>CI / CD</code> &nbsp;&nbsp;
    </td>
    <td align="left">
       <a href="https://github.com/mrz1836/go-emailable/actions"><img src="https://img.shields.io/github/actions/workflow/status/mrz1836/go-emailable/fortress.yml?branch=master&label=build&logo=github&style=flat-square" alt="Build"></a>
       <a href="https://github.com/mrz1836/go-emailable/actions"><img src="https://img.shields.io/github/last-commit/mrz1836/go-emailable.svg?style=flat-square&logo=git&logoColor=white&label=last%20update" alt="Last Commit"></a>
    </td>
    <td align="right">
       &nbsp;&nbsp;&nbsp;&nbsp; <code>Quality</code> &nbsp;&nbsp;
    </td>
    <td align="left">
       <a href="https://codecov.io/gh/mrz1836/go-emailable"><img src="https://codecov.io/gh/mrz1836/go-emailable/branch/master/graph/badge.svg?style=flat-square" alt="Coverage"></a>
    </td>
  </tr>

  <tr>
    <td align="right">
       <code>Security</code> &nbsp;&nbsp;
    </td>
    <td align="left">
       <a href="https://scorecard.dev/viewer/?uri=github.com/mrz1836/go-emailable"><img src="https://api.scorecard.dev/projects/github.com/mrz1836/go-emailable/badge?style=flat-square" alt="Scorecard"></a>
       <a href=".github/SECURITY.md"><img src="https://img.shields.io/badge/policy-active-success?style=flat-square&logo=security&logoColor=white" alt="Security"></a>
    </td>
    <td align="right">
       &nbsp;&nbsp;&nbsp;&nbsp; <code>Community</code> &nbsp;&nbsp;
    </td>
    <td align="left">
       <a href="https://github.com/mrz1836/go-emailable/graphs/contributors"><img src="https://img.shields.io/github/contributors/mrz1836/go-emailable?style=flat-square&color=orange" alt="Contributors"></a>
       <a href="https://mrz1818.com/"><img src="https://img.shields.io/badge/donate-bitcoin-ff9900?style=flat-square&logo=bitcoin" alt="Bitcoin"></a>
    </td>
  </tr>
</table>

</div>

<br/>
<br/>

<div align="center">

### <code>Project Navigation</code>

</div>

<table align="center">
  <tr>
    <td align="center" width="20%">
       🚀&nbsp;<a href="#-installation"><code>Installation</code></a>
    </td>
    <td align="center" width="20%">
       🏁&nbsp;<a href="#-quick-start"><code>Quick&nbsp;Start</code></a>
    </td>
    <td align="center" width="20%">
       💡&nbsp;<a href="#-usage"><code>Usage</code></a>
    </td>
    <td align="center" width="20%">
       ⚙️&nbsp;<a href="#%EF%B8%8F-behavior"><code>Behavior</code></a>
    </td>
    <td align="center" width="20%">
       📚&nbsp;<a href="#-documentation"><code>Documentation</code></a>
    </td>
  </tr>
  <tr>
    <td align="center">
       🧪&nbsp;<a href="#-examples--tests"><code>Examples&nbsp;&&nbsp;Tests</code></a>
    </td>
    <td align="center">
      🔧&nbsp;<a href="#-development"><code>Development</code></a>
    </td>
    <td align="center">
      🛠️&nbsp;<a href="#%EF%B8%8F-code-standards"><code>Code&nbsp;Standards</code></a>
    </td>
    <td align="center">
      🤖&nbsp;<a href="#-ai-usage--assistant-guidelines"><code>AI&nbsp;Usage</code></a>
    </td>
    <td align="center">
       🤝&nbsp;<a href="#-contributing"><code>Contributing</code></a>
    </td>
  </tr>
</table>
<br/>

go-emailable calls [Emailable](https://emailable.com)'s API v1 to verify email addresses: one address at a
time with `GET /v1/verify`, whole lists as Emailable's native asynchronous batches (`POST /v1/batch` and
`GET /v1/batch`), and the account's credit balance with `GET /v1/account`. Use it when a Go service needs
to know whether an address is deliverable and has to stay in control of every paid call.

- **One attempt per call.** No retries, redirects, replays, hedging, polling or follow-on calls: the SDK
  never repeats a request on its own, and Go's HTTP clients cannot resend one either.
- **Nothing paid happens implicitly.** Every paid or state-changing step (a verification, a batch) is a
  method you call. The one helper that repeats, `WaitForVerification`, sends the identical request again
  only after HTTP 249, within a small budget you set. A batch is never polled or resubmitted.
- **Typed results that keep Emailable's meaning.** `unknown` is an answer, not an error; HTTP 249 is never
  a result, but an error matching `ErrPending`; a field Emailable did not send is `nil`, not `false`, `0`
  or `""`; undocumented states and reasons are kept as sent when they have the documented token shape.
- **Bounded.** Every call has a timeout and a size limit, and every decoded field has a bound.
- **Private by construction.** Errors, printed clients and printed requests never contain the API key,
  email addresses, captcha responses, callback or download URLs, bodies or Emailable's messages.
- **Zero runtime dependencies:** the standard library only.

It is a library, not an application: there is no binary, CLI, callback server, queue or configuration
file. It does not decide what to do with a verdict (there is no "safe to send" helper), fetch or parse a
batch's result file, store anything, or retry anything. It covers verification, batch creation and
reading, account information and the callback decoder; see [what is not covered](#what-is-not-covered).

<br/>

## 📦 Installation

**go-emailable** requires a [supported release of Go](https://golang.org/doc/devel/release.html#policy) (Go 1.26 or newer).
```shell script
go get github.com/mrz1836/go-emailable
```

The library has **zero third-party runtime dependencies**: it uses only the standard library. `testify`
is used only by the tests and their internal helper package.

<br/>

### Credentials

You need an Emailable API key, created in the Emailable dashboard. go-emailable never reads an environment
variable or a configuration file for it: your code passes the key to `emailable.NewClient`, from wherever
it keeps secrets. The key travels only in the `Authorization: Bearer` header of each request, never in a
URL or a body, and only to `https://api.emailable.com` (or to a loopback test server).

| Key | Use |
|---|---|
| Private key | Server-side use: every operation. `CreateBatch`, `GetBatch` and `AccountInfo` need one. |
| Public key | Client-side use, as Emailable documents it: it works only for `VerifyEmail` and `WaitForVerification`, within the domain allowlist and daily cap set in Emailable's dashboard. It may require a captcha (`CaptchaResponse`), and Emailable refuses what it may not do with HTTP 403 (`ErrUnauthorized`). |
| Test key | Emailable's `test_` keys make calls that **use no credits**: the cheapest way to try the SDK against the real API. The SDK never inspects the prefix. |

`NewClient` accepts 1 to 4096 bytes of visible ASCII. With a live key, **every verification uses
credits**. Try the SDK offline first with `go run ./examples`.

<br/>

## 🏁 Quick Start

```go
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/mrz1836/go-emailable"
)

func main() {
	// The application chooses where the key comes from; the SDK reads no environment.
	apiKey := os.Getenv("EMAILABLE_API_KEY")
	if apiKey == "" {
		log.Fatal("set EMAILABLE_API_KEY first (a test_ key uses no credits)")
	}
	client, err := emailable.NewClient(apiKey)
	if err != nil {
		log.Fatal(err) // ErrInvalidConfig: no call was made
	}

	// One paid call (one credit with a live key), whatever the result.
	v, err := client.VerifyEmail(context.Background(), emailable.VerifyEmailRequest{
		Email: "jane.doe@example.com",
	})
	if err != nil {
		log.Fatal(err) // ErrPending (HTTP 249) is not a result; after ErrOutcomeUnknown it may be billed
	}
	fmt.Println(v.State, v.Reason) // "unknown" is an answer, not an error
}
```

Illustrative output (the values are Emailable's):

```text
deliverable accepted_email
```

This program is [examples/quickstart](examples/quickstart/main.go); `EMAILABLE_API_KEY=test_... go run
./examples/quickstart` runs it against the live API. No test ever runs it.

<br/>

### Next steps

- [Set up a client once](#set-up-once) and see [what each method costs](#operations-and-billing).
- [Verify one email](#verify-one-email), and handle [HTTP 249](#http-249-and-waitforverification).
- [Verify a list](#verify-a-list), [read its snapshots](#read-a-batch) and [receive its
  callback](#batch-callbacks).
- Handle [errors](#errors) without resending paid calls.
- Run the [offline example](examples): `go run ./examples`.

<br/>

## 💡 Usage

### Set up once

Create one `Client` per API key and share it: it is immutable and safe for concurrent use.

```go
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
```

| Option | Default | Accepted values |
|---|---|---|
| `WithTimeout(d)` | 30 seconds (`DefaultTimeout`) | More than 0, at most 10 minutes. Bounds each HTTP attempt, including reading the whole body. An earlier deadline on the call's context wins. Keep it at least 5 seconds above Emailable's verification timeout (`ProviderTimeout`, 5 seconds when not set), or the SDK may give up on a verification Emailable still bills. |
| `WithMaxResponseBytes(n)` | 8 MiB (`DefaultMaxResponseBytes`) | 1 KiB to 64 MiB: the `GetBatch` body, counted after any transport decompression. Every other operation is also capped at 1 MiB. |
| `WithMaxBatchEmails(n)` | 50,000 (`DefaultMaxBatchEmails`) | 1 to 1,000,000: the addresses `CreateBatch` accepts. Raise it only when Emailable has granted your account a larger limit. |
| `WithHTTPClient(hc)` | a transport the SDK builds | A non-nil `*http.Client`, copied; see [the transport](#the-transport-proxies-and-tracing). |
| `WithTestBaseURL(rawURL)` | `https://api.emailable.com` | Tests only: `https` on a canonical IPv4 address in `127.0.0.0/8` or exactly `[::1]`, an optional port, and no path (a lone `/` is allowed), query, fragment or userinfo. Names such as `localhost` are refused. |
| `WithProviderDiagnostics()` | off | Keeps Emailable's `message` on errors, behind `(*CallError).Diagnostics`; see [provider diagnostics](#provider-diagnostics). |

Nil options are skipped and the last option of each kind wins. `NewClient` validates the key and every
option before returning, and performs no I/O. Its errors are `*ValidationError` values matching
`ErrInvalidConfig` that name the argument or option, never the rejected value. There is no option for any
other base URL, so the key reaches only Emailable or loopback. A zero `Client` or a nil `*Client` returns
`ErrInvalidConfig` from every method, without any I/O. Printing a `Client` shows its settings only.

> **Holding many keys?** Without `WithHTTPClient`, each `Client` owns a connection pool that cannot be
> closed (idle connections close on their own after 90 seconds). Pass the same `*http.Client` to every
> `Client` so that they share one pool.

<br/>

### Operations and billing

Every `Client` method takes a context and a request struct, makes exactly one HTTP attempt
(`WaitForVerification` makes one per pending answer, within its budget), and returns a result or an error,
never both. `DecodeBatchCallback` is a function, not a method: it takes a reader and makes no request.

| Method | Operation | Effect | Key |
|---|---|---|---|
| `VerifyEmail` | `GET /v1/verify` | **PAID**: one credit per verification with a live key | private or public |
| `WaitForVerification` | `GET /v1/verify` | **PAID**: `VerifyEmail`, repeated identically after HTTP 249 only | private or public |
| `CreateBatch` | `POST /v1/batch` | **PAID and state-changing**: verification starts at once | private |
| `GetBatch` | `GET /v1/batch` | free, read-only, one snapshot | private |
| `AccountInfo` | `GET /v1/account` | free, read-only | private |
| `DecodeBatchCallback` | (no request) | decodes a callback body you received | none |

Nothing paid or state-changing happens unless you call the method that does it. A request that fails local
validation, or a call whose context is already done, sends nothing. See [billing and
spending](#billing-and-spending) for what Emailable charges.

<br/>

### Verify one email

`VerifyEmail` verifies one address. **With a live key it uses one credit per call**, a syntactically bad
address included.

```go
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
```

| Request field | Sent as | Meaning |
|---|---|---|
| `Email` | `email` (URL query) | Sent verbatim: never trimmed, normalized or checked against an RFC. 1 to 254 bytes of valid UTF-8, without control characters or leading or trailing whitespace. A malformed address is a legitimate input, answered (and billed) like any other. |
| `SMTP` | `smtp` | `false` skips the SMTP check; `nil` is not sent (Emailable's default is `true`). |
| `AcceptAll` | `accept_all` | `true` asks for the accept-all check; `nil` is not sent (Emailable's default is `false`). |
| `ProviderTimeout` | `timeout` (seconds) | Emailable's own verification timeout. 0 is not sent (Emailable's default, 5 seconds). Otherwise whole seconds from 2s to 10s, and at least 5 seconds below `WithTimeout`, so that the SDK does not give up on a verification Emailable will still bill. The check applies only to a value you set: with 0, keep `WithTimeout` at 10 seconds or more (the default is 30 seconds). |
| `CaptchaResponse` | `captcha_response` | For public keys that require a captcha: at most 8 KiB of valid UTF-8 without control characters; `""` is not sent. A captcha token is likely single-use. |

The address travels in the URL query, as Emailable's endpoint reference documents, so anything that sees
request URLs can see it; see [the transport](#the-transport-proxies-and-tracing).

<br/>

### Reading a result

| `State` | Meaning |
|---|---|
| `StateDeliverable` | The address accepted Emailable's check. |
| `StateUndeliverable` | The address does not exist or cannot receive mail. |
| `StateRisky` | The address may receive mail but carries a risk, such as a low-quality, accept-all or role address. |
| `StateUnknown` | Emailable could not reach a verdict, such as when the mail server did not answer. **A successful answer, not an error.** |
| `StateDuplicate` | A batch row that repeats an earlier address of the same batch. |

`Reason` explains the state; the SDK defines the eleven documented reasons: `ReasonAcceptedEmail`,
`ReasonLowQuality`, `ReasonLowDeliverability`, `ReasonInvalidEmail`, `ReasonInvalidDomain`,
`ReasonRejectedEmail`, `ReasonInvalidSMTP`, `ReasonNoConnect`, `ReasonTimeout`, `ReasonUnavailableSMTP` and
`ReasonUnexpectedError`. Good to know:

- **Both sets are open.** A value Emailable adds later is kept as sent when it has the documented token
  shape (lowercase letters, digits and `_`, up to 64 bytes), and `Known()` reports `false` for it; any
  other string fails the call as `ErrUnexpectedResponse`, and a value that is not a string, or a missing
  required `state`, as `ErrMalformedResponse`.
- **There is no mapping from `Reason` to `State`**, and no "safe to send" helper: read both from the
  result, and decide what each means for you.
- **`Score` is an observation** (0 to 100) on which the SDK sets no policy: any threshold is yours.
- **`DidYouMean` is a suggestion.** The SDK never applies it or verifies the suggested address.
- **`unknown` is an answer.** The SDK never verifies it again on its own; whether to retry later is your
  policy, and a retry is another paid call.
- **A verdict is an observation,** not consent, identity, ownership or a promise of future
  deliverability.

<br/>

### Optional fields

Every optional field of a `Verification` is a pointer or a string: `nil` means Emailable did not send the
field, or sent `null`, so an undetermined check is never mistaken for `false`, `0` or `""`. In a
`VerifyEmail` result, `Email` and `State` are always set; in a batch row every field is optional, because
`ResponseFields` can leave any of them out. `Tag` tells the two empty cases apart: `nil` is an address
without a tag, and a pointer to `""` is one such as `user+@example.com`. `Duration` comes only from
`VerifyEmail`. Emailable runs the accept-all check only when asked (`VerifyEmailRequest.AcceptAll`), so
without that, `AcceptAll` false may mean "not checked" rather than "checked and not accept-all".

Every result type round-trips through `encoding/json` unchanged (`reflect.DeepEqual`), under Emailable's
snake_case names: `nil` fields are omitted, a pointer to `false`, `0` or `""` is kept, and an empty `Email`,
`State`, `Reason` or `Message` is omitted and reads back as `""`. Only a `Batch`'s `DownloadFile` and
`Partial` are never serialized:

```go
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
```

Unmarshal only what the SDK returned: a raw Emailable body unmarshaled into these types skips every check
the SDK makes.

<br/>

### HTTP 249 and WaitForVerification

When a verification is taking longer than normal, Emailable answers **HTTP 249** (`StatusPending`): it is
still verifying and has no result yet. `VerifyEmail` then returns `nil` and a `*CallError` matching
`ErrPending`, never a zero-valued success, and never repeats the request. It is Emailable's own answer, so
`OutcomeUnknown` is not set:

```go
v, err := client.VerifyEmail(context.Background(), emailable.VerifyEmailRequest{Email: "jane.doe@example.com"})
fmt.Println("result:", v) // never a zero-valued success
fmt.Println("pending:", errors.Is(err, emailable.ErrPending))
fmt.Println("outcome unknown:", errors.Is(err, emailable.ErrOutcomeUnknown)) // the provider answered
fmt.Println("log line:", err)
```

Emailable documents that repeating the identical request within five minutes of a 249 is free, without
saying where that window starts. `WaitForVerification` does exactly that, and nothing more: it verifies
the address as `VerifyEmail` does, and after each 249 it pauses and sends the byte-identical request
again, within a budget you set.

```go
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
```

| Observed | Returns |
|---|---|
| An attempt succeeds | `(result, nil)` at once. |
| HTTP 249, with budget left | Pauses `Interval`, then sends the identical request again. |
| HTTP 249 on the last allowed attempt, or the next attempt could not start before `MaxWait` | `(nil, err)` matching `ErrBudgetExhausted`, with `StatusCode` 249 and `Attempts` the number of requests made. |
| The pause's timer fires late, past `MaxWait` | `(nil, err)` matching `ErrBudgetExhausted`. |
| Your context ends between attempts | `(nil, err)` matching `context.Canceled` or `context.DeadlineExceeded`. The attempt before it ended with 249, so nothing is in flight and the outcome is known. |
| Any other outcome of an attempt | `(nil, that error)` at once: it never repeats after a failure, an ambiguous one included. |

- **Call it instead of `VerifyEmail`, never after one.** It cannot know when an earlier request was sent,
  so it cannot know how much of the free window is left.
- **The budget is required.** `MaxAttempts` counts every request, the first included (2 to 30). `MaxWait`
  must be more than the effective `Interval` (or no repeat could ever start) and at most
  `MaxPendingWait`, 4 minutes: one minute inside the documented five-minute window. `Interval` is 1
  second to 30 seconds, and 0 means 2 seconds (`DefaultPendingInterval`).
- **Errors say how far it got.** `Attempts` is always the number of requests made. An error from an
  attempt keeps that attempt's `StatusCode`, `RateLimit` and diagnostics; an error between attempts
  (`budget_exhausted`, or your context ending) carries those of the last 249. A validation error names
  the `Verify` fields as `Verify.Email` and so on.
- **It uses the local clock.** `MaxWait` is counted from just before the first attempt, by a clock that
  cannot prove when Emailable accepted a request. No attempt starts unless it can start, after its pause,
  before `MaxWait` ends.
- **An attempt in flight is never cut off,** because that would turn a paid call into an unknown outcome:
  the worst-case wall time is about `MaxWait` plus one `WithTimeout`.
- **A captcha response is likely single-use,** so Emailable may refuse a repeat with HTTP 403
  (`ErrUnauthorized`), which ends the wait.
- It starts no background goroutine, and every pause is a timer your context can end. As in every call,
  the only code that may run on another goroutine is the `context.AfterFunc` that closes a response body
  when the call times out or is canceled.

<br/>

### Verify a list

`CreateBatch` submits a list for asynchronous verification. **It is paid and state-changing**:
verification, which uses credits, starts as soon as Emailable accepts the batch.

```go
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
```

- **Accepted is not verified.** An answer means Emailable accepted the batch; read its results later with
  `GetBatch`, or receive its callback.
- **Persist the ID first.** `CreateBatchResult.BatchID` is the only way to read the batch.
- **Never resubmit.** Emailable documents no idempotency key. A 4xx means Emailable refused the batch, so
  none should exist. After an error matching `ErrOutcomeUnknown` (a timeout, a cancellation after sending,
  a transport failure, a 5xx, an unexpected status such as a redirect, a 2xx other than 200 or a 249, or
  an unreadable answer), a batch may exist anyway: reconcile by hand, for example in Emailable's
  dashboard, and never resubmit automatically.
- **An error can carry a `BatchID`.** When an HTTP 200 answer is not strict JSON but still names exactly
  one well-formed batch ID, the error is a `malformed_response` carrying that `BatchID`, and it does not
  match `ErrOutcomeUnknown`: the batch exists, so persist the ID. An ID that overlaps the API key, the
  callback URL or its token, or an address, as an echoing server could send, is never kept: the error
  then keeps its kind and its unknown outcome.

```go
_, err = client.CreateBatch(context.Background(), emailable.CreateBatchRequest{
	Emails: []string{"jane.doe@example.com"},
})
if ce, ok := errors.AsType[*emailable.CallError](err); ok {
	fmt.Println("batch ID:", ce.BatchID) // persist it: the batch exists
	fmt.Println("outcome unknown:", ce.OutcomeUnknown)
	fmt.Println("log line:", err)
}
```

| Request field | Sent as | Meaning |
|---|---|---|
| `Emails` | `emails` (a JSON array) | 1 to the client's limit (50,000 by default), in the given order, duplicates included. Each follows the `VerifyEmailRequest.Email` rules and must not contain a comma, which Emailable treats as a separator: one address could become several billable rows. |
| `CallbackURL` | `url` | Where Emailable POSTs the results when the batch completes; see [batch callbacks](#batch-callbacks). An absolute `https` URL with a host and no userinfo or fragment, at most 2048 bytes of valid UTF-8 without whitespace or control characters. Put a secret token of your own in its query. The SDK never prints it. |
| `ResponseFields` | `response_fields` (comma-joined) | Limits each row to these members, in the given order. Each must be `Known()` (the 21 documented fields: every `Verification` member but `duration`), without duplicates, because a typo would otherwise cost a paid batch. |
| `Retries` | `retries` | `false` turns off **Emailable's own** retries of inconclusive verifications, which improve accuracy; `nil` is not sent (Emailable's default is `true`). These are never SDK retries. |
| `Simulate` | `simulate` | For test keys: `SimulateGenericError`, `SimulateInsufficientCreditsError` or `SimulatePaymentError`. Its effect with a live key is undocumented. |

The SDK validates the whole request and bounds its size (the encoded body must fit 64 MiB) before
encoding it, and streams nothing, so validation always ends before the first byte is sent. A request that
fails returns a `*ValidationError` matching `ErrInvalidRequest` that names the field by Go path, such as
`Emails[17]`, never its value. HTTP 402 can mean the account's auto-refill needs a card charge.

<br/>

### Read a batch

`GetBatch` reads one snapshot of a batch. It is free and read-only, so reading again is safe, but the SDK
never polls: each call is one snapshot.

```go
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
```

A `Batch` can hold any of these parts, and several together, so read each on its own:

| Part | Fields | When Emailable sends it |
|---|---|---|
| Progress counts | `Processed`, `Total` | While the batch is processing; such a snapshot may carry no ID. |
| Inline rows | `Emails` | For batches of up to 1,000 addresses. `nil` when absent, empty when present and empty. |
| A result file | `DownloadFile` | For larger batches; see [the download file](#the-download-file). |
| Counts | `TotalCounts` (per `State`, plus totals such as `processed`), `ReasonCounts` (per `Reason`) | Every key kept as sent; a key whose count is `null` is left out, like any null member. |

- **Progress is not completion.** `Progress()` returns the top-level pair, else `processed` and `total`
  from `TotalCounts`; a batch can report every row processed before its results are ready.
- **Deciding that a batch is complete is yours.** Emailable signals completion only in its free-text
  `Message`, which the SDK never interprets. Decide from what you need, such as a row for every address
  you submitted, or a `DownloadFile`.
- **Match rows by `Email`, never by position.** Emailable documents neither the order of rows nor how
  duplicates correlate, and does not promise one row per address: a repeated address comes back as more
  rows, such as one with `StateDuplicate`. `Email` is the address as Emailable echoes it, never normalized
  by the SDK.
- **`ResponseFields` shapes the rows.** Each row holds only the members you asked for; without
  `ResponseFieldEmail`, rows cannot be matched to addresses at all.
- **Partial snapshots.** `GetBatchRequest.Partial` asks for the rows verified so far in an unfinished
  batch (documented for batches of up to 1,000 addresses), and `Batch.Partial` reports it. It is a
  snapshot, not a delta: there is no cursor.
- **Retention.** Emailable keeps inline rows for 30 days and the result file for 5 days; the counts
  persist. After that, a snapshot holds counts alone. When that clock starts is undocumented: store what
  you need when you first read it.
- `GetBatchRequest.Simulate` accepts `SimulateGenericError`, `SimulateImporting`, `SimulateVerifying` and
  `SimulatePaused`, for test keys.
- A snapshot that names another batch, or has no documented member at all, fails as
  `ErrUnexpectedResponse`. Every `*CallError` from `GetBatch` carries the requested `BatchID`.

<br/>

### The download file

For a large batch, Emailable sends a signed URL to a result file instead of inline rows:

```go
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
```

`Batch.DownloadFile` is a `DownloadURL`:

```go
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
```

- **It is a bearer credential:** it grants access to the results without the API key. Every `fmt` verb
  but `%p` prints `[redacted]` (or `""` for the zero value), and `%p` prints only an address, never the
  URL; `encoding/json` never writes it, reflection-based dumpers cannot reach it, and only `Reveal`
  returns it. Compare two values with `Equal`: `==` does not
  compile, and `reflect.DeepEqual` reports two non-zero values as different.
- **The SDK never fetches it,** and never reads ZIP or CSV. Emailable documents neither its host, its
  authentication nor its format: the prose says a ZIP file, while the example URL ends in `.csv`. Fetch
  it, if at all, with a client you trust, and never send your API key with it.
- **It is signed for about five days.**
- **Treat the file as untrusted.** A spreadsheet application may execute cells that start with a formula
  character, such as `=`, `+`, `-` or `@`.
- `NewDownloadURL` builds one, for fakes and for URLs you persisted.

<br/>

### Batch callbacks

With a `CallbackURL`, Emailable POSTs the batch's results there when it completes. The body has the shape
of a `GetBatch` answer, with a required `id`; `DecodeBatchCallback` decodes it, inside a handler you own:

```go
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
```

```go
// A worker drains the queue: one free read per batch, however many hints.
for _, batchID := range slices.Compact(queue) {
	b, err := client.GetBatch(context.Background(), emailable.GetBatchRequest{BatchID: batchID})
	if err != nil {
		fmt.Println("read:", err)
		return
	}
	fmt.Println(batchID, "has", len(b.Emails), "rows")
}
```

- **Decoding is not authentication.** Emailable documents no signature, no event ID, no ordering and no
  exactly-once delivery. Check a secret token of your own in the callback URL before decoding anything.
- **Expect duplicates and late deliveries.** Emailable retries a callback hourly for up to three days
  until it gets HTTP 200: answer 200 promptly, and make handling the same batch twice harmless.
- **A callback is a hint.** Re-read the batch with `GetBatch` before acting on it.
- `DecodeBatchCallback(r, maxBytes)` reads `r` to EOF, but at most `maxBytes`+1 bytes, so that a longer
  body is detected and fails as `ErrResponseTooLarge` (`maxBytes` is 1 KiB to 64 MiB;
  `DefaultMaxResponseBytes` suits most batches). It applies the same strict checks as `GetBatch`, makes no
  request and never closes `r`. Its errors are a `*ValidationError` (a nil reader, or `maxBytes` out of
  range) or a `*CallError` with `Op` `batch_callback`, no `StatusCode` and no `Attempts`, whose `BatchID`
  is the body's `id` when one could be read.
- The SDK has no callback server: mount the handler on your own, with your own server timeouts.

<br/>

### Account information

`AccountInfo` reads the account's owner and credit balance. It is free, and needs a private key.

```go
info, err := client.AccountInfo(context.Background(), emailable.AccountInfoRequest{})
if err != nil {
	fmt.Println("account:", err)
	return
}
// A balance, not a reservation or a spending cap: concurrent calls and
// auto-refill change it at any time. OwnerEmail is personal data.
fmt.Println("available credits:", info.AvailableCredits)
```

<br/>

### Errors

Every error is either a `*ValidationError` (nothing was sent) or a `*CallError`. Match the classes with
`errors.Is`, and use `errors.As` or `errors.AsType` for the details:

```go
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
```

In short, each sentinel means the following; the [error reference](#error-reference) is the one full
mapping from every status and failure to its kind, sentinel and outcome:

| Sentinel | When |
|---|---|
| `ErrInvalidConfig` | An invalid `NewClient` argument or option, or a `Client` not created by `NewClient`. Nothing was sent. |
| `ErrInvalidRequest` | A request that fails local validation, a nil context, or an invalid `DecodeBatchCallback` argument. Nothing was sent. |
| `ErrPending` | HTTP 249 from `VerifyEmail`: still verifying, no result yet. |
| `ErrUnauthorized` | HTTP 401 (missing key) or 403 (invalid key, or a refused captcha or public-key request). |
| `ErrPaymentRequired` | HTTP 402: the account lacks credits, or auto-refill needs a card charge that did not happen. |
| `ErrNotFound` | HTTP 404, such as an unknown batch ID. |
| `ErrRateLimited` | HTTP 429; the raw rate-limit headers are in `CallError.RateLimit`. |
| `ErrRejected` | HTTP 400, or any other 4xx not named above. |
| `ErrUpstream` | An HTTP 5xx other than 503. |
| `ErrUnavailable` | HTTP 503. |
| `ErrUnexpectedResponse` | Any other status (a redirect, which is never followed, a 1xx, a 2xx other than 200, 600 and above, or 249 from any operation but `VerifyEmail`), or an answer the SDK cannot interpret; see the [error reference](#error-reference). |
| `ErrMalformedResponse` | An HTTP 200 body, or a callback body, that is not valid UTF-8 or JSON, or breaks its documented shape. A `CreateBatch` answer whose batch ID could still be read is reported as malformed, with that `BatchID`, unless the ID overlaps the key or a request value. |
| `ErrResponseTooLarge` | A body over its limit, or a text field, `download_file` or count object over its bound. |
| `ErrTransport` | A failure in transit: DNS, connection, TLS, proxy, reset, truncated or stalled body, or a callback body whose reader failed. |
| `ErrTimeout` | The SDK's timeout (`WithTimeout`), an injected `http.Client`'s `Timeout`, or a network timeout. It also matches `context.DeadlineExceeded`, so **check it first**. |
| `ErrBudgetExhausted` | `WaitForVerification` still saw HTTP 249 when its budget ran out. |
| `ErrOutcomeUnknown` | Not a kind of its own: any `CallError` whose `OutcomeUnknown` is set. |
| `context.Canceled`, `context.DeadlineExceeded` | Your context ended. |

A `*CallError` carries:

| Field | Meaning |
|---|---|
| `Kind` | The `FailureKind`, such as `rate_limited`: short, fixed and free of personal data, so safe as a metric label. Each kind matches one target with `errors.Is`, except `timeout`, which matches `ErrTimeout` and `context.DeadlineExceeded`. |
| `Op` | The `Operation`: `verify_email` (`OpVerifyEmail`), `create_batch` (`OpCreateBatch`), `get_batch` (`OpGetBatch`), `account_info` (`OpAccountInfo`) or `batch_callback` (`OpBatchCallback`). |
| `StatusCode` | The last HTTP status received, or 0 when none was. |
| `OutcomeUnknown` | A `VerifyEmail`, `WaitForVerification` or `CreateBatch` request reached the transport and then failed in a way that leaves open whether Emailable processed it, billed it or created a batch: a timeout, a cancellation after sending, a transport failure (of any class, even one that may have happened before anything was written), a 5xx, an unexpected status (a redirect, a 2xx other than 200, or a 249 from `CreateBatch`) or an unreadable 200 answer. Never set for a 4xx, for `VerifyEmail`'s HTTP 249, for a `CreateBatch` answer whose batch ID could be read, or for a free read. It drives `errors.Is(err, ErrOutcomeUnknown)`. |
| `BatchID` | The batch concerned, when known: the requested ID for `GetBatch`, and the ID read from the body for `CreateBatch` and `DecodeBatchCallback`. |
| `Attempts` | The HTTP requests made: 0 when nothing was sent, 1 for a single call, up to `MaxAttempts` for `WaitForVerification`. |
| `Field` | A JSON path, such as `emails[17].score`, for type and shape failures: a location, never a value. |
| `RateLimit` | The raw rate-limit headers of the last error response; see [rate limits](#rate-limits). |

Its `Error()` is `emailable: <op>: <kind>`, followed by ` (HTTP <status>)` and `: <field>` when known, such
as `emailable: verify_email: malformed_response (HTTP 200): score`: never values, URLs, bodies or
Emailable's messages, so it is safe to log. `Unwrap` returns only a context error or one of the package's
fixed causes. Every error falls into one row of the [error reference](#error-reference).

| How to tell | |
|---|---|
| Nothing was sent | A `*ValidationError`, or a `CallError` with `Attempts` 0: `canceled`/`deadline_exceeded` for a context already done, or (practically unreachable) a `transport` failure to build the HTTP request. |
| Emailable refused it | `unauthorized`, `payment_required`, `not_found`, `rate_limited` or `rejected`: nothing to reconcile. |
| Still verifying | `ErrPending`: not a result, and not an unknown outcome. |
| It may have been billed, or a batch created | `ErrOutcomeUnknown`: reconcile before repeating; never resubmit a batch automatically. |
| A valid "unknown" verdict | `err == nil && v.State == emailable.StateUnknown` |

<br/>

### Rate limits

On an error response, `CallError.RateLimit` holds Emailable's `ratelimit-limit`, `ratelimit-remaining` and
`ratelimit-reset` headers as raw integers:

```go
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
```

A field is `nil` when its header is absent, repeated, or not 1 to 19 decimal digits that fit an `int64`.
Emailable documents `ratelimit-reset` as a "timestamp, in seconds" without saying whether it is an epoch
time or a delay, so the SDK never converts it, sleeps on it or retries: back off on your own terms.
Emailable documents 25 requests a second for verify and 5 a second for batch and account calls (custom
for enterprise accounts). The SDK enforces no rate of its own: pace your calls yourself.

<br/>

### Provider diagnostics

Emailable's error responses carry a `message`. By default the SDK never reads an error body, because
errors are routinely logged and passed on, and Emailable's text may echo request data such as an email
address. `WithProviderDiagnostics` keeps it, reachable only through `(*CallError).Diagnostics`:

```go
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
```

The SDK then reads the error body best-effort, up to 64 KiB, only for the message: the status alone
decides the kind, and an oversized or unreadable body is skipped. The message is cut to 1 KiB on a UTF-8
boundary, with every occurrence of the API key, the request's captcha response, its callback URL and
that URL's query values of 8 bytes or more (such as your token), as written or query-escaped, replaced by
`[redacted]`. Other spellings, such as a truncated or re-cased value, are not recognized. It never appears
in `Error()`.

<br/>

### Timeouts

- `WithTimeout` (30 seconds by default) bounds each HTTP attempt, including reading the whole body. The
  call's context applies as well, and the earlier deadline wins. A call whose context is already done
  sends nothing.
- **A canceled or timed-out paid call may still be processed and billed.** Cancellation stops the SDK from
  waiting, not Emailable from working: such an error matches `ErrOutcomeUnknown`.
- `ErrTimeout` also matches `context.DeadlineExceeded`, so check it first to tell the SDK's own timeout
  from your context's deadline (`FailureDeadlineExceeded`).
- `VerifyEmailRequest.ProviderTimeout` is Emailable's verification timeout, not the client's: the whole
  request takes longer, so the SDK requires a value you set to stay at least 5 seconds below
  `WithTimeout`. When you leave it at 0, Emailable uses 5 seconds and the SDK checks nothing: keep
  `WithTimeout` at 10 seconds or more yourself.
- `WaitForVerification` can take about `MaxWait` plus one `WithTimeout`, because it never cuts off an
  attempt in flight.
- `DecodeBatchCallback` reads a body you received: bound it with your server's own read timeout.

<br/>

### Billing and spending

- **Paid:** `VerifyEmail`, every attempt of `WaitForVerification`, and `CreateBatch`. **Free:** `GetBatch`,
  `AccountInfo` and `DecodeBatchCallback`, which makes no request at all.
- **Test keys use no credits.** The SDK never inspects a key's prefix: it is Emailable that decides.
- **The SDK never buys credits or changes billing.** With auto-refill on, Emailable may charge your card;
  HTTP 402 (`ErrPaymentRequired`) can mean that a charge is needed.
- **A balance is not a reservation or a spending cap.** `AvailableCredits` is the balance when Emailable
  answered: concurrent calls, other clients and auto-refill change it at any time, so checking it before
  spending is a race, not a guard.
- **Free repeats after HTTP 249.** Emailable documents a repeat of a pending verification within five
  minutes as free, without saying where that window starts; `WaitForVerification` stays a minute inside
  it.
- **Unknown results and duplicates.** Emailable's pages describe how unknown results, and repeated
  addresses in a batch, are charged in wording that does not agree: "not charged" in one place,
  "refunded" in another. The SDK promises neither, nor when a refund would show: read the balance with
  `AccountInfo` if you need it.
- **A malformed address is billed like any other,** and a comma inside a batch address is refused locally,
  because Emailable would split it into several billable rows.

<br/>

### Testing your application

Depend on a small interface of your own, which the real `Client` satisfies, and fake it:

```go
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
```

```go
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
```

- **A `*CallError` built from its exported fields** matches `errors.Is` exactly like one the SDK returns
  (its `Unwrap` is nil). Set `OutcomeUnknown` to exercise your reconciliation path.
- **`NewDownloadURL`** builds the `DownloadFile` of a fake snapshot.
- **To exercise the real SDK offline,** serve synthetic responses from `httptest.NewTLSServer` and pass
  `emailable.WithTestBaseURL(srv.URL)` and `emailable.WithHTTPClient(srv.Client())`, as the package's
  examples and the [offline demo](examples) do. Build fakes as structs: never unmarshal a raw body into the
  result types.

<br/>

## ⚙️ Behavior

### Exactly one attempt

- The SDK never retries, hedges, falls back, polls, pages, splits a list or makes a follow-on call, and
  never follows a redirect: `CheckRedirect` returns `http.ErrUseLastResponse`, every 3xx `Location` header
  is removed, and a 3xx fails as `ErrUnexpectedResponse`.
- **Every request carries an anti-replay body.** Go's HTTP/1 transport resends a request whose body is
  nil, `http.NoBody` or rewindable (`GetBody`) when a reused connection fails, even after the server read
  it; HTTP/2 does the same after a refused stream or a `GOAWAY`. Every SDK request carries a body that is
  none of those (a POST its bytes, a GET an empty body that puts nothing on an HTTP/1 wire), with
  `GetBody` nil, so neither can resend it, and a 307 or 308 cannot be followed either.
- **So stale-connection failures surface.** When a pooled keep-alive connection turns out to be closed,
  the call fails as `ErrTransport` instead of being resent silently; for a paid call, `OutcomeUnknown` is
  set. Whether to try again is your decision.
- The SDK never sends an idempotency key (with one, the standard library may replay a POST) or cookies.
- **An injected transport may retry on its own,** which the SDK cannot prevent: one that retries or
  replays requests defeats this guarantee.

<br/>

### The transport, proxies and tracing

Without `WithHTTPClient`, each `Client` builds its own `*http.Transport` with the settings of
`http.DefaultTransport` (HTTP/2, and the standard dial, TLS and idle timeouts), plus a 64 KiB limit on
response headers, and its own connection pool. Like the default transport, it takes a proxy from the
`HTTPS_PROXY` and `NO_PROXY` environment variables through `http.ProxyFromEnvironment`: that is the
standard library reading the environment, not the SDK. **To bypass proxies, inject a client whose
transport sets no `Proxy`,** such as a clone of `http.DefaultTransport` with `Proxy` set to nil (a client
with a nil `Transport` uses `http.DefaultTransport`, which honors the proxy variables too).

With `WithHTTPClient(hc)`, the SDK uses a copy of `hc` taken at `NewClient`, with redirects refused, every
3xx `Location` removed, and no cookie jar. `hc` itself is never modified, its `Timeout` still applies, and
its `Transport` is wrapped, not changed.

**Whatever sees the requests sees their contents.** An injected transport, a proxy that terminates TLS,
and `net/http/httptrace` hooks can see the `Authorization` header with the API key, and the verify URL's
query with the email address; a transport also sees every batch body, with its addresses and callback URL.
So can the standard library's own HTTP/2 debug logging: `GODEBUG=http2debug=1` or `2` prints request
headers, the API key included, to standard error. Treat transports, proxies and hooks as trusted code, and
never turn that debug logging on where its output is kept.

Every request goes to `https://api.emailable.com/v1/<operation>` with `Accept: application/json`,
`User-Agent: go-emailable`, `Authorization: Bearer <key>`, and `Content-Type: application/json` on a POST.
`Accept-Encoding` is left to the transport, which decodes gzip itself; the limits apply to the decoded
bytes, and a body whose `Content-Encoding` nobody decoded fails as `ErrUnexpectedResponse`. A response body
is always closed, and never read more than one byte past its limit.

<br/>

### Limits

| Bound | Value |
|---|---|
| API key | 1 to 4096 bytes of visible ASCII |
| An address (`Email`, each of `Emails`) | 1 to 254 bytes of UTF-8 |
| `WithTimeout` | 30 seconds by default; more than 0, at most 10 minutes |
| `ProviderTimeout` | whole seconds from 2s to 10s, and at least 5 seconds below `WithTimeout` |
| `CaptchaResponse` | at most 8 KiB |
| Addresses in one batch | 50,000 by default; 1 to 1,000,000 with `WithMaxBatchEmails` |
| Encoded `CreateBatch` body | 64 MiB |
| `CallbackURL` | `https`, at most 2048 bytes |
| Batch ID | 1 to 256 bytes of visible ASCII |
| `WaitForVerification` | `Interval` 1 second to 30 seconds, 2 seconds when 0; `MaxAttempts` 2 to 30; `MaxWait` more than the effective `Interval`, up to 4 minutes (`MaxPendingWait`) |
| Pending status | HTTP 249 (`StatusPending`) |
| `GetBatch` response | 8 MiB by default; 1 KiB to 64 MiB with `WithMaxResponseBytes` |
| `DecodeBatchCallback` body | its `maxBytes` argument: 1 KiB to 64 MiB |
| Every other response | 1 MiB, or the `WithMaxResponseBytes` limit when lower |
| Error response body read | 64 KiB, best-effort, only with `WithProviderDiagnostics` |
| Response headers (SDK-built transport) | 64 KiB |
| JSON objects and arrays / values in one body | 100,000 / 1,000,000 |
| One text field / one token / `download_file` | 4 KiB / 64 bytes / 8 KiB |
| Keys in `reason_counts` or `total_counts` | 256 each |
| `Score` / `BirthYear` | 0 to 100 / 0 to 9999 |
| Diagnostics message | cut to 1 KiB |
| A body that makes no progress | 100 empty reads in a row |

A text field, `download_file` or count object over its bound fails the call as `ErrResponseTooLarge`: the
SDK never truncates data it returns. A state, reason or count key that is not a token (one over 64 bytes
included) and a batch ID over 256 bytes fail as `ErrUnexpectedResponse`, and a `Score` or `BirthYear` out
of range as `ErrMalformedResponse`. Only `CreateBatchResult.Message` and the opt-in diagnostics never fail
a call: text the SDK cannot read is dropped.

<br/>

### Error reference

<details>
<summary><strong><code>Every error, and what it matches</code></strong></summary>
<br/>

| What happened | `Kind` (constant) | `errors.Is` matches | `OutcomeUnknown` on a paid call |
|---|---|---|---|
| Invalid key or option, or a nil or zero `Client` | (`*ValidationError`) | `ErrInvalidConfig` | no: nothing sent |
| Nil context, or a request failing validation | (`*ValidationError`) | `ErrInvalidRequest` | no: nothing sent |
| Your context was already done before the call | `canceled` (`FailureCanceled`) / `deadline_exceeded` (`FailureDeadlineExceeded`) | `context.Canceled` / `context.DeadlineExceeded` | no: nothing sent |
| Your context was canceled or expired after the request was handed to the transport | `canceled` (`FailureCanceled`) / `deadline_exceeded` (`FailureDeadlineExceeded`) | `context.Canceled` / `context.DeadlineExceeded` | yes |
| `WithTimeout`, the `http.Client` timeout, or a network timeout | `timeout` (`FailureTimeout`) | `ErrTimeout` and `context.DeadlineExceeded` | yes |
| DNS, connection, TLS or proxy failure, reset, truncated or stalled body; a callback reader failed | `transport` (`FailureTransport`) | `ErrTransport` | yes |
| HTTP 249 from `VerifyEmail` | `pending` (`FailurePending`) | `ErrPending` | no |
| HTTP 401 or 403 | `unauthorized` (`FailureUnauthorized`) | `ErrUnauthorized` | no |
| HTTP 402 | `payment_required` (`FailurePaymentRequired`) | `ErrPaymentRequired` | no |
| HTTP 404 | `not_found` (`FailureNotFound`) | `ErrNotFound` | no |
| HTTP 429 | `rate_limited` (`FailureRateLimited`) | `ErrRateLimited` | no |
| Any other HTTP 4xx | `rejected` (`FailureRejected`) | `ErrRejected` | no |
| HTTP 503 | `unavailable` (`FailureUnavailable`) | `ErrUnavailable` | yes |
| Any other HTTP 5xx | `upstream` (`FailureUpstream`) | `ErrUpstream` | yes |
| HTTP 1xx, 3xx (never followed), a 2xx other than 200, 600 and above, or 249 from any operation but `VerifyEmail`; a body that is not a JSON object; an undecoded `Content-Encoding`; an invalid token; a batch snapshot with no documented member, or for another batch | `unexpected_response` (`FailureUnexpectedResponse`) | `ErrUnexpectedResponse` | yes |
| Invalid UTF-8 or JSON, data after the top-level value, a repeated key (compared ignoring case, as `encoding/json` matches keys), a missing required field, a wrong type or an out-of-range number | `malformed_response` (`FailureMalformedResponse`) | `ErrMalformedResponse` | yes |
| A body over its limit or over the JSON bounds, or a text field, `download_file` or count object over its bound | `response_too_large` (`FailureResponseTooLarge`) | `ErrResponseTooLarge` | yes |
| `WaitForVerification` still saw HTTP 249 when its budget ran out | `budget_exhausted` (`FailureBudgetExhausted`) | `ErrBudgetExhausted` | no |

A `CreateBatch` answer that is not strict JSON but whose batch ID could still be read is
`malformed_response` with that `BatchID`, and `OutcomeUnknown` false: the batch exists. An ID that overlaps
the API key, the callback URL or its token, or an address is never kept, and the error keeps its kind and
`OutcomeUnknown`. Free reads
(`GetBatch`, `AccountInfo`) and `DecodeBatchCallback` never set `OutcomeUnknown`. The status
alone classifies a non-200 response; its body is read only for the opt-in diagnostics.

A `*ValidationError` has no kind: its `Err` is `ErrInvalidConfig` or `ErrInvalidRequest`, its `Field` is
the Go name of the argument, option or request field (`apiKey`, `Timeout`, `ctx`, `Client`, `Email`,
`Emails[17]`, `ResponseFields[2]`, `request` for the whole batch request, `Verify.Email` inside
`WaitForVerification`, `r` and `maxBytes` for `DecodeBatchCallback`), and its `Reason` is one of these
fixed codes:

| `Reason` | Meaning |
|---|---|
| `required` | Missing or empty: the key, an address, `Emails`, `BatchID`, `MaxAttempts` or `MaxWait`. |
| `nil` | A nil `ctx`, `HTTPClient` or reader. |
| `not_initialized` | A nil `Client`, or one not created by `NewClient`. |
| `out_of_range` | A number or duration outside its bounds; see [limits](#limits). |
| `invalid_format` | A key or batch ID that is not visible ASCII of the right length, or a batch request that cannot be encoded. |
| `invalid_utf8` / `control_character` | Text that is not valid UTF-8, or that holds a control character. |
| `surrounding_whitespace` | An address with leading or trailing whitespace. |
| `too_long` | Text over its bound: an address, `CaptchaResponse` or `CallbackURL`. |
| `contains_comma` | An address in `Emails` with a comma, which Emailable would split. |
| `too_many` | More `Emails` than `WithMaxBatchEmails` allows. |
| `too_large` | A batch request whose encoded body would exceed 64 MiB. |
| `invalid_url` | A `CallbackURL` or `WithTestBaseURL` value that is not an acceptable URL. |
| `unsupported_scheme` | A `CallbackURL` that is not `https`. |
| `duplicate` | A response field listed twice. |
| `unknown_value` | An undocumented response field or simulation. |
| `wrong_operation` | A simulation of the other batch operation. |
| `not_whole_seconds` | A `ProviderTimeout` that is not a whole number of seconds. |
| `exceeds_client_timeout` | A `ProviderTimeout` less than 5 seconds below `WithTimeout`. |

</details>

<br/>

### Contact data and privacy

- **Results are personal data.** A `Verification` holds the address, its parts and Emailable's inferences
  about its owner (names, gender, birth year); a `Batch` holds rows of them; `AccountInfo.OwnerEmail` is an
  address too. Access control, logging, retention, security and lawful use are your responsibility, and
  **serializing or hashing an address does not anonymize it.**
- **The SDK never logs, prints, stores or caches anything,** has no telemetry, and never reads credentials
  or configuration from the environment.
- Error strings, `GoString`, every `fmt` verb on a `Client` or an error, and every error in an `Unwrap`
  chain never contain the API key, email addresses, captcha responses, callback or download URLs, header
  values, response bodies or Emailable's messages. The key lives only inside function values the `Client`
  holds, which `fmt` and reflection-based dumpers cannot see into.
- `VerifyEmailRequest`, `WaitForVerificationRequest` and `CreateBatchRequest` print only their shape (set
  or unset, counts) for every `fmt` verb. `fmt` cannot call their methods for `%p` on a value or for a request held in an unexported
  struct field, and reflection-based dumpers see every field: keep requests out of such output.
- **`encoding/json` writes every field of a request,** addresses, captcha responses and callback URLs
  included, and so does any logger that serializes values with it, such as `log/slog`'s JSON handler:
  log a request's `String()`, never the request.
- **The result types have no redacting formatter:** printing a `Verification` or a `Batch` prints contact
  data. Keep them out of logs.
- Diagnostics, when enabled, may echo request data such as an address.

<br/>

### Where Emailable's documentation is ambiguous

<details>
<summary><strong><code>What Emailable's v1 documentation leaves open, and what the SDK does</code></strong></summary>
<br/>

| Question | SDK behavior |
|---|---|
| The verify endpoint reference documents `GET` with a query; other pages show `POST`. | `GET` with the query, as the reference documents: the address is in the URL. |
| A batch's `emails` is documented as a JSON array; some examples show one comma-separated string. | A JSON array; a comma inside an address is refused. |
| `ratelimit-reset` is "a timestamp, in seconds": an epoch time or a delay? | Kept as a raw integer: never converted, slept on or retried. |
| The response shapes of the `importing` and `paused` simulations. | Accepted for `GetBatch`; decoded like any snapshot. |
| How completion is signaled. | Only in the free-text `message`, which the SDK never interprets: completion is your decision. |
| Retention (30 days for rows, 5 days for the file): counted from creation or from completion? | Nothing assumed. |
| Row order, one row per address, and how duplicates correlate. | Emailable's order is kept; match rows by `Email`. |
| The download file's format (ZIP in the prose, `.csv` in the example URL), host and authentication. | Never fetched; redacted; only `Reveal` returns it. |
| Callback authentication, ordering and exactly-once delivery. | None assumed: check your own token, tolerate duplicates, re-read with `GetBatch`. |
| Idempotency of batch creation. | None assumed: one attempt, never resubmitted, `ErrOutcomeUnknown` when in doubt. |
| Whether a public key's daily cap applies per user or per IP address. | Not enforced or assumed; the status Emailable sends decides the error. |
| A simulation's effect with a live key. | `Simulate` is sent only when you set it, and is meant for test keys. |
| Where the five-minute free-repeat window after an HTTP 249 starts. | `WaitForVerification` stays a minute inside it, by the local clock; call it instead of `VerifyEmail`, never after one. |
| Whether unknown results and duplicates are "not charged" or "refunded", and when. | No promise either way. |

The SDK assumes none of these and never retries or replays a request to find out.

</details>

<br/>

### What is not covered

OAuth, the verification widget, listing, cancelling or deleting batches, file uploads, a callback server,
and fetching or parsing the download file. Each missing operation would be added as one more method on
`Client` with its own request and result types, without breaking existing callers; see [adding an
operation](docs/adding-an-operation.md).

<br/>

## 📚 Documentation

View the generated [documentation](https://pkg.go.dev/github.com/mrz1836/go-emailable?tab=doc) and
[Emailable's API documentation](https://emailable.com/docs/api/).

<br/>

<details>
<summary><strong><code>Development Setup (Getting Started)</code></strong></summary>
<br/>

Install [MAGE-X](https://github.com/mrz1836/mage-x) build tool for development:

```bash
# Install MAGE-X for development and building
go install github.com/mrz1836/mage-x/cmd/magex@latest
magex update:install
```
</details>

<details>
<summary><strong><code>Library Deployment</code></strong></summary>
<br/>

This project uses [goreleaser](https://github.com/goreleaser/goreleaser) for streamlined library releases to GitHub. To get started, install it via:

```bash
brew install goreleaser
```

The release process is defined in the [.goreleaser.yml](.goreleaser.yml) configuration file.

Then create and push a new Git tag using:

```bash
magex version:bump bump=patch push=true branch=master
```

</details>

<details>
<summary><strong><code>Build Commands</code></strong></summary>
<br/>

View all build commands

```bash script
magex help
```

</details>

<details>
<summary><strong><code>GitHub Workflows</code></strong></summary>
<br/>

All workflows are driven by modular configuration in [`.github/env/`](.github/env/README.md) — no YAML editing required.

**[View all workflows and the control center →](.github/docs/workflows.md)**

</details>

<details>
<summary><strong><code>Updating Dependencies</code></strong></summary>
<br/>

To update all dependencies (Go modules, linters, and related tools), run:

```bash
magex deps:update
```

</details>

<br/>

## 🧪 Examples & Tests

All unit tests, fuzz tests and examples run via [GitHub Actions](https://github.com/mrz1836/go-emailable/actions) on Go 1.26.x, the supported baseline. View the [configuration file](.github/workflows/fortress.yml).

Runnable examples live in the package's `Example*` functions and in [examples/](examples), which plays a
synthetic Emailable from a local TLS server:

```bash
go run ./examples # offline: no key, no network, no cost
```

[examples/quickstart](examples/quickstart/main.go) is the [quick start](#-quick-start) program: a live
call with the key in `EMAILABLE_API_KEY`, which no test ever runs.

> **Note:** tests never touch the network. They use in-memory transports and local TLS servers, with
synthetic data only: `example.com` addresses and made-up keys.

### Live smoke test

[`live_smoke_internal_test.go`](live_smoke_internal_test.go) runs every operation once against the real
API, to check the SDK end to end and record what Emailable's documentation leaves open. It is behind the
`live` build tag, which normal builds and test runs leave out (`magex test` and CI included; only the linter
checks it), and only the owner runs it, by hand:

```bash
EMAILABLE_LIVE=1 EMAILABLE_LIVE_KEY=test_... go test -tags live -count=1 -p 1 -timeout 5m -run 'TestLiveSmoke$' -v .
```

- **Test keys only, so it costs nothing.** It fails, having sent nothing, unless `EMAILABLE_LIVE_KEY` is a
  valid key that starts with `test_`, and its transport refuses, unsent, any request without a test key.
  It never reads `EMAILABLE_API_KEY`, which may hold a live key.
- **Skipped unless enabled.** It skips when `CI` is set (even empty), when `EMAILABLE_LIVE` is not `1`, or
  when `EMAILABLE_LIVE_KEY` is empty.
- **At most 17 requests,** at least half a second apart, never retried; a rate-limit answer stops the run.
  The one batch it creates (and any a simulation creates) is a test-key batch, which Emailable deletes after
  24 hours.
- **Shape-only output:** methods, paths, statuses, header names and JSON type trees, the SDK's error text
  and value-free counts. No key, address, batch ID, header value or provider message is printed.

Its guard tests need neither a key nor the network, and run the whole plan in memory against a fake
provider:

```bash
go test -tags live -count=1 -run 'TestLiveSmokeGuard' -v .
```

<br/>

## 🔧 Development

```bash script
magex format:fix # format the code (gofumpt, goimports)
magex lint       # run golangci-lint
magex test       # run all tests (fast)
magex test:race  # run all tests with the race detector (slower)
magex bench      # run the benchmarks
```

The library's dependencies are the standard library alone. This prints only the module itself:

```bash script
go list -deps -f '{{if not .Standard}}{{.ImportPath}}{{end}}' .
```

To add an operation, follow [adding an operation](docs/adding-an-operation.md): one more method on
`Client`, with its own request and result types, and the one-attempt and no-values-in-errors rules intact.

<br/>

## 🛠️ Code Standards
Read more about this Go project's [code standards](.github/CODE_STANDARDS.md).

<br/>

## 🤖 AI Usage & Assistant Guidelines
Read the [AI Usage & Assistant Guidelines](.github/tech-conventions/ai-compliance.md) for details on how AI is used in this project and how to interact with the AI assistants.

<br/>

## 👥 Maintainers
| [<img src="https://github.com/mrz1836.png" height="50" alt="MrZ" />](https://github.com/mrz1836) |
|:------------------------------------------------------------------------------------------------:|
|                                [MrZ](https://github.com/mrz1836)                                 |

<br/>

## 🤝 Contributing
View the [contributing guidelines](.github/CONTRIBUTING.md) and please follow the [code of conduct](.github/CODE_OF_CONDUCT.md).

### How can I help?
All kinds of contributions are welcome :raised_hands:!
The most basic way to show your support is to star :star2: the project, or to raise issues :speech_balloon:.
You can also support this project by [becoming a sponsor on GitHub](https://github.com/sponsors/mrz1836) :clap:
or by making a [**bitcoin donation**](https://mrz1818.com/?tab=tips&utm_source=github&utm_medium=sponsor-link&utm_campaign=go-emailable&utm_term=go-emailable&utm_content=go-emailable) to ensure this journey continues indefinitely! :rocket:


[![Stars](https://img.shields.io/github/stars/mrz1836/go-emailable?label=Please%20like%20us&style=social)](https://github.com/mrz1836/go-emailable/stargazers)

<br/>

## 📝 License

go-emailable is released under the [MIT License](LICENSE).

[![License](https://img.shields.io/github/license/mrz1836/go-emailable.svg?style=flat&v=1)](LICENSE)
