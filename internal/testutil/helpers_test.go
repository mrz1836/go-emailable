package testutil_test

import (
	"errors"
	"testing"
)

// errBoom is a planted failure.
var errBoom = errors.New("boom")

// recorder is a testing.TB that records a failure instead of stopping the
// test, so a test can prove an assertion helper fails when it should. Only the
// methods the helpers call are implemented; any other panics.
type recorder struct {
	testing.TB

	failed bool
}

func (r *recorder) Helper() {}

func (r *recorder) Name() string { return "recorder" }

func (r *recorder) Errorf(string, ...any) { r.failed = true }

func (r *recorder) FailNow() { r.failed = true }
