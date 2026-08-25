package api

import (
	"fmt"
	"testing"
)

func TestCollapseFacetsCapsKeyCount(t *testing.T) {
	// Every key appears once, so ordering falls to the alphabetical tie-break
	// and the key cap has to cut the tail.
	m := map[string]map[string]int{}
	for i := 0; i < maxFacetKeys+10; i++ {
		m[fmt.Sprintf("key-%03d", i)] = map[string]int{"v": 1}
	}

	out, truncated := collapseFacets(m, false)
	if len(out) != maxFacetKeys {
		t.Errorf("got %d keys, want the cap of %d", len(out), maxFacetKeys)
	}
	if !truncated {
		t.Error("cutting keys must be reported as truncation")
	}
	// With equal counts the alphabetical tie-break decides who survives, so
	// the result is deterministic across runs.
	if out[0].Key != "key-000" {
		t.Errorf("first key = %q, want key-000", out[0].Key)
	}
	for i := 1; i < len(out); i++ {
		if out[i-1].Key >= out[i].Key {
			t.Fatalf("keys not in deterministic order: %q before %q", out[i-1].Key, out[i].Key)
		}
	}
}
