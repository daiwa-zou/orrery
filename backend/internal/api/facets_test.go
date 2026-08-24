package api

import (
	"fmt"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestComputeFacets(t *testing.T) {
	objs := []*unstructured.Unstructured{
		obj("a", "demo", map[string]string{"app": "web", "tier": "front"}, map[string]any{
			"status": map[string]any{"phase": "Running"},
			"spec":   map[string]any{"nodeName": "node-1"},
		}),
		obj("b", "demo", map[string]string{"app": "api"}, map[string]any{
			"status": map[string]any{"phase": "Running"},
		}),
		obj("c", "demo", map[string]string{"app": "web"}, map[string]any{
			"status": map[string]any{"phase": "Failed"},
		}),
	}

	got := computeFacets(objs)

	// app appears on three objects, tier on one — frequency orders the keys.
	if len(got.Labels) != 2 || got.Labels[0].Key != "app" || got.Labels[1].Key != "tier" {
		t.Fatalf("labels = %+v", got.Labels)
	}
	if fmt.Sprint(got.Labels[0].Values) != "[api web]" {
		t.Errorf("app values = %v, want sorted [api web]", got.Labels[0].Values)
	}

	if len(got.Fields) != 2 || got.Fields[0].Key != "status.phase" {
		t.Fatalf("fields = %+v", got.Fields)
	}
	if fmt.Sprint(got.Fields[0].Values) != "[Failed Running]" {
		t.Errorf("phase values = %v", got.Fields[0].Values)
	}
	if got.Fields[1].Key != "spec.nodeName" || fmt.Sprint(got.Fields[1].Values) != "[node-1]" {
		t.Errorf("nodeName facet = %+v", got.Fields[1])
	}
	if got.Truncated {
		t.Error("nothing was cut, so truncated must be false")
	}
}

func TestComputeFacetsTruncates(t *testing.T) {
	var objs []*unstructured.Unstructured
	for i := 0; i < maxFacetValuesPerKey+5; i++ {
		objs = append(objs, obj(fmt.Sprintf("p%02d", i), "demo",
			map[string]string{"app": fmt.Sprintf("v%02d", i)}, nil))
	}

	got := computeFacets(objs)
	if len(got.Labels[0].Values) != maxFacetValuesPerKey {
		t.Errorf("values not capped: %d", len(got.Labels[0].Values))
	}
	if !got.Truncated {
		t.Error("a capped value list must be reported as truncated")
	}
}

func TestComputeFacetsEmpty(t *testing.T) {
	got := computeFacets(nil)
	if len(got.Labels) != 0 || len(got.Fields) != 0 || got.Truncated {
		t.Errorf("empty input produced %+v", got)
	}
}
