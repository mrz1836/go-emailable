package emailable

import (
	"bytes"
	"math"
	"net/http"
	"strconv"
	"unicode/utf8"
)

// Field bounds. A field over its bound fails the call as response_too_large:
// the SDK never truncates provider data it returns.
const (
	maxTextBytes         = 4 << 10 // provider text fields
	maxDownloadFileBytes = 8 << 10 // the signed download_file URL
	maxTokenBytes        = 64      // state, reason and count-key tokens
	maxBatchIDBytes      = 256
	maxCountKeys         = 256 // non-null members of reason_counts or total_counts

	maxDiagnosticMessageBytes = 1 << 10
)

// maxFields is the most members any decoded object has: a verification.
const maxFields = verCount

// decodeError is a decoding failure at a JSON path, such as "emails[3].score".
// It becomes a CallError once the operation is known.
type decodeError struct {
	kind  FailureKind
	cause error
	field string
}

// callError returns the CallError for e on an HTTP 200 response.
func (e *decodeError) callError(op Operation) *CallError {
	ce := newCallError(op, e.kind, http.StatusOK, e.cause)
	ce.Field = e.field
	return ce
}

// at sets the field e concerns and returns e.
func (e *decodeError) at(field string) *decodeError {
	e.field = field
	return e
}

func missingField(field string) *decodeError {
	return &decodeError{kind: FailureMalformedResponse, cause: errMissingField, field: field}
}

func wrongType(field string) *decodeError {
	return &decodeError{kind: FailureMalformedResponse, cause: errWrongType, field: field}
}

func outOfRange(field string) *decodeError {
	return &decodeError{kind: FailureMalformedResponse, cause: errOutOfRange, field: field}
}

func badToken(field string) *decodeError {
	return &decodeError{kind: FailureUnexpectedResponse, cause: errInvalidToken, field: field}
}

func fieldTooLarge(field string) *decodeError {
	return &decodeError{kind: FailureResponseTooLarge, cause: errFieldTooLarge, field: field}
}

func idMismatch(field string) *decodeError {
	return &decodeError{kind: FailureUnexpectedResponse, cause: errIDMismatch, field: field}
}

func noMembers() *decodeError {
	return &decodeError{kind: FailureUnexpectedResponse, cause: errNoMembers}
}

// path names a JSON location for error messages, rendered only when an error
// needs it: a base such as "emails" and an index into it, or the top level
// when base is "".
type path struct {
	base  string
	index int
}

// String renders the path, such as "emails[3]".
func (p path) String() string {
	return p.base + "[" + strconv.Itoa(p.index) + "]"
}

// member renders the path of the member name inside p, such as
// "emails[3].score", or name alone at the top level.
func (p path) member(name string) string {
	if p.base == "" {
		return name
	}
	return p.String() + "." + name
}

// object is the recognized members of one JSON object, by index into the
// names it was read with. A member that is absent is nil.
type object struct {
	fields [maxFields][]byte
}

// readObject reads the JSON object raw against names, matching keys exactly:
// unlike encoding/json, "Email" is not "email". checkStrict has already
// rejected keys that repeat once escapes are decoded and case is folded, so
// no case variant can stand in for a documented member. Other members are
// ignored.
func readObject(raw []byte, names []string) object {
	var o object
	for key, value := range members(raw) {
		k := keyBytes(key)
		for i, name := range names {
			if string(k) == name {
				o.fields[i] = value
				break
			}
		}
	}
	return o
}

// present reports whether any member of o is present and not null.
func (o *object) present(n int) bool {
	for _, raw := range o.fields[:n] {
		if !isNull(raw) {
			return true
		}
	}
	return false
}

