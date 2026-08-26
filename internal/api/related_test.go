package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/daiwa-zou/orrery/internal/cluster"
)

// hndSeed adds an object to the fake before any informer has listed, so the
// caches pick it up on their first sync. Called after the rig is built but
// before the first request.
func hndSeed(t *testing.T, rig *hndRig, group, version, resource string, obj map[string]any) {
	t.Helper()
	rig.fake.mu.Lock()
	defer rig.fake.mu.Unlock()
	res := rig.fake.resources[hndKey(group, version, resource)]
	if res == nil {
		t.Fatalf("the fake serves no %s/%s/%s", group, version, resource)
	}
	res.items = append(res.items, obj)
}

func related(t *testing.T, rig *hndRig, path string) relatedResponse {
	t.Helper()
	rec := rig.get(t, path)
	hndWantStatus(t, rec, http.StatusOK)
	var body relatedResponse
	hndDecode(t, rec, &body)
	return body
}

// find returns the reference to one object, or fails.
func (rr relatedResponse) find(t *testing.T, kind, name string) objectRef {
	t.Helper()
	for _, ref := range rr.Related {
		if ref.Kind == kind && ref.Name == name {
			return ref
		}
	}
	t.Fatalf("no %s/%s in the neighbourhood: %s", kind, name, rr.render())
	return objectRef{}
}

func (rr relatedResponse) has(kind, name string) bool {
	for _, ref := range rr.Related {
		if ref.Kind == kind && ref.Name == name {
			return true
		}
	}
	return false
}

func (rr relatedResponse) render() string {
	var b strings.Builder
	for _, ref := range rr.Related {
		b.WriteString(ref.Relation + ":" + ref.Kind + "/" + ref.Name + " ")
	}
	if len(rr.Warnings) > 0 {
		b.WriteString("| warnings: " + strings.Join(rr.Warnings, "; "))
	}
	return b.String()
}

// The headline case: a Deployment reaches its pods through its ReplicaSets, so
// a caller never has to know that the edge is two hops and goes through an
// object nobody thinks about.
func TestRelatedWalksDeploymentToPods(t *testing.T) {
	rig := hndNewRig(t)
	hndSeed(t, rig, "", "v1", "pods", hndObj("", "v1", "Pod", "demo", "web-rev2-aaa", map[string]any{
		"labels": map[string]any{"app": "web", "pod-template-hash": "abc123"},
		"ownerReferences": []any{map[string]any{
			"apiVersion": "apps/v1", "kind": "ReplicaSet", "name": "web-rev2",
			"uid": "uid-web-rev2", "controller": true,
		}},
		"spec":   map[string]any{"nodeName": "node-1", "containers": []any{map[string]any{"name": "app", "image": "web:2"}}},
		"status": map[string]any{"phase": "Running"},
	}))

	got := related(t, rig, "/api/v1/clusters/fake/resources/apps/v1/deployments/demo/web/related")

	if got.Object.Kind != "Deployment" || got.Object.Name != "web" {
		t.Errorf("subject = %+v", got.Object)
	}

	rs := got.find(t, "ReplicaSet", "web-rev2")
	if rs.Relation != "child" || rs.Depth != 1 {
		t.Errorf("web-rev2 = %+v, want a depth-1 child", rs)
	}
	if !got.has("ReplicaSet", "web-rev1") {
		t.Errorf("the older revision is missing: %s", got.render())
	}

	pod := got.find(t, "Pod", "web-rev2-aaa")
	if pod.Relation != "descendant" || pod.Depth != 2 {
		t.Errorf("pod = %+v, want a depth-2 descendant", pod)
	}
	// The path is the point: a caller follows it instead of reassembling a
	// route out of placeholders.
	if want := "/api/v1/clusters/fake/resources/core/v1/pods/demo/web-rev2-aaa"; pod.Path != want {
		t.Errorf("path = %q, want %q", pod.Path, want)
	}
	if pod.UID != "uid-web-rev2-aaa" {
		t.Errorf("uid = %q", pod.UID)
	}
}

