package api

import (
	"fmt"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

// benchOwnedPods builds a namespace's worth of pods, each carrying the single
// controller reference a ReplicaSet leaves behind. Only some belong to the
// parent being asked about, which is the situation walkChildren is always in:
// it scans everything and keeps the matches.
func benchOwnedPods(n int, wanted types.UID) []*unstructured.Unstructured {
	objs := make([]*unstructured.Unstructured, n)
	for i := range objs {
		owner := types.UID(fmt.Sprintf("other-%d", i%10))
		if i%10 == 0 {
			owner = wanted
		}
		objs[i] = &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "v1",
			"kind":       "Pod",
			"metadata": map[string]any{
				"name":      fmt.Sprintf("web-%d", i),
				"namespace": "demo",
				"uid":       fmt.Sprintf("pod-%d", i),
				"ownerReferences": []any{map[string]any{
					"apiVersion":         "apps/v1",
					"kind":               "ReplicaSet",
					"name":               "web-7d9f",
					"uid":                string(owner),
					"controller":         true,
					"blockOwnerDeletion": true,
				}},
			},
		}}
	}
	return objs
}

// The neighbourhood walk asks this of every object of every candidate resource
// for every parent in the frontier, so it runs far more often than there are
// objects, and the answer it wants is one string.
func BenchmarkOwnedBy10k(b *testing.B) {
	const wanted = types.UID("rs-abc123")
	objs := benchOwnedPods(10_000, wanted)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		matched := 0
		for _, o := range objs {
			if ownedBy(o, wanted) {
				matched++
			}
		}
		if matched != 1_000 {
			b.Fatalf("matched %d, want 1000", matched)
		}
	}
}
