package emailable

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-emailable/internal/testutil"
)

// Literals the strict tests repeat.
const (
	strictEmoji        = "\U0001F600"
	strictFFFD         = "\uFFFD"
	strictKelvin       = "\u212A" // KELVIN SIGN: folds with k and K
	strictLongS        = "\u017F" // LATIN SMALL LETTER LONG S: folds with s and S
	strictDotI         = "\u0130" // LATIN CAPITAL LETTER I WITH DOT ABOVE: no simple folding
	strictDotlessI     = "\u0131" // LATIN SMALL LETTER DOTLESS I: no simple folding
	strictMicro        = "\u00B5" // MICRO SIGN: folds with Greek mu, and is its orbit's smallest rune
	strictObjectMember = `{"a":`
	strictArrayMember  = `{"a":[`
	strictTrue         = "true"
)

// strictValidDepth is the deepest nesting json.Valid accepts.
const strictValidDepth = 10000

// strictManyKeys returns a JSON object with n distinct keys, prefix0 to
// prefix(n-1), each holding its index, plus extra appended verbatim before the
// closing brace. prefix is spelled as JSON, so it may hold escapes.
func strictManyKeys(prefix string, n int, extra string) string {
	var b strings.Builder
	b.WriteByte('{')
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`"` + prefix + strconv.Itoa(i) + `":` + strconv.Itoa(i))
	}
	b.WriteString(extra)
	b.WriteByte('}')
	return b.String()
}

// strictFill returns {"a":[ then n copies of el separated by commas, then the
// closing ]}.
func strictFill(el string, n int) []byte {
	const prefix, suffix = strictArrayMember, "]}"
	var b bytes.Buffer
	b.Grow(len(prefix) + n*(len(el)+1) + len(suffix))
	b.WriteString(prefix)
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(el)
	}
	b.WriteString(suffix)
	return b.Bytes()
}

// strictNest returns depth nested empty arrays, such as [[[]]] for depth 3.
func strictNest(depth int) string { return strings.Repeat("[", depth) + strings.Repeat("]", depth) }

// strictOrbitMin returns the smallest rune of r's simple case-folding orbit,
// found by walking the whole orbit: an oracle written apart from foldRune.
func strictOrbitMin(r rune) rune {
	lowest := r
	for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
		lowest = min(lowest, f)
	}
	return lowest
}

// strictFold folds s rune by rune to the smallest rune of each orbit. Two
// strings fold equal exactly when bytes.EqualFold reports them equal, which
// is how encoding/json matches an object key to a struct field.
func strictFold(s string) string {
	var b strings.Builder
	for _, r := range s {
		b.WriteRune(strictOrbitMin(r))
	}
	return b.String()
}

// strictWalk walks the next JSON value of dec with encoding/json's tokenizer
// and reports whether any object in it repeats a key once encoding/json has
// decoded it and strictFold has folded it.
func strictWalk(dec *json.Decoder) (bool, error) {
	tok, err := dec.Token()
	if err != nil {
		return false, err
	}
	dup := false
	switch tok {
	case json.Delim('{'):
		seen := map[string]bool{}
		for dec.More() {
			ktok, kerr := dec.Token()
			if kerr != nil {
				return dup, kerr
			}
			key, _ := ktok.(string)
			folded := strictFold(key)
			dup = dup || seen[folded]
			seen[folded] = true
			inner, verr := strictWalk(dec)
			if verr != nil {
				return dup, verr
			}
			dup = dup || inner
		}
	case json.Delim('['):
		for dec.More() {
			inner, verr := strictWalk(dec)
			if verr != nil {
				return dup, verr
			}
			dup = dup || inner
		}
	default:
		return false, nil
	}
	_, err = dec.Token() // the closing delimiter
	return dup, err
}