// depth=1 stops at the ReplicaSets. A caller that wants the shallow answer
// should be able to ask for it and not pay for the pod scan.
func TestRelatedDepthBoundsTheWalk(t *testing.T) {
	rig := hndNewRig(t)
	hndSeed(t, rig, "", "v1", "pods", hndObj("", "v1", "Pod", "demo", "web-rev2-aaa", map[string]any{
		"ownerReferences": []any{map[string]any{
			"apiVersion": "apps/v1", "kind": "ReplicaSet", "name": "web-rev2",
			"uid": "uid-web-rev2", "controller": true,
		}},
		"spec": map[string]any{"containers": []any{}},
	}))

	got := related(t, rig, "/api/v1/clusters/fake/resources/apps/v1/deployments/demo/web/related?depth=1")
	if !got.has("ReplicaSet", "web-rev2") {
		t.Errorf("depth=1 lost the direct children: %s", got.render())
	}
	if got.has("Pod", "web-rev2-aaa") {
		t.Errorf("depth=1 reached a grandchild: %s", got.render())
	}
}

func TestRelatedClimbsToTheOwner(t *testing.T) {
	rig := hndNewRig(t)
	got := related(t, rig, "/api/v1/clusters/fake/resources/core/v1/pods/demo/ds-1/related")

	ds := got.find(t, "DaemonSet", "ds")
	if ds.Relation != "owner" || ds.Depth != 1 {
		t.Errorf("owner = %+v, want a depth-1 owner", ds)
	}
	if ds.Resource != "daemonsets" || ds.Group != "apps" {
		t.Errorf("owner resource = %s/%s, want apps/daemonsets", ds.Group, ds.Resource)
	}
	if ds.Status == "" {
		t.Errorf("owner has no status; the fixture DaemonSet is fully ready")
	}

	// The node a pod runs on is the edge people actually follow next.
	node := got.find(t, "Node", "node-1")
	if node.Relation != "node" {
		t.Errorf("node relation = %q", node.Relation)
	}
	if node.Namespace != "" {
		t.Errorf("node namespace = %q, want cluster scope", node.Namespace)
	}
	if want := "/api/v1/clusters/fake/resources/core/v1/nodes/_/node-1"; node.Path != want {
		t.Errorf("node path = %q, want %q", node.Path, want)
	}
}

func TestRelatedBundlesTheObjectsEvents(t *testing.T) {
	rig := hndNewRig(t)
	got := related(t, rig, "/api/v1/clusters/fake/resources/core/v1/pods/demo/web-1/related")

	if len(got.Events) != 1 {
		t.Fatalf("events = %v, want the one recorded against uid-web-1", got.Events)
	}
	if len(got.EventColumns) == 0 {
		t.Error("events came without columns; a caller cannot render them")
	}
	// Another pod's event must not leak in on a name or kind coincidence.
	for _, e := range got.Events {
		if msg, _ := e["message"].(string); strings.Contains(msg, "Back-off") {
			t.Errorf("web-2's event was attributed to web-1: %v", e)
		}
	}

	off := related(t, rig, "/api/v1/clusters/fake/resources/core/v1/pods/demo/web-1/related?events=false")
	if len(off.Events) != 0 {
		t.Errorf("events=false still returned %d events", len(off.Events))
	}
}

