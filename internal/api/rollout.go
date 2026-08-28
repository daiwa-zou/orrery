package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/yaml"

	"github.com/daiwa-zou/orrery/internal/cluster"
)

// revisionAnnotation is where the Deployment controller records which rollout
// a ReplicaSet belongs to.
const revisionAnnotation = "deployment.kubernetes.io/revision"

const changeCauseAnnotation = "kubernetes.io/change-cause"

// revisionSummary is one entry of `kubectl rollout history`, plus what the
// command cannot tell you: how this revision differs from the one running now.
type revisionSummary struct {
	Revision int64    `json:"revision"`
	Name     string   `json:"name"`
	Images   []string `json:"images"`
	Replicas int64    `json:"replicas"`
	// Ready is how many of those replicas ever became ready. A revision that
	// never reached readiness is a poor thing to go back to, and the number is
	// the only warning of that on the page.
	Ready       int64  `json:"ready"`
	Current     bool   `json:"current"`
	ChangeCause string `json:"changeCause,omitempty"`
	CreatedAt   string `json:"createdAt"`
	// Changes is what rolling back here would alter in the pod template,
	// phrased in the direction it would travel. Empty on the current revision.
	Changes []string `json:"changes"`
	// Diff is the same answer in full: the lines of the pod template that
	// differ from the deployed one. Naming a field says whether to look; this
	// says what would be going back.
	Diff []diffLine `json:"diff,omitempty"`
	// DiffTruncated counts the changed lines beyond the cap, so a diff that
	// stops partway does not read as a complete one.
	DiffTruncated int `json:"diffTruncated,omitempty"`
	// Identical says the template matches the one deployed now exactly, so a
	// rollback would change nothing. Empty Changes does not imply it: a
	// difference this server does not name leaves both empty and false.
	Identical bool `json:"identical"`
}

// deploymentRevisions lists the ReplicaSets owned by a deployment, newest
// revision first. Both the history endpoint and undo need the same walk.
func (a *API) deploymentRevisions(ctx context.Context, res *resolved, namespace, name string) (*unstructured.Unstructured, []*unstructured.Unstructured, error) {
	depRes, err := res.cluster.Discovery.Resolve(ctx, "apps", "v1", "deployments")
	if err != nil {
		return nil, nil, err
	}
	rsRes, err := res.cluster.Discovery.Resolve(ctx, "apps", "v1", "replicasets")
	if err != nil {
		return nil, nil, err
	}

	// Both reads are served from the shared cache, so both need their own
	// access review — history is exactly the kind of side door the invariant
	// exists to close.
	depScoped := *res
	depScoped.resource = depRes
	if err := a.authorize(ctx, &depScoped, "get", namespace, name, ""); err != nil {
		return nil, nil, err
	}
	rsScoped := *res
	rsScoped.resource = rsRes
	if err := a.authorize(ctx, &rsScoped, "list", namespace, "", ""); err != nil {
		return nil, nil, err
	}

	// InformerManager.Get returns (nil, nil) for an object that is not in the
	// cache and (nil, err) for a cache that could not be read, and folding
	// those together answers "no such deployment" to a question that was never
	// asked. This walk is what rollout history and undo are built on, so the
	// moment it lies is the moment someone is rolling back a bad deploy.
	dep, err := res.cluster.Informers.Get(ctx, depRes, namespace, name)
	if err != nil {
		return nil, nil, err
	}
	if dep == nil {
		return nil, nil, notFound("deployment %s/%s", namespace, name)
	}

	all, err := res.cluster.Informers.List(ctx, rsRes, namespace)
	if err != nil {
		return nil, nil, err
	}
	owned := make([]*unstructured.Unstructured, 0, 8)
	for _, rs := range all {
		for _, o := range rs.GetOwnerReferences() {
			if o.UID == dep.GetUID() {
				owned = append(owned, rs)
				break
			}
		}
	}
	sort.Slice(owned, func(i, j int) bool {
		return revisionOf(owned[i]) > revisionOf(owned[j])
	})
	return dep, owned, nil
}

// maxDiffLines is what one revision may contribute to the response. Enough for
// any change a person made by hand; a template rewritten wholesale is reported
// as truncated rather than shipped in full to a modal nobody will read it in.
const maxDiffLines = 120

// templateLines renders a revision's pod template as the YAML lines to diff.
//
// YAML rather than JSON because it is the shape the object is read in
// everywhere else in this console — the detail page's YAML tab, kubectl — and
// because one field per line is what makes a line diff mean anything. The
// controller's own hash is dropped for the same reason the summary ignores it:
// it differs between every pair of revisions and says nothing anyone did.
func templateLines(rs *unstructured.Unstructured) []string {
	template := podTemplateOf(rs)
	if template == nil {
		return nil
	}
	out, err := yaml.Marshal(withoutTemplateHash(template))
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimRight(string(out), "\n"), "\n")
}

