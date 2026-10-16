package testutil

import (
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
)

// SkipUnderRace skips the test when the race detector is on, because it changes
// allocation counts and sizes.
func SkipUnderRace(tb testing.TB) {
	tb.Helper()
	if RaceEnabled {
		tb.Skip("allocation counts and sizes differ under the race detector")
	}
}

// Ceiling is one allocation guard: a run of Run may allocate at most Max times.
type Ceiling struct {
	Name string
	Max  float64
	Run  func()
}

// CheckCeilings runs each guard as a subtest named after it. It measures Run
// with testing.AllocsPerRun, logs the count and fails when it is above Max.
func CheckCeilings(t *testing.T, ceilings []Ceiling) {
	t.Helper()
	for _, c := range ceilings {
		t.Run(c.Name, func(t *testing.T) {
			allocs := testing.AllocsPerRun(100, c.Run)
			t.Logf("%v allocations per run (ceiling %v)", allocs, c.Max)
			assert.LessOrEqual(t, allocs, c.Max)
		})
	}
}

// HeapGrowth reports the bytes allocated by one run of f, measured with
// runtime.MemStats after a warm-up run.
func HeapGrowth(f func()) uint64 {
	f()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}
