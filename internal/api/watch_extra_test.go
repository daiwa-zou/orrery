package api

import "testing"

func TestWatchVisibilityPermits(t *testing.T) {
	inDemo := mkObj(t, map[string]any{"name": "x", "namespace": "demo"}, nil)
	inOther := mkObj(t, map[string]any{"name": "y", "namespace": "other"}, nil)

	all := watchVisibility{all: true, namespaced: true}
	if !all.permits(inDemo) || !all.permits(inOther) {
		t.Error("cluster-wide visibility should permit everything")
	}

	// Cluster-scoped resources have no namespace to filter on.
	clusterScoped := watchVisibility{namespaced: false}
	if !clusterScoped.permits(inDemo) {
		t.Error("cluster-scoped streams are not namespace-filtered")
	}

	scoped := watchVisibility{
		namespaced: true,
		namespaces: map[string]struct{}{"demo": {}},
	}
	if !scoped.permits(inDemo) {
		t.Error("an object in a permitted namespace was filtered out")
	}
	// The whole point of the filter: events from namespaces the caller may not
	// list must never cross the boundary.
	if scoped.permits(inOther) {
		t.Error("an object outside the permitted namespaces leaked through")
	}
}