// decodeText decodes optional provider text: a JSON string, or null or absent
// as "". A string over limit bytes fails as response_too_large.
func decodeText(raw []byte, limit int) (string, *decodeError) {
	switch {
	case isNull(raw):
		return "", nil
	case raw[0] != '"':
		return "", wrongType("")
	}
	if len(raw) > 6*limit+2 {
		// No escape spells less than one byte in six: even fully escaped,
		// this is longer than limit.
		return "", fieldTooLarge("")
	}
	s := decodeKey(raw)
	if len(s) > limit {
		return "", fieldTooLarge("")
	}
	return s, nil
}

// decodeTextPtr decodes optional provider text where null or absent (nil)
// differs from "" (a pointer to "").
func decodeTextPtr(raw []byte) (*string, *decodeError) {
	if isNull(raw) {
		return nil, nil
	}
	s, derr := decodeText(raw, maxTextBytes)
	if derr != nil {
		return nil, derr
	}
	return &s, nil
}

// decodeRequiredText decodes required provider text: a non-empty JSON string.
func decodeRequiredText(raw []byte) (string, *decodeError) {
	if isNull(raw) {
		return "", missingField("")
	}
	s, derr := decodeText(raw, maxTextBytes)
	switch {
	case derr != nil:
		return "", derr
	case s == "":
		return "", missingField("")
	}
	return s, nil
}

// validToken reports whether s matches ^[a-z0-9_]{1,64}$, the shape of every
// documented state, reason and count key.
func validToken(s string) bool {
	if s == "" || len(s) > maxTokenBytes {
		return false
	}
	for i := range len(s) {
		if c := s[i]; (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' {
			return false
		}
	}
	return true
}

// decodeToken decodes a token member. A required one must be present and a
// non-empty string, else it fails as malformed_response; an optional one may
// be null, absent or "", all meaning unset. A string that is not a valid
// token fails as unexpected_response: the value is kept only when it has the
// documented shape.
func decodeToken(raw []byte, required bool) (string, *decodeError) {
	switch {
	case isNull(raw):
		if required {
			return "", missingField("")
		}
		return "", nil
	case raw[0] != '"':
		return "", wrongType("")
	case len(raw) > 6*maxTokenBytes+2:
		return "", badToken("")
	}
	s := decodeKey(raw)
	switch {
	case s == "" && required:
		return "", missingField("")
	case s == "":
		return "", nil
	case !validToken(s):
		return "", badToken("")
	}
	return s, nil
}

// decodeBool decodes an optional JSON boolean, or null or absent as nil.
func decodeBool(raw []byte) (*bool, *decodeError) {
	switch string(raw) {
	case "", "null":
		return nil, nil
	case "true":
		v := true
		return &v, nil
	case "false":
		v := false
		return &v, nil
	}
	return nil, wrongType("")
}

// parseInt parses the JSON number lit as an int64 with strconv.ParseInt,
// never through float64: a fraction, an exponent or an out-of-range value is
// not an integer. ok is false when lit is not an integer at all, and
// inRange is false when it is one that does not fit an int64.
func parseInt(lit []byte) (n int64, ok, inRange bool) {
	if len(lit) == 0 || (lit[0] != '-' && (lit[0] < '0' || lit[0] > '9')) {
		return 0, false, false
	}
	n, err := strconv.ParseInt(string(lit), 10, 64)
	if err == nil {
		return n, true, true
	}
	if bytes.ContainsAny(lit, ".eE") {
		return 0, false, false
	}
	return 0, true, false
}

// decodeIntRange decodes an optional JSON integer from lo to hi, or null or
// absent as nil.
func decodeIntRange(raw []byte, lo, hi int64) (*int, *decodeError) {
	if isNull(raw) {
		return nil, nil
	}
	n, ok, inRange := parseInt(raw)
	switch {
	case !ok:
		return nil, wrongType("")
	case !inRange || n < lo || n > hi:
		return nil, outOfRange("")
	}
	v := int(n)
	return &v, nil
}