// strictOracle is the independent verdict on data: valid reports one
// well-formed JSON value in valid UTF-8 with nothing after it, and dup that an
// object in it repeats a key ignoring case as encoding/json does. dup is
// meaningful only when valid.
func strictOracle(data []byte) (valid, dup bool) {
	if !utf8.Valid(data) || !json.Valid(data) {
		return false, false
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber() // a number beyond float64 is still valid JSON
	dup, err := strictWalk(dec)
	return err == nil, dup
}

// strictOracleCount counts containers and values (keys excluded) with
// encoding/json's tokenizer, as an oracle for countValues. data must be one
// valid JSON value.
func strictOracleCount(data []byte) (containers, values int) {
	type frame struct{ object, wantKey bool }
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var stack []frame
	for {
		tok, err := dec.Token()
		if err != nil {
			return containers, values
		}
		if n := len(stack); n > 0 && stack[n-1].object && stack[n-1].wantKey {
			if _, ok := tok.(string); ok {
				stack[n-1].wantKey = false
				continue
			}
		}
		switch tok {
		case json.Delim('{'), json.Delim('['):
			containers++
			values++
			stack = append(stack, frame{object: tok == json.Delim('{'), wantKey: true})
			continue
		case json.Delim('}'), json.Delim(']'):
			stack = stack[:len(stack)-1]
		default:
			values++
		}
		if n := len(stack); n > 0 && stack[n-1].object {
			stack[n-1].wantKey = true
		}
	}
}

// strictSyntaxCause reports whether cause is one of the causes checkStrict
// gives for input that is not one valid JSON value in valid UTF-8.
func strictSyntaxCause(cause error) bool {
	return errors.Is(cause, errInvalidUTF8) || errors.Is(cause, errInvalidJSON) || errors.Is(cause, errTrailingData)
}

// strictFoldCase is a valid document and whether an object in it repeats a
// key once keys are decoded and folded.
type strictFoldCase struct {
	name string
	doc  string
	dup  bool
}

// strictFoldCases returns the duplicate-key cases: case variants, escaped
// spellings, surrogates, the runes whose simple folding leaves ASCII or stops
// short of it, nesting, and the linear and sorted comparisons. The escape
// collisions are those of go-discern's jsonx tests, folded.
func strictFoldCases() []strictFoldCase {
	esc := testutil.Esc
	return []strictFoldCase{
		{name: "distinct keys", doc: `{"email":1,"state":2}`},
		{name: "exact repeat", doc: `{"email":1,"email":2}`, dup: true},
		{name: "upper case", doc: `{"email":1,"EMAIL":2}`, dup: true},
		{name: "title case", doc: `{"Email":1,"email":2}`, dup: true},
		{name: "mixed case both sides", doc: `{"eMaIl":1,"EmAiL":2}`, dup: true},
		{name: "case variant of a real member", doc: `{"state":"deliverable","State":"risky"}`, dup: true},
		{name: "escaped first letter", doc: esc(`{"~u0065mail":1,"email":2}`), dup: true},
		{name: "escaped upper case letter", doc: esc(`{"~u0045MAIL":1,"email":2}`), dup: true},
		{name: "fully escaped against upper case", doc: esc(`{"~u0065~u006d~u0061~u0069~u006c":1,"EMAIL":2}`), dup: true},
		{name: "upper-case hex digits", doc: esc(`{"~u004D":1,"m":2}`), dup: true},
		{name: "escaped spellings on both sides", doc: esc(`{"~u0061b":1,"A~u0062":2}`), dup: true},
		{name: "keys differ after an escape", doc: esc(`{"~u0061b":1,"~u0061c":2}`)},
		{name: "escape against another letter", doc: esc(`{"~u0061":1,"b":2}`)},
		{name: "surrogate pair and the raw rune", doc: esc(`{"~ud83d~ude00":1,"` + strictEmoji + `":2}`), dup: true},
		{name: "surrogate pair in both hex cases", doc: esc(`{"~uD83D~uDE00":1,"~ud83d~ude00":2}`), dup: true},
		{name: "pair and its lone high half", doc: esc(`{"~ud83d~ude00":1,"~ud83d":2}`)},
		{name: "supplementary case pair as surrogates", doc: esc(`{"~ud801~udc00":1,"~ud801~udc28":2}`), dup: true},
		{name: "supplementary case pair raw and escaped", doc: esc(`{"` + "\U00010400" + `":1,"~ud801~udc28":2}`), dup: true},
		{name: "lone surrogates both decode to U+FFFD", doc: esc(`{"~ud800":1,"~udfff":2}`), dup: true},
		{name: "lone surrogate and a raw U+FFFD", doc: esc(`{"~udc00":1,"` + strictFFFD + `":2}`), dup: true},
		{name: "lone surrogate and an escaped U+FFFD", doc: esc(`{"~ud800":1,"~uFFFD":2}`), dup: true},
		{name: "lone high before an escaped letter", doc: esc(`{"~ud800~u0041":1,"` + strictFFFD + `a":2}`), dup: true},
		{name: "reversed pair is two U+FFFD", doc: esc(`{"~ude00~ud83d":1,"` + strictFFFD + strictFFFD + `":2}`), dup: true},
		{name: "Kelvin sign and k", doc: `{"k":1,"` + strictKelvin + `":2}`, dup: true},
		{name: "Kelvin sign and K", doc: `{"` + strictKelvin + `":1,"K":2}`, dup: true},
		{name: "escaped Kelvin sign and k", doc: esc(`{"~u212a":1,"k":2}`), dup: true},
		{name: "Kelvin sign inside a key", doc: `{"key":1,"` + strictKelvin + `EY":2}`, dup: true},
		{name: "long s and s", doc: `{"s":1,"` + strictLongS + `":2}`, dup: true},
		{name: "long s spelling a real member", doc: `{"state":"deliverable","` + strictLongS + `tate":"risky"}`, dup: true},
		{name: "capital and small sigma", doc: `{"\u03a3":1,"\u03c3":2}`, dup: true},
		{name: "small and final sigma", doc: `{"\u03c3":1,"\u03c2":2}`, dup: true},
		{name: "final and capital sigma", doc: `{"\u03c2":1,"\u03a3":2}`, dup: true},
		{name: "dotted capital I and i", doc: `{"i":1,"` + strictDotI + `":2}`},
		{name: "dotted capital I and I", doc: `{"I":1,"` + strictDotI + `":2}`},
		{name: "dotless i and i", doc: `{"i":1,"` + strictDotlessI + `":2}`},
		{name: "dotless i and I", doc: `{"I":1,"` + strictDotlessI + `":2}`},
		{name: "dotted and dotless I", doc: `{"` + strictDotI + `":1,"` + strictDotlessI + `":2}`},
		{name: "i and I", doc: `{"i":1,"I":2}`, dup: true},
		{name: "micro sign and mu", doc: `{"` + strictMicro + `":1,"\u03bc":2}`, dup: true},
		{name: "micro sign and capital mu", doc: `{"` + strictMicro + `":1,"\u039c":2}`, dup: true},
		{name: "Angstrom sign and a ring", doc: `{"\u212b":1,"\u00e5":2}`, dup: true},
		{name: "Ohm sign and omega", doc: `{"\u2126":1,"\u03c9":2}`, dup: true},
		{name: "sharp s and capital sharp s", doc: `{"\u00df":1,"\u1e9e":2}`, dup: true},
		{name: "sharp s and ss", doc: `{"\u00df":1,"ss":2}`},
		{name: "sharp s and SS", doc: `{"\u00df":1,"SS":2}`},
		{name: "title-case digraph", doc: `{"\u01c4":1,"\u01c5":2,"x":3}`, dup: true},
		{name: "Cherokee small and capital", doc: `{"\u13a0":1,"\uab70":2}`, dup: true},
		{name: "e acute and E acute", doc: `{"\u00e9":1,"\u00c9":2}`, dup: true},
		{name: "precomposed and combining", doc: esc(`{"\u00e9":1,"e~u0301":2}`)},
		{name: "solidus escape", doc: esc(`{"a/b":1,"a~/b":2}`), dup: true},
		{name: "backslash escapes", doc: esc(`{"a~~b":1,"a~u005cB":2}`), dup: true},
		{name: "backslash and solidus differ", doc: esc(`{"a~~b":1,"a/b":2}`)},
		{name: "quote escapes", doc: esc(`{"~"":1,"~u0022":2}`), dup: true},
		{name: "quote and backslash differ", doc: esc(`{"~"":1,"~~":2,"~~~"":3}`)},
		{name: "control escapes", doc: esc(`{"~b~f~n~r~t":1,"~u0008~u000c~u000a~u000d~u0009":2}`), dup: true},
		{name: "empty keys", doc: `{"":1,"":2}`, dup: true},
		{name: "empty and space", doc: `{"":1," ":2}`},
		{name: "trailing space differs", doc: `{"a":1,"a ":2}`},
		{name: "underscore is not folded", doc: `{"first_name":1,"firstname":2}`},
		{name: "dash is not folded", doc: `{"a-b":1,"ab":2}`},
		{name: "underscore keys in other cases", doc: `{"first_name":1,"FIRST_NAME":2}`, dup: true},
		{name: "digits", doc: `{"1":1,"1":2}`, dup: true},
		{name: "prefix is not equal", doc: `{"ab":1,"a":2,"abc":3}`},
		{name: "same key in sibling objects", doc: `{"a":{"k":1},"b":{"K":2},"c":[{"k":3},{"K":4}]}`},
		{name: "same key at different depths", doc: `{"k":{"K":{"k":1}}}`},
		{name: "repeat inside a nested object", doc: `{"a":{"b":1,"B":2},"c":3}`, dup: true},
		{name: "repeat in an array of objects", doc: `{"emails":[{"email":"a"},{"email":"b","EMAIL":"c"}]}`, dup: true},
		{name: "repeat five levels deep", doc: `{"a":[{"b":{"c":[{"d":{"e":1,"E":2}}]}}]}`, dup: true},
		{name: "inner repeat closes first", doc: `{"a":1,"x":{"b":1,"b":2},"a":2}`, dup: true},
		{name: "outer repeat after a nested object", doc: `{"a":1,"x":{"a":1},"A":2}`, dup: true},
		{name: "values are not keys", doc: `{"a":"a","b":["A","a"],"c":{"d":"A"}}`},
		{name: "structural bytes inside strings", doc: esc(`{"a":"}{][,:~"","{":"~~","[":1}`)},
		{name: "whitespace around keys", doc: " {\n\t\"a\" : 1 ,\r \"A\" : 2 } ", dup: true},
		{name: "16 keys, linear, distinct", doc: strictManyKeys("k", 16, "")},
		{name: "16 keys, linear, case repeat", doc: strictManyKeys("k", 15, `,"K3":0`), dup: true},
		{name: "17 keys, sorted, distinct", doc: strictManyKeys("k", 17, "")},
		{name: "17 keys, sorted, exact repeat", doc: strictManyKeys("k", 16, `,"k15":0`), dup: true},
		{name: "17 keys, sorted, case repeat", doc: strictManyKeys("k", 16, `,"K0":0`), dup: true},
		{name: "30 keys, sorted, Kelvin repeat", doc: strictManyKeys("k", 29, `,"`+strictKelvin+`5":0`), dup: true},
		{name: "40 keys, sorted, escaped repeat", doc: strictManyKeys("k", 40, esc(`,"~u004b39":0`)), dup: true},
		{name: "30 escaped keys, sorted, plain repeat", doc: strictManyKeys(esc("~u006b"), 30, `,"K29":0`), dup: true},
		{name: "100 keys, sorted, distinct", doc: strictManyKeys("k", 100, "")},
		{name: "100 keys in two prefixes, distinct", doc: strictManyKeys("k", 50, ","+strings.Trim(strictManyKeys("J", 50, ""), "{}"))},
		{name: "100 keys in two prefixes, case repeat", doc: strictManyKeys("k", 50, ","+strings.Trim(strictManyKeys("J", 50, ""), "{}")+`,"j49":0`), dup: true},
		{name: "nested sorted repeat", doc: `{"a":[{"b":` + strictManyKeys("k", 20, `,"K19":1`) + `}]}`, dup: true},
		{name: "two sibling 18-key objects", doc: strictArrayMember + strictManyKeys("k", 18, "") + `,` + strictManyKeys("K", 18, "") + `]}`},
		{name: "many open keys around nested objects", doc: strictManyKeys("k", 30, `,"z":`+strictManyKeys("K", 30, ""))},
	}
}

// strictValidCorpus returns valid JSON documents for the scanners: the fold
// cases and the shapes the scanners must skip correctly. The oracle decides
// what each one should be.
func strictValidCorpus() []string {
	folds := strictFoldCases()
	docs := make([]string, 0, 32+len(folds))
	docs = append(docs,
		`{}`, `[]`, `1`, `"a"`, jsonNull, strictTrue, ` { } `, "\t{\"a\":1}\r\n", `-0.5e-7`, `[{}]`,
		`{"a":1,"b":2}`, `{"a":{"a":{"a":1}}}`, `{"a":"b","b":"a"}`, `[{"a":1},{"A":1}]`,
		`[{"a":1,"b":{"c":1,"C":1}}]`, `{"x":[1,2,{"y":[{"z":1},{"Z":2}]}]}`, ` { "a" : 1 , "A" : 2 } `,
		`{"a":"a:","b":"b"}`, `{"a":["a",":"],"b":{"a":"b"}}`, testutil.Esc(`{"a":"~"","b":"~"","A~"":1}`),
		strictObjectMember+strictNest(50)+`,"b":{"c":`+strictNest(10)+`}}`,
		strictObjectMember+strictNest(strictValidDepth-1)+`}`,
		`{"email":"jane.doe@example.com","state":"deliverable","reason":"accepted_email","score":97}`,
		`{"id":"b1","emails":[{"email":"a@example.com"},{"email":"b@example.com"}],"total_counts":{"total":2}}`,
		"2"+strings.Repeat("0", 400), `{"a":1e999,"b":[-1e400,`+strings.Repeat("9", 400)+`]}`,
	)
	for _, c := range folds {
		docs = append(docs, c.doc)
	}
	return docs
}

// strictInvalidCorpus returns inputs that are not one valid JSON value, or not
// valid UTF-8: no scanner may panic on them.
func strictInvalidCorpus() []string {
	esc := testutil.Esc
	return []string{
		"", " ", "\n\t\r ", "{", "}", "[", "]", `"`, `\`, esc(`"~`), esc(`"~u12`), `{"a"`, `{"a":`,
		`{"a":1,`, `{"a" 1}`, `[1,`, `[,]`, `{,}`, `{"a":1]`, `[{"a":1}`, `]]]}}}`, `{{{{`, `"abc`, `tru`, `-`, `1e`,
		"\xff\xfe", esc(`{"a":"~`), esc(`{"~`), `:::`, `,,,`, `{"a":[}`, "\x00", `{"k":"v"}garbage`,
		`{"a":{"b":{"c":`, `[[[[[[`, `{"":""`, esc(`"~"~"~"`), `{"a":1,"a":`, `{} {}`, `{}x`, `{}]`, `1 2`,
		"\xef\xbb\xbf{}", "{\"a\":\"\xff\"}", "{\"a\":\"\xe2\x82\"}", "{}\xff", `{"a":01}`, `{"a":1.}`, `{"a":.5}`,
		`{'a':1}`, `{"a":NaN}`, `{"a":Infinity}`, `{"a":+1}`, "{\"a\":\"\x01\"}", `{"a":1,}`, `{"a" : }`,
		esc(`{"a":"~x"}`), esc(`{"a":"~u00zz"}`), esc(`{"~ud800":"~udc0"}`), "{\"\xc3\":1,\"\xc3\":2}",
		`{"a":1}}`, `{"a" "a"}`, esc(`{"~x":1,"~x":2}`), esc(`{"~u12":1,"~u12":2}`), `{"a":1,"a`,
		strictManyKeys("k", 40, `,"k7`), strictManyKeys("k", 40, `}}}`), "{\"\xed\xa0\x80\":1}",
		strictNest(strictValidDepth + 1), strictObjectMember + strictNest(strictValidDepth) + `}`,
	}
}

// strictSweep runs every scanner that accepts any input over data, the
// index-based ones at every index, so that a panic on any input fails the
// caller. Sweeping every index is quadratic, so only short inputs get it.
func strictSweep(data []byte) {
	names := verificationNames()
	_, _ = checkStrict(data)
	_ = invalidJSONCause(data)
	_, _ = firstValue(data)
	_, _ = countValues(data)
	_ = duplicateKey(data)
	_, _ = rescueBatchID(data)
	for k, v := range members(data) {
		_ = keyBytes(k)
		_ = appendFolded(nil, k)
		_ = foldsToID(k)
		_ = decodeKey(v)
	}
	for _, el := range elements(data) {
		_ = appendUnquoted(nil, el)
	}
	_ = readObject(data, names[:])
	_ = keyBytes(data)
	_ = appendUnquoted(nil, data)
	_ = appendFolded(nil, data)
	_ = isNull(data)
	_ = isString(data)
	if len(data) > 4<<10 {
		return
	}
	for i := 0; i <= len(data)+1; i++ {
		_ = valueEnd(data, i)
		_ = skipSpace(data, i)
		_ = isKey(data, i)
		_ = hexEscape(data, i)
		if i < len(data) {
			_ = stringEnd(data, i)
		}
	}
}

func TestStrictCheckStrict(t *testing.T) {
	esc := testutil.Esc
	tests := []struct {
		name  string
		data  string
		kind  FailureKind
		cause error
	}{
		{name: "valid object", data: `{"a":1,"b":[{"c":true}],"d":null}`},
		{name: "empty object", data: `{}`},
		{name: "empty object with spaces inside", data: `{ }`},
		{name: "surrounding whitespace", data: " \n\t{\"a\":1} \r\n"},
		{name: "nested objects and arrays", data: `{"a":{"b":{"c":[[],{},[{"d":[1,"x",null,false]}]]}}}`},
		{name: "keys equal only to string values", data: `{"a":"A","b":"a"}`},
		{name: "lone surrogate escapes", data: esc(`{"a":"~ud800","b":"~udfff","~udbff":1}`)},
		{name: "paired surrogate escape", data: esc(`{"a":"~ud83d~ude00","~ud83d~ude00":1}`)},

		{name: "invalid UTF-8 in a string", data: "{\"a\":\"\xff\"}", kind: FailureMalformedResponse, cause: errInvalidUTF8},
		{name: "invalid UTF-8 in a key", data: "{\"\xc3\":1}", kind: FailureMalformedResponse, cause: errInvalidUTF8},
		{name: "truncated multibyte sequence", data: "{\"a\":\"\xe2\x82\"}", kind: FailureMalformedResponse, cause: errInvalidUTF8},
		{name: "invalid UTF-8 after the object", data: "{}\xff", kind: FailureMalformedResponse, cause: errInvalidUTF8},
		{name: "encoded surrogate half", data: "{\"a\":\"\xed\xa0\x80\"}", kind: FailureMalformedResponse, cause: errInvalidUTF8},
		{name: "overlong encoding", data: "{\"a\":\"\xc0\xaf\"}", kind: FailureMalformedResponse, cause: errInvalidUTF8},
		{name: "invalid UTF-8 checked before JSON", data: "{\xff", kind: FailureMalformedResponse, cause: errInvalidUTF8},
		{name: "UTF-8 byte order mark", data: "\xef\xbb\xbf{}", kind: FailureMalformedResponse, cause: errInvalidJSON},
		{name: "empty body", data: "", kind: FailureMalformedResponse, cause: errInvalidJSON},
		{name: "whitespace-only body", data: " \n\t\r ", kind: FailureMalformedResponse, cause: errInvalidJSON},
		{name: "missing value", data: `{"a":}`, kind: FailureMalformedResponse, cause: errInvalidJSON},
		{name: "unterminated object", data: `{"a":1`, kind: FailureMalformedResponse, cause: errInvalidJSON},
		{name: "unterminated string", data: `{"a":"b`, kind: FailureMalformedResponse, cause: errInvalidJSON},
		{name: "trailing comma", data: `{"a":[1,]}`, kind: FailureMalformedResponse, cause: errInvalidJSON},
		{name: "leading zero", data: `{"a":01}`, kind: FailureMalformedResponse, cause: errInvalidJSON},
		{name: "single quotes", data: `{'a':1}`, kind: FailureMalformedResponse, cause: errInvalidJSON},
		{name: "raw control character", data: "{\"a\":\"\x01\"}", kind: FailureMalformedResponse, cause: errInvalidJSON},
		{name: "invalid escape", data: esc(`{"a":"~x"}`), kind: FailureMalformedResponse, cause: errInvalidJSON},
		{name: "duplicate in invalid JSON", data: `{"a":1,"a":2`, kind: FailureMalformedResponse, cause: errInvalidJSON},
		{
			name: "nesting beyond json.Valid's limit", data: strictObjectMember + strictNest(strictValidDepth) + `}`,
			kind: FailureMalformedResponse, cause: errInvalidJSON,
		},
		{name: "second object", data: `{} {}`, kind: FailureMalformedResponse, cause: errTrailingData},
		{name: "second object without space", data: `{}{}`, kind: FailureMalformedResponse, cause: errTrailingData},
		{name: "trailing garbage", data: `{}x`, kind: FailureMalformedResponse, cause: errTrailingData},
		{name: "trailing bracket", data: `{"a":1}]`, kind: FailureMalformedResponse, cause: errTrailingData},
		{name: "trailing NUL", data: "{}\x00", kind: FailureMalformedResponse, cause: errTrailingData},
		{name: "trailing comma after the object", data: `{"a":1} ,`, kind: FailureMalformedResponse, cause: errTrailingData},
		{name: "duplicate then trailing data", data: `{"a":1,"a":2} x`, kind: FailureMalformedResponse, cause: errTrailingData},
		{name: "two numbers", data: `1 2`, kind: FailureMalformedResponse, cause: errTrailingData},
		{name: "top-level array", data: `[{"a":1}]`, kind: FailureUnexpectedResponse, cause: errNotObject},
		{name: "top-level array with a duplicate", data: `[{"a":1,"A":2}]`, kind: FailureUnexpectedResponse, cause: errNotObject},
		{name: "top-level empty array", data: ` [] `, kind: FailureUnexpectedResponse, cause: errNotObject},
		{name: "top-level string", data: `"x"`, kind: FailureUnexpectedResponse, cause: errNotObject},
		{name: "top-level number", data: `42`, kind: FailureUnexpectedResponse, cause: errNotObject},
		{name: "top-level negative number", data: `-0.5e3`, kind: FailureUnexpectedResponse, cause: errNotObject},
		{name: "top-level null", data: jsonNull, kind: FailureUnexpectedResponse, cause: errNotObject},
		{name: "top-level true", data: ` true `, kind: FailureUnexpectedResponse, cause: errNotObject},
		{name: "top-level false", data: `false`, kind: FailureUnexpectedResponse, cause: errNotObject},
		{name: "duplicate at top level", data: `{"a":1,"a":2}`, kind: FailureMalformedResponse, cause: errDuplicateKey},
		{name: "case duplicate at top level", data: `{"email":"x","Email":"y"}`, kind: FailureMalformedResponse, cause: errDuplicateKey},
		{name: "duplicate in a nested object", data: `{"a":{"b":1,"c":2,"b":3}}`, kind: FailureMalformedResponse, cause: errDuplicateKey},
		{
			name: "duplicate in an object inside an array", data: `{"a":[{"x":1},{"y":1,"Y":2}]}`,
			kind: FailureMalformedResponse, cause: errDuplicateKey,
		},
		{
			name: "duplicate in an ignored subtree", data: `{"email":"x","extra":{"page":0,"PAGE":1}}`,
			kind: FailureMalformedResponse, cause: errDuplicateKey,
		},
		{name: "same key in sibling objects", data: `{"a":{"k":1},"b":{"k":2},"c":[{"k":3},{"K":4}]}`},
		{name: "same key at different depths", data: `{"k":{"K":{"k":1}}}`},
		{name: "a string value holding a colon", data: `{"a":"b:","b":["a",":"]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kind, cause := checkStrict([]byte(tt.data))
			assert.Equal(t, tt.kind, kind)
			if tt.cause == nil {
				require.NoError(t, cause)
				return
			}
			require.ErrorIs(t, cause, tt.cause)
		})
	}
}

func TestStrictInvalidJSONCause(t *testing.T) {
	tests := []struct {
		data string
		want error
	}{
		{``, errInvalidJSON},
		{` `, errInvalidJSON},
		{`{`, errInvalidJSON},
		{`{"a":1,}`, errInvalidJSON},
		{`[1,]`, errInvalidJSON},
		{`tru`, errInvalidJSON},
		{`{} {}`, errTrailingData},
		{`{}x`, errTrailingData},
		{`{"a":1} ,`, errTrailingData},
		{`[] {`, errTrailingData},
		{`1 2`, errTrailingData},
		{` {"a":[1]} }`, errTrailingData},
		{`00`, errTrailingData},
		{`01`, errTrailingData},
		{`truex`, errTrailingData},
		{`nullnull`, errTrailingData},
		{`-0.5e2x`, errTrailingData},
		{`"s"x`, errTrailingData},
		{`-`, errInvalidJSON},
		{`1.`, errInvalidJSON},
		{`x`, errInvalidJSON},
	}
	for _, tt := range tests {
		t.Run(tt.data, func(t *testing.T) {
			require.ErrorIs(t, invalidJSONCause([]byte(tt.data)), tt.want)
		})
	}
}

func TestStrictFirstValue(t *testing.T) {
	tests := []struct {
		data string
		want string
		ok   bool
	}{
		{data: `{"id":"x"}`, want: `{"id":"x"}`, ok: true},
		{data: "  \n{\"id\":\"x\"} x", want: `{"id":"x"}`, ok: true},
		{data: `{}{"a":1}`, want: `{}`, ok: true},
		{data: `[1] 2`, want: `[1]`, ok: true},
		{data: `"s"x`, want: `"s"`, ok: true},
		{data: `00`, want: `0`, ok: true},
		{data: " truex", want: `true`, ok: true},
		{data: `-0.5e2x`, want: `-0.5e2`, ok: true},
		{data: `-`},
		{data: `x`},
		{data: `{"id":"x"`},
		{data: ``},
		{data: `]`},
	}
	for _, tt := range tests {
		t.Run(tt.data, func(t *testing.T) {
			got, ok := firstValue([]byte(tt.data))
			assert.Equal(t, tt.ok, ok)
			if !tt.ok {
				assert.Nil(t, got)
				return
			}
			assert.Equal(t, []byte(tt.want), got)
		})
	}

	t.Run("aliases the body, copying nothing", func(t *testing.T) {
		testutil.SkipUnderRace(t)
		data := []byte("  " + strings.Repeat(`{"a":[1,2,3]}`, 1) + ` trailing`)
		got, ok := firstValue(data)
		require.True(t, ok)
		assert.Same(t, &data[2], &got[0])
		assert.Zero(t, testing.AllocsPerRun(100, func() { _, _ = firstValue(data) }))
	})
}

// TestStrictTooManyValues proves the whole-body bounds at their exact edges,
// with bodies built in code: 100,000 containers and 1,000,000 values pass, and
// one more of either fails as response_too_large.
func TestStrictTooManyValues(t *testing.T) {
	tests := []struct {
		name       string
		body       []byte
		containers int
		values     int
		tooLarge   bool
	}{
		{
			name: "containers at the bound", body: strictFill(`{}`, maxContainers-2),
			containers: maxContainers, values: maxContainers,
		},
		{
			name: "one container over", body: strictFill(`[]`, maxContainers-1),
			containers: maxContainers + 1, values: maxContainers + 1, tooLarge: true,
		},
		{
			name: "nested containers at the bound", body: strictFill(`[[]]`, (maxContainers-2)/2),
			containers: maxContainers, values: maxContainers,
		},
		{
			name: "nested containers one over", body: strictFill(`[[]]`, (maxContainers-2)/2+1),
			containers: maxContainers + 2, values: maxContainers + 2, tooLarge: true,
		},
		{
			name: "values at the bound", body: strictFill(`0`, maxValues-2),
			containers: 2, values: maxValues,
		},
		{
			name: "one value over", body: strictFill(`0`, maxValues-1),
			containers: 2, values: maxValues + 1, tooLarge: true,
		},
		{
			name: "string values at the bound", body: strictFill(`""`, maxValues-2),
			containers: 2, values: maxValues,
		},
		{
			name: "literal values one over", body: strictFill(jsonNull, maxValues-1),
			containers: 2, values: maxValues + 1, tooLarge: true,
		},
		{
			name: "keys are not values", body: append(bytes.TrimSuffix(strictFill(`1`, maxValues-4), []byte("}")), `,"b":0,"c":0}`...),
			containers: 2, values: maxValues,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := tt.body
			require.True(t, json.Valid(body))
			containers, values := countValues(body)
			assert.Equal(t, tt.containers, containers)
			assert.Equal(t, tt.values, values)
			kind, cause := checkStrict(body)
			if !tt.tooLarge {
				assert.Empty(t, kind)
				require.NoError(t, cause)
				return
			}
			assert.Equal(t, FailureResponseTooLarge, kind)
			require.ErrorIs(t, cause, errTooManyValues)
		})
	}
}

// TestStrictCheckOrder proves the order of checkStrict's checks: UTF-8, then
// syntax, then the top-level type, then the bounds, then duplicate keys.
func TestStrictCheckOrder(t *testing.T) {
	overValues := strictFill(`0`, maxValues-1)
	tests := []struct {
		name  string
		data  []byte
		kind  FailureKind
		cause error
	}{
		{name: "invalid UTF-8 beats invalid JSON", data: []byte("[\xff"), kind: FailureMalformedResponse, cause: errInvalidUTF8},
		{name: "invalid JSON beats not an object", data: []byte(`[1,]`), kind: FailureMalformedResponse, cause: errInvalidJSON},
		{
			name: "not an object beats the bounds", data: []byte("[" + strings.Repeat("[],", maxContainers) + "[]]"),
			kind: FailureUnexpectedResponse, cause: errNotObject,
		},
		{
			name: "trailing data beats the bounds", data: []byte(strictNest(1) + strings.Repeat(",[]", maxContainers)),
			kind: FailureMalformedResponse, cause: errTrailingData,
		},
		{
			name: "the bounds beat a duplicate key", data: append(bytes.TrimSuffix(overValues, []byte("}")), `,"A":1}`...),
			kind: FailureResponseTooLarge, cause: errTooManyValues,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kind, cause := checkStrict(tt.data)
			assert.Equal(t, tt.kind, kind)
			require.ErrorIs(t, cause, tt.cause)
		})
	}
}

func TestStrictCountValues(t *testing.T) {
	tests := []struct {
		data               string
		containers, values int
	}{
		{`{}`, 1, 1},
		{`[]`, 1, 1},
		{`{"a":1}`, 1, 2},
		{`{"a":"b"}`, 1, 2},
		{` { "a" : [ 1 , "x" , true , null , { } , [ ] ] } `, 4, 8},
		{`[{"a":1,"b":2},"c",{"d":"e"}]`, 3, 7},
		{testutil.Esc(`{"a~"]":"~"}{","b":-1.5e+3}`), 1, 3},
		{`{"a":{"b":{"c":[]}},"d":false}`, 4, 5},
		{`{"a":[{"b":"c"},"d"],"e":"f"}`, 3, 6},
		{`[[["a","b"]],{"c":{"d":["e"]}}]`, 6, 9},
		{"{\"a\"\n:\t1\r,\"b\" : [ ] }", 2, 3},
		{strictManyKeys("k", 20, ""), 1, 21},
		{`"a"`, 0, 1},
		{`-0.5e-7`, 0, 1},
		{strictTrue, 0, 1},
	}
	for _, tt := range tests {
		t.Run(tt.data, func(t *testing.T) {
			containers, values := countValues([]byte(tt.data))
			assert.Equal(t, tt.containers, containers, "containers")
			assert.Equal(t, tt.values, values, "values")
			wantContainers, wantValues := strictOracleCount([]byte(tt.data))
			assert.Equal(t, wantContainers, containers, "the oracle's containers")
			assert.Equal(t, wantValues, values, "the oracle's values")
		})
	}
}

// TestStrictDuplicateKeyFolding checks every fold case three ways: against
// its expected verdict, against the encoding/json token-walk oracle with
// independent folding, and through checkStrict.
func TestStrictDuplicateKeyFolding(t *testing.T) {
	for _, tt := range strictFoldCases() {
		t.Run(tt.name, func(t *testing.T) {
			data := []byte(tt.doc)
			valid, oracleDup := strictOracle(data)
			require.True(t, valid, "the case must be valid JSON in valid UTF-8")
			assert.Equal(t, tt.dup, oracleDup, "the oracle's verdict")
			assert.Equal(t, tt.dup, duplicateKey(data), "duplicateKey's verdict")
			kind, cause := checkStrict(data)
			if !tt.dup {
				assert.Empty(t, kind)
				require.NoError(t, cause)
				return
			}
			assert.Equal(t, FailureMalformedResponse, kind)
			require.ErrorIs(t, cause, errDuplicateKey)
		})
	}
}

// strictFoldRunes returns the runes the encoding/json comparison spells keys
// and field tags with: the letters and digits of ASCII, the punctuation a tag
// name may hold, and the letters whose simple folding leaves ASCII, stops
// short of it, or spans more than two runes.
func strictFoldRunes() []rune {
	runes := []rune("!#$%&()*+-./:;<=>?@[]^_{|}~ ")
	for c := rune('0'); c <= 'z'; c++ {
		if unicode.IsLetter(c) || unicode.IsDigit(c) {
			runes = append(runes, c)
		}
	}
	return append(runes,
		0x212A, 0x017F, 0x0130, 0x0131, 0x00B5, 0x039C, 0x03BC, 0x03A3, 0x03C2, 0x03C3, 0x212B, 0x00C5,
		0x00E5, 0x2126, 0x03A9, 0x03C9, 0x00DF, 0x1E9E, 0x01C4, 0x01C5, 0x01C6, 0x13A0, 0xAB70, 0x00E9,
		0x00C9, 0x0398, 0x03D1, 0x03F4, 0x1E60, 0x1E9B, 0x10400, 0x10428,
	)
}

// strictEscapeRune spells r as JSON escapes: one \uXXXX, or a surrogate pair.
func strictEscapeRune(r rune) string {
	if r1, r2 := utf16.EncodeRune(r); r1 != utf8.RuneError {
		return `\u` + strictHex4(r1) + `\u` + strictHex4(r2)
	}
	return `\u` + strictHex4(r)
}

// strictHex4 formats r as four lowercase hexadecimal digits.
func strictHex4(r rune) string {
	s := strconv.FormatInt(int64(r), 16)
	return strings.Repeat("0", 4-len(s)) + s
}

// strictFieldType returns a struct type with one int field whose JSON name is
// name, caching the types it builds.
func strictFieldType(cache map[string]reflect.Type, name string) reflect.Type {
	if typ, ok := cache[name]; ok {
		return typ
	}
	typ := reflect.StructOf([]reflect.StructField{{
		Name: "F", Type: reflect.TypeFor[int](), Tag: reflect.StructTag(`json:"` + name + `"`),
	}})
	cache[name] = typ
	return typ
}

// strictMatches reports whether encoding/json, unmarshaling {<key>:1} into a
// struct whose one field is named field, sets the field.
func strictMatches(tb testing.TB, cache map[string]reflect.Type, key, field string) bool {
	tb.Helper()
	v := reflect.New(strictFieldType(cache, field))
	require.NoError(tb, json.Unmarshal([]byte(`{`+key+`:1}`), v.Interface()))
	return v.Elem().Field(0).Int() == 1
}

// TestStrictFoldMatchesEncodingJSON shows that duplicateKey's folding is
// encoding/json's own: for every pair of keys spelled from the fold runes,
// raw or escaped, encoding/json matches one to a field tagged with the other
// exactly when duplicateKey reports an object holding both as a repeat. Each
// key is prefixed with x, so that no tag is "-" or empty.
func TestStrictFoldMatchesEncodingJSON(t *testing.T) {
	runes := strictFoldRunes()
	cache := map[string]reflect.Type{}
	pairs, matched := 0, 0
	for _, b := range runes {
		field := "x" + string(b)
		require.True(t, strictMatches(t, cache, testutil.JSONQuote(field), field), "the tag %q must be a valid JSON name", field)
		for _, a := range runes {
			for _, key := range []string{testutil.JSONQuote("x" + string(a)), `"x` + strictEscapeRune(a) + `"`} {
				match := strictMatches(t, cache, key, field)
				doc := []byte(`{` + key + `:1,` + testutil.JSONQuote(field) + `:2}`)
				require.Equal(t, match, duplicateKey(doc), "encoding/json and duplicateKey disagree on %s vs %q", key, field)
				var decoded string
				require.NoError(t, json.Unmarshal([]byte(key), &decoded))
				require.Equal(t, match, strictFold(decoded) == strictFold(field), "the oracle disagrees on %s vs %q", key, field)
				pairs++
				if match {
					matched++
				}
			}
		}
	}
	t.Logf("%d key pairs compared, %d matched", pairs, matched)
	assert.Greater(t, matched, len(runes)*2, "the comparison must include case-insensitive matches")
}

// TestStrictFoldRuneMatchesOrbitMinimum checks foldRune against the oracle
// for every rune: the smallest rune of its simple case-folding orbit.
func TestStrictFoldRuneMatchesOrbitMinimum(t *testing.T) {
	mismatches := 0
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if utf16.IsSurrogate(r) {
			continue
		}
		if got, want := foldRune(r), strictOrbitMin(r); got != want {
			mismatches++
			assert.Equal(t, want, got, "foldRune(%U)", r)
		}
	}
	assert.Zero(t, mismatches)
}

// TestStrictOracleFoldIsEqualFold checks the oracle's own folding against
// strings.EqualFold, the bytes.EqualFold encoding/json documents its matching
// as.
func TestStrictOracleFoldIsEqualFold(t *testing.T) {
	runes := strictFoldRunes()
	for _, a := range runes {
		for _, b := range runes {
			x, y := "k"+string(a)+"!", "K"+string(b)+"!"
			require.Equal(t, strings.EqualFold(x, y), strictFold(x) == strictFold(y), "%q vs %q", x, y)
		}
	}
}

func TestStrictAppendFolded(t *testing.T) {
	esc := testutil.Esc
	tests := []struct {
		name string
		lit  string
		want string
	}{
		{name: "lower case", lit: `"email"`, want: "EMAIL"},
		{name: "upper case", lit: `"EMAIL"`, want: "EMAIL"},
		{name: "digits and punctuation", lit: `"a_b-1.c"`, want: "A_B-1.C"},
		{name: "escaped letters", lit: esc(`"~u0065m~u0041il"`), want: "EMAIL"},
		{name: "empty", lit: `""`, want: ""},
		{name: "Kelvin sign", lit: `"` + strictKelvin + `"`, want: "K"},
		{name: "long s", lit: `"` + strictLongS + `tate"`, want: "STATE"},
		{name: "sigmas", lit: "\"\u03c3\u03c2\u03a3\"", want: "\u03a3\u03a3\u03a3"},
		{name: "dotted capital I unchanged", lit: `"` + strictDotI + `d"`, want: strictDotI + "D"},
		{name: "dotless i unchanged", lit: `"` + strictDotlessI + `d"`, want: strictDotlessI + "D"},
		{name: "micro sign is the orbit's smallest", lit: "\"\u03bc\u039c" + strictMicro + "\"", want: strictMicro + strictMicro + strictMicro},
		{name: "e acute", lit: "\"\u00e9\"", want: "\u00c9"},
		{name: "digraphs", lit: "\"\u01c4\u01c5\u01c6\"", want: "\u01c4\u01c4\u01c4"},
		{name: "ASCII after a non-ASCII rune", lit: "\"\u00e9mail\"", want: "\u00c9MAIL"},
		{name: "supplementary letter from escapes", lit: esc(`"~ud801~udc28"`), want: "\U00010400"},
		{name: "lone surrogate", lit: esc(`"a~ud800"`), want: "A" + strictFFFD},
		{name: "escaped quote and backslash", lit: esc(`"a~"~~b"`), want: "A\"\\B"},
		{name: "emoji", lit: `"` + strictEmoji + `x"`, want: strictEmoji + "X"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lit := []byte(tt.lit)
			original := bytes.Clone(lit)
			assert.Equal(t, tt.want, string(appendFolded(nil, lit)))
			assert.Equal(t, "pre:"+tt.want, string(appendFolded([]byte("pre:"), lit)), "dst before the key is left as it is")
			assert.Equal(t, original, lit, "the literal is never modified")
			var decoded string
			require.NoError(t, json.Unmarshal(lit, &decoded))
			assert.Equal(t, strictFold(decoded), string(appendFolded(nil, lit)), "folds as the oracle does")
		})
	}
}

func TestStrictFoldsToID(t *testing.T) {
	esc := testutil.Esc
	for _, lit := range []string{`"id"`, `"ID"`, `"Id"`, `"iD"`, esc(`"~u0069d"`), esc(`"~u0049~u0044"`), esc(`"i~u0044"`)} {
		assert.True(t, foldsToID([]byte(lit)), lit)
	}
	for _, lit := range []string{
		`"i"`, `"idd"`, `"id "`, `" id"`, `""`, `"di"`, `"` + strictDotlessI + `d"`, `"` + strictDotI + `D"`,
		esc(`"~u0131d"`), esc(`"id~u0000"`), `"batch_id"`,
	} {
		assert.False(t, foldsToID([]byte(lit)), lit)
	}
}

func TestStrictKeySetRepeated(t *testing.T) {
	for n := 1; n <= 40; n++ {
		t.Run(strconv.Itoa(n), func(t *testing.T) {
			var ks keySet
			for i := range n {
				ks.add([]byte(`"k` + strconv.Itoa(i) + `"`))
			}
			assert.False(t, ks.repeated(0), "%d distinct keys", n)

			for at := range n {
				var dup keySet
				for i := range n {
					dup.add([]byte(`"k` + strconv.Itoa(i) + `"`))
				}
				dup.add([]byte(`"K` + strconv.Itoa(at) + `"`))
				dup.add([]byte(`"z"`))
				assert.False(t, dup.repeated(n), "only keys from start count")
				assert.True(t, dup.repeated(0), "%d keys and a repeat of key %d", n, at)
			}
		})
	}
}

// TestStrictScannersMatchOracle compares the hand-written scanners with the
// encoding/json token walk on every corpus document: validity, duplicate keys
// and the value count must agree.
func TestStrictScannersMatchOracle(t *testing.T) {
	docs := make([]string, 0, 256)
	docs = append(docs, strictValidCorpus()...)
	docs = append(docs, strictInvalidCorpus()...)
	for _, s := range docs {
		data := []byte(s)
		kind, cause := checkStrict(data)
		valid, oracleDup := strictOracle(data)
		require.Equal(t, !valid, strictSyntaxCause(cause), "validity of %q", data)
		if !valid {
			assert.Equal(t, FailureMalformedResponse, kind, "%q", data)
			assert.Equal(t, !utf8.Valid(data), errors.Is(cause, errInvalidUTF8), "%q", data)
			continue
		}
		assert.Equal(t, oracleDup, duplicateKey(data), "duplicateKey of %q", data)
		wantContainers, wantValues := strictOracleCount(data)
		containers, values := countValues(data)
		assert.Equal(t, wantContainers, containers, "containers of %q", data)
		assert.Equal(t, wantValues, values, "values of %q", data)
		if data[skipSpace(data, 0)] != '{' {
			assert.Equal(t, FailureUnexpectedResponse, kind)
			require.ErrorIs(t, cause, errNotObject)
			continue
		}
		assert.Equal(t, oracleDup, errors.Is(cause, errDuplicateKey), "%q", data)
		if cause == nil {
			var decoded map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(data, &decoded))
			assert.Len(t, strictMembers(data), len(decoded), "members sees every member of %q once", data)
		}
	}
}

