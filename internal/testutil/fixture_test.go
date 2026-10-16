package testutil_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-emailable/internal/testutil"
)

func TestFullVerification(t *testing.T) {
	var v map[string]any
	require.NoError(t, json.Unmarshal([]byte(testutil.FullVerification), &v))
	assert.Len(t, v, 22, "every documented member")
	assert.Equal(t, "jane.doe@example.com", v["email"])
}

func TestAppendBatchRow(t *testing.T) {
	prefix := []byte(`[`)
	row := testutil.AppendBatchRow(prefix, 7, 5)
	assert.Equal(t, prefix, row[:1], "the row is appended")
	var v map[string]any
	require.NoError(t, json.Unmarshal(row[1:], &v))
	assert.Len(t, v, 19, "one container and 19 values")
	assert.Equal(t, "user.7@example.com", v["email"])
	assert.Equal(t, "user.7", v["user"])
	assert.Equal(t, "nnnnn", v["first_name"], "first_name carries the padding")
	assert.Len(t, testutil.AppendBatchRow(nil, 7, 105), len(row)-1+100, "each pad byte adds one byte")
}

func TestFixture(t *testing.T) {
	t.Chdir(t.TempDir())
	require.NoError(t, os.MkdirAll(filepath.Join("testdata", "dir"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join("testdata", "dir", "f.json"), []byte(`{}`), 0o600))

	assert.Equal(t, []byte(`{}`), testutil.Fixture(t, "dir/f.json"), "a slash-separated path under testdata")

	rec := &recorder{}
	assert.Nil(t, testutil.Fixture(rec, "dir/missing.json"))
	assert.True(t, rec.failed, "a missing fixture fails the test")
}
