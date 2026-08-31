package api

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Projecting into a caller's map is what stops sorting from allocating a row
// per object, and it buys that with a hazard the returning version could not
// have: the projectors write conditional keys, so a key one object has and the
// next does not is still sitting in the map when the next one is projected.
// Sorting would then order an object by its predecessor's value — silently,
// and differently depending on what order the objects arrived in.

// namespaceSet projects the identity fields, one of which is conditional.
func namespaceSet() columnSet {
	return columnSet{columns: []Column{{Key: "namespace"}}, row: baseRow}
}

func TestSortByCellDoesNotCarryAKeyBetweenObjects(t *testing.T) {
	// Cluster-scoped objects have no namespace key at all, so each one that
	// follows a namespaced object is a chance to read the wrong value.
	objs := []*unstructured.Unstructured{
		obj("d-namespaced", "zeta", nil, nil),
		obj("a-cluster-scoped", "", nil, nil),
		obj("c-namespaced", "alpha", nil, nil),
		obj("b-cluster-scoped", "", nil, nil),
	}

	sortByCell(objs, namespaceSet(), "namespace", false)

	// The two with no namespace sort together under the absent cell, and tie
	// on name; the namespaced two follow in namespace order. What must not
	// happen is a cluster-scoped object sorting as though it were in "zeta".
	for i, want := range []string{"a-cluster-scoped", "b-cluster-scoped", "c-namespaced", "d-namespaced"} {
		if got := objs[i].GetName(); got != want {
			t.Fatalf("objs[%d] = %s, want %s: a cell leaked between objects",
				i, got, want)
		}
	}
}

// The same hazard for a flag rather than a value. _terminating is set only on
// objects being deleted, so one deleting pod would otherwise mark every pod
// projected after it.
func TestSortByCellDoesNotCarryTheTerminatingFlag(t *testing.T) {
	deleting := obj("deleting", "d", nil, map[string]any{})
	deleting.Object["metadata"].(map[string]any)["deletionTimestamp"] = "2024-01-02T03:04:05Z"
	live := obj("live", "d", nil, nil)

	set := columnSet{columns: []Column{{Key: "_terminating"}}, row: baseRow}
	objs := []*unstructured.Unstructured{deleting, live}

	sortByCell(objs, set, "_terminating", false)

	// Read each row back on its own; whichever order the sort left them in,
	// only the deleting object carries the flag.
	for _, o := range objs {
		row := set.rowOf(o)
		_, marked := row["_terminating"]
		if want := o.GetName() == "deleting"; marked != want {
			t.Errorf("%s: _terminating = %v, want %v", o.GetName(), marked, want)
		}
	}

	// And through the reusing path, in the order that would leak.
	scratch := map[string]any{}
	set.row(deleting, scratch)
	clear(scratch)
	set.row(live, scratch)
	if _, marked := scratch["_terminating"]; marked {
		t.Error("the terminating flag survived into the next object's row")
	}
}

// rowOf is the allocating wrapper the keeping callers use, and what they keep
// must not be shared with anything else.
func TestRowOfReturnsAnIndependentMap(t *testing.T) {
	set := namespaceSet()
	a := set.rowOf(obj("a", "one", nil, nil))
	b := set.rowOf(obj("b", "two", nil, nil))

	if a["name"] != "a" || b["name"] != "b" {
		t.Fatalf("rows = %v, %v", a, b)
	}
	a["name"] = "mutated"
	if b["name"] != "b" {
		t.Error("two rows shared a map")
	}
}

// terminating replaces a GetDeletionTimestamp call that parsed the stamp only
// to discard it. What it decides has to match, for everything the API server
// can actually produce.
func TestTerminating(t *testing.T) {
	for _, c := range []struct {
		name string
		meta map[string]any
		want bool
	}{
		{"no metadata at all", nil, false},
		{"no stamp", map[string]any{"name": "x"}, false},
		{"stamp set", map[string]any{"deletionTimestamp": "2024-01-02T03:04:05Z"}, true},
		// An explicit null is how a stamp is cleared in a patch, and it is not
		// a deletion.
		{"null stamp", map[string]any{"deletionTimestamp": nil}, false},
		{"empty stamp", map[string]any{"deletionTimestamp": ""}, false},
		{"stamp of the wrong type", map[string]any{"deletionTimestamp": int64(1)}, false},
		// Unparseable, but present. The old code read this as alive; a stamp
		// nobody can parse still means the object is on its way out.
		{"unparseable stamp", map[string]any{"deletionTimestamp": "soon"}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			o := &unstructured.Unstructured{Object: map[string]any{}}
			if c.meta != nil {
				o.Object["metadata"] = c.meta
			}
			if got := terminating(o); got != c.want {
				t.Errorf("terminating(%v) = %v, want %v", c.meta, got, c.want)
			}
		})
	}
}

// podStatus reports Terminating ahead of everything else, so a pod that is
// going away does not read as Running while its containers wind down.
func TestPodStatusTerminatingWins(t *testing.T) {
	u := obj("p", "d", nil, map[string]any{
		"status": map[string]any{"phase": "Running"},
	})
	u.Object["metadata"].(map[string]any)["deletionTimestamp"] = "2024-01-02T03:04:05Z"

	if got := podStatus(u); got != "Terminating" {
		t.Errorf("podStatus = %q, want Terminating", got)
	}
}