func TestStrictScannersNeverPanic(t *testing.T) {
	inputs := append(strictInvalidCorpus(), strictValidCorpus()...)
	for _, s := range inputs {
		data := []byte(s)
		assert.NotPanics(t, func() { strictSweep(data) }, "%q", data)
	}
	assert.NotPanics(t, func() { strictSweep(nil) })
}

// strictMember is one member as members yields it, with the key decoded.
type strictMember struct{ key, value string }

// strictMembers collects what members yields.
func strictMembers(data []byte) []strictMember {
	var out []strictMember
	for k, v := range members(data) {
		out = append(out, strictMember{key: decodeKey(k), value: string(v)})
	}
	return out
}

// strictElements collects what elements yields, checking that each element
// comes with its position as its index.
func strictElements(tb testing.TB, data []byte) []string {
	tb.Helper()
	var out []string
	for i, el := range elements(data) {
		assert.Equal(tb, len(out), i, "the index of %q", el)
		out = append(out, string(el))
	}
	return out
}

func TestStrictMembers(t *testing.T) {
	data := []byte(" {\n \"a\" : 1 , \"b\":\"x,y\" ,\"c\":{\"d\":[1,2]}, \"e\":[ ], \"f\":null, \"g\":true, " +
		testutil.Esc(`"~u0068":-1.5e3, "i~"":"~"}"`) + " } ")
	want := []strictMember{
		{"a", `1`},
		{"b", `"x,y"`},
		{"c", `{"d":[1,2]}`},
		{"e", `[ ]`},
		{"f", jsonNull},
		{"g", strictTrue},
		{"h", `-1.5e3`},
		{`i"`, testutil.Esc(`"~"}"`)},
	}
	assert.Equal(t, want, strictMembers(data))
	assert.Empty(t, strictMembers([]byte(`{}`)))
	assert.Empty(t, strictMembers([]byte(` { } `)))
	assert.Nil(t, strictMembers([]byte(`[1]`)))
	assert.Nil(t, strictMembers(nil))
	assert.Equal(t, []strictMember{{"a", "1"}, {"A", "2"}}, strictMembers([]byte(`{"a":1,"A":2}`)), "members never folds or drops keys")

	// Malformed input yields what it can and stops.
	assert.Equal(t, []strictMember{{"a", "1"}}, strictMembers([]byte(`{"a":1,`)))
	assert.Equal(t, []strictMember{{"a", "1"}}, strictMembers([]byte(`{"a":1 "b":2}`)))
	assert.Equal(t, []strictMember{{"a", "1"}}, strictMembers([]byte(`{"a":1,}`)))
	assert.Equal(t, []strictMember{{"a", ""}}, strictMembers([]byte(`{"a":`)))
	assert.Equal(t, []strictMember{{"a", "{"}}, strictMembers([]byte(`{"a":{`)))
	assert.Nil(t, strictMembers([]byte(`{"a" 1}`)))
	assert.Nil(t, strictMembers([]byte(`{1:2}`)))
	assert.Nil(t, strictMembers([]byte(`{"a"`)))
	assert.Nil(t, strictMembers([]byte(` x`)))
	assert.Nil(t, strictMembers([]byte(`{`)))

	// Breaking out of the loop stops the walk.
	n := 0
	for range members(data) {
		n++
		break
	}
	assert.Equal(t, 1, n)
	n = 0
	for k := range members(data) {
		n++
		if string(k) == `"c"` {
			break
		}
	}
	assert.Equal(t, 3, n, "a break after the third member stops there")
}

