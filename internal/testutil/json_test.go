package testutil_test

import (
	"encoding/json"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-emailable/internal/testutil"
)

func TestEsc(t *testing.T) {
	assert.Equal(t, `"\u0061\\"`, testutil.Esc(`"~u0061~~"`))
	assert.Equal(t, "plain", testutil.Esc("plain"))
}

func TestJSONQuote(t *testing.T) {
	assert.Equal(t, `"plain"`, testutil.JSONQuote("plain"))
	assert.Equal(t, `"a\"b\\c\n\u003c\u2028"`, testutil.JSONQuote("a\"b\\c\n<\u2028"))
	// Go 1.26 escapes the replacement character and Go 1.27 writes it as is;
	// either way the literal decodes to U+FFFD.
	var decoded string
	require.NoError(t, json.Unmarshal([]byte(testutil.JSONQuote("\xff")), &decoded))
	assert.Equal(t, string(utf8.RuneError), decoded, "invalid UTF-8 becomes U+FFFD")
}

func TestJSONStringLen(t *testing.T) {
	tests := []struct {
		name string
		s    string
		want int
	}{
		{name: "empty", s: "", want: len(`""`)},
		{name: "plain", s: "plain", want: len(`"plain"`)},
		{name: "HTML stays unescaped", s: "<&>", want: len(`"<&>"`)},
		{name: "quote and backslash", s: `"\`, want: len(`"\"\\"`)},
		{name: "control character", s: "\x01", want: len(`"\u0001"`)},
		{name: "line separator", s: "\u2028", want: len(`"\u2028"`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, testutil.JSONStringLen(tt.s))
		})
	}
}
