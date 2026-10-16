package emailable

import (
	"context"
	"strings"
	"unicode"
	"unicode/utf8"
)

// maxEmailBytes bounds an address: the longest a forward path allows.
const maxEmailBytes = 254

// prepare runs the checks every operation starts with: a non-nil context,
// then a Client built by NewClient.
func (c *Client) prepare(ctx context.Context) error {
	if ctx == nil {
		return invalidRequest("ctx", reasonNil)
	}
	return c.ready()
}

// emailProblem returns the reason s is not an address the SDK sends, or ""
// when it is one: 1 to 254 bytes of valid UTF-8, without control characters
// or leading or trailing whitespace. The SDK checks no syntax, not even an
// "@": that is the provider's verdict. It builds nothing, so callers can
// render a field path only on failure.
func emailProblem(s string) string {
	switch {
	case s == "":
		return reasonRequired
	case len(s) > maxEmailBytes:
		return reasonTooLong
	}
	if reason := textProblem(s); reason != "" {
		return reason
	}
	first, _ := utf8.DecodeRuneInString(s)
	last, _ := utf8.DecodeLastRuneInString(s)
	if unicode.IsSpace(first) || unicode.IsSpace(last) {
		return reasonSurroundingSpace
	}
	return ""
}

// textProblem returns the reason s is not valid UTF-8 free of control
// characters, or "".
func textProblem(s string) string {
	switch {
	case !utf8.ValidString(s):
		return reasonInvalidUTF8
	case strings.ContainsFunc(s, unicode.IsControl):
		return reasonControlCharacter
	}
	return ""
}

// checkOptionalText validates an optional text field: "" is not sent, and
// anything else must be at most maxBytes bytes of valid UTF-8 without control
// characters.
func checkOptionalText(field, s string, maxBytes int) error {
	if s == "" {
		return nil
	}
	if len(s) > maxBytes {
		return invalidRequest(field, reasonTooLong)
	}
	if reason := textProblem(s); reason != "" {
		return invalidRequest(field, reason)
	}
	return nil
}

// checkBatchID validates a batch ID: 1 to 256 bytes of visible ASCII.
func checkBatchID(field, id string) error {
	switch {
	case id == "":
		return invalidRequest(field, reasonRequired)
	case !validBatchID(id):
		return invalidRequest(field, reasonInvalidFormat)
	}
	return nil
}
