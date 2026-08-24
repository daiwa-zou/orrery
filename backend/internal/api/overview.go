package api

import (
	"context"
	"net/http"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/daiwazou/clusterlens/backend/internal/authz"
)

// countSummary is a count the caller may not have been allowed to compute.
type countSummary struct {
	Total     int            `json:"total"`
	ByStatus  map[string]int `json:"byStatus,omitempty"`
	Forbidden bool           `json:"forbidden,omitempty"`
}

type overviewResponse struct {
	Cluster    clusterSummary          `json:"cluster"`
	Nodes      countSummary            `json:"nodes"`
	Namespaces countSummary            `json:"namespaces"`
	Pods       countSummary            `json:"pods"`
	Workloads  map[string]countSummary `json:"workloads"`
	Warnings   []map[string]any        `json:"warnings"`
	Capacity   *usage                  `json:"capacity,omitempty"`
	Requested  *usage                  `json:"requested,omitempty"`
}

// visibleObjects reads a resource from cache within the caller's permitted
// namespaces, returning ok=false when they may not read it at all.
func (a *API) visibleObjects(ctx context.Context, res *resolved, group, version, resource string) ([]*unstructured.Unstructured, bool) {
	ar, err := res.cluster.Discovery.Resolve(ctx, group, version, resource)
	if err != nil {
		return nil, false
	}
	scoped := *res
	scoped.resource = ar

	if !ar.Namespaced {
		if err := a.authorize(ctx, &scoped, "list", "", "", ""); err != nil {
			return nil, false
		}
		objs, err := res.cluster.Informers.List(ctx, ar, "")
		return objs, err == nil
	}

	all, allowed, _ := res.cluster.Authz.VisibleNamespaces(ctx,
		res.cluster.AuthzClient(res.clients),
		res.cluster.AuthSubject(res.identity),
		authz.Attributes{Verb: "list", Group: ar.Group, Version: ar.Version, Resource: ar.Name},
		a.namespaceNames(ctx, res.cluster))

	if all {
		objs, err := res.cluster.Informers.List(ctx, ar, "")
		return objs, err == nil
	}
	if len(allowed) == 0 {
		return nil, false
	}
	var out []*unstructured.Unstructured
	for _, ns := range allowed {
		part, err := res.cluster.Informers.List(ctx, ar, ns)
		if err != nil {
			continue
		}
		out = append(out, part...)
	}
	return out, true
}

// clusterOverview is the landing page: everything is read from the shared
// caches, so it costs no API server calls beyond the access reviews.
func (a *API) clusterOverview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	res, err := a.clusterOnly(r)
	if err != nil {
		a.writeErr(w, r, err)
		return
	}

	out := overviewResponse{
		Cluster:   a.summarize(res),
		Workloads: map[string]countSummary{},
	}

	// Nodes: capacity and readiness.
	if nodes, ok := a.visibleObjects(ctx, res, "", "v1", "nodes"); ok {
		out.Nodes = countSummary{Total: len(nodes), ByStatus: map[string]int{}}
		capacity := usage{}
		for _, n := range nodes {
			out.Nodes.ByStatus[nodeStatus(n)]++
			c := quantityUsage(n, "allocatable")
			capacity.CPUMilli += c.CPUMilli
			capacity.MemoryMiB += c.MemoryMiB
		}
		out.Capacity = &capacity
	} else {
		out.Nodes.Forbidden = true
	}

	if ns, ok := a.visibleObjects(ctx, res, "", "v1", "namespaces"); ok {
		out.Namespaces = countSummary{Total: len(ns)}
	} else {
		out.Namespaces.Forbidden = true
	}

	// Pods: phase breakdown plus the sum of container requests, which is the
	// number that actually explains scheduling pressure.
	if pods, ok := a.visibleObjects(ctx, res, "", "v1", "pods"); ok {
		out.Pods = countSummary{Total: len(pods), ByStatus: map[string]int{}}
		requested := usage{}
		for _, p := range pods {
			out.Pods.ByStatus[podStatus(p)]++
			if phase := str(p, "status", "phase"); phase == "Succeeded" || phase == "Failed" {
				continue
			}
			cpu, mem := podRequests(p)
			requested.CPUMilli += cpu
			requested.MemoryMiB += mem
		}
		out.Requested = &requested
	} else {
		out.Pods.Forbidden = true
	}

	for _, wl := range []struct{ key, group, version, resource string }{
		{"deployments", "apps", "v1", "deployments"},
		{"statefulsets", "apps", "v1", "statefulsets"},
		{"daemonsets", "apps", "v1", "daemonsets"},
		{"jobs", "batch", "v1", "jobs"},
		{"cronjobs", "batch", "v1", "cronjobs"},
		{"services", "", "v1", "services"},
		{"ingresses", "networking.k8s.io", "v1", "ingresses"},
	} {
		objs, ok := a.visibleObjects(ctx, res, wl.group, wl.version, wl.resource)
		if !ok {
			out.Workloads[wl.key] = countSummary{Forbidden: true}
			continue
		}
		summary := countSummary{Total: len(objs), ByStatus: map[string]int{}}
		for _, o := range objs {
			summary.ByStatus[workloadHealth(o)]++
		}
		out.Workloads[wl.key] = summary
	}

	out.Warnings = a.recentWarnings(ctx, res, 20)
	writeJSON(w, http.StatusOK, out)
}

