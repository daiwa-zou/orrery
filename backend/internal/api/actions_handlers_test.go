package api

// HTTP-level tests for the action endpoints: scale, restart, cordon, drain,
// evict, the cronjob actions, rollout history/undo and the batch access
// check. All writes go through the CSRF group (a no-op in anonymous mode) and
// the real authorization walk before touching the fake cluster.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestActionHandlersHTTP(t *testing.T) {
	rig := hndNewRig(t)
	post := func(t *testing.T, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		return rig.do(t, http.MethodPost, "/api/v1/clusters/fake"+path, body,
			map[string]string{"Content-Type": "application/json"})
	}

	t.Run("checkAccess", func(t *testing.T) {
		rec := post(t, "/access", `{"checks":[
			{"verb":"list","version":"v1","resource":"pods"},
			{"verb":"delete","group":"apps","version":"v1","resource":"deployments","namespace":"demo"}
		]}`)
		hndWantStatus(t, rec, 200)
		var body struct {
			Results map[string]struct {
				Allowed bool `json:"allowed"`
			} `json:"results"`
		}
		hndDecode(t, rec, &body)
		if len(body.Results) != 2 || !body.Results["0"].Allowed || !body.Results["1"].Allowed {
			t.Errorf("results = %+v", body.Results)
		}

		rec = post(t, "/access", `{"checks":[]}`)
		hndWantStatus(t, rec, 200)

		many := `{"checks":[` + strings.Repeat(`{"verb":"get","version":"v1","resource":"pods"},`, 64) +
			`{"verb":"get","version":"v1","resource":"pods"}]}`
		rec = post(t, "/access", many)
		hndWantStatus(t, rec, 400)

		rec = post(t, "/access", `{broken`)
		hndWantStatus(t, rec, 400)
	})

	t.Run("scale", func(t *testing.T) {
		rec := post(t, "/actions/scale",
			`{"group":"apps","version":"v1","resource":"deployments","namespace":"demo","name":"web","replicas":3}`)
		hndWantStatus(t, rec, 200)
		var body map[string]any
		hndDecode(t, rec, &body)
		if body["scaled"] != true || body["replicas"] != float64(3) {
			t.Errorf("scale response = %v", body)
		}
		o := rig.fake.object("apps", "v1", "deployments", "demo", "web")
		spec, _ := o["spec"].(map[string]any)
		if spec["replicas"] != float64(3) {
			t.Errorf("cluster replicas = %v", spec["replicas"])
		}

		rec = post(t, "/actions/scale",
			`{"group":"apps","version":"v1","resource":"deployments","namespace":"demo","name":"web","replicas":-1}`)
		hndWantStatus(t, rec, 400)
	})

	t.Run("restart", func(t *testing.T) {
		rec := post(t, "/actions/restart",
			`{"group":"apps","version":"v1","resource":"deployments","namespace":"demo","name":"web"}`)
		hndWantStatus(t, rec, 200)
		o := rig.fake.object("apps", "v1", "deployments", "demo", "web")
		spec, _ := o["spec"].(map[string]any)
		tmpl, _ := spec["template"].(map[string]any)
		meta, _ := tmpl["metadata"].(map[string]any)
		ann, _ := meta["annotations"].(map[string]any)
		if _, ok := ann["kubectl.kubernetes.io/restartedAt"]; !ok {
			t.Errorf("restart annotation missing: %v", ann)
		}

		// Only workloads with a pod template can be rolling-restarted.
		rec = post(t, "/actions/restart",
			`{"version":"v1","resource":"pods","namespace":"demo","name":"web-1"}`)
		hndWantStatus(t, rec, 400)
	})

	t.Run("cordon", func(t *testing.T) {
		rec := post(t, "/actions/cordon", `{"node":"node-1","unschedulable":true}`)
		hndWantStatus(t, rec, 200)
		o := rig.fake.object("", "v1", "nodes", "", "node-1")
		spec, _ := o["spec"].(map[string]any)
		if spec["unschedulable"] != true {
			t.Errorf("node spec = %v", spec)
		}

		rec = post(t, "/actions/cordon", `{"node":"node-1","unschedulable":false}`)
		hndWantStatus(t, rec, 200)
		o = rig.fake.object("", "v1", "nodes", "", "node-1")
		spec, _ = o["spec"].(map[string]any)
		if spec["unschedulable"] != false {
			t.Errorf("uncordon did not land: %v", spec)
		}

		rec = post(t, "/actions/cordon", `{broken`)
		hndWantStatus(t, rec, 400)
	})

	t.Run("evict", func(t *testing.T) {
		rec := post(t, "/actions/evict", `{"namespace":"demo","pod":"web-2"}`)
		hndWantStatus(t, rec, 200)
		var body map[string]any
		hndDecode(t, rec, &body)
		if body["evicted"] != true {
			t.Errorf("evict response = %v", body)
		}
		found := false
		for _, e := range rig.fake.evictions() {
			if e == "demo/web-2" {
				found = true
			}
		}
		if !found {
			t.Errorf("no eviction reached the cluster: %v", rig.fake.evictions())
		}

		rec = post(t, "/actions/evict", `{"namespace":"demo"}`)
		hndWantStatus(t, rec, 400)
	})

	t.Run("drainDryRun", func(t *testing.T) {
		rec := post(t, "/actions/drain", `{"node":"node-1","ignoreDaemonSets":true,"deleteEmptyDirData":true,"dryRun":true}`)
		hndWantStatus(t, rec, 200)
		var body drainResult
		hndDecode(t, rec, &body)
		if body.Cordoned || !body.DryRun {
			t.Errorf("dry run flags = %+v", body)
		}
		if len(body.Evicted) != 2 { // web-1 and web-2
			t.Errorf("dry run evicted = %v", body.Evicted)
		}
		skipped := strings.Join(body.Skipped, ";")
		if !strings.Contains(skipped, "done-1 (already finished)") ||
			!strings.Contains(skipped, "ds-1 (daemonset-managed)") {
			t.Errorf("skipped = %v", body.Skipped)
		}

		rec = post(t, "/actions/drain", `{}`)
		hndWantStatus(t, rec, 400)
	})

	t.Run("drain", func(t *testing.T) {
		before := len(rig.fake.evictions())
		rec := post(t, "/actions/drain", `{"node":"node-1","ignoreDaemonSets":true,"deleteEmptyDirData":true}`)
		hndWantStatus(t, rec, 200)
		var body drainResult
		hndDecode(t, rec, &body)
		if !body.Cordoned || len(body.Evicted) != 2 || len(body.Failed) != 0 {
			t.Errorf("drain result = %+v", body)
		}
		o := rig.fake.object("", "v1", "nodes", "", "node-1")
		spec, _ := o["spec"].(map[string]any)
		if spec["unschedulable"] != true {
			t.Errorf("drain did not cordon: %v", spec)
		}
		if got := len(rig.fake.evictions()) - before; got != 2 {
			t.Errorf("evictions reaching the cluster = %d", got)
		}
	})

	t.Run("rolloutHistory", func(t *testing.T) {
		rec := rig.get(t, "/api/v1/clusters/fake/rollout/history?namespace=demo&name=web")
		hndWantStatus(t, rec, 200)
		var body struct {
			Revisions []revisionSummary `json:"revisions"`
		}
		hndDecode(t, rec, &body)
		if len(body.Revisions) != 2 {
			t.Fatalf("revisions = %+v", body.Revisions)
		}
		// Newest revision first, marked current.
		if body.Revisions[0].Revision != 2 || !body.Revisions[0].Current {
			t.Errorf("head revision = %+v", body.Revisions[0])
		}
		if body.Revisions[1].Revision != 1 || body.Revisions[1].ChangeCause != "initial rollout" {
			t.Errorf("old revision = %+v", body.Revisions[1])
		}
		if len(body.Revisions[1].Images) != 1 || body.Revisions[1].Images[0] != "web:1" {
			t.Errorf("old images = %v", body.Revisions[1].Images)
		}

		rec = rig.get(t, "/api/v1/clusters/fake/rollout/history?namespace=demo")
		hndWantStatus(t, rec, 400)

		rec = rig.get(t, "/api/v1/clusters/fake/rollout/history?namespace=demo&name=ghost")
		hndWantStatus(t, rec, 404)
	})

	t.Run("rolloutUndo", func(t *testing.T) {
		rec := post(t, "/actions/rollout-undo", `{"namespace":"demo","name":"web"}`)
		hndWantStatus(t, rec, 200)
		var body map[string]any
		hndDecode(t, rec, &body)
		if body["rolledBack"] != true || body["toRevision"] != float64(1) {
			t.Errorf("undo response = %v", body)
		}
		o := rig.fake.object("apps", "v1", "deployments", "demo", "web")
		spec, _ := o["spec"].(map[string]any)
		tmpl, _ := spec["template"].(map[string]any)
		tspec, _ := tmpl["spec"].(map[string]any)
		containers, _ := tspec["containers"].([]any)
		c0, _ := containers[0].(map[string]any)
		if c0["image"] != "web:1" {
			t.Errorf("rolled-back image = %v", c0["image"])
		}
		// The ReplicaSet's hash label must not leak into the deployment spec.
		tmeta, _ := tmpl["metadata"].(map[string]any)
		labels, _ := tmeta["labels"].(map[string]any)
		if _, ok := labels["pod-template-hash"]; ok {
			t.Error("pod-template-hash leaked into the deployment template")
		}

		rec = post(t, "/actions/rollout-undo", `{"namespace":"demo","name":"web","toRevision":99}`)
		hndWantStatus(t, rec, 404)
	})

	t.Run("triggerCronJob", func(t *testing.T) {
		rec := post(t, "/actions/trigger-cronjob", `{"namespace":"demo","name":"cj"}`)
		hndWantStatus(t, rec, 200)
		var body map[string]any
		hndDecode(t, rec, &body)
		jobName, _ := body["job"].(string)
		if body["triggered"] != true || !strings.HasPrefix(jobName, "cj-manual-") {
			t.Fatalf("trigger response = %v", body)
		}
		job := rig.fake.object("batch", "v1", "jobs", "demo", jobName)
		if job == nil {
			t.Fatal("job did not reach the cluster")
		}
		meta, _ := job["metadata"].(map[string]any)
		ann, _ := meta["annotations"].(map[string]any)
		if ann["cronjob.kubernetes.io/instantiate"] != "manual" {
			t.Errorf("job annotations = %v", ann)
		}

		rec = post(t, "/actions/trigger-cronjob", `{"namespace":"demo","name":"ghost"}`)
		hndWantStatus(t, rec, 404)

		rec = post(t, "/actions/trigger-cronjob", `{"name":"cj"}`)
		hndWantStatus(t, rec, 400)
	})

	t.Run("suspendCronJob", func(t *testing.T) {
		rec := post(t, "/actions/suspend-cronjob", `{"namespace":"demo","name":"cj","suspend":true}`)
		hndWantStatus(t, rec, 200)
		o := rig.fake.object("batch", "v1", "cronjobs", "demo", "cj")
		spec, _ := o["spec"].(map[string]any)
		if spec["suspend"] != true {
			t.Errorf("cronjob spec = %v", spec)
		}

		rec = post(t, "/actions/suspend-cronjob", `{"name":"cj","suspend":true}`)
		hndWantStatus(t, rec, 400)
	})
}