func TestRelatedMatchesServiceSelectors(t *testing.T) {
	rig := hndNewRig(t)
	hndSeed(t, rig, "", "v1", "services", hndObj("", "v1", "Service", "demo", "web-svc", map[string]any{
		"spec": map[string]any{
			"type":     "ClusterIP",
			"selector": map[string]any{"app": "web"},
		},
	}))

	fromService := related(t, rig, "/api/v1/clusters/fake/resources/core/v1/services/demo/web-svc/related")
	for _, name := range []string{"web-1", "web-2"} {
		pod := fromService.find(t, "Pod", name)
		if pod.Relation != "selects" {
			t.Errorf("%s relation = %q, want selects", name, pod.Relation)
		}
	}
	// ds-1 carries no app label, so an over-broad match would be visible here.
	if fromService.has("Pod", "ds-1") {
		t.Errorf("the selector matched an unlabelled pod: %s", fromService.render())
	}

	fromPod := related(t, rig, "/api/v1/clusters/fake/resources/core/v1/pods/demo/web-1/related")
	svc := fromPod.find(t, "Service", "web-svc")
	if svc.Relation != "selected-by" {
		t.Errorf("service relation = %q, want selected-by", svc.Relation)
	}
	// The fixture's selector-less Service must not claim every pod.
	if fromPod.has("Service", "svc") {
		t.Errorf("a Service with no selector was reported as selecting: %s", fromPod.render())
	}
}

func TestRelatedListsPodsOnANode(t *testing.T) {
	rig := hndNewRig(t)
	got := related(t, rig, "/api/v1/clusters/fake/resources/core/v1/nodes/_/node-1/related")

	pod := got.find(t, "Pod", "web-1")
	if pod.Relation != "hosts" {
		t.Errorf("relation = %q, want hosts", pod.Relation)
	}
	if got.Object.Kind != "Node" {
		t.Errorf("subject = %+v", got.Object)
	}
}

// A pod names the ConfigMaps and Secrets it mounts. Naming them is not reading
// them: the caller could already see those names in the pod spec, and the path
// leads to a route that will run its own access review.
func TestRelatedNamesWhatAPodMounts(t *testing.T) {
	rig := hndNewRig(t)
	hndSeed(t, rig, "", "v1", "pods", hndObj("", "v1", "Pod", "demo", "mounty", map[string]any{
		"spec": map[string]any{
			"serviceAccountName": "runner",
			"imagePullSecrets":   []any{map[string]any{"name": "regcred"}},
			"volumes": []any{
				map[string]any{"name": "cfg", "configMap": map[string]any{"name": "app-config"}},
				map[string]any{"name": "sec", "secret": map[string]any{"secretName": "app-secret"}},
				map[string]any{"name": "data", "persistentVolumeClaim": map[string]any{"claimName": "app-data"}},
			},
			"containers": []any{map[string]any{
				"name": "app", "image": "web:2",
				"envFrom": []any{map[string]any{"configMapRef": map[string]any{"name": "env-config"}}},
				"env": []any{map[string]any{
					"name":      "TOKEN",
					"valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": "env-secret", "key": "t"}},
				}},
			}},
		},
		"status": map[string]any{"phase": "Running"},
	}))
	// Reading Secrets is denied outright; the names must still come back.
	rig.fake.denyResource = "secrets"

	got := related(t, rig, "/api/v1/clusters/fake/resources/core/v1/pods/demo/mounty/related")

	for _, want := range []struct{ kind, name string }{
		{"ServiceAccount", "runner"},
		{"Secret", "regcred"},
		{"Secret", "app-secret"},
		{"Secret", "env-secret"},
		{"ConfigMap", "app-config"},
		{"ConfigMap", "env-config"},
		{"PersistentVolumeClaim", "app-data"},
	} {
		ref := got.find(t, want.kind, want.name)
		if ref.Relation != "reference" {
			t.Errorf("%s/%s relation = %q, want reference", want.kind, want.name, ref.Relation)
		}
		// Either the caller can follow it or it says why not. A reference with
		// neither is a dead end the caller cannot even report.
		if ref.Path == "" && ref.Note == "" {
			t.Errorf("%s/%s has no path and no explanation", want.kind, want.name)
		}
	}

	// The Secret names are the interesting half: reads of them are denied, and
	// they are still named, with a route that will refuse the read itself.
	secret := got.find(t, "Secret", "app-secret")
	if secret.Path == "" {
		t.Errorf("a Secret this cluster serves came back with no path: %+v", secret)
	}
	if secret.Note != "" {
		t.Errorf("naming a Secret should not need permission; note = %q", secret.Note)
	}

	// This cluster serves no ServiceAccounts, so the reference is honest about
	// being unfollowable rather than handing over a route that 404s.
	sa := got.find(t, "ServiceAccount", "runner")
	if sa.Note == "" || sa.Path != "" {
		t.Errorf("unserved resource = %+v, want a note and no path", sa)
	}
}

