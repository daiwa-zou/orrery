package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// The pod table, which is what these filters are written against.
var podSet = builtinColumns[gk("", "Pod")]

func obj(name, namespace string, labels map[string]string, extra map[string]any) *unstructured.Unstructured {
	meta := map[string]any{"name": name}
	if namespace != "" {
		meta["namespace"] = namespace
	}
	if len(labels) > 0 {
		l := map[string]any{}
		for k, v := range labels {
			l[k] = v
		}
		meta["labels"] = l
	}
	o := map[string]any{"metadata": meta}
	for k, v := range extra {
		o[k] = v
	}
	return &unstructured.Unstructured{Object: o}
}

func req(query string) *http.Request {
	return httptest.NewRequest(http.MethodGet, "/x?"+query, nil)
}

func names(objs []*unstructured.Unstructured) []string {
	out := make([]string, len(objs))
	for i, o := range objs {
		out[i] = o.GetName()
	}
	return out
}

func TestFilterObjectsByName(t *testing.T) {
	objs := []*unstructured.Unstructured{
		obj("web-abc", "demo", nil, nil),
		obj("api-xyz", "demo", nil, nil),
		obj("WEB-upper", "demo", nil, nil),
	}

	got, err := filterObjects(objs, req("q=web"), podSet)
	if err != nil {
		t.Fatal(err)
	}
	// Matching is case-insensitive; people type lower case.
	if len(got) != 2 {
		t.Errorf("got %v, want the two web entries", names(got))
	}
}

func TestFilterObjectsQueryMatchesNamespaceAndLabels(t *testing.T) {
	objs := []*unstructured.Unstructured{
		obj("payments-api", "prod", nil, nil),
		obj("worker", "payments", nil, nil),
		obj("cache", "demo", map[string]string{"team": "payments"}, nil),
		obj("unrelated", "demo", map[string]string{"app": "web"}, nil),
	}

	got, err := filterObjects(objs, req("q=payments"), podSet)
	if err != nil {
		t.Fatal(err)
	}
	// Name, namespace and label values all count as matches.
	if len(got) != 3 {
		t.Errorf("q=payments matched %v, want name+namespace+label hits", names(got))
	}

	// "key=value" reaches labels without selector syntax.
	got, err = filterObjects(objs, req("q=app%3Dweb"), podSet)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].GetName() != "unrelated" {
		t.Errorf("q=app=web matched %v, want [unrelated]", names(got))
	}
}

func TestFilterObjectsRejectsUnsupportedFieldKey(t *testing.T) {
	objs := []*unstructured.Unstructured{obj("a", "demo", nil, nil)}

	// A field this server never projects must 400, not silently match nothing.
	_, err := filterObjects(objs, req("fieldSelector=spec.serviceAccountName%3Dx"), podSet)
	if err == nil {
		t.Fatal("an unsupported field selector key should be rejected")
	}
	if !strings.Contains(err.Error(), "spec.serviceAccountName") {
		t.Errorf("the error should name the offending field, got %q", err)
	}
	if !strings.Contains(err.Error(), "spec.nodeName") {
		t.Errorf("the error should list the supported fields, got %q", err)
	}
}

func TestFilterObjectsByLabelSelector(t *testing.T) {
	objs := []*unstructured.Unstructured{
		obj("a", "demo", map[string]string{"app": "web", "tier": "front"}, nil),
		obj("b", "demo", map[string]string{"app": "api"}, nil),
		obj("c", "demo", map[string]string{"app": "web", "tier": "cache"}, nil),
	}

	got, err := filterObjects(objs, req("labelSelector=app%3Dweb"), podSet)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("app=web matched %v", names(got))
	}

	got, err = filterObjects(objs, req("labelSelector=app%3Dweb%2Ctier%21%3Dcache"), podSet)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].GetName() != "a" {
		t.Errorf("compound selector matched %v, want [a]", names(got))
	}
}

func TestFilterObjectsRejectsBadSelectors(t *testing.T) {
	objs := []*unstructured.Unstructured{obj("a", "demo", nil, nil)}

	if _, err := filterObjects(objs, req("labelSelector=%3D%3Dbroken"), podSet); err == nil {
		t.Error("an invalid label selector should be a bad request, not silently ignored")
	}
}

func TestFilterObjectsByFieldSelector(t *testing.T) {
	objs := []*unstructured.Unstructured{
		obj("a", "demo", nil, map[string]any{
			"spec":   map[string]any{"nodeName": "node-1"},
			"status": map[string]any{"phase": "Running"},
		}),
		obj("b", "demo", nil, map[string]any{
			"spec":   map[string]any{"nodeName": "node-2"},
			"status": map[string]any{"phase": "Running"},
		}),
		obj("c", "demo", nil, map[string]any{
			"spec":   map[string]any{"nodeName": "node-1"},
			"status": map[string]any{"phase": "Failed"},
		}),
	}

	got, err := filterObjects(objs, req("fieldSelector=spec.nodeName%3Dnode-1"), podSet)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("nodeName filter matched %v", names(got))
	}

	got, err = filterObjects(objs, req("fieldSelector=status.phase%3DFailed"), podSet)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].GetName() != "c" {
		t.Errorf("phase filter matched %v, want [c]", names(got))
	}
}

