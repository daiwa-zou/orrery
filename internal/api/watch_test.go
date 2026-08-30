package api

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"github.com/daiwa-zou/orrery/internal/cluster"
)

func uobj(name, namespace, uid string, labels map[string]string) *unstructured.Unstructured {
	o := obj(name, namespace, labels, nil)
	o.SetUID(types.UID(uid))
	return o
}

func filterFor(t *testing.T, query string) *watchEventFilter {
	t.Helper()
	f, err := parseListFilter(req(query), podSet())
	if err != nil {
		t.Fatal(err)
	}
	return newWatchEventFilter(f)
}

func TestWatchEventFilterPassesThroughWhenUnfiltered(t *testing.T) {
	wf := filterFor(t, "")
	o := uobj("a", "demo", "u1", nil)

	if !wf.admitInitial(o) {
		t.Error("an unfiltered stream must admit every object")
	}
	if got, ok := wf.translate(cluster.EventModified, o); !ok || got != cluster.EventModified {
		t.Errorf("unfiltered translate = %v,%v", got, ok)
	}
	if wf.matched != nil {
		t.Error("no per-object state should be kept without a filter")
	}
}

func TestWatchEventFilterCrossingTheBoundary(t *testing.T) {
	wf := filterFor(t, "labelSelector=app%3Dweb")

	in := uobj("a", "demo", "u1", map[string]string{"app": "web"})
	out := uobj("a", "demo", "u1", map[string]string{"app": "api"})

	if !wf.admitInitial(in) {
		t.Fatal("a matching object belongs in INIT")
	}

	// Still matching: a plain modification.
	if got, ok := wf.translate(cluster.EventModified, in); !ok || got != cluster.EventModified {
		t.Errorf("in-filter modify = %v,%v, want MODIFIED", got, ok)
	}

	// Edited out of the filter: the subscriber must see it leave.
	if got, ok := wf.translate(cluster.EventModified, out); !ok || got != cluster.EventDeleted {
		t.Errorf("modify out of filter = %v,%v, want DELETED", got, ok)
	}

	// Now unknown and not matching: invisible.
	if _, ok := wf.translate(cluster.EventModified, out); ok {
		t.Error("a non-matching unknown object should stay invisible")
	}

	// Edited back into the filter: it arrives as ADDED, not MODIFIED.
	if got, ok := wf.translate(cluster.EventModified, in); !ok || got != cluster.EventAdded {
		t.Errorf("modify into filter = %v,%v, want ADDED", got, ok)
	}
}

func TestWatchEventFilterDeletes(t *testing.T) {
	wf := filterFor(t, "q=web")

	shown := uobj("web-1", "demo", "u1", nil)
	hidden := uobj("api-1", "demo", "u2", nil)

	if !wf.admitInitial(shown) {
		t.Fatal("web-1 should match q=web")
	}
	if wf.admitInitial(hidden) {
		t.Fatal("api-1 should not match q=web")
	}

	if got, ok := wf.translate(cluster.EventDeleted, shown); !ok || got != cluster.EventDeleted {
		t.Errorf("delete of a shown object = %v,%v, want DELETED", got, ok)
	}
	if _, ok := wf.translate(cluster.EventDeleted, hidden); ok {
		t.Error("delete of a never-shown object should be invisible")
	}

	// A matching delete for an object that raced in before INIT is forwarded;
	// a spurious DELETED only costs the client a refetch.
	raced := uobj("web-2", "demo", "u3", nil)
	if got, ok := wf.translate(cluster.EventDeleted, raced); !ok || got != cluster.EventDeleted {
		t.Errorf("delete of a matching unknown object = %v,%v, want DELETED", got, ok)
	}
}

func TestWatchEventFilterAdds(t *testing.T) {
	wf := filterFor(t, "q=web")

	if got, ok := wf.translate(cluster.EventAdded, uobj("web-9", "demo", "u9", nil)); !ok || got != cluster.EventAdded {
		t.Errorf("matching add = %v,%v, want ADDED", got, ok)
	}
	if _, ok := wf.translate(cluster.EventAdded, uobj("api-9", "demo", "u10", nil)); ok {
		t.Error("a non-matching add should be invisible")
	}
}

func TestRowMatchesQuery(t *testing.T) {
	row := map[string]any{
		"object":  "Pod/web-abc",
		"reason":  "BackOff",
		"message": "Back-off restarting failed container",
		"count":   int64(7),
	}

	for _, q := range []string{"pod/web", "backoff", "restarting"} {
		if !rowMatchesQuery(row, q, eventSearchKeys) {
			t.Errorf("q=%q should match", q)
		}
	}
	if rowMatchesQuery(row, "nomatch", eventSearchKeys) {
		t.Error("q=nomatch should not match")
	}
	// Only the listed keys are scanned.
	if rowMatchesQuery(row, "7", eventSearchKeys) {
		t.Error("count is not a search key")
	}
}