func TestStrictMembersAliasData(t *testing.T) {
	data := []byte(`{"key":"value"}`)
	for k, v := range members(data) {
		assert.Same(t, &data[1], &k[0], "the key aliases data")
		assert.Same(t, &data[7], &v[0], "the value aliases data")
	}
}

func TestStrictElements(t *testing.T) {
	data := []byte(` [ 1 , "a]" , {"b":[2]} , [3,[4]] , null , true , -0.5 ] `)
	assert.Equal(t, []string{`1`, `"a]"`, `{"b":[2]}`, `[3,[4]]`, jsonNull, strictTrue, `-0.5`}, strictElements(t, data))
	assert.Empty(t, strictElements(t, []byte(`[]`)))
	assert.Empty(t, strictElements(t, []byte(`[ ]`)))
	assert.Nil(t, strictElements(t, []byte(`{}`)))
	assert.Nil(t, strictElements(t, nil))

	// Malformed input yields what it can and stops.
	assert.Equal(t, []string{"1"}, strictElements(t, []byte(`[1,`)))
	assert.Equal(t, []string{"1"}, strictElements(t, []byte(`[1 2]`)))
	assert.Equal(t, []string{"1"}, strictElements(t, []byte(`[1,]`)))
	assert.Equal(t, []string{""}, strictElements(t, []byte(`[,]`)))
	assert.Equal(t, []string{"[1"}, strictElements(t, []byte(`[[1`)))
	assert.Nil(t, strictElements(t, []byte(`[`)))

	// Breaking out of the loop stops the walk.
	n := 0
	for range elements(data) {
		n++
		break
	}
	assert.Equal(t, 1, n)
	n = 0
	for i := range elements(data) {
		n++
		if i == 3 {
			break
		}
	}
	assert.Equal(t, 4, n, "a break after the fourth element stops there")
}

