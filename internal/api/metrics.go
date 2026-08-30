package api

import (
	"fmt"
	"net/http"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"k8s.io/metrics/pkg/apis/metrics/v1beta1"

	"github.com/daiwa-zou/orrery/internal/authz"
)

// usage is a CPU/memory pair in the canonical units the frontend charts.
type usage struct {
	CPUMilli  int64 `json:"cpuMilli"`
	MemoryMiB int64 `json:"memoryMiB"`
}

func usageFrom(list corev1.ResourceList) usage {
	u := usage{}
	if cpu, ok := list[corev1.ResourceCPU]; ok {
		u.CPUMilli = cpu.MilliValue()
	}
	if mem, ok := list[corev1.ResourceMemory]; ok {
		u.MemoryMiB = mem.Value() / (1024 * 1024)
	}
	return u
}

type nodeMetric struct {
	Name        string  `json:"name"`
	Usage       usage   `json:"usage"`
	Capacity    usage   `json:"capacity"`
	Allocatable usage   `json:"allocatable"`
	CPUPercent  float64 `json:"cpuPercent"`
	MemPercent  float64 `json:"memPercent"`
}

type metricsResponse struct {
	Available bool         `json:"available"`
	Reason    string       `json:"reason,omitempty"`
	Nodes     []nodeMetric `json:"nodes,omitempty"`
	Pods      []podMetric  `json:"pods,omitempty"`
	Totals    *usage       `json:"totals,omitempty"`
	// Warnings surface partial answers — a truncated namespace scan must not
	// read as "these are all the pods".
	Warnings []string `json:"warnings,omitempty"`
}

type podMetric struct {
	Name       string           `json:"name"`
	Namespace  string           `json:"namespace"`
	Usage      usage            `json:"usage"`
	Containers map[string]usage `json:"containers,omitempty"`
	// Limits sums the pod's container limits, so the frontend can draw usage
	// as a fraction of what the kubelet will actually enforce. Omitted when no
	// container declares a limit for either resource.
	Limits *usage `json:"limits,omitempty"`
}

// metricsUnavailable turns the many ways metrics-server can be missing into
// one honest answer, instead of a 500 that looks like a dashboard bug.
func metricsUnavailable(err error) (metricsResponse, bool) {
	if err == nil {
		return metricsResponse{}, false
	}
	if apierrors.IsNotFound(err) || apierrors.IsServiceUnavailable(err) || apierrors.IsTimeout(err) {
		return metricsResponse{
			Available: false,
			Reason:    "metrics-server is not installed or not responding on this cluster",
		}, true
	}
	return metricsResponse{}, false
}

// nodeMetrics reports per-node utilisation against capacity.
func (a *API) nodeMetrics(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	res, err := a.clusterOnly(r)
	if err != nil {
		a.writeErr(w, r, err)
		return
	}

	nodeRes, err := res.cluster.Discovery.Resolve(ctx, "", "v1", "nodes")
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	res.resource = nodeRes
	if err := a.authorize(ctx, res, "list", "", "", ""); err != nil {
		a.writeErr(w, r, err)
		return
	}

	metrics, err := res.clients.Metrics.MetricsV1beta1().NodeMetricses().List(ctx, metav1.ListOptions{})
	if resp, ok := metricsUnavailable(err); ok {
		writeJSON(w, http.StatusOK, resp)
		return
	}
	if err != nil {
		a.writeErr(w, r, err)
		return
	}

	// Capacity comes from the node cache, so this endpoint costs exactly one
	// call to the metrics API and nothing to the API server.
	nodes, err := res.cluster.Informers.List(ctx, nodeRes, "")
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	capacityByNode := make(map[string][2]usage, len(nodes))
	for _, n := range nodes {
		capacityByNode[n.GetName()] = [2]usage{
			quantityUsage(n, "capacity"),
			quantityUsage(n, "allocatable"),
		}
	}

	out := metricsResponse{Available: true}
	totals := usage{}
	for _, m := range metrics.Items {
		u := usageFrom(m.Usage)
		nm := nodeMetric{Name: m.Name, Usage: u}
		if cap, ok := capacityByNode[m.Name]; ok {
			nm.Capacity, nm.Allocatable = cap[0], cap[1]
			if nm.Allocatable.CPUMilli > 0 {
				nm.CPUPercent = round1(float64(u.CPUMilli) / float64(nm.Allocatable.CPUMilli) * 100)
			}
			if nm.Allocatable.MemoryMiB > 0 {
				nm.MemPercent = round1(float64(u.MemoryMiB) / float64(nm.Allocatable.MemoryMiB) * 100)
			}
		}
		totals.CPUMilli += u.CPUMilli
		totals.MemoryMiB += u.MemoryMiB
		out.Nodes = append(out.Nodes, nm)
	}
	out.Totals = &totals
	writeJSON(w, http.StatusOK, out)
}

// quantityUsage reads a node's capacity or allocatable block. It shares the
// overview's quantity helpers so one parser answers for the whole dashboard.
func quantityUsage(n *unstructured.Unstructured, field string) usage {
	content := n.UnstructuredContent()
	status, _ := content["status"].(map[string]any)
	block, _ := status[field].(map[string]any)
	u := usage{}
	if cpu, ok := block["cpu"].(string); ok {
		u.CPUMilli = parseCPUMilli(cpu)
	}
	if mem, ok := block["memory"].(string); ok {
		u.MemoryMiB = parseMemMiB(mem)
	}
	return u
}

