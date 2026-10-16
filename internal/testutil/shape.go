package testutil

import (
	"bytes"
	"encoding/json"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// safeKey matches the object keys a shape may print. Any other key, which
// could carry data, prints as <key>.
var safeKey = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)

// JSONShape describes the structure of a JSON document without any of its
// values: objects list their safe key names (sorted) with the shape of each
// value, arrays list the distinct shapes of their elements and their length,
// and scalars print as their JSON type only. Booleans, strings and numbers are
// never printed. ok is false when data is not valid JSON, in which case the
// shape is empty.
func JSONShape(data []byte) (shape string, ok bool) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return "", false
	}
	var b strings.Builder
	writeShape(&b, v, 0)
	return b.String(), true
}

func writeShape(b *strings.Builder, v any, depth int) {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		b.WriteString("bool")
	case json.Number:
		b.WriteString("number")
	case string:
		if x == "" {
			b.WriteString("string(empty)")
		} else {
			b.WriteString("string")
		}
	case []any:
		writeArrayShape(b, x, depth)
	case map[string]any:
		writeObjectShape(b, x, depth)
	}
}

func writeArrayShape(b *strings.Builder, x []any, depth int) {
	var kinds []string
	for _, el := range x {
		var eb strings.Builder
		writeShape(&eb, el, depth+1)
		if s := eb.String(); !slices.Contains(kinds, s) {
			kinds = append(kinds, s)
		}
	}
	b.WriteString("array(len=")
	b.WriteString(strconv.Itoa(len(x)))
	b.WriteString(")[")
	b.WriteString(strings.Join(kinds, " | "))
	b.WriteString("]")
}

func writeObjectShape(b *strings.Builder, x map[string]any, depth int) {
	keys := slices.Sorted(maps.Keys(x))
	b.WriteString("{")
	for i, k := range keys {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString("\n")
		b.WriteString(strings.Repeat("  ", depth+1))
		if safeKey.MatchString(k) {
			b.WriteString(k)
		} else {
			b.WriteString("<key>")
		}
		b.WriteString(": ")
		writeShape(b, x[k], depth+1)
	}
	if len(keys) > 0 {
		b.WriteString("\n")
		b.WriteString(strings.Repeat("  ", depth))
	}
	b.WriteString("}")
}

// HeaderNames returns the sorted, canonical names of h, never its values.
func HeaderNames(h map[string][]string) []string {
	return slices.Sorted(maps.Keys(h))
}
