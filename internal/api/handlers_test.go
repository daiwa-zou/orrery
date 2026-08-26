package api

// HTTP-level tests for the generic resource surface: list/get/create/update/
// patch/delete through the router, facets, and the authorization outcomes the
// frontend depends on (403 from a denied SubjectAccessReview, 404 for unknown
// resources).

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestResourceHandlersHTTP(t *testing.T) {
	rig := hndNewRig(t)

	t.Run("listPodsTable", func(t *testing.T) {
		rec := rig.get(t, "/api/v1/clusters/fake/resources/_/v1/pods")
		hndWantStatus(t, rec, 200)
		var body listResponse
		hndDecode(t, rec, &body)
		if body.Total != 4 || len(body.Items) != 4 || len(body.Columns) == 0 {
			t.Fatalf("list = total %d, items %d, columns %d", body.Total, len(body.Items), len(body.Columns))
		}
		if !body.Scope.AllNamespaces {
			t.Errorf("scope = %+v, want allNamespaces", body.Scope)
		}
		if body.Resource.Kind != "Pod" || !body.Resource.Namespaced {
			t.Errorf("resource meta = %+v", body.Resource)
		}
		// Default sort is by name; labels ride along for the table.
		if body.Items[0]["name"] != "done-1" {
			t.Errorf("first row = %v", body.Items[0]["name"])
		}
		for _, it := range body.Items {
			if it["name"] == "web-1" {
				labels, _ := it["_labels"].(map[string]any)
				if labels["app"] != "web" {
					t.Errorf("web-1 labels = %v", it["_labels"])
				}
			}
		}
	})

	t.Run("listPodsFiltered", func(t *testing.T) {
		rec := rig.get(t, "/api/v1/clusters/fake/resources/_/v1/pods?q=web")
		var body listResponse
		hndDecode(t, rec, &body)
		if body.Total != 2 {
			t.Errorf("q=web matched %d", body.Total)
		}

		rec = rig.get(t, "/api/v1/clusters/fake/resources/_/v1/pods?labelSelector=app%3Dweb")
		hndDecode(t, rec, &body)
		if body.Total != 2 {
			t.Errorf("labelSelector matched %d", body.Total)
		}

		rec = rig.get(t, "/api/v1/clusters/fake/resources/_/v1/pods?fieldSelector=status.phase%3DSucceeded")
		hndDecode(t, rec, &body)
		if body.Total != 1 || body.Items[0]["name"] != "done-1" {
			t.Errorf("fieldSelector = %+v", body.Items)
		}

		// A bad selector is a 400, not an empty result.
		rec = rig.get(t, "/api/v1/clusters/fake/resources/_/v1/pods?labelSelector=%3D%3Dbroken")
		hndWantStatus(t, rec, 400)
	})

	t.Run("listPodsFullView", func(t *testing.T) {
		rec := rig.get(t, "/api/v1/clusters/fake/resources/_/v1/pods?view=full&pageSize=2&page=1")
		hndWantStatus(t, rec, 200)
		var body listResponse
		hndDecode(t, rec, &body)
		if body.Total != 4 || len(body.Objects) != 2 || len(body.Items) != 0 {
			t.Fatalf("full view = total %d, objects %d, items %d", body.Total, len(body.Objects), len(body.Items))
		}
		if body.Objects[0].GetKind() != "Pod" {
			t.Errorf("object kind = %q", body.Objects[0].GetKind())
		}
	})

	t.Run("listNamespaceScoped", func(t *testing.T) {
		rec := rig.get(t, "/api/v1/clusters/fake/resources/_/v1/pods?namespace=demo")
		var body listResponse
		hndDecode(t, rec, &body)
		if body.Total != 4 || body.Scope.Namespace != "demo" {
			t.Errorf("namespaced list = total %d scope %+v", body.Total, body.Scope)
		}
	})

	t.Run("listSortDescending", func(t *testing.T) {
		rec := rig.get(t, "/api/v1/clusters/fake/resources/_/v1/pods?sort=name&order=desc")
		var body listResponse
		hndDecode(t, rec, &body)
		if body.Items[0]["name"] != "web-2" {
			t.Errorf("desc sort first row = %v", body.Items[0]["name"])
		}
	})

	t.Run("unknownResource", func(t *testing.T) {
		rec := rig.get(t, "/api/v1/clusters/fake/resources/_/v1/gizmos")
		hndWantStatus(t, rec, 404)
		var body errorBody
		hndDecode(t, rec, &body)
		if body.Error != "unknown_resource" {
			t.Errorf("error kind = %q", body.Error)
		}
	})

	t.Run("notListable", func(t *testing.T) {
		rec := rig.get(t, "/api/v1/clusters/fake/resources/authorization.k8s.io/v1/subjectaccessreviews")
		hndWantStatus(t, rec, 400)
	})

	t.Run("forbiddenList", func(t *testing.T) {
		// The fake denies every access review for secrets, so the request must
		// die on the SubjectAccessReview — never reach the cache.
		rig.fake.mu.Lock()
		rig.fake.denyResource = "secrets"
		rig.fake.mu.Unlock()

		rec := rig.get(t, "/api/v1/clusters/fake/resources/_/v1/secrets")
		hndWantStatus(t, rec, 403)
		var body errorBody
		hndDecode(t, rec, &body)
		if body.Error != "forbidden" || !strings.Contains(body.Reason, "not allowed to list secrets") {
			t.Errorf("forbidden body = %+v", body)
		}
	})

	t.Run("facets", func(t *testing.T) {
		rec := rig.get(t, "/api/v1/clusters/fake/resources/_/v1/pods/facets")
		hndWantStatus(t, rec, 200)
		var body facetsResponse
		hndDecode(t, rec, &body)
		labels := map[string][]string{}
		for _, f := range body.Labels {
			labels[f.Key] = f.Values
		}
		if got := labels["app"]; len(got) != 1 || got[0] != "web" {
			t.Errorf("label facet app = %v", got)
		}
		fields := map[string][]string{}
		for _, f := range body.Fields {
			fields[f.Key] = f.Values
		}
		if got := fields["status.phase"]; len(got) != 2 {
			t.Errorf("status.phase facet = %v", got)
		}
	})

	t.Run("getPod", func(t *testing.T) {
		rec := rig.get(t, "/api/v1/clusters/fake/resources/_/v1/pods/demo/web-1")
		hndWantStatus(t, rec, 200)
		var body map[string]any
		hndDecode(t, rec, &body)
		meta, _ := body["metadata"].(map[string]any)
		if meta["name"] != "web-1" || meta["namespace"] != "demo" {
			t.Errorf("got %v", meta)
		}
	})

	t.Run("getPodYAML", func(t *testing.T) {
		rec := rig.get(t, "/api/v1/clusters/fake/resources/_/v1/pods/demo/web-1?format=yaml")
		hndWantStatus(t, rec, 200)
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/yaml") {
			t.Errorf("content type = %q", ct)
		}
		if !strings.Contains(rec.Body.String(), "name: web-1") {
			t.Errorf("yaml body = %q", rec.Body.String())
		}
	})

	t.Run("getMissingPod", func(t *testing.T) {
		// The API server's own 404 Status must pass through untranslated.
		rec := rig.get(t, "/api/v1/clusters/fake/resources/_/v1/pods/demo/ghost")
		hndWantStatus(t, rec, 404)
		var body errorBody
		hndDecode(t, rec, &body)
		if body.Error != "notfound" {
			t.Errorf("error kind = %q", body.Error)
		}
	})

	t.Run("getClusterScoped", func(t *testing.T) {
		// Cluster-scoped resources use the "_" namespace placeholder.
		rec := rig.get(t, "/api/v1/clusters/fake/resources/_/v1/nodes/_/node-1")
		hndWantStatus(t, rec, 200)
	})

	t.Run("createConfigMap", func(t *testing.T) {
		manifest := `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"cm-1"},"data":{"k":"v"}}`

		// A namespaced resource with no namespace anywhere is a 400.
		rec := rig.do(t, http.MethodPost, "/api/v1/clusters/fake/resources/_/v1/configmaps", manifest, nil)
		hndWantStatus(t, rec, 400)

		rec = rig.do(t, http.MethodPost, "/api/v1/clusters/fake/resources/_/v1/configmaps?namespace=demo", manifest, nil)
		hndWantStatus(t, rec, 201)
		if rig.fake.object("", "v1", "configmaps", "demo", "cm-1") == nil {
			t.Error("configmap did not reach the cluster")
		}
	})

	t.Run("createFromYAML", func(t *testing.T) {
		manifest := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: cm-2\n  namespace: demo\ndata:\n  a: b\n"
		rec := rig.do(t, http.MethodPost, "/api/v1/clusters/fake/resources/_/v1/configmaps", manifest, nil)
		hndWantStatus(t, rec, 201)

		// Kind-less garbage is refused before it touches the cluster.
		rec = rig.do(t, http.MethodPost, "/api/v1/clusters/fake/resources/_/v1/configmaps?namespace=demo", `{"metadata":{"name":"x"}}`, nil)
		hndWantStatus(t, rec, 400)
	})

	t.Run("updateConfigMap", func(t *testing.T) {
		manifest := `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"renamed-away"},"data":{"k":"v2"}}`
		rec := rig.do(t, http.MethodPut, "/api/v1/clusters/fake/resources/_/v1/configmaps/demo/cm-1", manifest, nil)
		hndWantStatus(t, rec, 200)
		// The URL wins over the body: a mangled manifest cannot rename.
		o := rig.fake.object("", "v1", "configmaps", "demo", "cm-1")
		if o == nil {
			t.Fatal("cm-1 vanished on update")
		}
		if data, _ := o["data"].(map[string]any); data["k"] != "v2" {
			t.Errorf("update did not land: %v", o["data"])
		}
	})

	t.Run("dryRunUpdateDoesNotPersist", func(t *testing.T) {
		before := rig.fake.object("", "v1", "configmaps", "demo", "cm-1")
		beforeData, _ := before["data"].(map[string]any)
		was := beforeData["k"]

		manifest := `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"cm-1"},"data":{"k":"dry"}}`
		rec := rig.do(t, http.MethodPut,
			"/api/v1/clusters/fake/resources/_/v1/configmaps/demo/cm-1?dryRun=true", manifest, nil)
		hndWantStatus(t, rec, 200)

		// The response shows what would be stored...
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if data, _ := body["data"].(map[string]any); data["k"] != "dry" {
			t.Errorf("dry run did not return the would-be object: %v", body["data"])
		}

		// ...but nothing was written.
		after := rig.fake.object("", "v1", "configmaps", "demo", "cm-1")
		afterData, _ := after["data"].(map[string]any)
		if afterData["k"] != was {
			t.Errorf("dry run persisted: data.k = %v, want %v", afterData["k"], was)
		}
	})

	t.Run("patchPod", func(t *testing.T) {
		rec := rig.do(t, http.MethodPatch, "/api/v1/clusters/fake/resources/_/v1/pods/demo/web-1",
			`{"metadata":{"labels":{"patched":"yes"}}}`,
			map[string]string{"Content-Type": "application/merge-patch+json"})
		hndWantStatus(t, rec, 200)
		var body map[string]any
		hndDecode(t, rec, &body)
		meta, _ := body["metadata"].(map[string]any)
		labels, _ := meta["labels"].(map[string]any)
		if labels["patched"] != "yes" {
			t.Errorf("patched labels = %v", labels)
		}

		rec = rig.do(t, http.MethodPatch, "/api/v1/clusters/fake/resources/_/v1/pods/demo/web-1",
			`{}`, map[string]string{"Content-Type": "text/plain"})
		hndWantStatus(t, rec, 400)
	})

	t.Run("sortByProjectedColumn", func(t *testing.T) {
		// A non-meta sort key takes the project-then-sort path, where numeric
		// cells must compare numerically.
		rec := rig.get(t, "/api/v1/clusters/fake/resources/_/v1/pods?sort=restarts&order=desc")
		hndWantStatus(t, rec, 200)
		var body listResponse
		hndDecode(t, rec, &body)
		if body.Total != 4 {
			t.Errorf("sorted list total = %d", body.Total)
		}
	})

	t.Run("applyPatch", func(t *testing.T) {
		// Server-side apply sets the field manager, and force is opt-in.
		rec := rig.do(t, http.MethodPatch, "/api/v1/clusters/fake/resources/_/v1/pods/demo/web-2?force=true",
			`{"apiVersion":"v1","kind":"Pod","metadata":{"labels":{"applied":"1"}}}`,
			map[string]string{"Content-Type": "application/apply-patch+yaml"})
		hndWantStatus(t, rec, 200)
	})

	t.Run("crdPrinterColumns", func(t *testing.T) {
		// A resource with no builtin table gets its CRD's own printer columns,
		// the same way kubectl renders a CRD it has never seen.
		rec := rig.get(t, "/api/v1/clusters/fake/resources/example.com/v1/widgets")
		hndWantStatus(t, rec, 200)
		var body listResponse
		hndDecode(t, rec, &body)
		if body.Total != 2 {
			t.Fatalf("widgets = %+v", body)
		}
		keys := map[string]string{}
		for _, c := range body.Columns {
			keys[c.Key] = string(c.Type)
		}
		if keys["x_color"] != "text" || keys["x_count"] != "number" {
			t.Errorf("printer columns = %v", keys)
		}
		byName := map[string]map[string]any{}
		for _, it := range body.Items {
			byName[asString(it["name"])] = it
		}
		if byName["w-1"]["x_color"] != "red" || byName["w-2"]["x_count"] != float64(7) {
			t.Errorf("projected rows = %v", body.Items)
		}
	})

	t.Run("deleteCRDInvalidatesDiscovery", func(t *testing.T) {
		rec := rig.do(t, http.MethodDelete,
			"/api/v1/clusters/fake/resources/apiextensions.k8s.io/v1/customresourcedefinitions/_/widgets.example.com", "", nil)
		hndWantStatus(t, rec, 200)
		// Discovery was invalidated and re-fetched; the fake still serves the
		// same surface, so the resource stays resolvable.
		rec = rig.get(t, "/api/v1/clusters/fake/resources/example.com/v1/widgets")
		hndWantStatus(t, rec, 200)
	})

	t.Run("partialNamespaceScope", func(t *testing.T) {
		// Cluster-wide list denied, per-namespace allowed: the response must
		// say exactly which namespaces the answer covers.
		rig.fake.mu.Lock()
		rig.fake.nsOnlyResource = "services"
		rig.fake.mu.Unlock()

		rec := rig.get(t, "/api/v1/clusters/fake/resources/_/v1/services")
		hndWantStatus(t, rec, 200)
		var body listResponse
		hndDecode(t, rec, &body)
		if body.Scope.AllNamespaces {
			t.Fatal("scope claims the whole cluster despite the denied review")
		}
		if len(body.Scope.Namespaces) != 2 || body.Scope.Namespaces[0] != "demo" {
			t.Errorf("scope namespaces = %v", body.Scope.Namespaces)
		}
		if body.Total != 1 || body.Items[0]["name"] != "svc" {
			t.Errorf("visible services = %+v", body.Items)
		}
	})

	t.Run("facetsErrors", func(t *testing.T) {
		rec := rig.get(t, "/api/v1/clusters/fake/resources/authorization.k8s.io/v1/subjectaccessreviews/facets")
		hndWantStatus(t, rec, 400)
	})

	t.Run("deleteConfigMap", func(t *testing.T) {
		rec := rig.do(t, http.MethodDelete, "/api/v1/clusters/fake/resources/_/v1/configmaps/demo/cm-1?propagationPolicy=Background&gracePeriodSeconds=0", "", nil)
		hndWantStatus(t, rec, 200)
		var body map[string]any
		hndDecode(t, rec, &body)
		if body["deleted"] != true || body["name"] != "cm-1" {
			t.Errorf("delete response = %v", body)
		}
		if rig.fake.object("", "v1", "configmaps", "demo", "cm-1") != nil {
			t.Error("configmap still present after delete")
		}
	})
}
