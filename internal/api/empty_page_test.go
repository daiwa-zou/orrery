package api

import (
	"encoding/json"
	"net/http"
	"testing"
)

// An empty page is an empty list, not a missing one.
//
// overview.go states the rule for its own warnings feed — "a nil slice
// marshals to JSON null, and 'no warnings we can show' must reach the browser
// as an empty list, not a missing field" — and the list endpoint, which is the
// one every client uses, did not follow it. `items` carried omitempty, so a
// namespace with nothing in it and a page past the end both arrived with no
// items key at all, leaving every caller to write `?? []` or crash.

// pageKeys reports which of the two view keys a response actually carries.
func pageKeys(t *testing.T, raw []byte) (hasItems, hasObjects bool, items, objects int) {
	t.Helper()
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("response is not an object: %v", err)
	}
	if v, ok := body["items"]; ok {
		hasItems = true
		var rows []json.RawMessage
		if err := json.Unmarshal(v, &rows); err != nil {
			t.Fatalf("items is not a list: %s", v)
		}
		items = len(rows)
	}
	if v, ok := body["objects"]; ok {
		hasObjects = true
		var objs []json.RawMessage
		if err := json.Unmarshal(v, &objs); err != nil {
			t.Fatalf("objects is not a list: %s", v)
		}
		objects = len(objs)
	}
	return
}

func TestEmptyPageStillCarriesItems(t *testing.T) {
	rig := hndNewRig(t)

	cases := []struct {
		name, path string
	}{
		// The fixture has no widgets in kube-system.
		{"a namespace with nothing in it", "/api/v1/clusters/fake/resources/example.com/v1/widgets?namespace=kube-system"},
		{"a page past the end", "/api/v1/clusters/fake/resources/core/v1/pods?namespace=demo&page=999"},
		{"a filter that matches nothing", "/api/v1/clusters/fake/resources/core/v1/pods?namespace=demo&q=zzzznope"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := rig.get(t, tc.path)
			hndWantStatus(t, rec, http.StatusOK)

			hasItems, hasObjects, items, _ := pageKeys(t, rec.Body.Bytes())
			if !hasItems {
				t.Errorf("no items key at all: %s", rec.Body.String())
			}
			if items != 0 {
				t.Errorf("expected an empty page, got %d rows", items)
			}
			// The view that was not asked for stays absent, so "empty because
			// you asked and there is nothing" is distinguishable from "not the
			// view you requested".
			if hasObjects {
				t.Errorf("a table request carried an objects key: %s", rec.Body.String())
			}
		})
	}
}

func TestEmptyFullViewStillCarriesObjects(t *testing.T) {
	rig := hndNewRig(t)

	rec := rig.get(t, "/api/v1/clusters/fake/resources/core/v1/pods?namespace=demo&page=999&view=full")
	hndWantStatus(t, rec, http.StatusOK)

	hasItems, hasObjects, _, objects := pageKeys(t, rec.Body.Bytes())
	if !hasObjects {
		t.Errorf("no objects key on an empty full-view page: %s", rec.Body.String())
	}
	if objects != 0 {
		t.Errorf("expected an empty page, got %d objects", objects)
	}
	if hasItems {
		t.Errorf("a full-view request carried an items key: %s", rec.Body.String())
	}
}

// The populated cases have to keep the same shape, or the guarantee is only
// about emptiness.
func TestPopulatedPageCarriesExactlyOneView(t *testing.T) {
	rig := hndNewRig(t)

	table := rig.get(t, "/api/v1/clusters/fake/resources/core/v1/pods?namespace=demo")
	hndWantStatus(t, table, http.StatusOK)
	hasItems, hasObjects, items, _ := pageKeys(t, table.Body.Bytes())
	if !hasItems || hasObjects || items == 0 {
		t.Errorf("table view = items:%v objects:%v rows:%d", hasItems, hasObjects, items)
	}

	full := rig.get(t, "/api/v1/clusters/fake/resources/core/v1/pods?namespace=demo&view=full")
	hndWantStatus(t, full, http.StatusOK)
	hasItems, hasObjects, _, objects := pageKeys(t, full.Body.Bytes())
	if hasItems || !hasObjects || objects == 0 {
		t.Errorf("full view = items:%v objects:%v objects:%d", hasItems, hasObjects, objects)
	}
}

// The events feed shares the response type and had the same hole.
func TestEmptyEventsFeedStillCarriesItems(t *testing.T) {
	rig := hndNewRig(t)

	rec := rig.get(t, "/api/v1/clusters/fake/events?namespace=demo&q=nothingmatchesthis")
	hndWantStatus(t, rec, http.StatusOK)

	hasItems, _, items, _ := pageKeys(t, rec.Body.Bytes())
	if !hasItems {
		t.Errorf("no items key on an empty event feed: %s", rec.Body.String())
	}
	if items != 0 {
		t.Errorf("expected no events, got %d", items)
	}
}