func TestFilterObjectsWithoutFiltersIsIdentity(t *testing.T) {
	objs := []*unstructured.Unstructured{obj("a", "d", nil, nil), obj("b", "d", nil, nil)}
	got, err := filterObjects(objs, req(""), podSet)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("unfiltered list changed length: %v", names(got))
	}
}

func TestSortByMeta(t *testing.T) {
	objs := []*unstructured.Unstructured{
		obj("charlie", "b", nil, nil),
		obj("alpha", "a", nil, nil),
		obj("bravo", "a", nil, nil),
	}

	sortByMeta(objs, "name", false)
	if got := names(objs); got[0] != "alpha" || got[2] != "charlie" {
		t.Errorf("ascending by name = %v", got)
	}

	sortByMeta(objs, "name", true)
	if got := names(objs); got[0] != "charlie" {
		t.Errorf("descending by name = %v", got)
	}

	sortByMeta(objs, "namespace", false)
	// Within a namespace, name is the tiebreaker so ordering is stable.
	if got := names(objs); got[0] != "alpha" || got[1] != "bravo" || got[2] != "charlie" {
		t.Errorf("by namespace then name = %v", got)
	}
}

func TestSortByCellHandlesNumbersNumerically(t *testing.T) {
	objs := []*unstructured.Unstructured{
		obj("a", "d", nil, nil),
		obj("b", "d", nil, nil),
		obj("c", "d", nil, nil),
	}
	set := restartsColumns(map[string]int64{"a": 9, "b": 10, "c": 2})

	sortByCell(objs, set, "restarts", false)

	// Lexical sorting would put 10 before 9; that is the bug this guards.
	if got := names(objs); got[0] != "c" || got[1] != "a" || got[2] != "b" {
		t.Fatalf("numeric sort gave %v, want [c a b] for restarts 2, 9, 10", got)
	}
}

func TestPageBounds(t *testing.T) {
	cases := []struct {
		total, page, size int
		wantStart         int
		wantEnd           int
	}{
		{100, 1, 25, 0, 25},
		{100, 4, 25, 75, 100},
		{100, 5, 25, 100, 100}, // past the end yields an empty page, not a panic
		{10, 1, 25, 0, 10},
		{0, 1, 25, 0, 0},
		{100, 99, 25, 100, 100},
	}

	for _, tc := range cases {
		start, end := pageBounds(tc.total, tc.page, tc.size)
		if start != tc.wantStart || end != tc.wantEnd {
			t.Errorf("pageBounds(%d,%d,%d) = %d,%d want %d,%d",
				tc.total, tc.page, tc.size, start, end, tc.wantStart, tc.wantEnd)
		}
		if start > end {
			t.Errorf("pageBounds(%d,%d,%d) produced an invalid slice range", tc.total, tc.page, tc.size)
		}
	}
}

func TestProjectPageNeverPanics(t *testing.T) {
	objs := make([]*unstructured.Unstructured, 10)
	for i := range objs {
		objs[i] = obj(strconv.Itoa(i), "d", nil, nil)
	}
	set := restartsColumns(nil)
	r := httptest.NewRequest(http.MethodGet, "/x", nil)

	if got := projectPage(objs, set, 100, 25, r); len(got) != 0 {
		t.Errorf("a page past the end returned %d rows", len(got))
	}
	if got := projectPage(objs, set, 1, 3, r); len(got) != 3 {
		t.Errorf("first page returned %d rows", len(got))
	}
}

func TestPatchTypeFor(t *testing.T) {
	ok := map[string]string{
		"application/merge-patch+json":                "application/merge-patch+json",
		"":                                            "application/merge-patch+json",
		"application/json-patch+json":                 "application/json-patch+json",
		"application/strategic-merge-patch+json":      "application/strategic-merge-patch+json",
		"application/apply-patch+yaml":                "application/apply-patch+yaml",
		"application/merge-patch+json; charset=utf-8": "application/merge-patch+json",
	}
	for in, want := range ok {
		got, err := patchTypeFor(in)
		if err != nil {
			t.Errorf("patchTypeFor(%q) errored: %v", in, err)
			continue
		}
		if string(got) != want {
			t.Errorf("patchTypeFor(%q) = %q, want %q", in, got, want)
		}
	}
	if _, err := patchTypeFor("text/plain"); err == nil {
		t.Error("an unsupported patch type should be rejected")
	}
}

func TestQueryIntClamps(t *testing.T) {
	r := req("pageSize=100000&page=0&neg=-5")
	if got := queryInt(r, "pageSize", 50, 1, 1000); got != 1000 {
		t.Errorf("pageSize was not clamped to the maximum: %d", got)
	}
	if got := queryInt(r, "page", 1, 1, 100); got != 1 {
		t.Errorf("page was not clamped to the minimum: %d", got)
	}
	if got := queryInt(r, "missing", 7, 1, 100); got != 7 {
		t.Errorf("missing param did not use the default: %d", got)
	}
	if got := queryInt(req("pageSize=abc"), "pageSize", 50, 1, 1000); got != 50 {
		t.Errorf("unparseable value did not fall back to the default: %d", got)
	}
}