func revisionOf(rs *unstructured.Unstructured) int64 {
	n, _ := strconv.ParseInt(rs.GetAnnotations()[revisionAnnotation], 10, 64)
	return n
}

// rolloutHistory answers `kubectl rollout history deployment/<name>`.
func (a *API) rolloutHistory(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	res, err := a.clusterOnly(r)
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	namespace, name := r.URL.Query().Get("namespace"), r.URL.Query().Get("name")
	if namespace == "" || name == "" {
		a.writeErr(w, r, badRequest("namespace and name are required"))
		return
	}

	dep, owned, err := a.deploymentRevisions(ctx, res, namespace, name)
	if err != nil {
		a.writeErr(w, r, err)
		return
	}

	currentRev, _ := strconv.ParseInt(dep.GetAnnotations()[revisionAnnotation], 10, 64)

	// Every revision is compared against the deployed one, so the question the
	// list exists to answer — which of these do I go back to — is answered on
	// the row rather than left to whoever remembers what changed.
	var currentRS *unstructured.Unstructured
	for _, rs := range owned {
		if revisionOf(rs) == currentRev {
			currentRS = rs
			break
		}
	}

	out := make([]revisionSummary, 0, len(owned))
	for _, rs := range owned {
		current := revisionOf(rs) == currentRev
		summary := revisionSummary{
			Revision:    revisionOf(rs),
			Name:        rs.GetName(),
			Images:      containerImages(rs, "spec", "template", "spec"),
			Replicas:    i64(rs, "status", "replicas"),
			Ready:       i64(rs, "status", "readyReplicas"),
			Current:     current,
			ChangeCause: rs.GetAnnotations()[changeCauseAnnotation],
			CreatedAt:   rs.GetCreationTimestamp().UTC().Format("2006-01-02T15:04:05Z"),
			Changes:     []string{},
		}
		if !current {
			changes, identical := revisionChanges(currentRS, rs)
			if changes != nil {
				summary.Changes = changes
			}
			summary.Identical = identical
			if !identical {
				diff := lineDiff(templateLines(currentRS), templateLines(rs), 2)
				summary.Diff, summary.DiffTruncated = truncateDiff(diff, maxDiffLines)
			}
		}
		out = append(out, summary)
	}
	writeJSON(w, http.StatusOK, map[string]any{"revisions": out})
}

type rolloutUndoRequest struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	// ToRevision selects the rollback target; 0 means the previous revision,
	// mirroring kubectl.
	ToRevision int64 `json:"toRevision,omitempty"`
}

// rolloutUndo answers `kubectl rollout undo deployment/<name>`: the target
// ReplicaSet's pod template is written back onto the deployment.
func (a *API) rolloutUndo(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	req, err := decodeBody[rolloutUndoRequest](r)
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	if req.Namespace == "" || req.Name == "" {
		a.writeErr(w, r, badRequest("namespace and name are required"))
		return
	}
	res, err := a.clusterOnly(r)
	if err != nil {
		a.writeErr(w, r, err)
		return
	}

	dep, owned, err := a.deploymentRevisions(ctx, res, req.Namespace, req.Name)
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	depRes, err := res.cluster.Discovery.Resolve(ctx, "apps", "v1", "deployments")
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	patchScoped := *res
	patchScoped.resource = depRes
	if err := a.authorize(ctx, &patchScoped, "patch", req.Namespace, req.Name, ""); err != nil {
		a.writeErr(w, r, err)
		return
	}

	currentRev, _ := strconv.ParseInt(dep.GetAnnotations()[revisionAnnotation], 10, 64)
	var target *unstructured.Unstructured
	for _, rs := range owned {
		rev := revisionOf(rs)
		if req.ToRevision > 0 && rev == req.ToRevision {
			target = rs
			break
		}
		// "Previous" is the newest revision that is not the current one.
		if req.ToRevision == 0 && rev != currentRev {
			target = rs
			break
		}
	}
	if target == nil {
		a.writeErr(w, r, notFound("no rollback revision found for %s/%s", req.Namespace, req.Name))
		return
	}

	template, ok, _ := unstructured.NestedMap(target.Object, "spec", "template")
	if !ok {
		a.writeErr(w, r, notFound("revision %d has no pod template", revisionOf(target)))
		return
	}
	// The hash label belongs to the ReplicaSet, not the deployment spec;
	// leaving it in would pin every future rollout to this hash.
	unstructured.RemoveNestedField(template, "metadata", "labels", "pod-template-hash")

	// A JSON-Patch replace swaps the whole template. A strategic merge would
	// keep containers that exist now but not in the target revision.
	patch, err := json.Marshal([]map[string]any{
		{"op": "replace", "path": "/spec/template", "value": template},
	})
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	updated, err := res.clients.Dynamic.Resource(depRes.GVR()).Namespace(req.Namespace).
		Patch(ctx, req.Name, types.JSONPatchType, patch, metav1.PatchOptions{})
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"rolledBack": true,
		"name":       req.Name,
		"toRevision": revisionOf(target),
		"object":     cluster.TrimForResponse(updated),
	})
}

