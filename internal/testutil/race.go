//go:build race

package testutil

// RaceEnabled reports whether the race detector is on. It changes allocation
// counts and timings, so allocation guards skip under it.
const RaceEnabled = true