func TestForbiddenErrorReadsWell(t *testing.T) {
	e := &forbiddenError{verb: "delete", resource: "pods", namespace: "demo"}
	want := "you are not allowed to delete pods in namespace demo"
	if e.Error() != want {
		t.Errorf("got %q, want %q", e.Error(), want)
	}

	cluster := &forbiddenError{verb: "list", resource: "nodes"}
	if cluster.Error() != "you are not allowed to list nodes cluster-wide" {
		t.Errorf("cluster-scoped message reads badly: %q", cluster.Error())
	}
}

func TestPathNamespacePlaceholder(t *testing.T) {
	// Cluster-scoped resources use "_" so one route shape serves both scopes.
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	if got := pathNamespace(r); got != "" {
		t.Errorf("a request with no namespace param gave %q", got)
	}
}

// restartsColumns projects the one value these tests sort on.
func restartsColumns(byName map[string]int64) columnSet {
	return columnSet{row: func(u *unstructured.Unstructured) map[string]any {
		return map[string]any{"name": u.GetName(), "restarts": byName[u.GetName()]}
	}}
}

func TestSortByCellOrdersTheObjectsThemselves(t *testing.T) {
	// view=table and view=full must page through the same ordering. Sorting
	// used to reorder a parallel slice of rows and could leave the objects
	// untouched, so the two views disagreed about what page 1 contained; rows
	// are now projected from the sorted objects, so there is only one order.
	objs := []*unstructured.Unstructured{
		obj("a", "d", nil, nil),
		obj("b", "d", nil, nil),
		obj("c", "d", nil, nil),
	}
	set := restartsColumns(map[string]int64{"a": 5, "b": 1, "c": 3})

	sortByCell(objs, set, "restarts", false)

	wantOrder := []string{"b", "c", "a"}
	for i, want := range wantOrder {
		if objs[i].GetName() != want {
			t.Fatalf("objs[%d] = %s, want %s", i, objs[i].GetName(), want)
		}
	}

	// The rows a caller receives come out of these objects, so they cannot
	// disagree with them.
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	rows := projectPage(objs, set, 1, 3, r)
	for i, want := range wantOrder {
		if rows[i]["name"] != want {
			t.Fatalf("rows[%d] = %v, want %s", i, rows[i]["name"], want)
		}
	}
}

func TestSortByCellDescending(t *testing.T) {
	objs := []*unstructured.Unstructured{obj("a", "d", nil, nil), obj("b", "d", nil, nil)}
	set := restartsColumns(map[string]int64{"a": 1, "b": 9})

	sortByCell(objs, set, "restarts", true)

	if objs[0].GetName() != "b" {
		t.Errorf("descending sort put %s first", objs[0].GetName())
	}
}

// Equal cells must not leave the order to the map iteration that produced
// them, or two requests for the same page return different objects.
func TestSortByCellBreaksTiesOnName(t *testing.T) {
	objs := []*unstructured.Unstructured{
		obj("c", "d", nil, nil),
		obj("a", "d", nil, nil),
		obj("b", "d", nil, nil),
	}
	set := restartsColumns(map[string]int64{"a": 7, "b": 7, "c": 7})

	sortByCell(objs, set, "restarts", false)

	for i, want := range []string{"a", "b", "c"} {
		if objs[i].GetName() != want {
			t.Fatalf("tied objects ordered %s at %d, want %s", objs[i].GetName(), i, want)
		}
	}
}

// Sorting must not need to keep a row per object alive; only the page is
// rendered. The allocation here is the sort's own bookkeeping, not a table.
func TestSortByCellDoesNotRetainARowPerObject(t *testing.T) {
	const n = 2000
	objs := make([]*unstructured.Unstructured, n)
	restarts := make(map[string]int64, n)
	for i := range objs {
		name := fmt.Sprintf("pod-%04d", i)
		objs[i] = obj(name, "d", nil, nil)
		restarts[name] = int64(n - i)
	}
	set := restartsColumns(restarts)

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	sortByCell(objs, set, "restarts", false)
	// Collect first: the rows the sort computed and dropped are garbage by
	// now, and counting them would measure allocation rather than retention.
	// Retention is the property under test — the old code kept every row.
	runtime.GC()
	runtime.ReadMemStats(&after)

	// Live heap after the sort should hold the objects, the cell slice and the
	// ordering — not n row maps. A row map per object would be several hundred
	// bytes each; this bounds it well under that.
	live := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	if live > int64(n)*200 {
		t.Errorf("sort left %d bytes live for %d objects, which looks like a row per object", live, n)
	}
	if objs[0].GetName() != "pod-1999" {
		t.Errorf("sort produced the wrong order: %s first", objs[0].GetName())
	}
}