func TestStrictValueEnd(t *testing.T) {
	tests := []struct {
		data string
		end  int
	}{
		{`"xyz",`, 5},
		{testutil.Esc(`"a~"b",`), 6},
		{testutil.Esc(`"a~~",`), 5},
		{`{"a":"}"},`, 9},
		{`[1,[2,"]"],3] `, 13},
		{`{"a":{"b":[]}}]`, 14},
		{`{"a":"{[}"}x`, 11},
		{`123,`, 3},
		{`-1.5e+3}`, 7},
		{`true]`, 4},
		{`null `, 4},
		{`false:`, 5},
		{"1\n", 1},
		{"1\t", 1},
		{"1\r", 1},
		{`1`, 1},
		{`{"a":`, 5},
		{`"abc`, 4},
		{`[[`, 2},
		{`}`, 0},
		{`,`, 0},
		{``, 0},
	}
	for _, tt := range tests {
		t.Run(tt.data, func(t *testing.T) {
			assert.Equal(t, tt.end, valueEnd([]byte(tt.data), 0))
		})
	}
	assert.Equal(t, 3, valueEnd([]byte("xyz"), 5), "an index past the end returns the length")
	assert.Equal(t, 6, valueEnd([]byte(`{"a":1}`), 5), "a value inside a document")
}

