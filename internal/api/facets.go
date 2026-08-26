package api

import (
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Facet cardinality caps. Autocomplete is a hint, not an inventory: a list
// that would not fit in a dropdown is cut and marked truncated rather than
// shipped whole.
const (
	maxFacetKeys         = 50
	maxFacetValuesPerKey = 20
)

// fieldFacetKeys are the projected field-selector keys with low enough
// cardinality to suggest. metadata.name is deliberately absent (that is what
// free text is for), and so is metadata.namespace (the namespace picker
// already enumerates it).
var fieldFacetKeys = []string{"status.phase", "spec.nodeName", "type", "involvedObject.kind"}

type facet struct {
	Key    string   `json:"key"`
	Values []string `json:"values"`
}

// facetsResponse powers search autocomplete: the distinct label keys/values
// and field-selector values present on the objects the caller may see.
type facetsResponse struct {
	Labels []facet `json:"labels"`
	Fields []facet `json:"fields"`
	// Truncated tells the UI the caps above were hit, so the dropdown can
	// avoid presenting a cut list as the complete vocabulary.
	Truncated bool `json:"truncated,omitempty"`
}


// facetsCacheTTL bounds how stale an autocomplete vocabulary may be. Facets
// are explicitly a hint rather than an inventory, and the set of distinct
// label keys on a resource moves far slower than it is queried, so a short
// window trades imperceptible staleness for not rescanning every object on
// every keystroke.
const facetsCacheTTL = 15 * time.Second

// facetsCacheSize caps how many (resource, scope) combinations are remembered.
const facetsCacheSize = 512

type facetsEntry struct {
	resp     facetsResponse
	computed time.Time
}

// facetCache memoises the search vocabulary.
//
// Computing it walks every object the caller may see and builds a map of every
// label key and value on them — the most expensive read in the system, and the
// one triggered by typing in the search bar.
//
// The key carries the caller's *visible scope*, never their identity. Two
// users whose RBAC grants the same reach are looking at the same objects and
// may share an entry; a user who may see less can never be handed a vocabulary
// harvested from a wider scope, because that scope hashes to a different key.
type facetCache struct {
	mu      sync.Mutex
	entries *lru.Cache[string, facetsEntry]
}

func newFacetCache() *facetCache {
	c, err := lru.New[string, facetsEntry](facetsCacheSize)
	if err != nil {
		// Only returned for a non-positive size, which is a constant here.
		panic(err)
	}
	return &facetCache{entries: c}
}

// scopeKey renders a visible scope as a cache key. The namespace list is
// sorted so two callers with the same reach in a different order agree.
func scopeKey(s scopeInfo) string {
	switch {
	case s.AllNamespaces:
		return "all"
	case s.Namespace != "":
		return "ns=" + s.Namespace
	default:
		ns := append([]string(nil), s.Namespaces...)
		sort.Strings(ns)
		return "set=" + strings.Join(ns, ",")
	}
}

func (c *facetCache) get(key string) (facetsResponse, bool) {
	if c == nil {
		return facetsResponse{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries.Get(key)
	if !ok || time.Since(e.computed) > facetsCacheTTL {
		return facetsResponse{}, false
	}
	return e.resp, true
}

func (c *facetCache) put(key string, resp facetsResponse) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries.Add(key, facetsEntry{resp: resp, computed: time.Now()})
}

// listFacets serves the search vocabulary for one resource. It runs the exact
// authorization walk the list endpoint runs, so a user can never autocomplete
// values harvested from objects they may not list.
func (a *API) listFacets(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	res, err := a.resolve(r)
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	if !res.resource.Supports("list") {
		a.writeErr(w, r, badRequest("resource %q cannot be listed", res.resource.Name))
		return
	}

	namespace := r.URL.Query().Get("namespace")
	if !res.resource.Namespaced {
		namespace = ""
	}

	objs, scope, _, err := a.visibleScope(ctx, res, namespace)
	if err != nil {
		a.writeErr(w, r, err)
		return
	}

	// The authorization walk above has already run; what is memoised is the
	// scan over the objects it returned.
	key := res.cluster.Cfg.Name + "|" + res.resource.GVR().String() + "|" + scopeKey(scope)
	if resp, ok := a.facets.get(key); ok {
		writeJSON(w, http.StatusOK, resp)
		return
	}
	resp := computeFacets(objs)
	a.facets.put(key, resp)
	writeJSON(w, http.StatusOK, resp)
}

func computeFacets(objs []*unstructured.Unstructured) facetsResponse {
	labelVals := map[string]map[string]int{}
	fieldVals := map[string]map[string]int{}

	for _, o := range objs {
		for k, v := range o.GetLabels() {
			bump(labelVals, k, v)
		}
		set := fieldSetFor(o)
		for _, k := range fieldFacetKeys {
			if v := set[k]; v != "" {
				bump(fieldVals, k, v)
			}
		}
	}

	var resp facetsResponse
	resp.Labels, resp.Truncated = collapseFacets(labelVals, resp.Truncated)
	resp.Fields, resp.Truncated = collapseFacets(fieldVals, resp.Truncated)
	return resp
}

func bump(m map[string]map[string]int, k, v string) {
	if m[k] == nil {
		m[k] = map[string]int{}
	}
	m[k][v]++
}

// collapseFacets orders keys by how many objects carry them, so the caps keep
// the vocabulary people actually filter by and drop the long tail.
func collapseFacets(m map[string]map[string]int, truncated bool) ([]facet, bool) {
	type keyed struct {
		key   string
		count int
	}
	keys := make([]keyed, 0, len(m))
	for k, vals := range m {
		n := 0
		for _, c := range vals {
			n += c
		}
		keys = append(keys, keyed{k, n})
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].count != keys[j].count {
			return keys[i].count > keys[j].count
		}
		return keys[i].key < keys[j].key
	})
	if len(keys) > maxFacetKeys {
		keys = keys[:maxFacetKeys]
		truncated = true
	}

	out := make([]facet, 0, len(keys))
	for _, k := range keys {
		vals := make([]string, 0, len(m[k.key]))
		for v := range m[k.key] {
			vals = append(vals, v)
		}
		sort.Strings(vals)
		if len(vals) > maxFacetValuesPerKey {
			vals = vals[:maxFacetValuesPerKey]
			truncated = true
		}
		out = append(out, facet{Key: k.key, Values: vals})
	}
	return out, truncated
}
