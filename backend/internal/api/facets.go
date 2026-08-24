package api

import (
	"net/http"
	"sort"

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

	objs, _, _, err := a.visibleScope(ctx, res, namespace)
	if err != nil {
		a.writeErr(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, computeFacets(objs))
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
