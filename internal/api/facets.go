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

	namespaces, err := queryNamespaces(r)
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	if !res.resource.Namespaced {
		namespaces = nil
	}

	objs, scope, _, err := a.visibleScope(ctx, res, namespaces)
	if err != nil {
		a.writeErr(w, r, err)
		return
	}

	// The authorization walk above has already run; what is memoised is the
	// scan over the objects it returned, per search.
	key := res.cluster.Cfg.Name + "|" + res.resource.GVR().String() + "|" +
		scopeKey(scope) + "|" + searchKey(r)
	if resp, ok := a.facets.get(key); ok {
		writeJSON(w, http.StatusOK, resp)
		return
	}

	// Autocomplete narrows with the search. Once `app=web` is applied, a
	// dropdown still offering every label key on the resource is offering
	// mostly dead ends: picking one of them adds a term that matches nothing,
	// and the reader learns that only by watching the list empty. Suggesting
	// from the objects that currently match means every suggestion leads
	// somewhere.
	//
	// filterObjects is the list endpoint's own predicate, read from the same
	// query parameters. Reimplementing the match here would let the two drift,
	// and a suggestion that the list then disagrees with is the exact failure
	// this is meant to remove.
	matching, err := filterObjects(objs, r, a.tableFor(ctx, res.cluster, res.resource))
	if err != nil {
		a.writeErr(w, r, err)
		return
	}

	resp := computeFacets(matching)
	a.facets.put(key, resp)
	writeJSON(w, http.StatusOK, resp)
}

// searchKey folds the active search into the cache key.
//
// Without it two different searches would share one vocabulary, which is worse
// than not narrowing at all: the dropdown would confidently offer keys
// harvested from somebody else's filter. Selector strings are compared as
// written, so two orderings of the same selector simply miss the cache rather
// than collide.
func searchKey(r *http.Request) string {
	q := r.URL.Query()
	return "q=" + strings.ToLower(strings.TrimSpace(q.Get("q"))) +
		"|l=" + q.Get("labelSelector") +
		"|f=" + q.Get("fieldSelector") +
		// where is repeatable and every term of it narrows, so all of them
		// belong in the key. Leaving it out was not a near miss: listFacets
		// filters with parseListFilter, the same predicate the list endpoint
		// uses, so `where=status=~Succeeded` and `where=status=~Running`
		// computed different vocabularies over the same resource and scope and
		// then collided on one entry — and whichever ran first inside the TTL
		// answered for both. Joined on a unit separator, which cannot occur in
		// a URL query value.
		"|w=" + strings.Join(q["where"], "\x1f")
}

func computeFacets(objs []*unstructured.Unstructured) facetsResponse {
	labelVals := map[string]map[string]int{}
	fieldVals := map[string]map[string]int{}

	// Two maps per object were being built and thrown away here — GetLabels
	// copies the label map, and a fields.Set was built beside it — to read the
	// labels once and four field keys. The list path stopped doing that a
	// while ago and left labelsOf and objectFields behind for the purpose;
	// this walk, which the file above calls the most expensive read in the
	// system and which every keystroke in the search bar triggers, had never
	// been changed over.
	for _, o := range objs {
		for k, raw := range labelsOf(o) {
			v, _ := raw.(string)
			bump(labelVals, k, v)
		}
		fields := objectFields{o}
		for _, k := range fieldFacetKeys {
			if v := fields.Get(k); v != "" {
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
		vals, cut := topValues(m[k.key])
		truncated = truncated || cut
		out = append(out, facet{Key: k.key, Values: vals})
	}
	return out, truncated
}

// topValues picks the values worth suggesting for one key, most common first,
// and returns them in alphabetical order for the dropdown.
//
// Which twenty matters more here than it does for keys. Keys were already
// chosen by how many objects carry them — "the caps keep the vocabulary people
// actually filter by and drop the long tail" — but values were sorted
// alphabetically and then cut, so a `version` label offered v1.0.0 through
// v1.0.19 and never the version that is actually deployed, and a nodeName
// facet on a large cluster offered the twenty nodes whose names sort first.
// The counts needed to do better were being collected already and thrown away.
//
// Displaying them alphabetically once chosen is the other half: a dropdown is
// scanned by eye for a value you already have in mind, which is a different
// job from deciding which values belong in it.
func topValues(counts map[string]int) ([]string, bool) {
	vals := make([]string, 0, len(counts))
	for v := range counts {
		vals = append(vals, v)
	}
	if len(vals) <= maxFacetValuesPerKey {
		sort.Strings(vals)
		return vals, false
	}

	sort.Slice(vals, func(i, j int) bool {
		if counts[vals[i]] != counts[vals[j]] {
			return counts[vals[i]] > counts[vals[j]]
		}
		// Ties break by name so the same objects always yield the same list.
		return vals[i] < vals[j]
	})
	vals = vals[:maxFacetValuesPerKey]
	sort.Strings(vals)
	return vals, true
}
