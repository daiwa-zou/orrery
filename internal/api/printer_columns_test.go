package api

import "testing"

// A CRD's printer columns are named by whoever wrote the CRD, and their names
// become row keys by way of sanitizeKey, which throws away everything that is
// not a letter or a digit. It has to: a column name is free-form text and a
// JSON key is not. The consequence was never handled.
//
// "Ready %" and "Ready!" both sanitize to "ready", so both columns were served
// under x_ready. The row map holds one value, the table renders two headings,
// and the second heading shows the first one's number. On screen that looks
// like data — which is the worst thing a wrong number can look like.

func TestUniqueKeySeparatesColumnsThatSanitizeAlike(t *testing.T) {
	taken := map[string]int{}

	first := uniqueKey(taken, "x_ready")
	second := uniqueKey(taken, "x_ready")
	third := uniqueKey(taken, "x_ready")

	if first != "x_ready" {
		t.Errorf("the first column moved to %q; it should keep the plain key", first)
	}
	if second == first || third == first || second == third {
		t.Fatalf("three columns share keys: %q, %q, %q", first, second, third)
	}
}

func TestUniqueKeyIsStableForOneCRD(t *testing.T) {
	// Suffixes follow the CRD's own column order, so the same CRD always
	// produces the same keys and a client may cache them.
	names := []string{"x_ready", "x_phase", "x_ready", "x_ready"}

	run := func() []string {
		taken := map[string]int{}
		out := make([]string, 0, len(names))
		for _, n := range names {
			out = append(out, uniqueKey(taken, n))
		}
		return out
	}

	first, second := run(), run()
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("key %d differs between runs: %q then %q", i, first[i], second[i])
		}
	}
}

func TestUniqueKeyNamesAColumnThatSanitizedToNothing(t *testing.T) {
	// A column called "%" or "→" leaves sanitizeKey with an empty string, so
	// every such column would answer to the bare prefix.
	taken := map[string]int{}

	got := uniqueKey(taken, "x_")
	if got == "x_" {
		t.Error("a column whose name sanitized away kept the bare prefix as its key")
	}
	if again := uniqueKey(taken, "x_"); again == got {
		t.Errorf("two nameless columns both answer to %q", got)
	}
}
