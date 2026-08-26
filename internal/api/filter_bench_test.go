package api

import (
	"fmt"
	"net/http/httptest"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// benchObjects builds a pod-shaped corpus with the label cardinality a real
// cluster tends to have: a handful of apps, a few tiers, one unique name each.
func benchObjects(n int) []*unstructured.Unstructured {
	objs := make([]*unstructured.Unstructured, n)
	for i := range objs {
		objs[i] = &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "v1",
			"kind":       "Pod",
			"metadata": map[string]any{
				"name":      fmt.Sprintf("web-%d", i),
				"namespace": fmt.Sprintf("ns-%d", i%40),
				"uid":       fmt.Sprintf("uid-%d", i),
				"labels": map[string]any{
					"app":                    fmt.Sprintf("app-%d", i%12),
					"tier":                   []string{"web", "cache", "db"}[i%3],
					"app.kubernetes.io/name": fmt.Sprintf("svc-%d", i%12),
				},
			},
			"spec":   map[string]any{"nodeName": fmt.Sprintf("node-%d", i%50)},
			"status": map[string]any{"phase": "Running"},
		}}
	}
	return objs
}

func benchFilter(b *testing.B, n int, query string) {
	objs := benchObjects(n)
	r := httptest.NewRequest("GET", "/?"+query, nil)
	f, err := parseListFilter(r)
	if err != nil {
		b.Fatalf("parseListFilter: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		kept := 0
		for _, o := range objs {
			if f.matches(o) {
				kept++
			}
		}
		if kept == 0 {
			b.Fatal("filter matched nothing; the benchmark is not measuring the work")
		}
	}
}

// The question these answer: is the linear label-selector scan actually worth
// indexing, or is the cost somewhere else entirely?
func BenchmarkFilterLabelSelector1k(b *testing.B)  { benchFilter(b, 1_000, "labelSelector=tier%3Dweb") }
func BenchmarkFilterLabelSelector10k(b *testing.B) { benchFilter(b, 10_000, "labelSelector=tier%3Dweb") }
func BenchmarkFilterLabelSelector50k(b *testing.B) { benchFilter(b, 50_000, "labelSelector=tier%3Dweb") }

func BenchmarkFilterFieldSelector50k(b *testing.B) {
	benchFilter(b, 50_000, "fieldSelector=status.phase%3DRunning")
}

func BenchmarkFilterFreeText50k(b *testing.B) { benchFilter(b, 50_000, "q=web") }
