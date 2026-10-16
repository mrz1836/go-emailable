package emailable

import (
	"bytes"
	"encoding/json"
	"iter"
	"slices"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// Whole-body bounds. A body within the byte limit may still nest or repeat
// values enough to cost many times its size to walk; these bounds stop that.
const (
	maxContainers = 100_000
	maxValues     = 1_000_000
)

// linearKeys is the largest object whose keys are compared pairwise; larger
// objects are sorted once, so that a check stays O(n log n).
const linearKeys = 16

// checkStrict validates a whole response body before anything is decoded. It
// returns the failure kind and a fixed cause, or an empty kind when data is one
// JSON object in valid UTF-8, with nothing after it, within the container and
// value bounds, and with no object anywhere that repeats a key once escapes
// are decoded and case is folded.
//
// encoding/json alone would accept invalid UTF-8 (replacing it), and would let
// a repeated key silently override an earlier one. It also matches keys to
// struct fields ignoring case, so "Email" and "email" count as the same key
// here: no case variant can shadow a documented member for a caller who
// decodes the same body with encoding/json.
func checkStrict(data []byte) (FailureKind, error) {
	if !utf8.Valid(data) {
		return FailureMalformedResponse, errInvalidUTF8
	}
	if !json.Valid(data) {
		return FailureMalformedResponse, invalidJSONCause(data)
	}
	if i := skipSpace(data, 0); i >= len(data) || data[i] != '{' {
		return FailureUnexpectedResponse, errNotObject
	}
	if containers, values := countValues(data); containers > maxContainers || values > maxValues {
		return FailureResponseTooLarge, errTooManyValues
	}
	if duplicateKey(data) {
		return FailureMalformedResponse, errDuplicateKey
	}
	return "", nil
}

// invalidJSONCause tells data after a complete value apart from invalid syntax.
// Neither cause quotes the body.
func invalidJSONCause(data []byte) error {
	if _, ok := firstValue(data); ok {
		return errTrailingData
	}
	return errInvalidJSON
}

// firstValue returns the first complete JSON value in data and whether there
// is one, ignoring anything after it, exactly as a json.Decoder reads it. An
// object, an array or a string, the only values that can be large, is found
// by its closing delimiter and returned aliasing data, so a large failing
// body costs no memory to inspect. A number or a literal ends where the
// decoder stops reading it, so "00" is the value 0 followed by data; it is
// read with a json.Decoder, which buffers only a few bytes past it.
func firstValue(data []byte) ([]byte, bool) {
	i := skipSpace(data, 0)
	if i >= len(data) {
		return nil, false
	}
	switch data[i] {
	case '{', '[', '"':
		v := data[i:valueEnd(data, i)]
		if !json.Valid(v) {
			return nil, false
		}
		return v, true
	}
	var v json.RawMessage
	if err := json.NewDecoder(bytes.NewReader(data[i:])).Decode(&v); err != nil {
		return nil, false
	}
	return v, true
}

// countValues counts the objects and arrays (containers) and all values (keys
// excluded) in data, which must be valid JSON. It allocates only its nesting
// stack, which json.Valid has already bounded.
func countValues(data []byte) (containers, values int) {
	var objects []bool // per open container: whether it is an object
	expectKey := false
	for i := 0; i < len(data); i++ {
		switch c := data[i]; c {
		case '{', '[':
			containers++
			values++
			objects = append(objects, c == '{')
			expectKey = c == '{'
		case '}', ']':
			if len(objects) > 0 {
				objects = objects[:len(objects)-1]
			}
			expectKey = false
		case ',':
			expectKey = len(objects) > 0 && objects[len(objects)-1]
		case ':':
			expectKey = false
		case '"':
			i = stringEnd(data, i) - 1
			if !expectKey {
				values++
			}
			expectKey = false
		case ' ', '\t', '\n', '\r':
		default:
			// A number or a literal: count it once and skip to its end.
			values++
			i = valueEnd(data, i) - 1
		}
	}
	return containers, values
}

// duplicateKey reports whether any object in data, at any depth, has two keys
// that are equal once unescaped and case-folded as encoding/json folds them,
// so "a", "a" and "A" collide. data must be valid JSON; for anything else
// the result is unspecified, but it never panics.
//
// Only the keys of the objects still open are held: each is decoded and
// folded once into a side buffer, which shrinks again when its object closes,
// so even a body made only of keys costs a small multiple of its size.
func duplicateKey(data []byte) bool {
	var ks keySet
	type open struct{ spans, folded int }
	var stack []open // per open container: where its keys begin, or -1 for an array
	for i := 0; i < len(data); i++ {
		switch data[i] {
		case '{':
			stack = append(stack, open{spans: len(ks.spans), folded: len(ks.folded)})
		case '[':
			stack = append(stack, open{spans: -1})
		case '}', ']':
			n := len(stack) - 1
			if n < 0 {
				return false
			}
			if o := stack[n]; o.spans >= 0 {
				if ks.repeated(o.spans) {
					return true
				}
				ks.spans, ks.folded = ks.spans[:o.spans], ks.folded[:o.folded]
			}
			stack = stack[:n]
		case '"':
			end := stringEnd(data, i)
			if isKey(data, end) {
				ks.add(data[i:end])
			}
			i = end - 1
		}
	}
	return false
}

// keySet holds the folded keys of every open object as spans of folded.
type keySet struct {
	folded []byte
	spans  []keySpan
}

// keySpan locates one folded key.
type keySpan struct {
	start, end int32
}

// add records the key whose literal, quotes included, is lit.
func (ks *keySet) add(lit []byte) {
	start := len(ks.folded)
	ks.folded = appendFolded(ks.folded, lit)
	end := len(ks.folded)
	ks.spans = append(ks.spans, keySpan{start: int32(start), end: int32(end)}) //nolint:gosec // bodies are at most 64 MiB
}

// bytes returns the folded content of the key at span s.
func (ks *keySet) bytes(s keySpan) []byte { return ks.folded[s.start:s.end] }

// repeated reports whether the keys from spans[start:] hold the same key
// twice. It may reorder them.
func (ks *keySet) repeated(start int) bool {
	keys := ks.spans[start:]
	if len(keys) <= linearKeys {
		for i := 1; i < len(keys); i++ {
			for _, k := range keys[:i] {
				if bytes.Equal(ks.bytes(k), ks.bytes(keys[i])) {
					return true
				}
			}
		}
		return false
	}
	slices.SortFunc(keys, func(a, b keySpan) int { return bytes.Compare(ks.bytes(a), ks.bytes(b)) })
	for i := 1; i < len(keys); i++ {
		if bytes.Equal(ks.bytes(keys[i]), ks.bytes(keys[i-1])) {
			return true
		}
	}
	return false
}

// appendFolded appends the content of the JSON string literal lit, quotes
// included, decoded and then case-folded exactly as encoding/json folds an
// object key to match it to a struct field: ASCII letters to upper case, and
// every other rune to the smallest rune of its simple case-folding orbit.
func appendFolded(dst, lit []byte) []byte {
	start := len(dst)
	dst = appendUnquoted(dst, lit)
	if foldASCII(dst[start:]) {
		return dst
	}
	// A rune may fold to one of another length, so fold a copy. Its ASCII
	// prefix is already folded, which folding again leaves unchanged.
	decoded := append([]byte(nil), dst[start:]...)
	dst = dst[:start]
	for i := 0; i < len(decoded); {
		if c := decoded[i]; c < utf8.RuneSelf {
			if 'a' <= c && c <= 'z' {
				c -= 'a' - 'A'
			}
			dst = append(dst, c)
			i++
			continue
		}
		r, n := utf8.DecodeRune(decoded[i:])
		dst = utf8.AppendRune(dst, foldRune(r))
		i += n
	}
	return dst
}

// foldASCII upper-cases the ASCII letters of b in place, up to its first
// non-ASCII byte, and reports whether b is all ASCII, and so fully folded.
func foldASCII(b []byte) bool {
	for i, c := range b {
		switch {
		case c >= utf8.RuneSelf:
			return false
		case 'a' <= c && c <= 'z':
			b[i] = c - ('a' - 'A')
		}
	}
	return true
}

// foldRune returns the smallest rune of r's simple case-folding orbit.
func foldRune(r rune) rune {
	for {
		r2 := unicode.SimpleFold(r)
		if r2 <= r {
			return r2
		}
		r = r2
	}
}

// keyBytes returns the decoded content of the JSON string literal lit, quotes
// included. A key without escapes is returned in place, without allocating.
func keyBytes(lit []byte) []byte {
	if unescaped(lit) {
		return lit[1 : len(lit)-1 : len(lit)-1]
	}
	// Decoding never lengthens the content, so one allocation suffices.
	return appendUnquoted(make([]byte, 0, len(lit)), lit)
}

// unescaped reports whether the JSON string literal lit, quotes included, has
// no escapes, so that its content is lit without the quotes.
func unescaped(lit []byte) bool { return len(lit) >= 2 && bytes.IndexByte(lit, '\\') < 0 }

// members yields the members of the JSON object that data holds, in order:
// each key as its raw string literal, quotes included, and its raw value. Both
// alias data. data must be a valid JSON object, optionally surrounded by
// whitespace; for anything else it yields what it can and never panics. It
// allocates nothing, so a huge object costs no memory to walk.
func members(data []byte) iter.Seq2[[]byte, []byte] {
	return func(yield func(key, value []byte) bool) {
		i := skipSpace(data, 0)
		if i >= len(data) || data[i] != '{' {
			return
		}
		for i++; ; i++ {
			i = skipSpace(data, i)
			if i >= len(data) || data[i] != '"' {
				return
			}
			kend := stringEnd(data, i)
			key := data[i:kend]
			i = skipSpace(data, kend)
			if i >= len(data) || data[i] != ':' {
				return
			}
			i = skipSpace(data, i+1)
			vend := valueEnd(data, i)
			if !yield(key, data[i:vend]) {
				return
			}
			i = skipSpace(data, vend)
			if i >= len(data) || data[i] != ',' {
				return
			}
		}
	}
}

// elements yields the raw elements of the JSON array that data holds, in
// order, each with its index, aliasing data. data must be a valid JSON array;
// for anything else it yields what it can and never panics. It allocates
// nothing.
func elements(data []byte) iter.Seq2[int, []byte] {
	return func(yield func(int, []byte) bool) {
		i := skipSpace(data, 0)
		if i >= len(data) || data[i] != '[' {
			return
		}
		for n := 0; ; n++ {
			i = skipSpace(data, i+1)
			if i >= len(data) || data[i] == ']' {
				return
			}
			end := valueEnd(data, i)
			if !yield(n, data[i:end]) {
				return
			}
			i = skipSpace(data, end)
			if i >= len(data) || data[i] != ',' {
				return
			}
		}
	}
}

// isNull reports whether the raw JSON value is null or absent.
func isNull(raw []byte) bool { return len(raw) == 0 || string(raw) == "null" }

// isString reports whether the raw JSON value is a string.
func isString(raw []byte) bool { return len(raw) >= 2 && raw[0] == '"' }

// decodeKey returns the content of the JSON string literal lit, quotes
// included, decoded exactly as encoding/json decodes it.
func decodeKey(lit []byte) string { return string(keyBytes(lit)) }

// appendUnquoted appends the content of the JSON string literal lit, quotes
// included, to dst, decoded as encoding/json decodes it: escapes are resolved,
// a valid surrogate pair becomes one rune, and any other surrogate escape
// becomes U+FFFD. lit must come from a valid document, so its bytes are valid
// UTF-8 and copied as they are; at the first malformed escape, which a valid
// document never has, decoding stops.
func appendUnquoted(dst, lit []byte) []byte {
	if len(lit) < 2 {
		return dst
	}
	content := lit[1 : len(lit)-1]
	for i := 0; i < len(content); {
		c := content[i]
		if c != '\\' {
			dst = append(dst, c)
			i++
			continue
		}
		if i+1 >= len(content) {
			return dst
		}
		switch e := content[i+1]; e {
		case '"', '\\', '/':
			dst = append(dst, e)
		case 'b':
			dst = append(dst, '\b')
		case 'f':
			dst = append(dst, '\f')
		case 'n':
			dst = append(dst, '\n')
		case 'r':
			dst = append(dst, '\r')
		case 't':
			dst = append(dst, '\t')
		case 'u':
			r := hexEscape(content, i)
			if r < 0 {
				return dst
			}
			i += 6
			if utf16.IsSurrogate(r) {
				if low := hexEscape(content, i); r < 0xdc00 && isLowSurrogate(low) {
					r = utf16.DecodeRune(r, low)
					i += 6
				} else {
					r = utf8.RuneError
				}
			}
			dst = utf8.AppendRune(dst, r)
			continue
		default:
			return dst
		}
		i += 2
	}
	return dst
}

// stringEnd returns the index just past the JSON string whose opening quote is
// at data[i], or len(data) when it is not terminated.
func stringEnd(data []byte, i int) int {
	for j := i + 1; j < len(data); j++ {
		switch data[j] {
		case '\\':
			j++
		case '"':
			return j + 1
		}
	}
	return len(data)
}

// valueEnd returns the index just past the JSON value that starts at data[i].
// data must be valid JSON from i on; otherwise the result is at most len(data).
func valueEnd(data []byte, i int) int {
	if i >= len(data) {
		return len(data)
	}
	switch data[i] {
	case '"':
		return stringEnd(data, i)
	case '{', '[':
		depth := 0
		for j := i; j < len(data); j++ {
			switch data[j] {
			case '"':
				j = stringEnd(data, j) - 1
			case '{', '[':
				depth++
			case '}', ']':
				if depth--; depth == 0 {
					return j + 1
				}
			}
		}
		return len(data)
	}
	if n := bytes.IndexAny(data[i:], ",}]: \t\n\r"); n >= 0 {
		return i + n
	}
	return len(data)
}

// isKey reports whether the string ending just before data[end] is an object
// key: in valid JSON, exactly the strings followed by a colon.
func isKey(data []byte, end int) bool {
	j := skipSpace(data, end)
	return j < len(data) && data[j] == ':'
}

// skipSpace returns the index of the first non-whitespace byte at or after i.
func skipSpace(data []byte, i int) int {
	for i < len(data) {
		switch data[i] {
		case ' ', '\t', '\n', '\r':
			i++
		default:
			return i
		}
	}
	return i
}

// hexEscape returns the value of the \uXXXX escape at data[i], or -1 when
// there is none.
func hexEscape(data []byte, i int) rune {
	if i+6 > len(data) || data[i] != '\\' || data[i+1] != 'u' {
		return -1
	}
	var r rune
	for _, c := range data[i+2 : i+6] {
		switch {
		case c >= '0' && c <= '9':
			c -= '0'
		case c >= 'a' && c <= 'f':
			c -= 'a' - 10
		case c >= 'A' && c <= 'F':
			c -= 'A' - 10
		default:
			return -1
		}
		r = r<<4 | rune(c)
	}
	return r
}

func isLowSurrogate(r rune) bool { return r >= 0xdc00 && r <= 0xdfff }
