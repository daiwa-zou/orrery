package api

import "testing"

// computeFacets walks every object the caller may see, and the search bar
// triggers it. It was building two maps per object — GetLabels copies the
// label map, fieldSetFor builds a fields.Set — to read three labels and four
// field keys, which is the cost the list path removed with labelsOf and
// objectFields and this walk never adopted.
func BenchmarkComputeFacets50k(b *testing.B) {
	objs := benchObjects(50_000)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		resp := computeFacets(objs)
		if len(resp.Labels) == 0 {
			b.Fatal("no facets; the benchmark is not measuring the work")
		}
	}
}
