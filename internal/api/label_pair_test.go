package api

import (
	"math/rand"
	"strings"
	"testing"
)

// matchesLabelPair replaced a line that built `strings.ToLower(k+"="+v)` and
// searched it. The rewrite exists to stop allocating that string on every
// label of every object, and it is only worth having if it answers exactly the
// same question. These check that it does.

// oldMatchesLabelPair is the implementation this replaced, kept as the oracle.
func oldMatchesLabelPair(k, v, q string) bool {
	return strings.Contains(strings.ToLower(k+"="+v), q)
}

func TestMatchesLabelPairAgreesWithTheStringItReplaced(t *testing.T) {
	keys := []string{
		"app", "tier", "app.kubernetes.io/name", "", "=", "a=b",
		"App", "SPARK", "x", "kubernetes.io/metadata.name",
	}
	values := []string{
		"web", "cache", "", "=", "a=b", "Web", "WEB", "web-1", "1", "a",
	}
	queries := []string{
		"", "=", "app", "app=", "=web", "app=web", "web", "pp=we", "tier=cache",
		"a=b", "==", "x", "APP", "app=web=extra", ".io/name", "name=", "-1",
	}

	for _, k := range keys {
		for _, v := range values {
			for _, q := range queries {
				// parseListFilter lower-cases the query before it reaches here.
				lq := strings.ToLower(q)
				want := oldMatchesLabelPair(k, v, lq)
				got := matchesLabelPair(k, v, lq)
				if got != want {
					t.Errorf("matchesLabelPair(%q, %q, %q) = %v, want %v", k, v, lq, got, want)
				}
			}
		}
	}
}

// The pairs above are hand-picked, which is exactly the way to miss the case
// nobody thought of. This throws random ones at both implementations.
func TestMatchesLabelPairAgreesOnRandomInput(t *testing.T) {
	rng := rand.New(rand.NewSource(20260826))
	alphabet := []rune("abABz=-./01")

	pick := func(maxLen int) string {
		n := rng.Intn(maxLen + 1)
		var b strings.Builder
		for range n {
			b.WriteRune(alphabet[rng.Intn(len(alphabet))])
		}
		return b.String()
	}

	for i := range 20000 {
		k, v, q := pick(6), pick(6), strings.ToLower(pick(5))
		want := oldMatchesLabelPair(k, v, q)
		if got := matchesLabelPair(k, v, q); got != want {
			t.Fatalf("case %d: matchesLabelPair(%q, %q, %q) = %v, want %v", i, k, v, q, got, want)
		}
	}
}

// The behaviour callers actually rely on, stated plainly rather than by
// reference to the old implementation.
func TestMatchesLabelPairSemantics(t *testing.T) {
	cases := []struct {
		name, k, v, q string
		want          bool
	}{
		{"whole pair", "app", "web", "app=web", true},
		{"inside the key", "app.kubernetes.io/name", "web", "kubernetes", true},
		{"inside the value", "app", "frontend", "ronte", true},
		{"straddling the equals", "app", "web", "pp=we", true},
		{"case-insensitive on both sides", "App", "WEB", "app=web", true},
		{"wrong value", "app", "web", "app=cache", false},
		{"key only, no match", "tier", "cache", "app", false},
		{"empty query matches", "app", "web", "", true},
		{"bare equals matches any pair", "app", "web", "=", true},
		{"longer than the pair", "a", "b", "a=b=c", false},
		{"empty value", "app", "", "app=", true},
		{"empty key", "", "web", "=web", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := matchesLabelPair(tc.k, tc.v, tc.q); got != tc.want {
				t.Errorf("matchesLabelPair(%q, %q, %q) = %v, want %v", tc.k, tc.v, tc.q, got, tc.want)
			}
		})
	}
}

// The point of the rewrite: no allocations for the ordinary case, which is a
// lower-case ASCII label and a lower-case query.
func TestMatchesLabelPairDoesNotAllocate(t *testing.T) {
	got := testing.AllocsPerRun(200, func() {
		matchesLabelPair("app.kubernetes.io/name", "frontend-service", "zzz")
		matchesLabelPair("tier", "cache", "tier=cache")
		matchesLabelPair("app", "web", "pp=we")
	})
	if got != 0 {
		t.Errorf("matchesLabelPair allocated %.1f times per run, want 0", got)
	}
}

// Mixed case still has to work; it is allowed to cost an allocation, since
// ToLower has real work to do.
func TestMatchesLabelPairHandlesMixedCase(t *testing.T) {
	if !matchesLabelPair("Tier", "Cache", "tier=cache") {
		t.Error("a mixed-case pair should still match a lower-case query")
	}
	if matchesLabelPair("Tier", "Cache", "tier=web") {
		t.Error("mixed case must not make an unrelated query match")
	}
}