// podMetrics reports per-pod usage, optionally scoped to a namespace.
func (a *API) podMetrics(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	res, err := a.clusterOnly(r)
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	namespace := r.URL.Query().Get("namespace")

	podRes, err := res.cluster.Discovery.Resolve(ctx, "", "v1", "pods")
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	res.resource = podRes

	// permitted is nil when the caller may see every namespace; otherwise it is
	// the filter applied to the response below. "May list pods in at least one
	// namespace" must never gate a cluster-wide answer.
	var permitted map[string]struct{}
	// permittedOrder is the same set in the order VisibleNamespaces sorted it,
	// so the per-namespace fallback below reads namespaces — and reports the
	// ones it could not read — in a stable order rather than a map's.
	var permittedOrder []string
	var warnings []string
	if namespace != "" {
		if err := a.authorize(ctx, res, "list", namespace, "", ""); err != nil {
			a.writeErr(w, r, err)
			return
		}
	} else {
		all, allowed, scanErr := res.cluster.Authz.VisibleNamespaces(ctx,
			res.cluster.AuthzClient(res.clients),
			res.cluster.AuthSubject(res.identity),
			authz.Attributes{Verb: "list", Group: "", Version: "v1", Resource: "pods"},
			func() ([]string, error) { return a.namespaceNames(ctx, res.cluster) })
		// A truncated scan is reported, like every other VisibleNamespaces
		// caller: a partial answer that looks complete is worse than an error.
		if scanErr != nil && !all && len(allowed) == 0 {
			a.writeErr(w, r, scanErr)
			return
		}
		if scanErr != nil {
			warnings = append(warnings, scanErr.Error())
		}
		if !all && len(allowed) == 0 {
			a.writeErr(w, r, &forbiddenError{verb: "list", resource: "pods"})
			return
		}
		if !all {
			permitted = make(map[string]struct{}, len(allowed))
			for _, ns := range allowed {
				permitted[ns] = struct{}{}
			}
			permittedOrder = allowed
		}
	}

	metrics, err := res.clients.Metrics.MetricsV1beta1().PodMetricses(namespace).List(ctx, metav1.ListOptions{})
	if apierrors.IsForbidden(err) && permitted != nil {
		// Under impersonation a namespace-scoped user cannot list cluster-wide;
		// gather the namespaces they can see instead of handing them a 403.
		merged := metrics
		if merged == nil {
			merged = &v1beta1.PodMetricsList{}
		}
		merged.Items = nil
		for _, ns := range permittedOrder {
			nsList, nsErr := res.clients.Metrics.MetricsV1beta1().PodMetricses(ns).List(ctx, metav1.ListOptions{})
			if nsErr != nil {
				// Skipping in silence makes the totals below quietly short,
				// and a total that is short without saying so does not read as
				// "some pods are missing" — it reads as "these pods are using
				// less than you thought", which is the wrong conclusion to
				// hand someone looking at capacity. Every other read on this
				// surface names what it could not reach; this one did not.
				warnings = append(warnings, fmt.Sprintf(
					"pod metrics for namespace %s could not be read, so its pods are not counted: %v",
					ns, nsErr))
				continue
			}
			merged.Items = append(merged.Items, nsList.Items...)
		}
		metrics, err = merged, nil
	}
	if resp, ok := metricsUnavailable(err); ok {
		writeJSON(w, http.StatusOK, resp)
		return
	}
	if err != nil {
		a.writeErr(w, r, err)
		return
	}

	// Limits come from the pod cache, mirroring how nodeMetrics reads capacity:
	// one metrics call, zero extra API-server traffic. Only pods already in the
	// (authorization-filtered) metrics list are looked up.
	limitsByPod := map[string]usage{}
	if pods, listErr := res.cluster.Informers.List(ctx, podRes, namespace); listErr == nil {
		for _, p := range pods {
			if l, ok := podLimits(p); ok {
				limitsByPod[p.GetNamespace()+"/"+p.GetName()] = l
			}
		}
	}

	out := metricsResponse{Available: true, Warnings: warnings}
	totals := usage{}
	for _, m := range metrics.Items {
		if permitted != nil {
			if _, ok := permitted[m.Namespace]; !ok {
				continue
			}
		}
		pm := podMetric{Name: m.Name, Namespace: m.Namespace, Containers: map[string]usage{}}
		for _, c := range m.Containers {
			cu := usageFrom(c.Usage)
			pm.Containers[c.Name] = cu
			pm.Usage.CPUMilli += cu.CPUMilli
			pm.Usage.MemoryMiB += cu.MemoryMiB
		}
		if l, ok := limitsByPod[m.Namespace+"/"+m.Name]; ok {
			pm.Limits = &l
		}
		totals.CPUMilli += pm.Usage.CPUMilli
		totals.MemoryMiB += pm.Usage.MemoryMiB
		out.Pods = append(out.Pods, pm)
	}
	out.Totals = &totals
	writeJSON(w, http.StatusOK, out)
}

// podLimits sums container limits from a cached pod's spec. The bool is false
// when no container declares a limit for either resource.
func podLimits(p *unstructured.Unstructured) (usage, bool) {
	content := p.UnstructuredContent()
	spec, _ := content["spec"].(map[string]any)
	containers, _ := spec["containers"].([]any)
	u := usage{}
	found := false
	for _, c := range containers {
		cm, _ := c.(map[string]any)
		resources, _ := cm["resources"].(map[string]any)
		limits, _ := resources["limits"].(map[string]any)
		if cpu, ok := limits["cpu"].(string); ok {
			u.CPUMilli += parseCPUMilli(cpu)
			found = true
		}
		if mem, ok := limits["memory"].(string); ok {
			u.MemoryMiB += parseMemMiB(mem)
			found = true
		}
	}
	return u, found
}

func round1(f float64) float64 {
	return float64(int64(f*10+0.5)) / 10
}
