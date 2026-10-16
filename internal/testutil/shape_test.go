package testutil_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-emailable/internal/testutil"
)

// TestJSONShapeNeverPrintsValues proves the live smoke test's shape printer
// keeps structure and drops every value, including keys that could carry
// data. The body is a synthetic batch answer with planted markers.
func TestJSONShapeNeverPrintsValues(t *testing.T) {
	const body = `{"id":"MARKER-ID","message":"","download_file":null,"emails":[` +
		`{"email":"marker.user@example.com","state":"deliverable","score":87,"accept_all":true,"tag":null},` +
		`{"email":"marker.other@example.com","state":"risky","score":41,"accept_all":false}],` +
		`"reason_counts":{"accepted_email":1,"MARKER KEY WITH SPACES":1},"total_counts":{"processed":2,"total":2}}`
	shape, ok := testutil.JSONShape([]byte(body))
	require.True(t, ok)
	for _, marker := range []string{"MARKER", "marker", "deliverable", "risky", "87", "41", "true", "false"} {
		assert.NotContains(t, shape, marker)
	}
	for _, want := range []string{
		"id: string", "message: string(empty)", "download_file: null", "emails: array(len=2)[",
		"accept_all: bool", "score: number", "tag: null", "reason_counts", "accepted_email: number", "<key>: number",
		"total_counts",
	} {
		assert.Contains(t, shape, want)
	}

	_, ok = testutil.JSONShape([]byte(`{"a":`))
	assert.False(t, ok)

	assert.Equal(t, []string{"A", "B"}, testutil.HeaderNames(map[string][]string{"B": {"MARKER"}, "A": nil}))
}
