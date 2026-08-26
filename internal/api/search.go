package api

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"sync"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/daiwa-zou/orrery/internal/cluster"
)

// Search answers the question a list endpoint cannot: "where is the thing
// called X?"
//
// Every existing read starts by naming a cluster, a group, a version and a
// resource. That is the right shape for a console, where a human has already
// clicked their way to a table. It is the wrong shape for anyone who knows
// only a name — the string out of an alert, a ticket, a chat message — and has
// to find which of eleven clusters it lives in. Answering that from the
// resource routes means a list per resource per cluster and the matching done
// on the far side of the wire, which is the same scan this server can do
// against caches it already holds.
//
// The scan is bounded on purpose: a curated set of resources rather than
// everything discovery advertises, because listing every resource would start
// an informer for every resource, and informer caches are shared and
// long-lived — a broad search would permanently enlarge the dashboard's
// footprint to answer one question. Callers who need something outside the set
// name it with `resource`.

// defaultSearchResources is what "search" means with no resource named: the
// kinds people actually go looking for by name. Every one of them is already
// cached for the cluster overview, so the default search starts no informer
// that was not running anyway.
var defaultSearchResources = []schema.GroupVersionResource{
	{Version: "v1", Resource: "pods"},
	{Group: "apps", Version: "v1", Resource: "deployments"},
	{Group: "apps", Version: "v1", Resource: "statefulsets"},
	{Group: "apps", Version: "v1", Resource: "daemonsets"},
	{Group: "batch", Version: "v1", Resource: "jobs"},
	{Group: "batch", Version: "v1", Resource: "cronjobs"},
	{Version: "v1", Resource: "services"},
	{Group: "networking.k8s.io", Version: "v1", Resource: "ingresses"},
	{Version: "v1", Resource: "nodes"},
	{Version: "v1", Resource: "namespaces"},
	{Version: "v1", Resource: "configmaps"},
	{Version: "v1", Resource: "persistentvolumeclaims"},
}

// maxSearchResources bounds a caller-named resource set, so one request cannot
// ask the server to start thirty informers.
const maxSearchResources = 12

