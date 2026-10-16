// Package testutil holds the test plumbing go-emailable's tests share:
// testdata fixtures and synthetic batch rows, HTTP fakes and scriptable
// bodies, a recording TLS server for HTTP/1.1 and HTTP/2, a scripted
// transport, JSON quoting helpers, formatting-leak assertions, race-aware
// allocation guards, and the value-free shape printer the live smoke test
// reports with.
//
// It is imported only by tests. It imports testify and the standard library,
// never the emailable package, so that the root package's internal tests can
// use it too.
package testutil
