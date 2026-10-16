package testutil_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-emailable/internal/testutil"
)

// lkMarker is the secret the leak tests plant.
const lkMarker = "lk-marker"

// lkVisible holds the marker where every fmt verb and reflection can see it.
type lkVisible struct{ Name string }

// lkHidden holds the marker only inside a closure, which neither fmt nor
// reflection can see into.
type lkHidden struct{ get func() string }

func TestFormatVerbs(t *testing.T) {
	verbs := testutil.FormatVerbs()
	assert.Len(t, verbs, 15)
	for _, v := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x"} {
		assert.Contains(t, verbs, v)
	}
}

func TestShapesHoldTheValue(t *testing.T) {
	shapes := testutil.Shapes(&lkVisible{Name: lkMarker})
	require.Len(t, shapes, 10)
	for _, s := range shapes {
		assert.Contains(t, testutil.ReflectStrings(s.Value), lkMarker, s.Name)
	}
}

func TestAssertRedacted(t *testing.T) {
	const secret = "key" // 6b6579 as hex
	assert.True(t, testutil.AssertRedacted(t, "clean output", []string{secret}))
	for _, out := range []string{"a key", "hex 6b6579", "HEX 6B6579"} {
		rec := &recorder{}
		assert.False(t, testutil.AssertRedacted(rec, out, []string{secret}), out)
		assert.True(t, rec.failed, out)
	}
}

func TestAssertNeverPrints(t *testing.T) {
	assert.True(t, testutil.AssertNeverPrints(t, lkHidden{get: func() string { return lkMarker }}, lkMarker))

	rec := &recorder{}
	assert.False(t, testutil.AssertNeverPrints(rec, lkVisible{Name: lkMarker}, lkMarker))
	assert.True(t, rec.failed)
}

func TestAssertShapesNeverPrint(t *testing.T) {
	assert.True(t, testutil.AssertShapesNeverPrint(t, &lkHidden{get: func() string { return lkMarker }}, lkMarker))

	rec := &recorder{}
	assert.False(t, testutil.AssertShapesNeverPrint(rec, &lkVisible{Name: lkMarker}, lkMarker))
	assert.True(t, rec.failed)
}

func TestAssertDiffRedacted(t *testing.T) {
	a, b := &lkVisible{Name: "a"}, &lkVisible{Name: "b"}
	assert.True(t, testutil.AssertDiffRedacted(t, a, b, lkMarker))

	rec := &recorder{}
	assert.False(t, testutil.AssertDiffRedacted(rec, a, &lkVisible{Name: "a"}, lkMarker))
	assert.True(t, rec.failed, "equal values print no diff to check")

	rec = &recorder{}
	assert.False(t, testutil.AssertDiffRedacted(rec, &lkVisible{Name: lkMarker}, b, lkMarker))
	assert.True(t, rec.failed, "the diff prints the marker")
}

func TestReflectStrings(t *testing.T) {
	type node struct {
		s    string
		b    []byte
		arr  [1]string
		list []string
		m    map[string]string
		v    any
		get  func() string
		n    int
		next *node
	}
	n := &node{
		s: "s", b: []byte("b"), arr: [1]string{"arr"}, list: []string{"list"},
		m: map[string]string{"key": "value"}, v: "iface", get: func() string { return lkMarker }, n: 7,
	}
	n.next = n // a cycle is followed once
	assert.ElementsMatch(t, []string{"s", "b", "arr", "list", "key", "value", "iface"}, testutil.ReflectStrings(n))
	assert.Empty(t, testutil.ReflectStrings(nil))
	assert.Empty(t, testutil.ReflectStrings((*node)(nil)))

	// Each node is two levels below the previous one, so the walk stops
	// before the 40th.
	top := &node{s: "top"}
	cur := top
	for range 40 {
		cur.next = &node{s: "middle"}
		cur = cur.next
	}
	cur.s = "bottom"
	got := testutil.ReflectStrings(top)
	assert.Contains(t, got, "top")
	assert.NotContains(t, got, "bottom")
}
