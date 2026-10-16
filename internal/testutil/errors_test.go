package testutil_test

import (
	"fmt"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mrz1836/go-emailable/internal/testutil"
)

// typedError is an error type for TestRequireErrorAs to look for.
type typedError struct{}

func (typedError) Error() string { return "typed" }

func TestRequireErrorAs(t *testing.T) {
	wrapped := fmt.Errorf("wrapped: %w", typedError{})
	assert.Equal(t, typedError{}, testutil.RequireErrorAs[typedError](t, wrapped), "found in the chain")

	for name, err := range map[string]error{"nil": nil, "another type": errBoom} {
		rec := &recorder{}
		assert.Zero(t, testutil.RequireErrorAs[typedError](rec, err), name)
		assert.True(t, rec.failed, "%s fails the test", name)
	}
}

func TestTimeoutError(t *testing.T) {
	var err net.Error = testutil.TimeoutError{Msg: "planted"}
	assert.Equal(t, "planted", err.Error())
	assert.True(t, err.Timeout())
	assert.True(t, err.Temporary()) //nolint:staticcheck // Temporary is part of net.Error
	assert.Equal(t, "testutil: network timeout", testutil.TimeoutError{}.Error())
}
