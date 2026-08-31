package api

import (
	"fmt"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// A CRD printer column is whatever JSONPath pulls out of the object, and the
// unstructured decoder types a whole JSON number as int64 and a fractional one
// as float64. So one column over one field arrives mixed the moment a single
// object carries a fraction — and a mixed column used to fall to the text
// fallback, where "10" sorts before "9".
func TestCompareCellOrdersMixedNumericTypesNumerically(t *testing.T) {
	cases := []struct {
		name string
		x, y any
		want int
	}{
		{"int64 above float64", int64(10), float64(9), 1},
		{"float64 below int64", float64(2), int64(10), -1},
		{"float64 above int64", float64(9.5), int64(9), 1},
		{"equal across types", int64(3), float64(3), 0},
		{"negative float below zero int", float64(-0.5), int64(0), -1},
		{"int below int64", 2, int64(10), -1},
		{"int32 above float32", int32(10), float32(9.5), 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := sign(compareCell(c.x, c.y)); got != c.want {
				t.Errorf("compareCell(%v, %v) = %d, want %d", c.x, c.y, got, c.want)
			}
			// Order is a relation, not a value: whichever way round it is
			// asked, the two answers have to agree.
			if got := sign(compareCell(c.y, c.x)); got != -c.want {
				t.Errorf("compareCell(%v, %v) = %d, want %d", c.y, c.x, got, -c.want)
			}
		})
	}
}

// Sorting a column that mixes the two numeric types through the real path, so
// the fix is not just a property of the comparator in isolation.
func TestSortByCellOrdersAMixedNumericColumn(t *testing.T) {
	// The values a CRD's "progress" printer column would hold: whole numbers
	// decode to int64, the fraction to float64.
	cells := map[string]any{
		"a": int64(100),
		"b": float64(2.5),
		"c": int64(9),
		"d": float64(0.5),
	}
	objs := []*unstructured.Unstructured{
		obj("a", "d", nil, nil),
		obj("b", "d", nil, nil),
		obj("c", "d", nil, nil),
		obj("d", "d", nil, nil),
	}
	set := columnSet{row: func(u *unstructured.Unstructured) map[string]any {
		return map[string]any{"name": u.GetName(), "progress": cells[u.GetName()]}
	}}

	sortByCell(objs, set, "progress", false)

	for i, want := range []string{"d", "b", "c", "a"} {
		if got := objs[i].GetName(); got != want {
			t.Fatalf("objs[%d] = %s (%v), want %s (%v)",
				i, got, cells[got], want, cells[want])
		}
	}
}

// Numeric-looking text is still text. Sorting must not reach for cellNumber's
// reading of a string, which would pull a name like "10" out of the ordering
// the rest of its column is sorted in.
func TestCompareCellKeepsNumericLookingStringsAsText(t *testing.T) {
	if got := sign(compareCell("10", "9")); got != -1 {
		t.Errorf("compareCell(%q, %q) = %d, want -1: strings sort as text", "10", "9", got)
	}
	// A string against a number has no shared ordering to fall back on; all
	// that is required is that it is decided consistently rather than by which
	// operand came first.
	if a, b := sign(compareCell("9", int64(10))), sign(compareCell(int64(10), "9")); a != -b {
		t.Errorf("compareCell disagreed with itself: %d and %d", a, b)
	}
}

func TestCompareCellSameTypedPairs(t *testing.T) {
	cases := []struct {
		name string
		x, y any
		want int
	}{
		{"text is case-insensitive", "Alpha", "alpha", 0},
		{"text orders lexically", "alpha", "beta", -1},
		{"int64 orders numerically", int64(9), int64(10), -1},
		{"negative int64 below zero", int64(-1), int64(0), -1},
		{"float64 orders numerically", 1.5, 1.25, 1},
		{"false before true", false, true, -1},
		{"equal booleans", true, true, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := sign(compareCell(c.x, c.y)); got != c.want {
				t.Errorf("compareCell(%v, %v) = %d, want %d", c.x, c.y, got, c.want)
			}
		})
	}
}