type searchHit struct {
	Cluster   string `json:"cluster"`
	Group     string `json:"group,omitempty"`
	Version   string `json:"version"`
	Resource  string `json:"resource"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	// Path is the route that serves this object, already assembled.
	Path string `json:"path"`
	// Status is the one-word health the tables show, for kinds that have one.
	Status string `json:"status,omitempty"`
	// Score orders the results: an exact name match before a prefix before a
	// match that only appeared in a label. Reported so a caller can tell an
	// exact hit from a coincidence rather than trusting the order blindly.
	Score int `json:"score"`
}

type searchResponse struct {
	Query   string      `json:"query"`
	Hits    []searchHit `json:"hits"`
	Total   int         `json:"total"`
	Limit   int         `json:"limit"`
	Scanned []string    `json:"scanned"`
	// Warnings name the clusters and resources that could not be searched, so
	// "no results" is never confused with "nowhere to look".
	Warnings  []string `json:"warnings,omitempty"`
	Truncated bool     `json:"truncated,omitempty"`
}

// searchResources serves GET /api/v1/search.
func (a *API) searchResources(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	if q == "" {
		a.writeErr(w, r, badRequest("q is required"))
		return
	}
	targets, err := searchTargets(r.URL.Query()["resource"])
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	namespace := r.URL.Query().Get("namespace")
	limit := queryInt(r, "limit", 50, 1, 500)

	wanted := map[string]bool{}
	for _, name := range r.URL.Query()["cluster"] {
		wanted[name] = true
	}

	type clusterResult struct {
		hits     []searchHit
		warnings []string
		scanned  []string
	}
	entries := a.registry.Entries()
	results := make([]clusterResult, len(entries))

	// Clusters are independent and each scan is a cache read plus a handful of
	// access reviews; doing them in sequence makes a search of eleven clusters
	// take eleven times as long as it needs to.
	var wg sync.WaitGroup
	for i, e := range entries {
		if len(wanted) > 0 && !wanted[e.Name] {
			continue
		}
		if e.Cluster == nil {
			results[i].warnings = append(results[i].warnings,
				"cluster "+e.Name+" is unreachable")
			continue
		}
		wg.Add(1)
		go func(i int, e cluster.Entry) {
			defer wg.Done()
			hits, warnings, scanned := a.searchOne(ctx, r, e.Cluster, targets, q, namespace)
			results[i] = clusterResult{hits: hits, warnings: warnings, scanned: scanned}
		}(i, e)
	}
	wg.Wait()

	out := searchResponse{Query: q, Limit: limit, Hits: []searchHit{}, Scanned: []string{}}
	seenScan := map[string]bool{}
	for _, res := range results {
		out.Hits = append(out.Hits, res.hits...)
		out.Warnings = append(out.Warnings, res.warnings...)
		for _, s := range res.scanned {
			if !seenScan[s] {
				seenScan[s] = true
				out.Scanned = append(out.Scanned, s)
			}
		}
	}
	sort.Strings(out.Scanned)
	sortHits(out.Hits)

	out.Total = len(out.Hits)
	if len(out.Hits) > limit {
		out.Hits = out.Hits[:limit]
		out.Truncated = true
	}
	writeJSON(w, http.StatusOK, out)
}

// searchOne scans one cluster. Failures are warnings, never errors: a search
// across eleven clusters must still answer when one of them is down.
func (a *API) searchOne(
	ctx context.Context,
	r *http.Request,
	c *cluster.Cluster,
	targets []schema.GroupVersionResource,
	q, namespace string,
) (hits []searchHit, warnings, scanned []string) {
	res, err := a.resolvedFor(r, c)
	if err != nil {
		return nil, []string{"cluster " + c.Cfg.Name + ": " + err.Error()}, nil
	}

	for _, target := range targets {
		ar, err := a.resolveSpelling(ctx, c, target.Group, target.Version, target.Resource)
		if err != nil {
			// A cluster that does not serve ingresses is not a problem worth
			// reporting on every search; it is the normal state of things.
			continue
		}
		if !ar.Supports("list") {
			continue
		}
		scanned = append(scanned, ar.Name)

		objs, err := a.searchScope(ctx, res, ar, namespace)
		if err != nil {
			if isForbidden(err) {
				warnings = append(warnings,
					"cluster "+c.Cfg.Name+": not allowed to list "+ar.Name)
			} else {
				warnings = append(warnings,
					"cluster "+c.Cfg.Name+": listing "+ar.Name+": "+err.Error())
			}
			continue
		}
		for _, o := range objs {
			if !matchesQuery(o, q) {
				continue
			}
			hits = append(hits, searchHit{
				Cluster: c.Cfg.Name,
				Group:   ar.Group, Version: ar.Version, Resource: ar.Name,
				Kind: o.GetKind(), Namespace: o.GetNamespace(), Name: o.GetName(),
				Path:   resourcePath(c.Cfg.Name, ar, o.GetNamespace(), o.GetName()),
				Status: refStatus(o),
				Score:  matchScore(o, q),
			})
		}
	}
	return hits, warnings, scanned
}

// searchScope lists a resource within the caller's permitted namespaces,
// honouring an explicit namespace when one was given. It is the same
// cache-then-authorize walk every list performs.
func (a *API) searchScope(
	ctx context.Context,
	res *resolved,
	ar cluster.APIResource,
	namespace string,
) ([]*unstructured.Unstructured, error) {
	scoped := *res
	scoped.resource = ar
	if namespace != "" && ar.Namespaced {
		if err := a.authorize(ctx, &scoped, "list", namespace, "", ""); err != nil {
			return nil, err
		}
		return res.cluster.Informers.List(ctx, ar, namespace)
	}
	return a.visibleObjects(ctx, res, ar.Group, ar.Version, ar.Name)
}

// searchTargets resolves the caller's resource list, or the default set.
func searchTargets(raw []string) ([]schema.GroupVersionResource, error) {
	if len(raw) == 0 {
		return defaultSearchResources, nil
	}
	out := make([]schema.GroupVersionResource, 0, len(raw))
	for _, entry := range raw {
		for _, spelling := range strings.Split(entry, ",") {
			spelling = strings.TrimSpace(spelling)
			if spelling == "" {
				continue
			}
			group, version, name, ok := parseChildResource(spelling)
			if !ok {
				return nil, badRequest("resource %q is not a resource path", spelling)
			}
			out = append(out, schema.GroupVersionResource{
				Group: group, Version: version, Resource: name,
			})
		}
	}
	if len(out) == 0 {
		return defaultSearchResources, nil
	}
	if len(out) > maxSearchResources {
		return nil, badRequest("at most %d resources per search (asked for %d)",
			maxSearchResources, len(out))
	}
	return out, nil
}

// matchScore says how well an object answers the query, so an exact hit is not
// buried under forty objects that merely share a label.
func matchScore(o *unstructured.Unstructured, q string) int {
	name := strings.ToLower(o.GetName())
	switch {
	case name == q:
		return 100
	case strings.HasPrefix(name, q):
		return 75
	case strings.Contains(name, q):
		return 50
	case strings.Contains(strings.ToLower(o.GetNamespace()), q):
		return 25
	default:
		return 10 // matched only on a label
	}
}

func sortHits(hits []searchHit) {
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		if hits[i].Cluster != hits[j].Cluster {
			return hits[i].Cluster < hits[j].Cluster
		}
		if hits[i].Resource != hits[j].Resource {
			return hits[i].Resource < hits[j].Resource
		}
		if hits[i].Namespace != hits[j].Namespace {
			return hits[i].Namespace < hits[j].Namespace
		}
		return hits[i].Name < hits[j].Name
	})
}

// resolvedFor builds the per-request cluster context for a cluster that was
// not named in the URL, which is what a cross-cluster read needs.
func (a *API) resolvedFor(r *http.Request, c *cluster.Cluster) (*resolved, error) {
	id, err := identityFrom(r)
	if err != nil {
		return nil, err
	}
	clients, err := c.ClientsFor(id)
	if err != nil {
		return nil, err
	}
	return &resolved{cluster: c, clients: clients, identity: id}, nil
}