// A scan the caller may not run is a gap in the answer, and saying so is the
// difference between "this Deployment has no pods" and "I could not look".
func TestRelatedReportsForbiddenScansAsWarnings(t *testing.T) {
	rig := hndNewRig(t)
	rig.fake.denyResource = "pods"

	got := related(t, rig, "/api/v1/clusters/fake/resources/apps/v1/deployments/demo/web/related")

	if !got.has("ReplicaSet", "web-rev2") {
		t.Errorf("a forbidden pod scan sank the readable part of the answer: %s", got.render())
	}
	if len(got.Warnings) == 0 {
		t.Fatal("the pod scan was skipped silently")
	}
	joined := strings.Join(got.Warnings, "; ")
	if !strings.Contains(joined, "pods") {
		t.Errorf("warnings = %q, want them to name pods", joined)
	}
	// One fact, however many parents hit it.
	seen := map[string]bool{}
	for _, wmsg := range got.Warnings {
		if seen[wmsg] {
			t.Errorf("warning repeated: %q", wmsg)
		}
		seen[wmsg] = true
	}
}

// Reading the subject is gated exactly as a plain GET of it would be.
func TestRelatedRefusesAnUnreadableSubject(t *testing.T) {
	rig := hndNewRig(t)
	rig.fake.denyResource = "deployments"
	rec := rig.get(t, "/api/v1/clusters/fake/resources/apps/v1/deployments/demo/web/related")
	hndWantStatus(t, rec, http.StatusForbidden)
}

func TestRelatedRejections(t *testing.T) {
	rig := hndNewRig(t)
	cases := []struct {
		name, path string
		want       int
	}{
		{"no such object", "/api/v1/clusters/fake/resources/apps/v1/deployments/demo/ghost/related", http.StatusNotFound},
		{"no such resource", "/api/v1/clusters/fake/resources/apps/v1/gizmos/demo/web/related", http.StatusNotFound},
		{"no such cluster", "/api/v1/clusters/other/resources/apps/v1/deployments/demo/web/related", http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hndWantStatus(t, rig.get(t, tc.path), tc.want)
		})
	}
}

// A custom controller's children are unreachable from the built-in table, so a
// caller can name the resource to scan.
func TestRelatedScansCallerNamedChildResources(t *testing.T) {
	rig := hndNewRig(t)
	hndSeed(t, rig, "example.com", "v1", "widgets", hndObj("example.com", "v1", "Widget", "demo", "w-owned", map[string]any{
		"ownerReferences": []any{map[string]any{
			"apiVersion": "apps/v1", "kind": "Deployment", "name": "web",
			"uid": "uid-web", "controller": true,
		}},
		"spec": map[string]any{"color": "green", "count": int64(1)},
	}))

	plain := related(t, rig, "/api/v1/clusters/fake/resources/apps/v1/deployments/demo/web/related")
	if plain.has("Widget", "w-owned") {
		t.Errorf("widgets were scanned without being asked for: %s", plain.render())
	}

	asked := related(t, rig,
		"/api/v1/clusters/fake/resources/apps/v1/deployments/demo/web/related?childResource=example.com/v1/widgets")
	ref := asked.find(t, "Widget", "w-owned")
	if ref.Relation != "child" {
		t.Errorf("relation = %q, want child", ref.Relation)
	}

	bad := rig.get(t, "/api/v1/clusters/fake/resources/apps/v1/deployments/demo/web/related?childResource=a/b/c/d")
	hndWantStatus(t, bad, http.StatusOK)
	var body relatedResponse
	hndDecode(t, bad, &body)
	if len(body.Warnings) == 0 {
		t.Error("a malformed childResource was ignored silently")
	}
}

