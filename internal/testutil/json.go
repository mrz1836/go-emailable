package testutil

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Esc returns s with every ~ replaced by a backslash, so that JSON escapes read
// naturally in raw string literals: Esc(`"~u0061"`) is the JSON string "a"
// spelled with an escape.
func Esc(s string) string { return strings.ReplaceAll(s, "~", `\`) }

// JSONQuote returns s as a JSON string literal, escaped as encoding/json
// escapes it.
func JSONQuote(s string) string {
	b, _ := json.Marshal(s) //nolint:errchkjson // a string always marshals: invalid UTF-8 becomes U+FFFD
	return string(b)
}

// JSONStringLen returns the length of s encoded as a JSON string by a
// json.Encoder that does not escape HTML, the encoder CreateBatch uses, without
// the newline the encoder ends with.
func JSONStringLen(s string) int {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s) //nolint:errchkjson // a string always encodes, and a bytes.Buffer never fails a write
	return b.Len() - len("\n")
}
