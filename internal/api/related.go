package api

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/daiwa-zou/orrery/internal/cluster"
)

// The neighbourhood endpoint answers the question every investigation starts
// with: "this object is unhappy — what else is involved?"
//
// A client can already assemble the answer from the resource routes, and that
// is exactly the problem. Finding a Deployment's pods means listing its
// ReplicaSets, matching owner UIDs, listing pods, matching owner UIDs again,
// then fetching the events for each. That is four or five round trips and a
// pile of Kubernetes convention re-implemented on the far side of the wire —
// convention this server already knows, because its own tables and rollout
// history walk the same edges. Encoding it once here means every client agrees
// on what "related" means, and a client that reasons rather than clicks — an
// agent driving the API through an MCP server — gets the whole neighbourhood
// in one call instead of guessing which of five follow-ups to make.
//
// It reads nothing the resource routes would not serve: every scan is
// preceded by the same access review, and a link the caller may not follow
// comes back as a named reference with a note rather than as data.

// maxRelated bounds one neighbourhood. A DaemonSet on a thousand-node cluster
// owns a thousand pods, and nobody reads a thousand-entry answer; the cap is
// reported rather than silently applied.
const maxRelated = 500

// maxRelatedEvents bounds the events bundled with a neighbourhood.
const maxRelatedEvents = 50

// maxChildResources bounds the repeated ?childResource= parameter.
//
// It is the most expensive repeated parameter this server has, and it was the
// one left uncapped. Every name that resolves costs a discovery lookup, a
// SubjectAccessReview, and — the part that matters — a call into the shared
// cache, which starts an informer if one is not already running: a cluster-wide
// LIST and a WATCH held open afterwards. One request naming everything
// discovery advertises makes the dashboard read the whole cluster, and does it
// again on the next request, because maxInformersPerCluster is evicting behind
// it the whole time. All of it runs under one cluster's client rate limit, so
// the users who are merely looking at a page wait behind it.
//
// The hazard is already written down. docs/API.md says of /search that "the
// default set is deliberately not everything discovery advertises: listing
// every resource would start an informer for every resource, and informer
// caches are shared and long-lived" — which is why that endpoint caps its
// ?resource= at twelve. The same sentence is true here and the cap was not.
//
// maxRelated bounds the answer, not the work, and cannot stand in for this: a
// named resource that owns nothing adds no references, so a request naming four
// hundred resources that own nothing scans all four hundred and never reaches
// the cap it would have been stopped by.
//
// Eight is past the purpose. The parameter is for a custom controller whose
// children are not one of the built-in edges, and an object has one or two of
// those, not eight.
const maxChildResources = 8

// queryChildResources reads the repeated ?childResource= parameter.
//
// Duplicates are dropped before counting, for the reason queryNamespaces drops
// them: repeating a name is not a second request for the same scan, and
// refusing a caller for something it did not ask for is its own bug.
func queryChildResources(r *http.Request) ([]string, error) {
	raw := r.URL.Query()["childResource"]
	out := make([]string, 0, min(len(raw), maxChildResources))
	seen := make(map[string]bool, len(out))
	for _, c := range raw {
		c = strings.TrimSpace(c)
		if c == "" || seen[c] {
			continue
		}
		if len(out) == maxChildResources {
			return nil, badRequest(
				"at most %d childResource values per request", maxChildResources)
		}
		seen[c] = true
		out = append(out, c)
	}
	return out, nil
}

