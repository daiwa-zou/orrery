package api

import (
	"encoding/json"
	"net/http"
	"testing"
)

// facetsOf reads the autocomplete vocabulary for a query string.
func facetsOf(t *testing.T, rig *hndRig, query string) facetsResponse {
	t.Helper()
	rec := rig.get(t, "/api/v1/clusters/fake/resources/core/v1/pods/facets"+query)
	hndWantStatus(t, rec, http.StatusOK)
	var resp facetsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode facets: %v", err)
	}
	return resp
}

func valuesFor(fs []facet, key string) []string {
	for _, f := range fs {
		if f.Key == key {
			return f.Values
		}
	}
	return nil
}

func contains(vals []string, want string) bool {
	for _, v := range vals {
		if v == want {
			return true
		}
	}
	return false
}

// Autocomplete narrows with the search: a suggestion offered after a filter is
// applied has to lead somewhere, or picking it silently empties the list.
func TestFacetsNarrowWithTheSearch(t *testing.T) {
	rig := hndNewRig(t)

	all := facetsOf(t, rig, "?namespace=demo")
	phases := valuesFor(all.Fields, "status.phase")
	if !contains(phases, "Running") || !contains(phases, "Succeeded") {
		t.Fatalf("unfiltered phases = %v, want both Running and Succeeded", phases)
	}

	// Only the app=web pods are Running; the Succeeded one carries no labels.
	narrowed := facetsOf(t, rig, "?namespace=demo&labelSelector=app%3Dweb")
	got := valuesFor(narrowed.Fields, "status.phase")
	if contains(got, "Succeeded") {
		t.Errorf("phases under app=web = %v, still offering Succeeded — no app=web pod has it", got)
	}
	if !contains(got, "Running") {
		t.Errorf("phases under app=web = %v, want Running", got)
	}
}

// The point of narrowing is that the dropdown and the list agree. A value the
// facets still offer must return rows, and one they dropped must not — checked
// against the list endpoint rather than against a hand-written expectation, so
// the two cannot drift apart without this failing.
func TestNarrowedFacetsAgreeWithTheList(t *testing.T) {
	rig := hndNewRig(t)

	count := func(query string) int {
		t.Helper()
		rec := rig.get(t, "/api/v1/clusters/fake/resources/core/v1/pods"+query)
		hndWantStatus(t, rec, http.StatusOK)
		var page struct {
			Items []map[string]any `json:"items"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
			t.Fatalf("decode list: %v", err)
		}
		return len(page.Items)
	}

	narrowed := facetsOf(t, rig, "?namespace=demo&labelSelector=app%3Dweb")
	for _, phase := range valuesFor(narrowed.Fields, "status.phase") {
		q := "?namespace=demo&labelSelector=app%3Dweb&fieldSelector=status.phase%3D" + phase
		if n := count(q); n == 0 {
			t.Errorf("facets offered status.phase=%s under app=web, but the list returns nothing", phase)
		}
	}

	// And the converse: Succeeded was dropped precisely because it is a dead end.
	dead := count("?namespace=demo&labelSelector=app%3Dweb&fieldSelector=status.phase%3DSucceeded")
	if dead != 0 {
		t.Fatalf("expected app=web + Succeeded to match nothing, got %d — the fixture changed", dead)
	}
}

// Two searches must not share one cached vocabulary. Narrowing that served a
// different filter's answer would be worse than not narrowing at all.
func TestFacetCacheIsKeyedBySearch(t *testing.T) {
	rig := hndNewRig(t)

	unfiltered := facetsOf(t, rig, "?namespace=demo")
	filtered := facetsOf(t, rig, "?namespace=demo&fieldSelector=status.phase%3DSucceeded")
	again := facetsOf(t, rig, "?namespace=demo")

	if len(valuesFor(filtered.Labels, "app")) != 0 {
		t.Errorf("the Succeeded pod carries no labels, so app must be absent: %+v", filtered.Labels)
	}
	if len(valuesFor(again.Labels, "app")) == 0 {
		t.Error("the unfiltered vocabulary came back without app — a filtered entry was served for it")
	}
	if len(unfiltered.Labels) != len(again.Labels) {
		t.Errorf("unfiltered vocabulary changed across calls: %+v then %+v", unfiltered.Labels, again.Labels)
	}
}
