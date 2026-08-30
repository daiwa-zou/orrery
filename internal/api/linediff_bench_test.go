package api

import (
	"fmt"
	"testing"
)

// diffCorpus is two versions of a pod template's YAML: mostly the same lines,
// with a scattering of edits, which is what a rollout diff actually compares.
func diffCorpus(n int) (before, after []string) {
	before = make([]string, n)
	after = make([]string, n)
	for i := range n {
		line := fmt.Sprintf("  - name: field-%d", i)
		before[i] = line
		if i%17 == 0 {
			after[i] = line + " # changed"
		} else {
			after[i] = line
		}
	}
	return before, after
}

// The table is quadratic in the two inputs, and rolloutHistory builds one per
// revision on the page. These say what that costs at a realistic template size
// and at the ceiling the input cap allows.
func benchLineDiff(b *testing.B, n int) {
	before, after := diffCorpus(n)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if lineDiff(before, after, 2) == nil {
			b.Fatal("the corpus must produce a diff")
		}
	}
}

func BenchmarkLineDiff300(b *testing.B)  { benchLineDiff(b, 300) }
func BenchmarkLineDiff2000(b *testing.B) { benchLineDiff(b, 2000) }
