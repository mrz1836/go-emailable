package testutil_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mrz1836/go-emailable/internal/testutil"
)

func TestSkipUnderRace(t *testing.T) {
	testutil.SkipUnderRace(t)
	assert.False(t, testutil.RaceEnabled, "only reached without the race detector")
}

func TestAllocationMeasures(t *testing.T) {
	// Storing into sink makes each allocation escape to the heap.
	sink := make([][]byte, 1)
	assert.GreaterOrEqual(t, testutil.HeapGrowth(func() { sink[0] = make([]byte, 1<<20) }), uint64(1<<20))
	testutil.CheckCeilings(t, []testutil.Ceiling{{Name: "no allocations", Max: 0, Run: func() {}}})
}
