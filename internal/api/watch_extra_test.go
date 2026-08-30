package api

import (
	"context"
	"testing"

	"github.com/daiwa-zou/orrery/internal/authz"
	"github.com/daiwa-zou/orrery/internal/cluster"
)

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

// watchScopeFor is the scope a stream handler would be holding for a request
// that named these namespaces.
func watchScopeFor(t *testing.T, rig *hndRig, namespaces []string) watchVisibility {
	t.Helper()
	ctx := context.Background()
	res := hndResolved(t, rig, cluster.Identity{Username: "orrery:anonymous"})
	ar, err := res.cluster.Discovery.Resolve(ctx, "", "v1", "pods")
	if err != nil {
		t.Fatal(err)
	}
	res.resource = ar

	watched := ""
	if len(namespaces) == 1 {
		watched = namespaces[0]
	}
	vis, _, err := rig.api.watchScope(ctx, res, authz.Attributes{
		Verb: "watch", Group: ar.Group, Version: ar.Version,
		Resource: ar.Name, Namespace: watched,
	}, namespaces)
	if err != nil {
		t.Fatalf("watchScope(%v) = %v, want a scope", namespaces, err)
	}
	return vis
}

// A stream narrowed to one namespace must still be filtered to it.
//
// The tempting shortcut is that asking the informer for one namespace is
// already the filter, so the stream needs none of its own. That is true of the
// INIT snapshot, which comes from a namespace-indexed List, and false of every
// event after it: there is exactly one informer per resource, it lists and
// watches at NamespaceAll, and its broadcaster fans every change out to every
// subscriber. Take the filter away and a viewer of one namespace is sent live
// pods from all of them — the cache holds the dashboard's view, not theirs,
// which is the reason the access review exists at all.
func TestWatchScopeFiltersEvenASingleNamespace(t *testing.T) {
	rig := hndNewRig(t)
	vis := watchScopeFor(t, rig, []string{"demo"})

	if !vis.permits(mkObj(t, map[string]any{"name": "x", "namespace": "demo"}, nil)) {
		t.Error("the namespace the caller asked for was filtered out")
	}
	if vis.permits(mkObj(t, map[string]any{"name": "y", "namespace": "other"}, nil)) {
		t.Error("a stream scoped to one namespace admitted another namespace's objects")
	}
}

// The same holds for several: allowed namespaces pass, everything else does not.
func TestWatchScopeFiltersSeveralNamespaces(t *testing.T) {
	rig := hndNewRig(t)
	vis := watchScopeFor(t, rig, []string{"demo", "kube-system"})

	for _, ns := range []string{"demo", "kube-system"} {
		if !vis.permits(mkObj(t, map[string]any{"name": "x", "namespace": ns}, nil)) {
			t.Errorf("namespace %q was filtered out", ns)
		}
	}
	if vis.permits(mkObj(t, map[string]any{"name": "y", "namespace": "other"}, nil)) {
		t.Error("a namespace nobody asked for admitted objects")
	}
}
