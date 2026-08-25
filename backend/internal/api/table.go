package api

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/util/jsonpath"

	"github.com/daiwa-zou/orrery/backend/internal/cluster"
)

// crdResource is where additional printer columns live.
var crdResource = cluster.APIResource{
	Group:      "apiextensions.k8s.io",
	Version:    "v1",
	Name:       "customresourcedefinitions",
	Kind:       "CustomResourceDefinition",
	Namespaced: false,
}

// printerColumn is one compiled additionalPrinterColumn.
//
// The mutex is load-bearing: JSONPath.FindResults mutates parser state on the
// shared object, and one compiled columnSet is cached and used by every
// concurrent list, watch and event projection for the CRD. Serialising the
// evaluation is cheap; a corrupted parse tree that blanks a column until the
// cache expires is not.
type printerColumn struct {
	column Column
	mu     *sync.Mutex
	path   *jsonpath.JSONPath
}

func (pc printerColumn) findResults(obj map[string]any) ([][]reflect.Value, error) {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	return pc.path.FindResults(obj)
}

type tableCacheEntry struct {
	set     columnSet
	expires time.Time
}

// tableCache memoises resolved column sets per cluster and resource. Parsing
// JSONPath on every list request would be wasteful, and the CRD lookup behind
// it costs an informer read.
type tableCache struct {
	mu      sync.RWMutex
	entries map[string]tableCacheEntry
	ttl     time.Duration
}

func newTableCache(ttl time.Duration) *tableCache {
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	return &tableCache{entries: make(map[string]tableCacheEntry), ttl: ttl}
}

func (t *tableCache) get(key string) (columnSet, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	e, ok := t.entries[key]
	if !ok || time.Now().After(e.expires) {
		return columnSet{}, false
	}
	return e.set, true
}

func (t *tableCache) put(key string, set columnSet) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.entries[key] = tableCacheEntry{set: set, expires: time.Now().Add(t.ttl)}
}

// tableFor resolves the columns for a resource: a hand-tuned builtin when one
// exists, otherwise the CRD's own additionalPrinterColumns, otherwise a
// generic name/age table.
func (a *API) tableFor(ctx context.Context, c *cluster.Cluster, ar cluster.APIResource) columnSet {
	if set, ok := builtinColumns[gk(ar.Group, ar.Kind)]; ok {
		return finalize(set, ar)
	}

	key := c.Cfg.Name + "|" + ar.Group + "|" + ar.Version + "|" + ar.Name
	if set, ok := a.tables.get(key); ok {
		return set
	}

	set := a.crdColumns(ctx, c, ar)
	if set.row == nil {
		set = genericColumns()
	}
	set = finalize(set, ar)
	a.tables.put(key, set)
	return set
}

// finalize prepends the identity columns and appends age exactly once.
func finalize(set columnSet, ar cluster.APIResource) columnSet {
	cols := append(identityColumns(ar.Namespaced), set.columns...)
	hasAge := false
	for _, c := range cols {
		if c.Key == "age" {
			hasAge = true
			break
		}
	}
	if !hasAge {
		cols = append(cols, ageColumn)
	}
	return columnSet{columns: cols, row: set.row}
}

func genericColumns() columnSet {
	return columnSet{
		columns: nil,
		row:     func(u *unstructured.Unstructured) map[string]any { return baseRow(u) },
	}
}

// crdColumns reads a custom resource's own printer columns, which is how
// kubectl produces a useful table for a CRD it has never seen. Reusing them
// means an operator's carefully chosen columns show up in the dashboard for
// free.
func (a *API) crdColumns(ctx context.Context, c *cluster.Cluster, ar cluster.APIResource) columnSet {
	if ar.Group == "" {
		return columnSet{}
	}
	crdName := ar.Name + "." + ar.Group

	obj, err := c.Informers.Get(ctx, crdResource, "", crdName)
	if err != nil || obj == nil {
		return columnSet{}
	}

	var raw []any
	for _, v := range slice(obj, "spec", "versions") {
		vm := mapOf(v)
		if mstr(vm, "name") != ar.Version {
			continue
		}
		raw, _ = vm["additionalPrinterColumns"].([]any)
		break
	}
	if len(raw) == 0 {
		return columnSet{}
	}

	var compiled []printerColumn
	for _, item := range raw {
		m := mapOf(item)
		name := mstr(m, "name")
		path := mstr(m, "jsonPath")
		if name == "" || path == "" {
			continue
		}
		// The CRD schema uses bare JSONPath; the library wants it braced.
		jp := jsonpath.New(name).AllowMissingKeys(true)
		if err := jp.Parse("{" + strings.TrimSpace(path) + "}"); err != nil {
			continue
		}
		key := "x_" + sanitizeKey(name)
		col := Column{
			Key:      key,
			Label:    name,
			Type:     printerColumnType(mstr(m, "type")),
			Priority: int(mint(m, "priority")),
		}
		if col.Type == ColNumber {
			col.Align = "right"
		}
		compiled = append(compiled, printerColumn{column: col, mu: &sync.Mutex{}, path: jp})
	}
	if len(compiled) == 0 {
		return columnSet{}
	}

	cols := make([]Column, 0, len(compiled))
	for _, pc := range compiled {
		cols = append(cols, pc.column)
	}

	return columnSet{
		columns: cols,
		row: func(u *unstructured.Unstructured) map[string]any {
			r := baseRow(u)
			for _, pc := range compiled {
				results, err := pc.findResults(u.Object)
				if err != nil || len(results) == 0 || len(results[0]) == 0 {
					continue
				}
				v := results[0][0].Interface()
				if v != nil {
					r[pc.column.Key] = v
				}
			}
			return r
		},
	}
}

// printerColumnType maps OpenAPI column types onto the frontend's renderers.
func printerColumnType(t string) ColumnType {
	switch t {
	case "integer", "number":
		return ColNumber
	case "boolean":
		return ColBool
	case "date":
		return ColAge
	default:
		return ColText
	}
}

// sanitizeKey turns a human column name into a stable JSON key.
func sanitizeKey(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return strings.Trim(b.String(), "_")
}