// objectRef identifies one object in a neighbourhood, and says why it is there.
type objectRef struct {
	// Relation names the edge. See relatedResources for the vocabulary.
	Relation string `json:"relation"`
	// Depth is how many ownership hops from the subject, for owners and
	// descendants; zero for edges that are not part of the ownership walk.
	Depth     int    `json:"depth,omitempty"`
	Group     string `json:"group,omitempty"`
	Version   string `json:"version,omitempty"`
	Resource  string `json:"resource,omitempty"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	UID       string `json:"uid,omitempty"`
	// Path is where this object can be fetched from, already assembled, so a
	// caller follows a link instead of rebuilding one out of placeholders.
	Path string `json:"path,omitempty"`
	// Status is the same one-word health the tables show, for the kinds that
	// have one. Empty means "this kind has no summary", never "healthy".
	Status string `json:"status,omitempty"`
	// Note explains a link that could not be followed — not served by this
	// cluster, not readable, gone. The reference is still returned: knowing a
	// pod names a Secret is not the same as reading it.
	Note string `json:"note,omitempty"`
}

type relatedResponse struct {
	Object  objectRef   `json:"object"`
	Related []objectRef `json:"related"`
	// Events are the subject's own events, bundled because they are what the
	// next request would have asked for anyway.
	//
	// A pointer, for the reason listResponse.Items is one. "You did not ask
	// for events" and "this object has no events" are different answers, and a
	// plain slice with omitempty makes them the same absence. An empty event
	// list is the one field a reader takes as reassurance — it is why
	// overview.go grew warningsForbidden — and the reader here is often an
	// agent, which cannot look at the warnings and infer what a person would.
	// Asked for and empty is now `"events": []`.
	Events       *[]map[string]any `json:"events,omitempty"`
	EventColumns []Column          `json:"eventColumns,omitempty"`
	// Warnings name the scans that were skipped and why, so a short answer is
	// never mistaken for a complete one.
	Warnings  []string `json:"warnings,omitempty"`
	Truncated bool     `json:"truncated,omitempty"`
}

// neighbourhood accumulates one response, deduplicating and capping as it goes.
type neighbourhood struct {
	cluster   string
	refs      []objectRef
	seen      map[string]bool
	warnings  []string
	warned    map[string]bool
	truncated bool

	// lists memoises each (resource, namespace) scan for the life of one
	// request. Walking a Deployment's two ReplicaSets down to their pods would
	// otherwise list the namespace's pods twice for one answer.
	lists map[string][]*unstructured.Unstructured
}

func newNeighbourhood(clusterName string) *neighbourhood {
	return &neighbourhood{
		cluster: clusterName,
		seen:    map[string]bool{},
		warned:  map[string]bool{},
		lists:   map[string][]*unstructured.Unstructured{},
	}
}

func refKey(group, resource, namespace, name string) string {
	return strings.Join([]string{group, resource, namespace, name}, "|")
}

// add records a reference, and reports whether the walk may continue.
func (n *neighbourhood) add(ref objectRef) bool {
	key := refKey(ref.Group, ref.Resource+"/"+ref.Kind, ref.Namespace, ref.Name)
	if n.seen[key] {
		return true
	}
	if len(n.refs) >= maxRelated {
		n.truncated = true
		return false
	}
	n.seen[key] = true
	n.refs = append(n.refs, ref)
	return true
}

// warn records a reason once. The same forbidden scan reached from three
// parents is one fact, not three.
func (n *neighbourhood) warn(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if n.warned[msg] {
		return
	}
	n.warned[msg] = true
	n.warnings = append(n.warnings, msg)
}

// resourcePath renders the route that serves an object, using the same
// placeholders the resource routes accept.
func resourcePath(clusterName string, ar cluster.APIResource, namespace, name string) string {
	group := ar.Group
	if group == "" {
		group = "core"
	}
	if namespace == "" {
		namespace = "_"
	}
	return "/api/v1/clusters/" + clusterName + "/resources/" +
		group + "/" + ar.Version + "/" + ar.Name + "/" + namespace + "/" + name
}

// refStatus is the one-word health of an object, for the kinds that have one.
// Anything else gets no status rather than a reassuring default.
func refStatus(o *unstructured.Unstructured) string {
	switch o.GetKind() {
	case "Pod":
		return podStatus(o)
	case "Node":
		return nodeStatus(o)
	case "Deployment", "StatefulSet", "ReplicaSet", "DaemonSet", "Job":
		return workloadHealth(o)
	default:
		return ""
	}
}

// refFor builds a reference to an object this server has in hand.
func (n *neighbourhood) refFor(relation string, depth int, ar cluster.APIResource, o *unstructured.Unstructured) objectRef {
	return objectRef{
		Relation: relation, Depth: depth,
		Group: ar.Group, Version: ar.Version, Resource: ar.Name,
		Kind: o.GetKind(), Namespace: o.GetNamespace(), Name: o.GetName(),
		UID: string(o.GetUID()), Path: resourcePath(n.cluster, ar, o.GetNamespace(), o.GetName()),
		Status: refStatus(o),
	}
}

// relatedResources serves the neighbourhood of one object.
//
// Relations, in the vocabulary a client should switch on:
//
//	owner        an object named in the subject's ownerReferences, or in one
//	             of its owners' — Depth says how far up
//	child        an object whose ownerReferences name the subject
//	descendant   the same, further down the chain — a Deployment's pods
//	node         the node a pod is scheduled on
//	hosts        a pod scheduled on the subject node
//	selects      a pod matched by the subject service's selector
//	selected-by  a service whose selector matches the subject pod
//	reference    an object named in the subject's spec — a mounted ConfigMap,
//	             an Ingress backend, a bound PersistentVolume. Named, not read.
func (a *API) relatedResources(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	res, err := a.resolve(r)
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	namespace, name := pathNamespace(r), chi.URLParam(r, "name")
	if !res.resource.Namespaced {
		namespace = ""
	}
	// Read before the subject is fetched, so a bad parameter is a 400 that
	// costs nothing rather than one paid for with a live read.
	childResources, err := queryChildResources(r)
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	if err := a.authorize(ctx, res, "get", namespace, name, ""); err != nil {
		a.writeErr(w, r, err)
		return
	}
	// Read live for the same reason getResource does: the subject's own spec
	// drives every edge below, and a stale nodeName or selector sends the
	// whole walk to the wrong place.
	subject, err := res.clients.Dynamic.
		Resource(res.resource.GVR()).Namespace(namespace).
		Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		a.writeErr(w, r, err)
		return
	}

	depth := queryInt(r, "depth", 2, 1, 4)
	n := newNeighbourhood(res.cluster.Cfg.Name)
	n.seen[refKey(res.resource.Group, res.resource.Name+"/"+subject.GetKind(), namespace, name)] = true

	a.walkOwners(ctx, res, subject, depth, n)
	a.walkChildren(ctx, res, res.resource, subject, depth, childResources, n)
	a.walkAffinities(ctx, res, subject, n)

	out := relatedResponse{
		Object:    n.refFor("subject", 0, res.resource, subject),
		Related:   n.refs,
		Warnings:  n.warnings,
		Truncated: n.truncated,
	}
	if out.Related == nil {
		out.Related = []objectRef{}
	}
	if queryBool(r, "events", true) {
		// A nil slice back means the scan could not run, and the warning it
		// left says so; anything else is an answer, empty or not.
		if rows, cols := a.objectEvents(ctx, res, subject, n); rows != nil {
			out.Events, out.EventColumns = &rows, cols
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// walkOwners climbs the ownerReferences chain.
//
// A reference is not data: it came out of an object the caller was already
// allowed to read, so naming the owner leaks nothing. Reading it does, so each
// hop is authorized, and a hop the caller may not take is reported as a named
// reference and ends the climb.
func (a *API) walkOwners(ctx context.Context, res *resolved, obj *unstructured.Unstructured, depth int, n *neighbourhood) {
	current := obj
	for d := 1; d <= depth; d++ {
		var next *unstructured.Unstructured
		refs := current.GetOwnerReferences()
		if len(refs) == 0 {
			return
		}
		for _, o := range refs {
			got, ref := a.readOwner(ctx, res, current.GetNamespace(), o, d, n)
			n.add(ref)
			if got == nil {
				continue
			}
			// The controller owner is the chain worth climbing; a plain owner
			// reference is a side edge.
			if next == nil || (o.Controller != nil && *o.Controller) {
				next = got
			}
		}
		if next == nil {
			return
		}
		current = next
	}
}

// readOwner resolves and fetches one owner reference. It returns nil for the
// object when the owner could not be read, with the reason on the reference.
func (a *API) readOwner(
	ctx context.Context,
	res *resolved,
	childNamespace string,
	o metav1.OwnerReference,
	depth int,
	n *neighbourhood,
) (*unstructured.Unstructured, objectRef) {
	ref := objectRef{
		Relation: "owner", Depth: depth,
		Kind: o.Kind, Name: o.Name, UID: string(o.UID),
	}
	gv, err := schema.ParseGroupVersion(o.APIVersion)
	if err != nil {
		ref.Note = "unparseable apiVersion " + o.APIVersion
		return nil, ref
	}
	// Owner references name a kind, not a resource; discovery accepts either.
	ar, err := res.cluster.Discovery.Resolve(ctx, gv.Group, gv.Version, strings.ToLower(o.Kind))
	if err != nil {
		ref.Group, ref.Version = gv.Group, gv.Version
		ref.Note = "not served by this cluster"
		return nil, ref
	}
	namespace := ""
	if ar.Namespaced {
		// An owner is always in its child's namespace, or cluster-scoped.
		namespace = childNamespace
	}
	ref.Group, ref.Version, ref.Resource = ar.Group, ar.Version, ar.Name
	ref.Namespace = namespace
	ref.Path = resourcePath(n.cluster, ar, namespace, o.Name)

	scoped := *res
	scoped.resource = ar
	if err := a.authorize(ctx, &scoped, "get", namespace, o.Name, ""); err != nil {
		ref.Note = err.Error()
		return nil, ref
	}
	got, err := res.clients.Dynamic.
		Resource(ar.GVR()).Namespace(namespace).
		Get(ctx, o.Name, metav1.GetOptions{})
	if err != nil {
		ref.Note = err.Error()
		return nil, ref
	}
	ref.Status = refStatus(got)
	return got, ref
}

// childCandidates names the resources worth scanning for a kind's children.
//
// Scanning every namespaced resource instead would start an informer for each
// one to answer a question about a single object — the cache is shared and
// long-lived, so a broad walk is not a cost paid once. These are the edges
// Kubernetes controllers actually create; anything else is reachable by naming
// it with childResource.
func childCandidates(kind string) []schema.GroupVersionResource {
	gvr := func(group, version, resource string) schema.GroupVersionResource {
		return schema.GroupVersionResource{Group: group, Version: version, Resource: resource}
	}
	switch kind {
	case "Deployment":
		return []schema.GroupVersionResource{gvr("apps", "v1", "replicasets")}
	case "ReplicaSet", "StatefulSet", "DaemonSet", "Job":
		return []schema.GroupVersionResource{gvr("", "v1", "pods")}
	case "CronJob":
		return []schema.GroupVersionResource{gvr("batch", "v1", "jobs")}
	case "Service":
		return []schema.GroupVersionResource{gvr("discovery.k8s.io", "v1", "endpointslices")}
	default:
		return nil
	}
}

// parseChildResource reads a caller-named resource to scan, spelled
// "group/version/resource", "version/resource" or just "resource".
func parseChildResource(raw string) (group, version, resource string, ok bool) {
	parts := strings.Split(strings.TrimSpace(raw), "/")
	switch len(parts) {
	case 1:
		return "", "", parts[0], parts[0] != ""
	case 2:
		return "", parts[0], parts[1], parts[1] != ""
	case 3:
		return parts[0], parts[1], parts[2], parts[2] != ""
	default:
		return "", "", "", false
	}
}

// walkChildren descends the ownership chain, level by level, so a Deployment
// reaches its pods through its ReplicaSets without the caller doing the
// matching.
func (a *API) walkChildren(
	ctx context.Context,
	res *resolved,
	ar cluster.APIResource,
	obj *unstructured.Unstructured,
	depth int,
	extra []string,
	n *neighbourhood,
) {
	type node struct {
		ar  cluster.APIResource
		obj *unstructured.Unstructured
	}
	frontier := []node{{ar, obj}}

	for d := 1; d <= depth && len(frontier) > 0; d++ {
		var next []node
		for _, parent := range frontier {
			candidates := childCandidates(parent.obj.GetKind())
			if d == 1 {
				// A caller-named resource answers about the subject, not about
				// whatever the subject happens to own.
				for _, raw := range extra {
					g, v, rname, ok := parseChildResource(raw)
					if !ok {
						n.warn("childResource %q is not a resource path", raw)
						continue
					}
					candidates = append(candidates, schema.GroupVersionResource{
						Group: g, Version: v, Resource: rname,
					})
				}
			}
			for _, cand := range candidates {
				childRes, objs, err := a.scanFor(ctx, res, cand, parent.obj.GetNamespace(), n)
				if err != nil {
					continue
				}
				parentUID := parent.obj.GetUID()
				for _, o := range objs {
					if !ownedBy(o, parentUID) {
						continue
					}
					relation := "child"
					if d > 1 {
						relation = "descendant"
					}
					if !n.add(n.refFor(relation, d, childRes, o)) {
						return
					}
					next = append(next, node{childRes, o})
				}
			}
		}
		frontier = next
	}
}

// scanFor resolves, authorizes and lists one candidate resource, memoising the
// result for the request. Every failure is a warning rather than an error: a
// neighbourhood missing one scan is still worth returning, and saying which
// scan is missing is the whole point.
func (a *API) scanFor(
	ctx context.Context,
	res *resolved,
	cand schema.GroupVersionResource,
	namespace string,
	n *neighbourhood,
) (cluster.APIResource, []*unstructured.Unstructured, error) {
	ar, err := a.resolveSpelling(ctx, res.cluster, cand.Group, cand.Version, cand.Resource)
	if err != nil {
		n.warn("%s is not served by this cluster", cand.Resource)
		return cluster.APIResource{}, nil, err
	}
	if !ar.Supports("list") {
		n.warn("%s cannot be listed", ar.Name)
		return ar, nil, fmt.Errorf("%s cannot be listed", ar.Name)
	}
	if !ar.Namespaced {
		namespace = ""
	}
	key := refKey(ar.Group, ar.Name, namespace, "")
	if cached, ok := n.lists[key]; ok {
		return ar, cached, nil
	}

	scoped := *res
	scoped.resource = ar
	if err := a.authorize(ctx, &scoped, "list", namespace, "", ""); err != nil {
		n.warn("%s", err.Error())
		return ar, nil, err
	}
	objs, err := res.cluster.Informers.List(ctx, ar, namespace)
	if err != nil {
		n.warn("listing %s: %v", ar.Name, err)
		return ar, nil, err
	}
	n.lists[key] = objs
	return ar, objs, nil
}

// ownedBy reports whether o names uid among its owner references.
//
// It reads the raw references rather than calling GetOwnerReferences, which
// builds a []metav1.OwnerReference and pulls apiVersion, kind, name, uid,
// controller and blockOwnerDeletion out of every entry — the whole struct, to
// compare one string. walkChildren asks this of every object of every
// candidate resource for every parent in the frontier, so it is asked far more
// often than there are objects to ask about, and the answer is one field.
func ownedBy(o *unstructured.Unstructured, uid types.UID) bool {
	if uid == "" {
		return false
	}
	raw, _, _ := unstructured.NestedFieldNoCopy(o.Object, "metadata", "ownerReferences")
	for _, ref := range slice2(raw) {
		if mstr(mapOf(ref), "uid") == string(uid) {
			return true
		}
	}
	return false
}

// walkAffinities adds the edges Kubernetes expresses through fields rather
// than ownership: scheduling, selectors and named references.
func (a *API) walkAffinities(
	ctx context.Context,
	res *resolved,
	obj *unstructured.Unstructured,
	n *neighbourhood,
) {
	switch obj.GetKind() {
	case "Pod":
		a.podAffinities(ctx, res, obj, n)
	case "Service":
		a.selectedPods(ctx, res, obj, mapOf(nestedMap(obj, "spec", "selector")), "selects", n)
	case "Node":
		a.podsOnNode(ctx, res, obj, n)
	case "Ingress":
		a.ingressBackends(ctx, res, obj, n)
	case "PersistentVolumeClaim":
		if pv := str(obj, "spec", "volumeName"); pv != "" {
			a.addReference(ctx, res, "reference", "", "v1", "persistentvolumes", "PersistentVolume", "", pv, n)
		}
	}
}

// nestedMap reads a map-valued field without copying it.
func nestedMap(o *unstructured.Unstructured, fields ...string) any {
	v, _, _ := unstructured.NestedFieldNoCopy(o.Object, fields...)
	return v
}

func (a *API) podAffinities(ctx context.Context, res *resolved, pod *unstructured.Unstructured, n *neighbourhood) {
	// The node a pod runs on is a stronger edge than an incidental name in a
	// spec, and clients switch on the relation.
	if node := str(pod, "spec", "nodeName"); node != "" {
		a.addReference(ctx, res, "node", "", "v1", "nodes", "Node", "", node, n)
	}
	ns := pod.GetNamespace()
	if sa := str(pod, "spec", "serviceAccountName"); sa != "" {
		a.addReference(ctx, res, "reference", "", "v1", "serviceaccounts", "ServiceAccount", ns, sa, n)
	}
	for _, name := range podSecretRefs(pod) {
		a.addReference(ctx, res, "reference", "", "v1", "secrets", "Secret", ns, name, n)
	}
	for _, name := range podConfigMapRefs(pod) {
		a.addReference(ctx, res, "reference", "", "v1", "configmaps", "ConfigMap", ns, name, n)
	}
	for _, name := range podClaimRefs(pod) {
		a.addReference(ctx, res, "reference", "", "v1", "persistentvolumeclaims", "PersistentVolumeClaim", ns, name, n)
	}

	// Services whose selector matches this pod. This one is a scan, so it is
	// authorized like any other list.
	labels := mapOf(nestedMap(pod, "metadata", "labels"))
	if len(labels) == 0 {
		return
	}
	svcRes, services, err := a.scanFor(ctx, res,
		schema.GroupVersionResource{Version: "v1", Resource: "services"}, ns, n)
	if err != nil {
		return
	}
	for _, svc := range services {
		sel := mapOf(nestedMap(svc, "spec", "selector"))
		if len(sel) == 0 || !selectorMatches(sel, labels) {
			continue
		}
		if !n.add(n.refFor("selected-by", 0, svcRes, svc)) {
			return
		}
	}
}

// selectedPods lists the pods a selector matches.
func (a *API) selectedPods(
	ctx context.Context,
	res *resolved,
	obj *unstructured.Unstructured,
	selector map[string]any,
	relation string,
	n *neighbourhood,
) {
	if len(selector) == 0 {
		return
	}
	podRes, pods, err := a.scanFor(ctx, res,
		schema.GroupVersionResource{Version: "v1", Resource: "pods"}, obj.GetNamespace(), n)
	if err != nil {
		return
	}
	for _, p := range pods {
		if !selectorMatches(selector, mapOf(nestedMap(p, "metadata", "labels"))) {
			continue
		}
		if !n.add(n.refFor(relation, 0, podRes, p)) {
			return
		}
	}
}

func (a *API) podsOnNode(ctx context.Context, res *resolved, node *unstructured.Unstructured, n *neighbourhood) {
	podRes, pods, err := a.scanFor(ctx, res,
		schema.GroupVersionResource{Version: "v1", Resource: "pods"}, "", n)
	if err != nil {
		return
	}
	for _, p := range pods {
		if str(p, "spec", "nodeName") != node.GetName() {
			continue
		}
		if !n.add(n.refFor("hosts", 0, podRes, p)) {
			return
		}
	}
}

func (a *API) ingressBackends(ctx context.Context, res *resolved, ing *unstructured.Unstructured, n *neighbourhood) {
	ns := ing.GetNamespace()
	add := func(backend map[string]any) {
		svc := mapOf(backend["service"])
		if name := mstr(svc, "name"); name != "" {
			a.addReference(ctx, res, "reference", "", "v1", "services", "Service", ns, name, n)
		}
	}
	add(mapOf(nestedMap(ing, "spec", "defaultBackend")))
	for _, ruleAny := range slice(ing, "spec", "rules") {
		http := mapOf(mapOf(ruleAny)["http"])
		paths, _ := http["paths"].([]any)
		for _, pAny := range paths {
			add(mapOf(mapOf(pAny)["backend"]))
		}
	}
}

// addReference records an object named in the subject's spec. The name is
// already visible to a caller who could read the subject, so no access review
// gates naming it; the path leads to a route that will review the read.
func (a *API) addReference(
	ctx context.Context,
	res *resolved,
	relation string,
	group, version, resource, kind, namespace, name string,
	n *neighbourhood,
) {
	ref := objectRef{
		Relation: relation, Kind: kind, Namespace: namespace, Name: name,
		Group: group, Version: version, Resource: resource,
	}
	ar, err := res.cluster.Discovery.Resolve(ctx, group, version, resource)
	if err != nil {
		ref.Note = "not served by this cluster"
		n.add(ref)
		return
	}
	if !ar.Namespaced {
		ref.Namespace, namespace = "", ""
	}
	ref.Group, ref.Version, ref.Resource = ar.Group, ar.Version, ar.Name
	ref.Path = resourcePath(n.cluster, ar, namespace, name)
	n.add(ref)
}

// selectorMatches is the equality-based selector Services and ReplicationControllers
// use: every key in the selector must be present on the object with the same value.
func selectorMatches(selector map[string]any, labels map[string]any) bool {
	for k, v := range selector {
		got, ok := labels[k]
		if !ok {
			return false
		}
		if fmt.Sprint(got) != fmt.Sprint(v) {
			return false
		}
	}
	return true
}

// podSecretRefs names every Secret a pod's spec points at.
func podSecretRefs(pod *unstructured.Unstructured) []string {
	out := newNameSet()
	for _, pull := range slice(pod, "spec", "imagePullSecrets") {
		out.add(mstr(mapOf(pull), "name"))
	}
	for _, vol := range slice(pod, "spec", "volumes") {
		v := mapOf(vol)
		out.add(mstr(mapOf(v["secret"]), "secretName"))
		for _, src := range slice2(mapOf(v["projected"])["sources"]) {
			out.add(mstr(mapOf(mapOf(src)["secret"]), "name"))
		}
	}
	forEachContainer(pod, func(c map[string]any) {
		for _, ef := range slice2(c["envFrom"]) {
			out.add(mstr(mapOf(mapOf(ef)["secretRef"]), "name"))
		}
		for _, e := range slice2(c["env"]) {
			from := mapOf(mapOf(e)["valueFrom"])
			out.add(mstr(mapOf(from["secretKeyRef"]), "name"))
		}
	})
	return out.sorted()
}

// podConfigMapRefs names every ConfigMap a pod's spec points at.
func podConfigMapRefs(pod *unstructured.Unstructured) []string {
	out := newNameSet()
	for _, vol := range slice(pod, "spec", "volumes") {
		v := mapOf(vol)
		out.add(mstr(mapOf(v["configMap"]), "name"))
		for _, src := range slice2(mapOf(v["projected"])["sources"]) {
			out.add(mstr(mapOf(mapOf(src)["configMap"]), "name"))
		}
	}
	forEachContainer(pod, func(c map[string]any) {
		for _, ef := range slice2(c["envFrom"]) {
			out.add(mstr(mapOf(mapOf(ef)["configMapRef"]), "name"))
		}
		for _, e := range slice2(c["env"]) {
			from := mapOf(mapOf(e)["valueFrom"])
			out.add(mstr(mapOf(from["configMapKeyRef"]), "name"))
		}
	})
	return out.sorted()
}

// podClaimRefs names every PersistentVolumeClaim a pod mounts.
func podClaimRefs(pod *unstructured.Unstructured) []string {
	out := newNameSet()
	for _, vol := range slice(pod, "spec", "volumes") {
		out.add(mstr(mapOf(mapOf(vol)["persistentVolumeClaim"]), "claimName"))
	}
	return out.sorted()
}

func forEachContainer(pod *unstructured.Unstructured, fn func(map[string]any)) {
	for _, field := range []string{"initContainers", "containers", "ephemeralContainers"} {
		for _, c := range slice(pod, "spec", field) {
			fn(mapOf(c))
		}
	}
}

// slice2 reads a nested []any that has already been extracted as a bare value.
func slice2(v any) []any {
	s, _ := v.([]any)
	return s
}

// nameSet collects distinct, non-empty names in a stable order.
type nameSet struct {
	seen map[string]bool
}

func newNameSet() *nameSet { return &nameSet{seen: map[string]bool{}} }

func (s *nameSet) add(name string) {
	if name != "" {
		s.seen[name] = true
	}
}

func (s *nameSet) sorted() []string {
	out := make([]string, 0, len(s.seen))
	for k := range s.seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// objectEvents returns the subject's own events, projected through the same
// columns the events endpoint uses so a client renders one shape everywhere.
func (a *API) objectEvents(
	ctx context.Context,
	res *resolved,
	obj *unstructured.Unstructured,
	n *neighbourhood,
) ([]map[string]any, []Column) {
	eventRes, err := res.cluster.Discovery.Resolve(ctx, "", "v1", "events")
	if err != nil {
		n.warn("events are not served by this cluster")
		return nil, nil
	}
	events, err := a.visibleObjects(ctx, res, "", "v1", "events")
	if err != nil {
		n.warn("%s", err.Error())
		return nil, nil
	}

	uid, kind, name, ns := obj.GetUID(), obj.GetKind(), obj.GetName(), obj.GetNamespace()
	set := a.tableFor(ctx, res.cluster, eventRes)
	matched := make([]*unstructured.Unstructured, 0, 8)
	for _, e := range events {
		involvedUID := str(e, "involvedObject", "uid")
		switch {
		case involvedUID != "" && uid != "":
			if involvedUID != string(uid) {
				continue
			}
		// A recycled name is the wrong object, but an event with no UID
		// recorded is all there is to go on for some controllers.
		case str(e, "involvedObject", "kind") == kind &&
			str(e, "involvedObject", "name") == name &&
			str(e, "involvedObject", "namespace") == ns:
		default:
			continue
		}
		matched = append(matched, e)
	}
	sort.Slice(matched, func(i, j int) bool {
		return eventTime(matched[i]) > eventTime(matched[j])
	})
	if len(matched) > maxRelatedEvents {
		matched = matched[:maxRelatedEvents]
	}
	// Never nil on the way out: a nil slice here is how the caller recognises
	// a scan that could not run, and an object that is genuinely quiet is not
	// that. The columns come too, so an empty table still has its headings.
	rows := make([]map[string]any, 0, len(matched))
	for _, e := range matched {
		rows = append(rows, set.row(e))
	}
	return rows, set.columns
}
