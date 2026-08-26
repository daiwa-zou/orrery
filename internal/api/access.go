package api

import (
	"net/http"
	"sort"
	"strings"

	"github.com/daiwa-zou/orrery/internal/authz"
)

// readVerbs is what "may I read this?" means when a caller names no verb.
var readVerbs = []string{"get", "list", "watch"}

// maxProbeVerbs bounds one probe. Each verb is a SubjectAccessReview, and the
// batch POST already caps its own fan-out; a GET that anyone can paste into a
// URL bar deserves the same ceiling.
const maxProbeVerbs = 16

// accessProbeResponse answers "what am I allowed to do to this?".
type accessProbeResponse struct {
	Cluster   string       `json:"cluster"`
	Resource  resourceMeta `json:"resource"`
	Namespace string       `json:"namespace,omitempty"`
	Name      string       `json:"name,omitempty"`
	// Allowed is the permitted subset of the verbs asked about, sorted. It
	// carries no information Results does not, but it is the shape a caller
	// deciding "can I offer this?" actually wants.
	Allowed []string                  `json:"allowed"`
	Results map[string]authz.Decision `json:"results"`
}

// accessProbe answers a permission question over GET, for callers that read
// but never write.
//
// The batch POST at /access is the console's tool: it asks dozens of unrelated
// questions at once to decide which buttons to render, and it carries a CSRF
// token because every POST on this surface does. A read-only client — an agent
// or an MCP server holding a session — has no token to attach and no reason to
// acquire one, yet still needs the same answer before it reports "there are no
// deployments" when the truth is "you may not list them". Asking is not doing:
// a SubjectAccessReview changes nothing, so the question belongs on a GET.
//
// One resource, several verbs, because that is the question as it is actually
// asked. Naming none means the read verbs.
func (a *API) accessProbe(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	res, err := a.clusterOnly(r)
	if err != nil {
		a.writeErr(w, r, err)
		return
	}

	q := r.URL.Query()
	name := q.Get("resource")
	if name == "" {
		a.writeErr(w, r, badRequest("resource is required"))
		return
	}
	// Resolved through discovery so a caller may spell it as a kind, a
	// singular or a short name — "deploy" and "Deployment" reach the same
	// place the resource routes do — and so the answer names the canonical
	// group/version it was actually asked about.
	ar, err := a.resolveSpelling(ctx, res.cluster, q.Get("group"), q.Get("version"), name)
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	verbs, err := probeVerbs(q["verb"])
	if err != nil {
		a.writeErr(w, r, err)
		return
	}

	namespace := q.Get("namespace")
	if !ar.Namespaced {
		namespace = ""
	}
	object, subresource := q.Get("name"), q.Get("subresource")

	attrs := make([]authz.Attributes, 0, len(verbs))
	for _, v := range verbs {
		attrs = append(attrs, authz.Attributes{
			Verb: v, Group: ar.Group, Version: ar.Version, Resource: ar.Name,
			Subresource: subresource, Namespace: namespace, Name: object,
		})
	}

	decisions := res.cluster.Authz.AllowedMany(ctx,
		res.cluster.AuthzClient(res.clients),
		res.cluster.AuthSubject(res.identity),
		attrs)

	out := accessProbeResponse{
		Cluster:   res.cluster.Cfg.Name,
		Resource:  metaOf(ar),
		Namespace: namespace,
		Name:      object,
		Allowed:   []string{},
		Results:   make(map[string]authz.Decision, len(verbs)),
	}
	for i, v := range verbs {
		d := decisions[authz.AttributesKey(attrs[i])]
		out.Results[v] = d
		if d.Allowed {
			out.Allowed = append(out.Allowed, v)
		}
	}
	sort.Strings(out.Allowed)
	writeJSON(w, http.StatusOK, out)
}

// probeVerbs normalises the requested verbs: comma-separated or repeated
// parameters both work, duplicates collapse, and an empty ask means "can I
// read this?".
func probeVerbs(raw []string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(raw))
	for _, group := range raw {
		for _, v := range strings.Split(group, ",") {
			v = strings.ToLower(strings.TrimSpace(v))
			if v == "" || seen[v] {
				continue
			}
			seen[v] = true
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return append([]string(nil), readVerbs...), nil
	}
	if len(out) > maxProbeVerbs {
		return nil, badRequest("at most %d verbs per probe (asked for %d)", maxProbeVerbs, len(out))
	}
	return out, nil
}

// namespaceAccessResponse reports where a caller may read one resource.
type namespaceAccessResponse struct {
	Cluster  string       `json:"cluster"`
	Resource resourceMeta `json:"resource"`
	Verb     string       `json:"verb"`
	// AllNamespaces means the permission is cluster-wide, in which case
	// Namespaces is not enumerated at all.
	AllNamespaces bool     `json:"allNamespaces"`
	Namespaces    []string `json:"namespaces,omitempty"`
	// Truncated marks an answer cut short by the scan limit, so a short list
	// is never mistaken for the complete one.
	Truncated bool   `json:"truncated,omitempty"`
	Warning   string `json:"warning,omitempty"`
}

// namespaceAccess answers "where may I read this?" in one call.
//
// Without it, a client that gets a 403 from a cluster-wide list has no way to
// find the namespaces it *can* read short of probing each one, which is a
// SubjectAccessReview per namespace re-run on every question. The scan behind
// this is the same one every list already performs and caches, so asking for
// it directly costs nothing extra — and it turns "forbidden" into a usable
// next step instead of a dead end.
func (a *API) namespaceAccess(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	res, err := a.clusterOnly(r)
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	q := r.URL.Query()
	name := q.Get("resource")
	if name == "" {
		a.writeErr(w, r, badRequest("resource is required"))
		return
	}
	ar, err := a.resolveSpelling(ctx, res.cluster, q.Get("group"), q.Get("version"), name)
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	verb := strings.ToLower(strings.TrimSpace(q.Get("verb")))
	if verb == "" {
		verb = "list"
	}

	out := namespaceAccessResponse{
		Cluster: res.cluster.Cfg.Name, Resource: metaOf(ar), Verb: verb,
	}
	if !ar.Namespaced {
		// A cluster-scoped resource has no namespaces to enumerate; the one
		// honest answer is the cluster-wide verdict.
		scoped := *res
		scoped.resource = ar
		if err := a.authorize(ctx, &scoped, verb, "", "", ""); err != nil {
			if !isForbidden(err) {
				a.writeErr(w, r, err)
				return
			}
		} else {
			out.AllNamespaces = true
		}
		writeJSON(w, http.StatusOK, out)
		return
	}

	all, allowed, scanErr := res.cluster.Authz.VisibleNamespaces(ctx,
		res.cluster.AuthzClient(res.clients),
		res.cluster.AuthSubject(res.identity),
		authz.Attributes{
			Verb: verb, Group: ar.Group, Version: ar.Version, Resource: ar.Name,
		},
		func() ([]string, error) { return a.namespaceNames(ctx, res.cluster) })
	if scanErr != nil {
		if !all && len(allowed) == 0 {
			a.writeErr(w, r, scanErr)
			return
		}
		out.Truncated, out.Warning = true, scanErr.Error()
	}
	out.AllNamespaces = all
	if !all {
		out.Namespaces = allowed
	}
	writeJSON(w, http.StatusOK, out)
}
