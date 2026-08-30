package api

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/daiwa-zou/orrery/internal/authz"
)

// countSummary is a count the caller may not have been allowed to compute.
type countSummary struct {
	Total    int            `json:"total"`
	ByStatus map[string]int `json:"byStatus,omitempty"`
	// Forbidden: the caller lacks permission. Unavailable: the dashboard could
	// not answer (informer not synced, discovery miss). Conflating the two
	// sends users chasing RBAC problems that do not exist.
	Forbidden   bool `json:"forbidden,omitempty"`
	Unavailable bool `json:"unavailable,omitempty"`
}

// mark records why a summary has no data.
func (s *countSummary) mark(err error) {
	if isForbidden(err) {
		s.Forbidden = true
	} else {
		s.Unavailable = true
	}
}

type overviewResponse struct {
	Cluster    clusterSummary          `json:"cluster"`
	Nodes      countSummary            `json:"nodes"`
	Namespaces countSummary            `json:"namespaces"`
	Pods       countSummary            `json:"pods"`
	Workloads  map[string]countSummary `json:"workloads"`
	Warnings   []map[string]any        `json:"warnings"`
	// WarningsForbidden and WarningsUnavailable say why Warnings is empty when
	// it is empty. Every count on this response already distinguishes "you may
	// not" from "we could not"; the warnings feed did not, and an empty feed is
	// the one field a reader takes as reassurance — the console renders it as
	// "No warning events. That is a good sign." Saying that because an access
	// review came back no is worse than saying nothing.
	WarningsForbidden   bool   `json:"warningsForbidden,omitempty"`
	WarningsUnavailable bool   `json:"warningsUnavailable,omitempty"`
	Capacity            *usage `json:"capacity,omitempty"`
	Requested           *usage `json:"requested,omitempty"`
}

// visibleObjects reads a resource from cache within the caller's permitted
// namespaces. A *forbiddenError means the caller may not read it; any other
// error means the answer could not be produced — an informer timeout must not
// be reported to the user as an RBAC problem, or operators chase permission
// bugs that do not exist.
func (a *API) visibleObjects(ctx context.Context, res *resolved, group, version, resource string) ([]*unstructured.Unstructured, error) {
	ar, err := res.cluster.Discovery.Resolve(ctx, group, version, resource)
	if err != nil {
		return nil, err
	}
	scoped := *res
	scoped.resource = ar

	if !ar.Namespaced {
		if err := a.authorize(ctx, &scoped, "list", "", "", ""); err != nil {
			return nil, err
		}
		return res.cluster.Informers.List(ctx, ar, "")
	}

	all, allowed, scanErr := res.cluster.Authz.VisibleNamespaces(ctx,
		res.cluster.AuthzClient(res.clients),
		res.cluster.AuthSubject(res.identity),
		authz.Attributes{Verb: "list", Group: ar.Group, Version: ar.Version, Resource: ar.Name},
		func() ([]string, error) { return a.namespaceNames(ctx, res.cluster) })

	if all {
		return res.cluster.Informers.List(ctx, ar, "")
	}
	if len(allowed) == 0 {
		if scanErr != nil {
			return nil, scanErr
		}
		return nil, &forbiddenError{verb: "list", resource: ar.Name}
	}
	return listAcross(ctx, res.cluster, ar, allowed)
}

// isForbidden distinguishes "you may not" from "we could not".
func isForbidden(err error) bool {
	var f *forbiddenError
	return errors.As(err, &f)
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
	if nodes, err := a.visibleObjects(ctx, res, "", "v1", "nodes"); err == nil {
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
		out.Nodes.mark(err)
	}

	if ns, err := a.visibleObjects(ctx, res, "", "v1", "namespaces"); err == nil {
		out.Namespaces = countSummary{Total: len(ns)}
	} else {
		out.Namespaces.mark(err)
	}

	// Pods: phase breakdown plus the sum of container requests, which is the
	// number that actually explains scheduling pressure.
	if pods, err := a.visibleObjects(ctx, res, "", "v1", "pods"); err == nil {
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
		out.Pods.mark(err)
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
		objs, err := a.visibleObjects(ctx, res, wl.group, wl.version, wl.resource)
		if err != nil {
			s := countSummary{}
			s.mark(err)
			out.Workloads[wl.key] = s
			continue
		}
		summary := countSummary{Total: len(objs), ByStatus: map[string]int{}}
		for _, o := range objs {
			summary.ByStatus[workloadHealth(o)]++
		}
		out.Workloads[wl.key] = summary
	}

	warnings, err := a.recentWarnings(ctx, res, 20)
	out.Warnings = warnings
	if err != nil {
		if isForbidden(err) {
			out.WarningsForbidden = true
		} else {
			out.WarningsUnavailable = true
		}
	}
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
func (a *API) recentWarnings(ctx context.Context, res *resolved, limit int) ([]map[string]any, error) {
	// Never nil: a nil slice marshals to JSON null, and "no warnings we can
	// show" must reach the browser as an empty list, not a missing field. The
	// error travels alongside it so the caller can say which kind of empty
	// this is.
	events, err := a.visibleObjects(ctx, res, "", "v1", "events")
	if err != nil {
		return []map[string]any{}, err
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
	return out, nil
}

func eventTime(e *unstructured.Unstructured) string {
	for _, f := range []string{"lastTimestamp", "eventTime", "firstTimestamp"} {
		if v := str(e, f); v != "" {
			return v
		}
	}
	return e.GetCreationTimestamp().UTC().Format("2006-01-02T15:04:05Z")
}

// parseCPUMilli and parseMemMiB use the real quantity parser: a hand-rolled
// one silently mangles the legal forms it forgot ("1e3", bare bytes, "1Pi",
// negative signs), and metrics.go already uses this parser for the same job —
// two parsers means two answers for one cluster.
func parseCPUMilli(s string) int64 {
	if s == "" {
		return 0
	}
	q, err := resource.ParseQuantity(s)
	if err != nil {
		return 0
	}
	return q.MilliValue()
}

func parseMemMiB(s string) int64 {
	if s == "" {
		return 0
	}
	q, err := resource.ParseQuantity(s)
	if err != nil {
		return 0
	}
	return q.Value() / (1024 * 1024)
}
