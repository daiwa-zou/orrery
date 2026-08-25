package api

import (
	"context"
	"sync"
	"testing"
	"time"

	"k8s.io/client-go/util/jsonpath"

	"github.com/daiwa-zou/orrery/internal/cluster"
)

func TestFinalizePrependsIdentityAndAppendsAge(t *testing.T) {
	set := columnSet{
		columns: []Column{{Key: "status", Label: "Status", Type: ColStatus}},
		row:     baseRow,
	}
	out := finalize(set, cluster.APIResource{Namespaced: true})

	keys := make([]string, len(out.columns))
	for i, c := range out.columns {
		keys[i] = c.Key
	}
	want := []string{"name", "namespace", "status", "age"}
	if len(keys) != len(want) {
		t.Fatalf("columns = %v, want %v", keys, want)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("columns = %v, want %v", keys, want)
		}
	}
}

func TestFinalizeDoesNotDuplicateAge(t *testing.T) {
	// A CRD can declare its own Age printer column; finalize must not add a
	// second one.
	set := columnSet{
		columns: []Column{{Key: "age", Label: "Age", Type: ColAge}},
		row:     baseRow,
	}
	out := finalize(set, cluster.APIResource{Namespaced: false})
	ages := 0
	for _, c := range out.columns {
		if c.Key == "age" {
			ages++
		}
	}
	if ages != 1 {
		t.Errorf("finalize produced %d age columns", ages)
	}
	// Cluster-scoped: no namespace column either.
	for _, c := range out.columns {
		if c.Key == "namespace" {
			t.Error("cluster-scoped table grew a namespace column")
		}
	}
}

func TestGenericColumnsProjectsBaseRow(t *testing.T) {
	set := genericColumns()
	if set.columns != nil {
		t.Errorf("generic columns should add nothing beyond identity: %v", set.columns)
	}
	u := mkObj(t, map[string]any{"name": "thing", "namespace": "demo"}, nil)
	row := set.row(u)
	if row["name"] != "thing" || row["namespace"] != "demo" {
		t.Errorf("generic row = %v", row)
	}
}

func TestNewTableCacheDefaultsTTL(t *testing.T) {
	c := newTableCache(0)
	if c.ttl != 5*time.Minute {
		t.Errorf("zero ttl should default, got %v", c.ttl)
	}
	if c := newTableCache(time.Second); c.ttl != time.Second {
		t.Errorf("explicit ttl not honoured: %v", c.ttl)
	}
}

func TestTableCachePutGet(t *testing.T) {
	c := newTableCache(time.Hour)
	if _, ok := c.get("missing"); ok {
		t.Error("empty cache reported a hit")
	}

	c.put("k", genericColumns())
	set, ok := c.get("k")
	if !ok || set.row == nil {
		t.Fatal("stored entry did not come back")
	}
}

func TestTableCacheExpiry(t *testing.T) {
	// Plant an already-expired entry directly; the clock never sleeps in tests.
	c := newTableCache(time.Hour)
	c.entries["stale"] = tableCacheEntry{
		set:     genericColumns(),
		expires: time.Now().Add(-time.Minute),
	}
	if _, ok := c.get("stale"); ok {
		t.Error("an expired entry must miss, or CRD column changes never propagate")
	}
}

func TestTableForBuiltinKind(t *testing.T) {
	// Builtin kinds resolve without touching the cluster or the cache.
	a := &API{tables: newTableCache(time.Hour)}
	set := a.tableFor(context.Background(), nil, cluster.APIResource{
		Group: "", Version: "v1", Name: "pods", Kind: "Pod", Namespaced: true,
	})
	if set.row == nil {
		t.Fatal("builtin kind returned no projector")
	}
	if set.columns[0].Key != "name" || set.columns[1].Key != "namespace" {
		t.Errorf("builtin table missing identity columns: %+v", set.columns[:2])
	}
}

func TestPrinterColumnType(t *testing.T) {
	cases := map[string]ColumnType{
		"integer": ColNumber,
		"number":  ColNumber,
		"boolean": ColBool,
		"date":    ColAge,
		"string":  ColText,
		"":        ColText,
	}
	for in, want := range cases {
		if got := printerColumnType(in); got != want {
			t.Errorf("printerColumnType(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPrinterColumnFindResults(t *testing.T) {
	jp := jsonpath.New("replicas").AllowMissingKeys(true)
	if err := jp.Parse("{.spec.replicas}"); err != nil {
		t.Fatal(err)
	}
	pc := printerColumn{
		column: Column{Key: "x_replicas"},
		mu:     &sync.Mutex{},
		path:   jp,
	}

	results, err := pc.findResults(map[string]any{
		"spec": map[string]any{"replicas": int64(3)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 || len(results[0]) == 0 {
		t.Fatal("jsonpath found nothing")
	}
	if got := results[0][0].Interface(); got != int64(3) {
		t.Errorf("jsonpath value = %v, want 3", got)
	}
}
