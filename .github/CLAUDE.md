# CLAUDE.md

## 🤖 Welcome, Claude

This repository uses **`AGENTS.md`** as the single source of truth for:

* Coding conventions (naming, formatting, commenting, testing)
* Contribution workflows (branch prefixes, commit message style, PR templates)
* Release, CI, and dependency‑management policies
* Security reporting and governance links

> **TL;DR:** **Read `AGENTS.md` first.**
> All technical or procedural questions are answered there.

### Quick Checklist for Claude

1. **Study `AGENTS.md`**
   Make sure every automated change or suggestion respects those rules.
2. **Follow branch‑prefix and commit‑message standards**
   They drive Mergify auto‑labeling and CI gates.
3. **Never tag releases**
4. **Pass CI**
   Run `go fmt`, `goimports`, `go vet`, `staticcheck`, and `golangci‑lint` locally before opening a PR.

If you encounter conflicting guidance elsewhere, `AGENTS.md` wins.
Questions or ambiguities? Open a discussion or ping a maintainer instead of guessing.

Happy hacking!

---

## About This Project

`go-emailable` is a small Go SDK for the **Emailable API v1**: single-email verification
(`GET /v1/verify`), native asynchronous bulk verification (`POST /v1/batch`, `GET /v1/batch`) and
account information (`GET /v1/account`), plus two bounded helpers: `WaitForVerification`, which
repeats an identical verify request only after HTTP 249 within a small budget, and
`DecodeBatchCallback`, a strict, bounded decoder for the provider's batch callback body. It is a
library, not an application: there is no binary, no CLI, and no runtime configuration file.

- Module: `github.com/mrz1836/go-emailable` (package `emailable`, one flat package, one method
  per operation on `*Client`)
- Go version: 1.26 (CI runs 1.26.x)
- Runtime dependencies: none (`testify` is used only by tests and `internal/testutil`)

**Invariants that changes must preserve:**
- **No runtime dependencies.** Library code imports only the standard library, and never `log`,
  `os`, `unsafe`, `encoding/csv`, `archive/zip`, `compress/gzip` or `net/http/httputil`. The
  `depguard` and `forbidigo` linters enforce this.
- **Exactly one HTTP attempt per call.** No retries, redirects, hedging, fallbacks, polling or
  follow-on calls. Every request carries a non-rewindable body (`GetBody` stays nil, never
  `http.NoBody`), so the standard library cannot replay a GET or a POST on its own. Nothing paid or
  state-changing (`VerifyEmail`, `WaitForVerification`, `CreateBatch`) ever happens unless the
  caller calls that method, and a batch is never resubmitted.
- **The API key reaches only the provider or loopback.** It travels only in the `Authorization:
  Bearer` header to `https://api.emailable.com`. There is no general base URL option;
  `WithTestBaseURL` accepts only `https` on `127.0.0.0/8` or `[::1]`. Redirects are refused and
  their `Location` is stripped. The key lives only inside function values the `Client` holds.
- **No values in errors.** `Error()` holds only the operation, kind, HTTP status and JSON field
  paths: never the key, email addresses, captcha responses, callback or download URLs, bodies or
  provider text. Provider messages on errors are opt-in (`WithProviderDiagnostics`).
- **Signed download URLs stay redacted.** `DownloadURL` prints as `[redacted]` with every fmt verb
  but `%p`, which prints only an address; it is never serialized, and is reachable only through
  `Reveal`. The SDK never fetches it, and never reads ZIP or CSV.
- **No logging, globals (other than sentinel errors) or `init`, no background goroutines, no
  sleeps, and no environment reads** in SDK code. The only code that runs apart from the caller
  is the `context.AfterFunc` that closes a response body on timeout or cancellation, and the
  SDK-built transport's `http.ProxyFromEnvironment` honors the standard proxy variables.
- **Unit tests stay offline.** The live smoke test (`live_smoke_internal_test.go`, `live` build
  tag) runs only with `EMAILABLE_LIVE=1` and a `test_` key in `EMAILABLE_LIVE_KEY`, outside CI,
  sends at most 17 requests, refuses any key that does not start with `test_`, and is run only by
  the owner, by hand. Never run it, and never set those variables. Its offline guard tests are
  safe to run: `go test -tags live -count=1 -run 'TestLiveSmokeGuard' .`
- **Synthetic data only.** Fixtures are written from scratch to the documented shape, use
  `example.com` addresses, and are labeled as SDK fixtures; never copy provider examples or commit
  real responses.