// Cells that are neither numbers, strings nor booleans — a list column, or a
// key absent from this object's row — still have to order deterministically,
// because a sort that varies between requests pages a reader past rows they
// never saw.
func TestCompareCellFallsBackDeterministically(t *testing.T) {
	cases := [][2]any{
		{[]string{"a"}, []string{"b"}},
		{nil, int64(1)},
		{nil, "text"},
		{map[string]any{"k": "v"}, nil},
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("%v vs %v", c[0], c[1]), func(t *testing.T) {
			forward, back := sign(compareCell(c[0], c[1])), sign(compareCell(c[1], c[0]))
			if forward != -back {
				t.Errorf("compareCell(%v, %v) = %d but the reverse gave %d",
					c[0], c[1], forward, back)
			}
			if got := sign(compareCell(c[0], c[0])); got != 0 {
				t.Errorf("compareCell(%v, %v) = %d, want 0", c[0], c[0], got)
			}
		})
	}
}

func TestOrderableNumberDeclinesWhatIsNotANumber(t *testing.T) {
	// Strings included: cellNumber reads "5" as five for a `where` bound, and
	// sorting deliberately does not.
	for _, cell := range []any{"5", "text", true, nil, []string{"1"}} {
		if v, ok := orderableNumber(cell); ok {
			t.Errorf("orderableNumber(%#v) = %v, true; want false", cell, v)
		}
	}
	for _, c := range []struct {
		cell any
		want float64
	}{
		{int(7), 7},
		{int32(7), 7},
		{int64(7), 7},
		{float32(0.5), 0.5},
		{float64(0.5), 0.5},
	} {
		v, ok := orderableNumber(c.cell)
		if !ok || v != c.want {
			t.Errorf("orderableNumber(%#v) = %v, %v; want %v, true", c.cell, v, ok, c.want)
		}
	}
}

// cellNumber is orderableNumber's opposite number, used by `where` bounds
// rather than by sorting, and it does read a numeric-looking string — because
// there the reader has typed a quantity and means one. Everything a column can
// hold has to be decided one way or the other: a cell it declines never
// matches a numeric predicate, which is right, and a cell it misreads matches
// the wrong rows silently.
func TestCellNumberReadsEveryNumericShape(t *testing.T) {
	for _, c := range []struct {
		cell any
		want float64
	}{
		{int(7), 7},
		{int32(7), 7},
		{int64(7), 7},
		{float32(0.5), 0.5},
		{float64(0.5), 0.5},
		{"42", 42},
		{"-3", -3},
		{"1.5", 1.5},
		// Column values arrive rendered, so the quantities Kubernetes prints
		// have to read as the numbers they are.
		{"128Mi", 128 * 1024 * 1024},
		{"250m", 0.25},
	} {
		got, ok := cellNumber(c.cell)
		if !ok || got != c.want {
			t.Errorf("cellNumber(%#v) = %v, %v; want %v, true", c.cell, got, ok, c.want)
		}
	}
}

func TestCellNumberDeclinesWhatIsNotANumber(t *testing.T) {
	// A declined cell never matches a numeric `where`, so what matters is that
	// nothing here is quietly read as zero.
	for _, cell := range []any{"", "Running", "1/2", true, nil, []string{"1"}, map[string]any{}} {
		if v, ok := cellNumber(cell); ok {
			t.Errorf("cellNumber(%#v) = %v, true; want false", cell, v)
		}
	}
}

// cellString renders a cell for pattern matching. A list column is joined so a
// pattern can reach any one entry — matching only the first would silently
// answer "no such rows" for objects whose match is second.
func TestCellStringFlattensListColumns(t *testing.T) {
	for _, c := range []struct {
		cell any
		want string
	}{
		{"web", "web"},
		{[]string{"web", "sidecar"}, "web sidecar"},
		{[]any{"web", "sidecar"}, "web sidecar"},
		{[]any{"a", []string{"b", "c"}}, "a b c"},
		{[]string{}, ""},
		{int64(3), "3"},
		{true, "true"},
		{nil, "<nil>"},
	} {
		if got := cellString(c.cell); got != c.want {
			t.Errorf("cellString(%#v) = %q, want %q", c.cell, got, c.want)
		}
	}
}

// sign reduces a comparison to -1, 0 or 1 so the tests can state the ordering
// without depending on the magnitudes strings.Compare happens to return.
func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}
