package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"github.com/daiwazou/clusterlens/backend/internal/cluster"
)

// targetRef identifies the object an action applies to.
type targetRef struct {
	Group     string `json:"group"`
	Version   string `json:"version"`
	Resource  string `json:"resource"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

func decodeBody[T any](r *http.Request) (T, error) {
	var out T
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
	if err != nil {
		return out, badRequest("read body: %v", err)
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, badRequest("decode body: %v", err)
	}
	return out, nil
}

// resolveTarget resolves a body-supplied target the same way a URL-supplied
// one is resolved, so actions share the resource and authorization logic.
func (a *API) resolveTarget(r *http.Request, t targetRef) (*resolved, error) {
	res, err := a.clusterOnly(r)
	if err != nil {
		return nil, err
	}
	ar, err := res.cluster.Discovery.Resolve(r.Context(), t.Group, t.Version, t.Resource)
	if err != nil {
		return nil, err
	}
	res.resource = ar
	return res, nil
}

type scaleRequest struct {
	targetRef
	Replicas int32 `json:"replicas"`
}

// scaleWorkload sets replicas through the scale subresource, which is the
// only correct way: it works for Deployments, StatefulSets, ReplicaSets and
// any CRD that declares a scale subresource.
func (a *API) scaleWorkload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	req, err := decodeBody[scaleRequest](r)
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	if req.Replicas < 0 {
		a.writeErr(w, r, badRequest("replicas must not be negative"))
		return
	}
	res, err := a.resolveTarget(r, req.targetRef)
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	if err := a.authorize(ctx, res, "update", req.Namespace, req.Name, "scale"); err != nil {
		a.writeErr(w, r, err)
		return
	}

	patch := fmt.Sprintf(`{"spec":{"replicas":%d}}`, req.Replicas)
	updated, err := res.clients.Dynamic.
		Resource(res.resource.GVR()).Namespace(req.Namespace).
		Patch(ctx, req.Name, types.MergePatchType, []byte(patch), metav1.PatchOptions{}, "scale")
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"scaled": true, "name": req.Name, "namespace": req.Namespace,
		"replicas": req.Replicas, "object": updated,
	})
}

// restartWorkload triggers a rolling restart the same way kubectl does, by
// stamping the pod template. Using the documented annotation means the change
// is legible to anyone reading the manifest afterwards.
func (a *API) restartWorkload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	req, err := decodeBody[targetRef](r)
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	res, err := a.resolveTarget(r, req)
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	switch res.resource.Kind {
	case "Deployment", "StatefulSet", "DaemonSet":
	default:
		a.writeErr(w, r, badRequest("%s does not support a rolling restart", res.resource.Kind))
		return
	}
	if err := a.authorize(ctx, res, "patch", req.Namespace, req.Name, ""); err != nil {
		a.writeErr(w, r, err)
		return
	}

	patch := fmt.Sprintf(
		`{"spec":{"template":{"metadata":{"annotations":{"kubectl.kubernetes.io/restartedAt":%q}}}}}`,
		time.Now().UTC().Format(time.RFC3339))

	updated, err := res.clients.Dynamic.
		Resource(res.resource.GVR()).Namespace(req.Namespace).
		Patch(ctx, req.Name, types.StrategicMergePatchType, []byte(patch), metav1.PatchOptions{})
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"restarted": true, "name": req.Name, "object": cluster.TrimForResponse(updated),
	})
}

type cordonRequest struct {
	Node          string `json:"node"`
	Unschedulable bool   `json:"unschedulable"`
}

func (a *API) cordonNode(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	req, err := decodeBody[cordonRequest](r)
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	res, err := a.resolveTarget(r, targetRef{Version: "v1", Resource: "nodes", Name: req.Node})
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	if err := a.authorize(ctx, res, "patch", "", req.Node, ""); err != nil {
		a.writeErr(w, r, err)
		return
	}

	patch := fmt.Sprintf(`{"spec":{"unschedulable":%t}}`, req.Unschedulable)
	if _, err := res.clients.Dynamic.
		Resource(res.resource.GVR()).
		Patch(ctx, req.Node, types.MergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
		a.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"node": req.Node, "unschedulable": req.Unschedulable})
}

type drainRequest struct {
	Node               string `json:"node"`
	GracePeriodSeconds *int64 `json:"gracePeriodSeconds,omitempty"`
	// IgnoreDaemonSets mirrors kubectl's flag; without it a drain would always
	// fail, since DaemonSet pods are immediately recreated.
	IgnoreDaemonSets bool `json:"ignoreDaemonSets"`
	// DeleteEmptyDirData acknowledges that draining discards emptyDir volumes.
	DeleteEmptyDirData bool `json:"deleteEmptyDirData"`
	DryRun             bool `json:"dryRun"`
}

type drainResult struct {
	Node     string   `json:"node"`
	Cordoned bool     `json:"cordoned"`
	Evicted  []string `json:"evicted"`
	Skipped  []string `json:"skipped"`
	Failed   []string `json:"failed"`
	DryRun   bool     `json:"dryRun"`
}

// drainNode cordons a node and evicts its pods, honouring PodDisruptionBudgets
// because it goes through the eviction API rather than deleting pods.
func (a *API) drainNode(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	req, err := decodeBody[drainRequest](r)
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	if req.Node == "" {
		a.writeErr(w, r, badRequest("node is required"))
		return
	}

	nodeRes, err := a.resolveTarget(r, targetRef{Version: "v1", Resource: "nodes", Name: req.Node})
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	if err := a.authorize(ctx, nodeRes, "patch", "", req.Node, ""); err != nil {
		a.writeErr(w, r, err)
		return
	}

	podRes, err := nodeRes.cluster.Discovery.Resolve(ctx, "", "v1", "pods")
	if err != nil {
		a.writeErr(w, r, err)
		return
	}

	pods, err := nodeRes.cluster.Informers.List(ctx, podRes, "")
	if err != nil {
		a.writeErr(w, r, err)
		return
	}

	result := drainResult{Node: req.Node, DryRun: req.DryRun}

	if !req.DryRun {
		patch := []byte(`{"spec":{"unschedulable":true}}`)
		if _, err := nodeRes.clients.Dynamic.Resource(nodeRes.resource.GVR()).
			Patch(ctx, req.Node, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
			a.writeErr(w, r, err)
			return
		}
		result.Cordoned = true
	}

	evictRes := *nodeRes
	evictRes.resource = podRes

	for _, pod := range pods {
		if str(pod, "spec", "nodeName") != req.Node {
			continue
		}
		ns, name := pod.GetNamespace(), pod.GetName()
		ref := ns + "/" + name

		if reason, skip := skipDrain(pod, req); skip {
			result.Skipped = append(result.Skipped, ref+" ("+reason+")")
			continue
		}
		if req.DryRun {
			result.Evicted = append(result.Evicted, ref)
			continue
		}
		if err := a.authorize(ctx, &evictRes, "create", ns, name, "eviction"); err != nil {
			result.Failed = append(result.Failed, ref+" (not permitted)")
			continue
		}
		eviction := &policyv1.Eviction{
			ObjectMeta:    metav1.ObjectMeta{Name: name, Namespace: ns},
			DeleteOptions: &metav1.DeleteOptions{GracePeriodSeconds: req.GracePeriodSeconds},
		}
		if err := nodeRes.clients.Kube.PolicyV1().Evictions(ns).Evict(ctx, eviction); err != nil {
			result.Failed = append(result.Failed, ref+" ("+err.Error()+")")
			continue
		}
		result.Evicted = append(result.Evicted, ref)
	}

	writeJSON(w, http.StatusOK, result)
}

// skipDrain reproduces kubectl drain's exclusion rules.
func skipDrain(pod *unstructured.Unstructured, req drainRequest) (string, bool) {
	if pod.GetDeletionTimestamp() != nil {
		return "already terminating", true
	}
	switch str(pod, "status", "phase") {
	case "Succeeded", "Failed":
		return "already finished", true
	}
	if _, ok := pod.GetAnnotations()["kubernetes.io/config.mirror"]; ok {
		return "mirror pod", true
	}
	for _, ownerAny := range slice(pod, "metadata", "ownerReferences") {
		owner := mapOf(ownerAny)
		if mstr(owner, "kind") == "DaemonSet" {
			if req.IgnoreDaemonSets {
				return "daemonset-managed", true
			}
			return "daemonset-managed (set ignoreDaemonSets to proceed)", true
		}
	}
	if !req.DeleteEmptyDirData {
		for _, volAny := range slice(pod, "spec", "volumes") {
			if _, ok := mapOf(volAny)["emptyDir"]; ok {
				return "uses emptyDir (set deleteEmptyDirData to proceed)", true
			}
		}
	}
	return "", false
}

type evictRequest struct {
	Namespace          string `json:"namespace"`
	Pod                string `json:"pod"`
	GracePeriodSeconds *int64 `json:"gracePeriodSeconds,omitempty"`
}

// evictPod removes a single pod through the eviction API so disruption budgets
// still apply.
func (a *API) evictPod(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	req, err := decodeBody[evictRequest](r)
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	if req.Namespace == "" || req.Pod == "" {
		a.writeErr(w, r, badRequest("namespace and pod are required"))
		return
	}
	res, err := a.resolveTarget(r, targetRef{Version: "v1", Resource: "pods", Namespace: req.Namespace, Name: req.Pod})
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	if err := a.authorize(ctx, res, "create", req.Namespace, req.Pod, "eviction"); err != nil {
		a.writeErr(w, r, err)
		return
	}
	eviction := &policyv1.Eviction{
		ObjectMeta:    metav1.ObjectMeta{Name: req.Pod, Namespace: req.Namespace},
		DeleteOptions: &metav1.DeleteOptions{GracePeriodSeconds: req.GracePeriodSeconds},
	}
	if err := res.clients.Kube.PolicyV1().Evictions(req.Namespace).Evict(ctx, eviction); err != nil {
		a.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"evicted": true, "pod": req.Pod, "namespace": req.Namespace})
}