func TestResourcePathPlaceholders(t *testing.T) {
	cases := []struct {
		group, version, name, namespace, object, want string
	}{
		{"apps", "v1", "deployments", "demo", "web",
			"/api/v1/clusters/c/resources/apps/v1/deployments/demo/web"},
		// The core group and cluster scope both have a spelling, and the
		// resource routes only accept those spellings.
		{"", "v1", "nodes", "", "node-1",
			"/api/v1/clusters/c/resources/core/v1/nodes/_/node-1"},
	}
	for _, tc := range cases {
		ar := cluster.APIResource{Group: tc.group, Version: tc.version, Name: tc.name}
		if got := resourcePath("c", ar, tc.namespace, tc.object); got != tc.want {
			t.Errorf("got %q, want %q", got, tc.want)
		}
	}
}

func TestSelectorMatches(t *testing.T) {
	labels := map[string]any{"app": "web", "tier": "front"}
	cases := []struct {
		name     string
		selector map[string]any
		want     bool
	}{
		{"subset matches", map[string]any{"app": "web"}, true},
		{"exact match", map[string]any{"app": "web", "tier": "front"}, true},
		{"wrong value", map[string]any{"app": "api"}, false},
		{"missing key", map[string]any{"app": "web", "zone": "a"}, false},
		// An empty selector matching everything is how a Service with no
		// selector would claim the namespace; callers guard against it, and
		// this pins that the guard is theirs to make.
		{"empty selector", map[string]any{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := selectorMatches(tc.selector, labels); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParseChildResource(t *testing.T) {
	cases := []struct {
		in                       string
		group, version, resource string
		ok                       bool
	}{
		{"pods", "", "", "pods", true},
		{"v1/pods", "", "v1", "pods", true},
		{"apps/v1/deployments", "apps", "v1", "deployments", true},
		{"", "", "", "", false},
		{"a/b/c/d", "", "", "", false},
	}
	for _, tc := range cases {
		g, v, r, ok := parseChildResource(tc.in)
		if g != tc.group || v != tc.version || r != tc.resource || ok != tc.ok {
			t.Errorf("%q = (%q,%q,%q,%v), want (%q,%q,%q,%v)",
				tc.in, g, v, r, ok, tc.group, tc.version, tc.resource, tc.ok)
		}
	}
}

func TestNeighbourhoodCapsAndDeduplicates(t *testing.T) {
	n := newNeighbourhood("c")
	ref := objectRef{Kind: "Pod", Resource: "pods", Namespace: "demo", Name: "web-1"}
	if !n.add(ref) || !n.add(ref) {
		t.Fatal("add refused a reference before the cap")
	}
	if len(n.refs) != 1 {
		t.Errorf("the same object was recorded %d times", len(n.refs))
	}

	for i := 0; i < maxRelated+5; i++ {
		n.add(objectRef{Kind: "Pod", Resource: "pods", Namespace: "demo", Name: string(rune('a'+i%26)) + string(rune('a'+i/26))})
	}
	if len(n.refs) > maxRelated {
		t.Errorf("refs = %d, over the cap of %d", len(n.refs), maxRelated)
	}
	if !n.truncated {
		t.Error("the cap was applied without being reported")
	}
}

func TestRelatedFollowsIngressBackends(t *testing.T) {
	rig := hndNewRig(t)
	got := related(t, rig, "/api/v1/clusters/fake/resources/networking.k8s.io/v1/ingresses/demo/web-ing/related")

	// Both the default backend and the one named in a rule: an Ingress that
	// routes to two services and reports one is worse than reporting neither.
	for _, name := range []string{"svc", "web-backend"} {
		ref := got.find(t, "Service", name)
		if ref.Relation != "reference" {
			t.Errorf("%s relation = %q, want reference", name, ref.Relation)
		}
		if ref.Namespace != "demo" {
			t.Errorf("%s namespace = %q", name, ref.Namespace)
		}
	}
	// web-backend does not exist; naming it is still the useful answer, since
	// a missing backend is usually why the Ingress is being looked at.
	if !got.has("Service", "web-backend") {
		t.Error("a backend that resolves to nothing was dropped")
	}
}

func TestRelatedLinksAClaimToItsVolume(t *testing.T) {
	rig := hndNewRig(t)
	got := related(t, rig, "/api/v1/clusters/fake/resources/core/v1/persistentvolumeclaims/demo/data/related")

	pv := got.find(t, "PersistentVolume", "pv-data")
	if pv.Relation != "reference" {
		t.Errorf("relation = %q, want reference", pv.Relation)
	}
	if pv.Namespace != "" {
		t.Errorf("namespace = %q, want cluster scope", pv.Namespace)
	}
	if want := "/api/v1/clusters/fake/resources/core/v1/persistentvolumes/_/pv-data"; pv.Path != want {
		t.Errorf("path = %q, want %q", pv.Path, want)
	}
}

// An owner reference this server cannot follow is still worth reporting: it
// names the controller, which is often the whole answer.
func TestRelatedReportsOwnersItCannotFollow(t *testing.T) {
	rig := hndNewRig(t)
	hndSeed(t, rig, "", "v1", "pods", hndObj("", "v1", "Pod", "demo", "orphan", map[string]any{
		"ownerReferences": []any{
			map[string]any{
				"apiVersion": "acme.example/v1", "kind": "Sprocket",
				"name": "sprocket-1", "uid": "uid-sprocket-1", "controller": true,
			},
			map[string]any{
				// Two slashes is the one spelling apimachinery refuses outright.
				"apiVersion": "too/many/slashes", "kind": "Mystery",
				"name": "mystery-1", "uid": "uid-mystery-1",
			},
		},
		"spec":   map[string]any{"containers": []any{}},
		"status": map[string]any{"phase": "Running"},
	}))

	got := related(t, rig, "/api/v1/clusters/fake/resources/core/v1/pods/demo/orphan/related")

	unserved := got.find(t, "Sprocket", "sprocket-1")
	if unserved.Note == "" {
		t.Errorf("an unserved owner came back with no explanation: %+v", unserved)
	}
	if unserved.Path != "" {
		t.Errorf("a route was offered for a resource the cluster does not serve: %q", unserved.Path)
	}
	if unserved.UID != "uid-sprocket-1" {
		t.Errorf("uid = %q; the reference is the only thing left to identify it by", unserved.UID)
	}

	broken := got.find(t, "Mystery", "mystery-1")
	if !strings.Contains(broken.Note, "apiVersion") {
		t.Errorf("note = %q, want it to name the unparseable apiVersion", broken.Note)
	}
}

// The climb is gated hop by hop: an owner the caller may not read ends the
// walk with a named reference rather than leaking the object.
func TestRelatedStopsClimbingAtAnUnreadableOwner(t *testing.T) {
	rig := hndNewRig(t)
	rig.fake.denyResource = "daemonsets"

	got := related(t, rig, "/api/v1/clusters/fake/resources/core/v1/pods/demo/ds-1/related")

	ds := got.find(t, "DaemonSet", "ds")
	if ds.Note == "" {
		t.Errorf("an unreadable owner came back without saying so: %+v", ds)
	}
	// The name was already visible in the pod's own ownerReferences, so
	// reporting it leaks nothing; its status would have been new information.
	if ds.Status != "" {
		t.Errorf("status = %q, read from an object the caller may not get", ds.Status)
	}
	if ds.Path == "" {
		t.Error("no path offered; the caller cannot even try the read itself")
	}
}