// countValue decodes a count: a JSON integer from 0 to MaxInt64. present is
// false, without an error, for null or absent. It returns the value itself,
// so that callers that keep it in a map allocate nothing for it.
func countValue(raw []byte) (n int64, present bool, derr *decodeError) {
	if isNull(raw) {
		return 0, false, nil
	}
	n, ok, inRange := parseInt(raw)
	switch {
	case !ok:
		return 0, true, wrongType("")
	case !inRange || n < 0:
		return 0, true, outOfRange("")
	}
	return n, true, nil
}

// decodeCount decodes an optional count: a JSON integer from 0 to MaxInt64, or
// null or absent as nil.
func decodeCount(raw []byte) (*int64, *decodeError) {
	n, present, derr := countValue(raw)
	if !present || derr != nil {
		return nil, derr
	}
	return &n, nil
}

// decodeRequiredCount decodes a required count: a JSON integer from 0 to
// MaxInt64.
func decodeRequiredCount(raw []byte) (int64, *decodeError) {
	n, present, derr := countValue(raw)
	if !present {
		return 0, missingField("")
	}
	return n, derr
}

// decodeSeconds decodes an optional duration in seconds: a finite JSON number,
// integer or not, of at least 0, or null or absent as nil.
func decodeSeconds(raw []byte) (*float64, *decodeError) {
	switch {
	case isNull(raw):
		return nil, nil
	case raw[0] != '-' && (raw[0] < '0' || raw[0] > '9'):
		return nil, wrongType("")
	}
	f, err := strconv.ParseFloat(string(raw), 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f < 0 {
		return nil, outOfRange("")
	}
	f += 0 // -0 becomes +0
	return &f, nil
}

// validBatchID reports whether s has the shape of a batch ID: 1 to 256 bytes
// of visible ASCII (0x21 to 0x7E).
func validBatchID(s string) bool {
	return s != "" && len(s) <= maxBatchIDBytes && asciiRange(s, 0x21, 0x7e)
}

// decodeBatchID decodes a batch ID member: a JSON string of the batch ID
// shape, or null or absent as "". Any other string fails as
// unexpected_response.
func decodeBatchID(raw []byte) (string, *decodeError) {
	switch {
	case isNull(raw):
		return "", nil
	case raw[0] != '"':
		return "", wrongType("")
	case len(raw) > 6*maxBatchIDBytes+2:
		return "", badToken("")
	}
	s := decodeKey(raw)
	if !validBatchID(s) {
		return "", badToken("")
	}
	return s, nil
}

// decodeCounts decodes an optional object of counts keyed by token, such as
// reason_counts, keeping every key as sent. Null or absent is nil; {} is an
// empty map; a key whose count is null is absent, like any null member. The
// path of a failing count names its key only when documented reports it:
// any other key is provider data, which could echo a secret of the right
// shape, such as the API key, so the path is then field alone.
func decodeCounts[K ~string](raw []byte, field string, documented func(K) bool) (map[K]int64, *decodeError) {
	switch {
	case isNull(raw):
		return nil, nil
	case raw[0] != '{':
		return nil, wrongType(field)
	}
	out := map[K]int64{}
	for key, value := range members(raw) {
		if len(out) == maxCountKeys {
			return nil, fieldTooLarge(field)
		}
		k := K(decodeKey(key))
		if !validToken(string(k)) {
			return nil, badToken(field)
		}
		n, present, derr := countValue(value)
		switch {
		case derr != nil && documented(k):
			return nil, derr.at(field + "." + string(k))
		case derr != nil:
			return nil, derr.at(field)
		case present:
			out[k] = n
		}
	}
	return out, nil
}

// asciiRange reports whether every byte of s is from lo to hi.
func asciiRange(s string, lo, hi byte) bool {
	for i := range len(s) {
		if c := s[i]; c < lo || c > hi {
			return false
		}
	}
	return true
}

// truncateUTF8 cuts s to at most n bytes without splitting a UTF-8 sequence.
func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
