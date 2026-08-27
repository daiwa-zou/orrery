package api

import (
	"net/http"
	"testing"
)

// explain answers `kubectl explain`, and the type of a field is most of that
// answer: kubectl reports pod.spec.containers as []Container, which says both
// that it is a list and what the list holds.
//
// Orrery reported "Object". Two things cost it. The walk resolved each
// reference and unwrapped each array before computing the type, so by the time
// the type was read there was no array and no reference left to name. And the
// naming itself only looked at a bare $ref, while a real document wraps
// references in a single-element allOf so it can hang a description on them —
// which is nearly always.
//
// The fixture used the bare form, so these came out right here and wrong on
// every real cluster. It now uses the shape an API server actually serves.

func hndExplain(t *testing.T, rig *hndRig, field string) explainResponse {
	t.Helper()
	path := "/api/v1/clusters/fake/explain?version=v1&kind=Pod"
	if field != "" {
		path += "&field=" + field
	}
	rec := rig.get(t, path)
	hndWantStatus(t, rec, http.StatusOK)
	var body explainResponse
	hndDecode(t, rec, &body)
	return body
}

func TestExplainNamesTheTypeOfAField(t *testing.T) {
	rig := hndNewRig(t)

	cases := []struct {
		field, want string
	}{
		// Reached through the allOf-wrapped reference the fixture now uses.
		{"spec", "PodSpec"},
		// An array of references: both halves have to survive.
		{"spec.containers", "[]Container"},
		// Through the array, into the item, to a plain scalar.
		{"spec.containers.name", "string"},
		{"spec.nodeName", "string"},
		// A bare $ref still works; both spellings occur.
		{"metadata", "ObjectMeta"},
	}

	for _, tc := range cases {
		t.Run(tc.field, func(t *testing.T) {
			if got := hndExplain(t, rig, tc.field).Type; got != tc.want {
				t.Errorf("explain field %q reported type %q, want %q", tc.field, got, tc.want)
			}
		})
	}
}

// The listing under a field goes through the same naming, so a Pod's fields
// should say what they hold rather than all reporting "Object".
func TestExplainNamesTheTypesItLists(t *testing.T) {
	rig := hndNewRig(t)
	body := hndExplain(t, rig, "spec")

	types := map[string]string{}
	for _, f := range body.Fields {
		types[f.Name] = f.Type
	}
	if got := types["containers"]; got != "[]Container" {
		t.Errorf("spec.containers listed as %q, want []Container", got)
	}
	if got := types["nodeName"]; got != "string" {
		t.Errorf("spec.nodeName listed as %q, want string", got)
	}
}

// Drilling in must still produce the fields of the thing drilled into — the
// type is read before the array is unwrapped, but the listing is read after.
func TestExplainStillListsThroughAnArray(t *testing.T) {
	rig := hndNewRig(t)
	body := hndExplain(t, rig, "spec.containers")

	if body.Type != "[]Container" {
		t.Fatalf("type = %q", body.Type)
	}
	names := map[string]bool{}
	for _, f := range body.Fields {
		names[f.Name] = true
	}
	// These are the Container's fields, not the array's.
	for _, want := range []string{"name", "image"} {
		if !names[want] {
			t.Errorf("no %q among the fields of spec.containers: %v", want, names)
		}
	}
}

func TestExplainRootAndRejections(t *testing.T) {
	rig := hndNewRig(t)

	if body := hndExplain(t, rig, ""); body.Kind != "Pod" || len(body.Fields) == 0 {
		t.Errorf("root explain = %+v", body)
	}
	for _, tc := range []struct {
		name, query string
		want        int
	}{
		{"no version", "kind=Pod", http.StatusBadRequest},
		{"no kind", "version=v1", http.StatusBadRequest},
		{"unknown kind", "version=v1&kind=Nonesuch", http.StatusNotFound},
		{"unknown field", "version=v1&kind=Pod&field=nope", http.StatusNotFound},
		{"unknown nested field", "version=v1&kind=Pod&field=spec.nope", http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hndWantStatus(t, rig.get(t, "/api/v1/clusters/fake/explain?"+tc.query), tc.want)
		})
	}
}
