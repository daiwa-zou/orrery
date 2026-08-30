package api

import (
	"fmt"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Facet keys were already chosen by how many objects carry them — "the caps
// keep the vocabulary people actually filter by and drop the long tail".
// Values were not: they were sorted alphabetically and then cut at twenty, so
// a `version` label offered v1.0.0 through v1.0.19 and never the version
// actually deployed, and a nodeName facet on a large cluster offered the
// twenty nodes whose names happen to sort first. The counts needed to choose
// properly were already being collected, and thrown away.

func TestTopValuesKeepsTheCommonOnes(t *testing.T) {
	counts := map[string]int{"zzz-live": 500}
	// Enough alphabetically-earlier values to fill the cap on their own.
	for i := range maxFacetValuesPerKey + 5 {
		counts[fmt.Sprintf("aaa-%02d", i)] = 1
	}

	vals, truncated := topValues(counts)
	if !truncated {
		t.Fatalf("%d values were cut to %d without saying so", len(counts), len(vals))
	}
	if len(vals) != maxFacetValuesPerKey {
		t.Fatalf("got %d values, want the cap of %d", len(vals), maxFacetValuesPerKey)
	}
	if !contains(vals, "zzz-live") {
		t.Errorf("the value on 500 objects was cut in favour of ones on 1: %v", vals)
	}
}

func TestTopValuesReadsAlphabetically(t *testing.T) {
	// Whatever the counts, the dropdown is scanned by eye for a value the
	// reader already has in mind.
	vals, truncated := topValues(map[string]int{"charlie": 9, "alpha": 1, "bravo": 5})
	if truncated {
		t.Error("three values were reported as truncated")
	}
	if strings.Join(vals, ",") != "alpha,bravo,charlie" {
		t.Errorf("values = %v, want them in reading order", vals)
	}
}

// The projection swapped GetLabels and fieldSetFor for the no-copy views the
// list path uses. Same answers, or the optimisation is a regression.
func TestComputeFacetsStillSeesLabelsAndFields(t *testing.T) {
	objs := []*unstructured.Unstructured{
		obj("a", "demo", map[string]string{"app": "web"}, map[string]any{
			"spec":   map[string]any{"nodeName": "node-1"},
			"status": map[string]any{"phase": "Running"},
		}),
		obj("b", "demo", map[string]string{"app": "web", "tier": "cache"}, map[string]any{
			"spec":   map[string]any{"nodeName": "node-2"},
			"status": map[string]any{"phase": "Pending"},
		}),
	}

	resp := computeFacets(objs)

	want := map[string][]string{"app": {"web"}, "tier": {"cache"}}
	for _, f := range resp.Labels {
		if exp, ok := want[f.Key]; ok {
			if strings.Join(f.Values, ",") != strings.Join(exp, ",") {
				t.Errorf("label %s = %v, want %v", f.Key, f.Values, exp)
			}
			delete(want, f.Key)
		}
	}
	if len(want) > 0 {
		t.Errorf("labels missing from the facets: %v", want)
	}

	fields := map[string][]string{}
	for _, f := range resp.Fields {
		fields[f.Key] = f.Values
	}
	if got := fields["status.phase"]; !contains(got, "Running") || !contains(got, "Pending") {
		t.Errorf("status.phase = %v, want both phases", got)
	}
	if got := fields["spec.nodeName"]; !contains(got, "node-1") || !contains(got, "node-2") {
		t.Errorf("spec.nodeName = %v, want both nodes", got)
	}
}
