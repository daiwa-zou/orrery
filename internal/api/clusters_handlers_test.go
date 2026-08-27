package api

// HTTP-level tests for the cluster-scoped read endpoints: cluster listing,
// discovery, overview, cache stats, events, metrics, explain and pod logs.
// Everything goes through the router and the real authorization walk.

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestClusterEndpointsHTTP(t *testing.T) {
	rig := hndNewRig(t)

	t.Run("healthz", func(t *testing.T) {
		rec := rig.get(t, "/api/v1/healthz")
		hndWantStatus(t, rec, 200)
		var body map[string]string
		hndDecode(t, rec, &body)
		if body["status"] != "ok" {
			t.Errorf("healthz = %v", body)
		}
	})

	t.Run("authConfig", func(t *testing.T) {
		rec := rig.get(t, "/api/v1/auth/config")
		hndWantStatus(t, rec, 200)
		var body struct {
			OIDCEnabled bool   `json:"oidcEnabled"`
			Anonymous   bool   `json:"anonymous"`
			LoginPath   string `json:"loginPath"`
		}
		hndDecode(t, rec, &body)
		if body.OIDCEnabled || !body.Anonymous || body.LoginPath == "" {
			t.Errorf("auth config = %+v", body)
		}
	})

	t.Run("whoami", func(t *testing.T) {
		rec := rig.get(t, "/api/v1/me")
		hndWantStatus(t, rec, 200)
		var body struct {
			Authenticated bool `json:"authenticated"`
			Anonymous     bool `json:"anonymous"`
			User          struct {
				Username string `json:"username"`
			} `json:"user"`
		}
		hndDecode(t, rec, &body)
		if !body.Authenticated || !body.Anonymous {
			t.Errorf("whoami flags = %+v", body)
		}
		if body.User.Username != "orrery:anonymous" {
			t.Errorf("username = %q", body.User.Username)
		}
	})

	t.Run("listClusters", func(t *testing.T) {
		rec := rig.get(t, "/api/v1/clusters")
		hndWantStatus(t, rec, 200)
		var body struct {
			Clusters []struct {
				Name        string            `json:"name"`
				DisplayName string            `json:"displayName"`
				Labels      map[string]string `json:"labels"`
				AuthMode    string            `json:"authMode"`
				Available   bool              `json:"available"`
			} `json:"clusters"`
		}
		hndDecode(t, rec, &body)
		if len(body.Clusters) != 1 {
			t.Fatalf("got %d clusters", len(body.Clusters))
		}
		c := body.Clusters[0]
		if c.Name != "fake" || c.DisplayName != "Fake cluster" || !c.Available {
			t.Errorf("cluster = %+v", c)
		}
		if c.AuthMode != "impersonation" || c.Labels["env"] != "test" {
			t.Errorf("cluster metadata = %+v", c)
		}
	})

	t.Run("unknownCluster", func(t *testing.T) {
		rec := rig.get(t, "/api/v1/clusters/nope/overview")
		hndWantStatus(t, rec, 404)
		var body errorBody
		hndDecode(t, rec, &body)
		if body.Error != "unknown_cluster" {
			t.Errorf("error kind = %q", body.Error)
		}
	})

	t.Run("discovery", func(t *testing.T) {
		rec := rig.get(t, "/api/v1/clusters/fake/discovery")
		hndWantStatus(t, rec, 200)
		var body struct {
			Groups []struct {
				Group     string `json:"group"`
				Resources []struct {
					Name  string   `json:"name"`
					Kind  string   `json:"kind"`
					Verbs []string `json:"verbs"`
				} `json:"resources"`
			} `json:"groups"`
			ServerVersion string `json:"serverVersion"`
		}
		hndDecode(t, rec, &body)
		if body.ServerVersion != "v1.30.0" {
			t.Errorf("serverVersion = %q", body.ServerVersion)
		}
		if len(body.Groups) == 0 || body.Groups[0].Group != "" {
			t.Fatalf("the core group should lead, got %+v", body.Groups)
		}
		found := map[string]bool{}
		for _, g := range body.Groups {
			for _, r := range g.Resources {
				found[g.Group+"/"+r.Name] = true
			}
		}
		if !found["/pods"] || !found["apps/deployments"] {
			t.Errorf("expected pods and deployments in discovery, got %v", found)
		}
		// The default listable filter must hide create-only resources.
		if found["authorization.k8s.io/subjectaccessreviews"] {
			t.Error("subjectaccessreviews should be filtered out by listable=true")
		}

		rec = rig.get(t, "/api/v1/clusters/fake/discovery?listable=false")
		hndWantStatus(t, rec, 200)
		if !strings.Contains(rec.Body.String(), "subjectaccessreviews") {
			t.Error("listable=false should include non-listable resources")
		}
	})

	t.Run("overview", func(t *testing.T) {
		rec := rig.get(t, "/api/v1/clusters/fake/overview")
		hndWantStatus(t, rec, 200)
		var body struct {
			Cluster    struct{ Name string } `json:"cluster"`
			Nodes      struct{ Total int }   `json:"nodes"`
			Namespaces struct{ Total int }   `json:"namespaces"`
			Pods       struct{ Total int }   `json:"pods"`
			Workloads  map[string]struct {
				Total    int            `json:"total"`
				ByStatus map[string]int `json:"byStatus"`
			} `json:"workloads"`
			Warnings  []map[string]any `json:"warnings"`
			Capacity  *usage           `json:"capacity"`
			Requested *usage           `json:"requested"`
		}
		hndDecode(t, rec, &body)
		if body.Cluster.Name != "fake" {
			t.Errorf("cluster name = %q", body.Cluster.Name)
		}
		if body.Nodes.Total != 1 || body.Namespaces.Total != 2 || body.Pods.Total != 4 {
			t.Errorf("counts = nodes %d, namespaces %d, pods %d",
				body.Nodes.Total, body.Namespaces.Total, body.Pods.Total)
		}
		dep := body.Workloads["deployments"]
		if dep.Total != 1 || dep.ByStatus["Healthy"] != 1 {
			t.Errorf("deployments summary = %+v", dep)
		}
		if ds := body.Workloads["daemonsets"]; ds.ByStatus["Healthy"] != 1 {
			t.Errorf("daemonsets summary = %+v", ds)
		}
		if body.Capacity == nil || body.Capacity.CPUMilli != 2000 || body.Capacity.MemoryMiB != 4096 {
			t.Errorf("capacity = %+v", body.Capacity)
		}
		// Three running pods requesting 100m/64Mi each.
		if body.Requested == nil || body.Requested.CPUMilli != 300 || body.Requested.MemoryMiB != 192 {
			t.Errorf("requested = %+v", body.Requested)
		}
		if len(body.Warnings) != 1 {
			t.Fatalf("warnings = %v", body.Warnings)
		}
		if body.Warnings[0]["reason"] != "BackOff" {
			t.Errorf("warning = %v", body.Warnings[0])
		}
	})

	t.Run("cacheStats", func(t *testing.T) {
		// Overview above has already spun up informers; stats must report them.
		rec := rig.get(t, "/api/v1/clusters/fake/stats")
		hndWantStatus(t, rec, 200)
		var body struct {
			Cluster   string `json:"cluster"`
			Informers []struct {
				GVR     string `json:"gvr"`
				Objects int    `json:"objects"`
			} `json:"informers"`
			TotalObjects int `json:"totalObjects"`
		}
		hndDecode(t, rec, &body)
		if body.Cluster != "fake" || len(body.Informers) == 0 {
			t.Fatalf("stats = %+v", body)
		}
		podsSeen := false
		for _, inf := range body.Informers {
			if strings.Contains(inf.GVR, "pods") && inf.Objects == 4 {
				podsSeen = true
			}
		}
		if !podsSeen || body.TotalObjects < 4 {
			t.Errorf("informer stats missing pods: %+v", body)
		}
	})

	t.Run("cacheCollector", func(t *testing.T) {
		// The Prometheus collector reads the same informer stats; with caches
		// running it must emit per-cluster and per-GVR gauges on scrape.
		col := NewCacheCollector(rig.api)
		descs := make(chan *prometheus.Desc, 8)
		col.Describe(descs)
		if len(descs) != 3 {
			t.Errorf("described %d metrics, want 3", len(descs))
		}
		metrics := make(chan prometheus.Metric, 128)
		col.Collect(metrics)
		if len(metrics) < 2 {
			t.Errorf("collected %d metrics with informers running", len(metrics))
		}
	})

	t.Run("events", func(t *testing.T) {
		rec := rig.get(t, "/api/v1/clusters/fake/events")
		hndWantStatus(t, rec, 200)
		var body listResponse
		hndDecode(t, rec, &body)
		if body.Total != 2 || len(body.Columns) == 0 {
			t.Fatalf("events = total %d, columns %d", body.Total, len(body.Columns))
		}
		// Newest first.
		if rowsOf(body)[0]["reason"] != "BackOff" {
			t.Errorf("events not sorted newest-first: %v", rowsOf(body)[0])
		}

		rec = rig.get(t, "/api/v1/clusters/fake/events?warningsOnly=true")
		hndDecode(t, rec, &body)
		if body.Total != 1 || rowsOf(body)[0]["reason"] != "BackOff" {
			t.Errorf("warningsOnly = %+v", rowsOf(body))
		}

		rec = rig.get(t, "/api/v1/clusters/fake/events?involvedName=web-1")
		hndDecode(t, rec, &body)
		if body.Total != 1 || rowsOf(body)[0]["reason"] != "Started" {
			t.Errorf("involvedName filter = %+v", rowsOf(body))
		}

		rec = rig.get(t, "/api/v1/clusters/fake/events?namespace=demo&q=back-off")
		hndDecode(t, rec, &body)
		if body.Total != 1 {
			t.Errorf("free-text filter = %+v", rowsOf(body))
		}
	})

	t.Run("nodeMetrics", func(t *testing.T) {
		rec := rig.get(t, "/api/v1/clusters/fake/metrics/nodes")
		hndWantStatus(t, rec, 200)
		var body metricsResponse
		hndDecode(t, rec, &body)
		if !body.Available || len(body.Nodes) != 1 {
			t.Fatalf("node metrics = %+v", body)
		}
		n := body.Nodes[0]
		if n.Name != "node-1" || n.Usage.CPUMilli != 500 || n.Usage.MemoryMiB != 1024 {
			t.Errorf("node usage = %+v", n)
		}
		// 500m of 2000m allocatable, 1024Mi of 4096Mi.
		if n.CPUPercent != 25.0 || n.MemPercent != 25.0 {
			t.Errorf("percentages = %v cpu, %v mem", n.CPUPercent, n.MemPercent)
		}
	})

	t.Run("podMetrics", func(t *testing.T) {
		rec := rig.get(t, "/api/v1/clusters/fake/metrics/pods")
		hndWantStatus(t, rec, 200)
		var body metricsResponse
		hndDecode(t, rec, &body)
		if !body.Available || len(body.Pods) != 2 {
			t.Fatalf("pod metrics = %+v", body)
		}
		byName := map[string]podMetric{}
		for _, p := range body.Pods {
			byName[p.Name] = p
		}
		w1 := byName["web-1"]
		if w1.Usage.CPUMilli != 250 || w1.Containers["app"].MemoryMiB != 128 {
			t.Errorf("web-1 metrics = %+v", w1)
		}
		// Limits are joined in from the pod cache.
		if w1.Limits == nil || w1.Limits.CPUMilli != 200 || w1.Limits.MemoryMiB != 128 {
			t.Errorf("web-1 limits = %+v", w1.Limits)
		}
		if body.Totals == nil || body.Totals.CPUMilli != 350 {
			t.Errorf("totals = %+v", body.Totals)
		}

		rec = rig.get(t, "/api/v1/clusters/fake/metrics/pods?namespace=demo")
		hndWantStatus(t, rec, 200)
		hndDecode(t, rec, &body)
		if !body.Available || len(body.Pods) != 2 {
			t.Errorf("namespaced pod metrics = %+v", body)
		}
	})

	t.Run("podLogs", func(t *testing.T) {
		rec := rig.get(t, "/api/v1/clusters/fake/pods/demo/web-1/logs")
		hndWantStatus(t, rec, 200)
		if rec.Body.String() != "line-1\nline-2\n" {
			t.Errorf("log body = %q", rec.Body.String())
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
			t.Errorf("content type = %q", ct)
		}

		rec = rig.get(t, "/api/v1/clusters/fake/pods/demo/web-1/logs?download=true")
		hndWantStatus(t, rec, 200)
		if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "web-1.log") {
			t.Errorf("download disposition = %q", cd)
		}
	})

	t.Run("explain", func(t *testing.T) {
		rec := rig.get(t, "/api/v1/clusters/fake/explain?version=v1&kind=Pod")
		hndWantStatus(t, rec, 200)
		var body explainResponse
		hndDecode(t, rec, &body)
		if body.Kind != "Pod" || body.Type != "Object" {
			t.Errorf("explain root = %+v", body)
		}
		fields := map[string]explainField{}
		for _, f := range body.Fields {
			fields[f.Name] = f
		}
		if !fields["spec"].HasChildren {
			t.Errorf("spec should be drillable: %+v", fields["spec"])
		}

		// Drilling through an array of containers must land on Container.
		rec = rig.get(t, "/api/v1/clusters/fake/explain?version=v1&kind=Pod&field=spec.containers")
		hndWantStatus(t, rec, 200)
		hndDecode(t, rec, &body)
		names := map[string]bool{}
		required := map[string]bool{}
		for _, f := range body.Fields {
			names[f.Name] = true
			required[f.Name] = f.Required
		}
		if !names["image"] || !required["name"] {
			t.Errorf("container fields = %+v", body.Fields)
		}
		// Required fields sort first.
		if len(body.Fields) == 0 || body.Fields[0].Name != "name" {
			t.Errorf("field order = %+v", body.Fields)
		}

		rec = rig.get(t, "/api/v1/clusters/fake/explain?kind=Pod")
		hndWantStatus(t, rec, 400)

		rec = rig.get(t, "/api/v1/clusters/fake/explain?version=v1&kind=Gadget")
		hndWantStatus(t, rec, 404)

		rec = rig.get(t, "/api/v1/clusters/fake/explain?version=v1&kind=Pod&field=spec.bogus")
		hndWantStatus(t, rec, 404)
	})
}
