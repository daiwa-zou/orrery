package api

import (
	"fmt"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// benchConfigMaps builds config maps carrying real payloads. A ConfigMap may
// hold up to a mebibyte, and the column its table shows is the number of keys
// — so what the projector reads and what it reports are wildly different
// sizes, which is exactly the shape a copying accessor punishes.
func benchConfigMaps(n, keys, valueBytes int) []*unstructured.Unstructured {
	objs := make([]*unstructured.Unstructured, n)
	for i := range objs {
		data := make(map[string]any, keys)
		for k := range keys {
			data[fmt.Sprintf("key-%d", k)] = strings.Repeat("x", valueBytes)
		}
		objs[i] = &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "v1",
			"kind":       "ConfigMap",
			"metadata": map[string]any{
				"name":      fmt.Sprintf("cm-%d", i),
				"namespace": "demo",
				"uid":       fmt.Sprintf("uid-%d", i),
			},
			"data": data,
		}}
	}
	return objs
}

// Counting the keys of a page of config maps should not depend on how much
// they contain.
func BenchmarkProjectConfigMapPage(b *testing.B) {
	objs := benchConfigMaps(200, 20, 4096)
	set := builtinColumns[gk("", "ConfigMap")]
	if set.row == nil {
		b.Fatal("no ConfigMap table registered")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		for _, o := range objs[:50] {
			if set.rowOf(o)["keys"].(int64) != 20 {
				b.Fatal("wrong key count")
			}
		}
	}
}