type cronJobRequest struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	// Suspend is only read by the suspend action.
	Suspend bool `json:"suspend"`
}

// triggerCronJob answers `kubectl create job --from=cronjob/<name>`.
func (a *API) triggerCronJob(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	req, err := decodeBody[cronJobRequest](r)
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	if req.Namespace == "" || req.Name == "" {
		a.writeErr(w, r, badRequest("namespace and name are required"))
		return
	}
	res, err := a.clusterOnly(r)
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	cjRes, err := res.cluster.Discovery.Resolve(ctx, "batch", "v1", "cronjobs")
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	jobRes, err := res.cluster.Discovery.Resolve(ctx, "batch", "v1", "jobs")
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	cjScoped := *res
	cjScoped.resource = cjRes
	if err := a.authorize(ctx, &cjScoped, "get", req.Namespace, req.Name, ""); err != nil {
		a.writeErr(w, r, err)
		return
	}
	jobScoped := *res
	jobScoped.resource = jobRes
	if err := a.authorize(ctx, &jobScoped, "create", req.Namespace, "", ""); err != nil {
		a.writeErr(w, r, err)
		return
	}

	cj, err := res.cluster.Informers.Get(ctx, cjRes, req.Namespace, req.Name)
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	if cj == nil {
		a.writeErr(w, r, notFound("cronjob %s/%s", req.Namespace, req.Name))
		return
	}

	jobSpec, ok, _ := unstructured.NestedMap(cj.Object, "spec", "jobTemplate", "spec")
	if !ok {
		a.writeErr(w, r, badRequest("cronjob has no job template"))
		return
	}
	labels, _, _ := unstructured.NestedMap(cj.Object, "spec", "jobTemplate", "metadata", "labels")

	suffix := make([]byte, 3)
	_, _ = rand.Read(suffix)
	jobName := req.Name + "-manual-" + hex.EncodeToString(suffix)
	if len(jobName) > 63 {
		jobName = jobName[len(jobName)-63:]
	}

	job := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "batch/v1",
		"kind":       "Job",
		"metadata": map[string]any{
			"name":      jobName,
			"namespace": req.Namespace,
			// The annotation kubectl sets, so controllers and humans can tell
			// a manual run from a scheduled one.
			"annotations": map[string]any{"cronjob.kubernetes.io/instantiate": "manual"},
		},
		"spec": jobSpec,
	}}
	if len(labels) > 0 {
		_ = unstructured.SetNestedMap(job.Object, labels, "metadata", "labels")
	}

	created, err := res.clients.Dynamic.Resource(jobRes.GVR()).Namespace(req.Namespace).
		Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"triggered": true,
		"job":       created.GetName(),
		"namespace": req.Namespace,
		"object":    cluster.TrimForResponse(created),
	})
}

// suspendCronJob flips spec.suspend, i.e. `kubectl patch cronjob -p
// '{"spec":{"suspend":true}}'` with a button.
func (a *API) suspendCronJob(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	req, err := decodeBody[cronJobRequest](r)
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	if req.Namespace == "" || req.Name == "" {
		a.writeErr(w, r, badRequest("namespace and name are required"))
		return
	}
	res, err := a.resolveTarget(r, targetRef{Group: "batch", Version: "v1", Resource: "cronjobs", Namespace: req.Namespace, Name: req.Name})
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	if err := a.authorize(ctx, res, "patch", req.Namespace, req.Name, ""); err != nil {
		a.writeErr(w, r, err)
		return
	}

	patch, _ := json.Marshal(map[string]any{"spec": map[string]any{"suspend": req.Suspend}})
	if _, err := res.clients.Dynamic.Resource(res.resource.GVR()).Namespace(req.Namespace).
		Patch(ctx, req.Name, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
		a.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"name": req.Name, "namespace": req.Namespace, "suspended": req.Suspend,
	})
}