// podRequests totals a pod's container resource requests.
func podRequests(p *unstructured.Unstructured) (cpuMilli, memMiB int64) {
	for _, cAny := range slice(p, "spec", "containers") {
		reqs := mapOf(mapOf(mapOf(cAny)["resources"])["requests"])
		if cpu, ok := reqs["cpu"].(string); ok {
			cpuMilli += parseCPUMilli(cpu)
		}
		if mem, ok := reqs["memory"].(string); ok {
			memMiB += parseMemMiB(mem)
		}
	}
	return cpuMilli, memMiB
}

// workloadHealth reduces a controller to Healthy / Progressing / Degraded,
// which is what a summary tile can usefully show.
func workloadHealth(o *unstructured.Unstructured) string {
	switch o.GetKind() {
	case "Deployment", "StatefulSet", "ReplicaSet":
		desired := specReplicas(o)
		ready := i64(o, "status", "readyReplicas")
		switch {
		case desired == 0:
			return "Scaled to zero"
		case ready >= desired:
			return "Healthy"
		case ready == 0:
			return "Degraded"
		default:
			return "Progressing"
		}
	case "DaemonSet":
		desired := i64(o, "status", "desiredNumberScheduled")
		ready := i64(o, "status", "numberReady")
		switch {
		case desired == 0:
			return "Not scheduled"
		case ready >= desired:
			return "Healthy"
		case ready == 0:
			return "Degraded"
		default:
			return "Progressing"
		}
	case "Job":
		return jobStatus(o)
	default:
		return "Healthy"
	}
}

// recentWarnings surfaces the newest Warning events, the single most useful
// thing to put on a cluster landing page.
func (a *API) recentWarnings(ctx context.Context, res *resolved, limit int) []map[string]any {
	events, ok := a.visibleObjects(ctx, res, "", "v1", "events")
	if !ok {
		return nil
	}
	warnings := make([]*unstructured.Unstructured, 0, 32)
	for _, e := range events {
		if str(e, "type") == "Warning" {
			warnings = append(warnings, e)
		}
	}
	sort.Slice(warnings, func(i, j int) bool {
		return eventTime(warnings[i]) > eventTime(warnings[j])
	})
	if len(warnings) > limit {
		warnings = warnings[:limit]
	}
	out := make([]map[string]any, 0, len(warnings))
	for _, e := range warnings {
		out = append(out, map[string]any{
			"namespace": e.GetNamespace(),
			"reason":    str(e, "reason"),
			"message":   str(e, "message"),
			"object":    strings.TrimPrefix(str(e, "involvedObject", "kind")+"/"+str(e, "involvedObject", "name"), "/"),
			"count":     i64(e, "count"),
			"lastSeen":  eventTime(e),
		})
	}
	return out
}

func eventTime(e *unstructured.Unstructured) string {
	for _, f := range []string{"lastTimestamp", "eventTime", "firstTimestamp"} {
		if v := str(e, f); v != "" {
			return v
		}
	}
	return e.GetCreationTimestamp().UTC().Format("2006-01-02T15:04:05Z")
}

// parseCPUMilli handles the "100m" / "0.5" / "2" forms without pulling in the
// full quantity parser for a hot loop.
func parseCPUMilli(s string) int64 {
	if s == "" {
		return 0
	}
	if strings.HasSuffix(s, "m") {
		return int64(parseFloat(strings.TrimSuffix(s, "m")))
	}
	return int64(parseFloat(s) * 1000)
}

func parseMemMiB(s string) int64 {
	mult := map[string]float64{
		"Ki": 1.0 / 1024, "Mi": 1, "Gi": 1024, "Ti": 1024 * 1024,
		"K": 1000.0 / (1024 * 1024), "M": 1000000.0 / (1024 * 1024), "G": 1000000000.0 / (1024 * 1024),
	}
	for _, suffix := range []string{"Ki", "Mi", "Gi", "Ti", "K", "M", "G"} {
		if strings.HasSuffix(s, suffix) {
			return int64(parseFloat(strings.TrimSuffix(s, suffix)) * mult[suffix])
		}
	}
	return int64(parseFloat(s) / (1024 * 1024))
}

func parseFloat(s string) float64 {
	var v float64
	var frac float64 = 0
	var seenDot bool
	div := 1.0
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			if seenDot {
				div *= 10
				frac += float64(r-'0') / div
			} else {
				v = v*10 + float64(r-'0')
			}
		case r == '.':
			seenDot = true
		default:
			return v + frac
		}
	}
	return v + frac
}