func TestStrictStringEnd(t *testing.T) {
	tests := []struct {
		data string
		end  int
	}{
		{`""`, 2},
		{`"a"x`, 3},
		{testutil.Esc(`"~""`), 4},
		{testutil.Esc(`"~~"`), 4},
		{testutil.Esc(`"~~~""`), 6},
		{testutil.Esc(`"~u0022"`), 8},
		{testutil.Esc(`"~/"`), 4},
		{"\"\u00e9\"", 4},
		{`"abc`, 4},
		{testutil.Esc(`"~`), 2},
		{`"`, 1},
	}
	for _, tt := range tests {
		t.Run(tt.data, func(t *testing.T) {
			assert.Equal(t, tt.end, stringEnd([]byte(tt.data), 0))
		})
	}
	assert.Equal(t, 9, stringEnd([]byte(`{"a":"bc"}`), 5), "a string inside a document")
	assert.Equal(t, 3, stringEnd([]byte(`abc`), 7), "an index past the end returns the length")
}

func TestStrictSkipSpace(t *testing.T) {
	assert.Equal(t, 4, skipSpace([]byte(" \t\n\rx"), 0))
	assert.Equal(t, 0, skipSpace([]byte("x "), 0))
	assert.Equal(t, 2, skipSpace([]byte("  "), 0))
	assert.Equal(t, 3, skipSpace([]byte("x  "), 1))
	assert.Equal(t, 1, skipSpace([]byte("x\u00a0"), 1), "only JSON whitespace is skipped")
	assert.Equal(t, 0, skipSpace([]byte("\vx"), 0), "only JSON whitespace is skipped")
	assert.Equal(t, 0, skipSpace([]byte("\fx"), 0), "only JSON whitespace is skipped")
	assert.Equal(t, 5, skipSpace([]byte("xyz"), 5))
	assert.Equal(t, 0, skipSpace(nil, 0))
}

func TestStrictIsNullIsStringIsKey(t *testing.T) {
	assert.True(t, isNull(nil))
	assert.True(t, isNull([]byte{}))
	assert.True(t, isNull([]byte(jsonNull)))
	assert.False(t, isNull([]byte("nul")))
	assert.False(t, isNull([]byte("null ")))
	assert.False(t, isNull([]byte(`"null"`)))
	assert.False(t, isNull([]byte("0")))

	assert.True(t, isString([]byte(`""`)))
	assert.True(t, isString([]byte(`"a"`)))
	assert.False(t, isString([]byte(`"`)))
	assert.False(t, isString([]byte(`1`)))
	assert.False(t, isString([]byte(jsonNull)))
	assert.False(t, isString(nil))

	data := []byte(`{"a" : "b", "c":"d", "e"` + "\n\t:" + `["f"]}`)
	assert.True(t, isKey(data, 4), `"a" is a key`)
	assert.False(t, isKey(data, 10), `"b" is a value`)
	assert.True(t, isKey(data, 15), `"c" is a key`)
	assert.False(t, isKey(data, 19), `"d" is a value`)
	assert.True(t, isKey(data, 24), `"e" is a key across whitespace`)
	assert.False(t, isKey(data, 31), `"f" is an element`)
	assert.False(t, isKey(data, len(data)))
	assert.False(t, isKey(data, len(data)+3))
}

func TestStrictKeyBytes(t *testing.T) {
	lit := []byte(`"email"`)
	got := keyBytes(lit)
	assert.Equal(t, "email", string(got))
	assert.Same(t, &lit[1], &got[0], "a key without escapes is returned in place")
	assert.Len(t, got, cap(got), "the in-place key cannot be appended into the body")

	escaped := []byte(testutil.Esc(`"~u0065mail"`))
	dec := keyBytes(escaped)
	assert.Equal(t, "email", string(dec))
	assert.NotSame(t, &escaped[1], &dec[0], "an escaped key is decoded into a copy")
	assert.Equal(t, testutil.Esc(`"~u0065mail"`), string(escaped), "decoding leaves the literal unchanged")

	assert.Empty(t, keyBytes([]byte(`""`)))
	assert.Empty(t, keyBytes([]byte(`"`)))
	assert.Empty(t, keyBytes(nil))
	assert.Equal(t, "a\"b", string(keyBytes([]byte(testutil.Esc(`"a~"b"`)))))
	assert.Equal(t, "\u00e9", string(keyBytes([]byte("\"\u00e9\""))))
	assert.Equal(t, "Email", string(keyBytes([]byte(`"Email"`))), "keys are never folded")

	assert.True(t, unescaped([]byte(`""`)))
	assert.True(t, unescaped([]byte(`"a/b"`)))
	assert.False(t, unescaped([]byte(testutil.Esc(`"a~/b"`))))
	assert.False(t, unescaped([]byte(`"`)))
	assert.False(t, unescaped(nil))
}

func TestStrictScannerAllocations(t *testing.T) {
	testutil.SkipUnderRace(t)
	plain := []byte(`"email"`)
	escaped := []byte(testutil.Esc(`"~u0065mail"`))
	doc := []byte(`{"email":"jane.doe@example.com","state":"deliverable","score":97,"free":false,"tag":null}`)
	arr := []byte(`["a","b",1,{"a":2},[3]]`)
	names := verificationNames()
	var sink int
	testutil.CheckCeilings(t, []testutil.Ceiling{
		{Name: "keyBytes without escapes", Max: 0, Run: func() { sink += len(keyBytes(plain)) }},
		{Name: "keyBytes with escapes", Max: 1, Run: func() { sink += len(keyBytes(escaped)) }},
		{Name: "members", Max: 0, Run: func() {
			for k, v := range members(doc) {
				sink += len(k) + len(v)
			}
		}},
		{Name: "elements", Max: 0, Run: func() {
			for _, el := range elements(arr) {
				sink += len(el)
			}
		}},
		{Name: "readObject", Max: 0, Run: func() { sink += len(readObject(doc, names[:]).fields[verEmail]) }},
		{Name: "valueEnd", Max: 0, Run: func() { sink += valueEnd(doc, 0) }},
		{Name: "skipSpace and isKey", Max: 0, Run: func() {
			if isKey(doc, 8) {
				sink += skipSpace(doc, 0)
			}
		}},
		{Name: "countValues", Max: 1, Run: func() {
			c, v := countValues(doc)
			sink += c + v
		}},
	})
	assert.Positive(t, sink)
}

func TestStrictAppendUnquoted(t *testing.T) {
	esc := testutil.Esc
	tests := []struct {
		name string
		lit  string
		want string
	}{
		{name: "plain", lit: `"email"`, want: "email"},
		{name: "empty literal", lit: `""`, want: ""},
		{name: "quote escape", lit: esc(`"~""`), want: `"`},
		{name: "backslash escape", lit: esc(`"~~"`), want: `\`},
		{name: "solidus escape", lit: esc(`"~/"`), want: "/"},
		{name: "backspace escape", lit: esc(`"~b"`), want: "\b"},
		{name: "form feed escape", lit: esc(`"~f"`), want: "\f"},
		{name: "newline escape", lit: esc(`"~n"`), want: "\n"},
		{name: "carriage return escape", lit: esc(`"~r"`), want: "\r"},
		{name: "tab escape", lit: esc(`"~t"`), want: "\t"},
		{name: "every two-byte escape", lit: esc(`"~"~~~/~b~f~n~r~t"`), want: "\"\\/\b\f\n\r\t"},
		{name: "lowercase hex", lit: esc(`"~u00e9"`), want: "\u00e9"},
		{name: "uppercase hex", lit: esc(`"~u00E9"`), want: "\u00e9"},
		{name: "NUL escape", lit: esc(`"a~u0000b"`), want: "a\x00b"},
		{name: "DEL escape", lit: esc(`"~u007f"`), want: "\x7f"},
		{name: "three-byte rune", lit: esc(`"~u20ac"`), want: "\u20ac"},
		{name: "line separator", lit: esc(`"~u2028"`), want: "\u2028"},
		{name: "highest BMP rune", lit: esc(`"~uffff"`), want: "\uffff"},
		{name: "escaped U+FFFD", lit: esc(`"~ufffd"`), want: strictFFFD},
		{name: "surrogate pair", lit: esc(`"~ud83d~ude00"`), want: strictEmoji},
		{name: "surrogate pair in mixed case", lit: esc(`"~uD83d~uDe00"`), want: strictEmoji},
		{name: "lowest pair", lit: esc(`"~ud800~udc00"`), want: "\U00010000"},
		{name: "highest pair", lit: esc(`"~udbff~udfff"`), want: "\U0010FFFF"},
		{name: "lone high", lit: esc(`"~ud800"`), want: strictFFFD},
		{name: "lone low", lit: esc(`"~udc00"`), want: strictFFFD},
		{name: "lone high at the end of text", lit: esc(`"x~udbff"`), want: "x" + strictFFFD},
		{name: "lone high then text", lit: esc(`"~udbffx"`), want: strictFFFD + "x"},
		{name: "high then a non-surrogate escape", lit: esc(`"~ud800~u0041"`), want: strictFFFD + "A"},
		{name: "high then a two-byte escape", lit: esc(`"~ud800~n"`), want: strictFFFD + "\n"},
		{name: "high then a high", lit: esc(`"~ud800~udbff"`), want: strictFFFD + strictFFFD},
		{name: "high then a valid pair", lit: esc(`"~ud800~ud800~udc00"`), want: strictFFFD + "\U00010000"},
		{name: "reversed pair", lit: esc(`"~ude00~ud83d"`), want: strictFFFD + strictFFFD},
		{name: "two lone lows", lit: esc(`"~udc00~udfff"`), want: strictFFFD + strictFFFD},
		{name: "raw multibyte kept", lit: "\"\u00e9\u20ac" + strictEmoji + "\"", want: "\u00e9\u20ac" + strictEmoji},
		{name: "raw U+FFFD kept", lit: `"` + strictFFFD + `"`, want: strictFFFD},
		{name: "escaped backslash before u", lit: esc(`"~~u0041"`), want: esc(`~u0041`)},

		// Malformed escapes, which a valid document never has, stop decoding.
		{name: "unknown escape stops", lit: esc(`"ab~xcd"`), want: "ab"},
		{name: "upper-case U escape stops", lit: esc(`"ab~U0041"`), want: "ab"},
		{name: "truncated unicode escape stops", lit: esc(`"ab~u12"`), want: "ab"},
		{name: "bad hex digit stops", lit: esc(`"ab~u12g4cd"`), want: "ab"},
		{name: "backslash at the end stops", lit: esc(`"ab~"`), want: "ab"},
		{name: "high then a truncated escape", lit: esc(`"~ud800~u12"`), want: strictFFFD},
		{name: "too short", lit: `"`, want: ""},
		{name: "nothing", lit: ``, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lit := []byte(tt.lit)
			assert.Equal(t, tt.want, string(appendUnquoted(nil, lit)))
			assert.Equal(t, "pre:"+tt.want, string(appendUnquoted([]byte("pre:"), lit)), "appends after dst")
			assert.Equal(t, tt.want, decodeKey(lit))
			assert.Equal(t, tt.want, string(keyBytes(lit)))
			if json.Valid(lit) {
				var want string
				require.NoError(t, json.Unmarshal(lit, &want))
				assert.Equal(t, want, decodeKey(lit), "decodes as encoding/json does")
			}
		})
	}
}

// TestStrictEveryUnicodeEscape decodes every \uXXXX escape, alone and after a
// high surrogate, and a grid of surrogate pairs, exactly as encoding/json
// does.
func TestStrictEveryUnicodeEscape(t *testing.T) {
	check := func(lit string) {
		var want string
		require.NoError(t, json.Unmarshal([]byte(lit), &want), lit)
		require.Equal(t, want, decodeKey([]byte(lit)), "decodeKey(%s)", lit)
		require.Equal(t, strictFold(want), string(appendFolded(nil, []byte(lit))), "appendFolded(%s)", lit)
	}
	for r := rune(0); r <= 0xffff; r++ {
		check(`"\u` + strictHex4(r) + `"`)
		check(`"\ud83d\u` + strictHex4(r) + `x"`)
	}
	for hi := rune(0xd7ff); hi <= 0xe000; hi += 0x41 {
		for lo := rune(0xdbff); lo <= 0xe000; lo += 0x1f {
			check(`"\u` + strictHex4(hi) + `\u` + strictHex4(lo) + `"`)
		}
	}
}

func TestStrictHexEscape(t *testing.T) {
	esc := testutil.Esc
	tests := []struct {
		data string
		i    int
		want rune
	}{
		{esc(`~u0041`), 0, 0x41},
		{esc(`~uabcd`), 0, 0xabcd},
		{esc(`~uABCD`), 0, 0xabcd},
		{esc(`~uFfFf`), 0, 0xffff},
		{esc(`~u0000`), 0, 0},
		{esc(`~u9a9F`), 0, 0x9a9f},
		{esc(`x~ud83dy`), 1, 0xd83d},
		{esc(`~u004`), 0, -1},
		{esc(`~x0041`), 0, -1},
		{`u0041x`, 0, -1},
		{esc(`~U0041`), 0, -1},
		{esc(`~u004g`), 0, -1},
		{esc(`~ug041`), 0, -1},
		{esc(`~u0/41`), 0, -1},
		{esc(`~u0:41`), 0, -1},
		{esc(`~u0@41`), 0, -1},
		{esc(`~u0G41`), 0, -1},
		{esc("~u0`41"), 0, -1},
		{esc(`~u0 41`), 0, -1},
		{esc(`~u0041`), 1, -1},
		{esc(`~u0041`), 6, -1},
		{esc(`~u0041`), 9, -1},
		{``, 0, -1},
	}
	for _, tt := range tests {
		t.Run(tt.data+"@"+strconv.Itoa(tt.i), func(t *testing.T) {
			assert.Equal(t, tt.want, hexEscape([]byte(tt.data), tt.i))
		})
	}
	assert.True(t, isLowSurrogate(0xdc00))
	assert.True(t, isLowSurrogate(0xdfff))
	assert.False(t, isLowSurrogate(0xdbff))
	assert.False(t, isLowSurrogate(0xe000))
}
