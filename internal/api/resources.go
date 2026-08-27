package api

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/yaml"

	"github.com/daiwa-zou/orrery/internal/authz"
	"github.com/daiwa-zou/orrery/internal/cluster"
)

// maxBodyBytes bounds manifest uploads. Kubernetes itself refuses objects
// larger than about 1.5 MiB, so this is generous.
const maxBodyBytes = 4 << 20

// listResponse is what a table page returns.
type listResponse struct {
	Items    []map[string]any             `json:"items,omitempty"`
	Objects  []*unstructured.Unstructured `json:"objects,omitempty"`
	Columns  []Column                     `json:"columns,omitempty"`
	Total    int                          `json:"total"`
	Page     int                          `json:"page"`
	PageSize int                          `json:"pageSize"`
	Resource resourceMeta                 `json:"resource"`
	Scope    scopeInfo                    `json:"scope"`
	Warnings []string                     `json:"warnings,omitempty"`
}

type resourceMeta struct {
	Group      string   `json:"group"`
	Version    string   `json:"version"`
	Name       string   `json:"name"`
	Kind       string   `json:"kind"`
	Namespaced bool     `json:"namespaced"`
	Verbs      []string `json:"verbs"`
}

func metaOf(ar cluster.APIResource) resourceMeta {
	return resourceMeta{
		Group: ar.Group, Version: ar.Version, Name: ar.Name,
		Kind: ar.Kind, Namespaced: ar.Namespaced, Verbs: ar.Verbs,
	}
}

// scopeInfo tells the UI what the user was actually allowed to see, so a
// partial list is never silently presented as the whole cluster.
type scopeInfo struct {
	AllNamespaces bool     `json:"allNamespaces"`
	Namespaces    []string `json:"namespaces,omitempty"`
	Namespace     string   `json:"namespace,omitempty"`
}

// authorize runs an access review before touching the cache. Reads are served
// from a cache populated with the dashboard's own credentials, so this check
// is what stands between a user and data they may not see.
func (a *API) authorize(ctx context.Context, res *resolved, verb, namespace, name, subresource string) error {
	attrs := authz.Attributes{
		Verb:        verb,
		Group:       res.resource.Group,
		Version:     res.resource.Version,
		Resource:    res.resource.Name,
		Subresource: subresource,
		Namespace:   namespace,
		Name:        name,
	}
	subj := res.cluster.AuthSubject(res.identity)
	client := res.cluster.AuthzClient(res.clients)

	d, err := res.cluster.Authz.Allowed(ctx, client, subj, attrs)
	if err != nil {
		return err
	}
	if !d.Allowed {
		return &forbiddenError{verb: verb, resource: res.resource.Name, namespace: namespace, reason: d.Reason}
	}
	return nil
}

// errNoNamespaceScan reports that the candidate list for the per-namespace
// fallback scan could not be built.
//
// It matters because of what an empty list does downstream. VisibleNamespaces
// scans the candidates it is given; given none it finds none allowed, returns
// that as a perfectly successful answer, and caches it for the checker's TTL.
// Every caller then reads "allowed in no namespace" as "forbidden" and tells
// the user they may not list something they may in fact list — for the next
// thirty seconds, out of one transient hiccup.
//
// The dashboard's own service account not being permitted to list namespaces
// makes it permanent rather than transient, and every narrowly bound user on
// that deployment sees a standing RBAC error that no RBAC change will fix.
//
// So the two cases are kept apart: "there are no namespaces" is a list, and
// "the namespaces could not be read" is this error. Callers surface it as the
// unavailability it is, which is the same distinction countSummary already
// draws between "you may not" and "we could not".
var errNoNamespaceScan = errors.New("the cluster's namespaces could not be listed, so it is not possible to determine where you may read")

// namespaceNames lists namespaces from the cache, used for the fallback scan
// that finds where a narrowly bound user is allowed to read.
func (a *API) namespaceNames(ctx context.Context, c *cluster.Cluster) ([]string, error) {
	nsRes, err := c.Discovery.Resolve(ctx, "", "v1", "namespaces")
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errNoNamespaceScan, err)
	}
	objs, err := c.Informers.List(ctx, nsRes, "")
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errNoNamespaceScan, err)
	}
	out := make([]string, 0, len(objs))
	for _, o := range objs {
		out = append(out, o.GetName())
	}
	sort.Strings(out)
	return out, nil
}

// visibleScope collects the objects the caller is allowed to list, together
// with the scope actually granted and any partial-scan warnings. Every read
// that serves cached objects must come through here (or perform the same
// checks): the cache holds the dashboard's view, not the caller's.
func (a *API) visibleScope(ctx context.Context, res *resolved, namespace string) ([]*unstructured.Unstructured, scopeInfo, []string, error) {
	var (
		scope    scopeInfo
		warnings []string
	)

	if namespace != "" {
		if err := a.authorize(ctx, res, "list", namespace, "", ""); err != nil {
			return nil, scope, nil, err
		}
		scope.Namespace = namespace
		objs, err := res.cluster.Informers.List(ctx, res.resource, namespace)
		return objs, scope, nil, err
	}

	all, allowed, scanErr := res.cluster.Authz.VisibleNamespaces(
		ctx,
		res.cluster.AuthzClient(res.clients),
		res.cluster.AuthSubject(res.identity),
		authz.Attributes{
			Verb: "list", Group: res.resource.Group, Version: res.resource.Version,
			Resource: res.resource.Name,
		},
		func() ([]string, error) { return a.namespaceNames(ctx, res.cluster) },
	)
	if scanErr != nil && !all && len(allowed) == 0 {
		return nil, scope, nil, scanErr
	}
	if scanErr != nil {
		warnings = append(warnings, scanErr.Error())
	}

	switch {
	case all:
		scope.AllNamespaces = true
		objs, err := res.cluster.Informers.List(ctx, res.resource, "")
		return objs, scope, warnings, err
	case len(allowed) == 0 || !res.resource.Namespaced:
		return nil, scope, nil, &forbiddenError{verb: "list", resource: res.resource.Name}
	default:
		scope.Namespaces = allowed
		var objs []*unstructured.Unstructured
		for _, ns := range allowed {
			part, err := res.cluster.Informers.List(ctx, res.resource, ns)
			if err != nil {
				return nil, scope, warnings, err
			}
			objs = append(objs, part...)
		}
		return objs, scope, warnings, nil
	}
}

// listResources serves a page of a resource from the shared cache.
func (a *API) listResources(w http.ResponseWriter, r *http.Request) {
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

	objs, scope, warnings, err := a.visibleScope(ctx, res, namespace)
	if err != nil {
		a.writeErr(w, r, err)
		return
	}

	objs, err = filterObjects(objs, r)
	if err != nil {
		a.writeErr(w, r, err)
		return
	}

	set := a.tableFor(ctx, res.cluster, res.resource)
	sortKey := r.URL.Query().Get("sort")
	desc := r.URL.Query().Get("order") == "desc"

	page := queryInt(r, "page", 1, 1, 1<<20)
	pageSize := queryInt(r, "pageSize", 50, 1, 1000)
	total := len(objs)

	// Well-known keys sort straight off object metadata, which avoids
	// projecting rows that the requested page will then throw away. Sorting by
	// any other column needs the projected value, so those rows are built up
	// front and the object slice is reordered to match — otherwise view=full
	// would page through a differently ordered list than view=table.
	if isMetaSortKey(sortKey) {
		sortByMeta(objs, sortKey, desc)
	} else {
		sortByCell(objs, set, sortKey, desc)
	}
	rows := projectPage(objs, set, page, pageSize, r)

	resp := listResponse{
		Total:    total,
		Page:     page,
		PageSize: pageSize,
		Resource: metaOf(res.resource),
		Scope:    scope,
		Warnings: warnings,
	}

	if r.URL.Query().Get("view") == "full" {
		start, end := pageBounds(total, page, pageSize)
		for _, o := range objs[start:end] {
			resp.Objects = append(resp.Objects, cluster.TrimForResponse(o))
		}
	} else {
		resp.Items = rows
		resp.Columns = set.columns
	}

	writeJSON(w, http.StatusOK, resp)
}

func buildRow(o *unstructured.Unstructured, set columnSet, r *http.Request) map[string]any {
	row := set.row(o)
	if queryBool(r, "labels", true) {
		if l := o.GetLabels(); len(l) > 0 {
			row["_labels"] = l
		}
	}
	return row
}

func projectPage(objs []*unstructured.Unstructured, set columnSet, page, pageSize int, r *http.Request) []map[string]any {
	start, end := pageBounds(len(objs), page, pageSize)
	out := make([]map[string]any, 0, end-start)
	for _, o := range objs[start:end] {
		out = append(out, buildRow(o, set, r))
	}
	return out
}

func pageBounds(total, page, pageSize int) (int, int) {
	start := (page - 1) * pageSize
	if start > total {
		start = total
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	return start, end
}

func pageOf(rows []map[string]any, page, pageSize int) []map[string]any {
	start, end := pageBounds(len(rows), page, pageSize)
	return rows[start:end]
}

func isMetaSortKey(key string) bool {
	switch key {
	case "", "name", "namespace", "age", "creationTimestamp":
		return true
	}
	return false
}

func sortByMeta(objs []*unstructured.Unstructured, key string, desc bool) {
	less := func(i, j int) bool { return objs[i].GetName() < objs[j].GetName() }
	switch key {
	case "namespace":
		less = func(i, j int) bool {
			if objs[i].GetNamespace() != objs[j].GetNamespace() {
				return objs[i].GetNamespace() < objs[j].GetNamespace()
			}
			return objs[i].GetName() < objs[j].GetName()
		}
	case "age", "creationTimestamp":
		less = func(i, j int) bool {
			ti, tj := objs[i].GetCreationTimestamp(), objs[j].GetCreationTimestamp()
			if ti.Equal(&tj) {
				return objs[i].GetName() < objs[j].GetName()
			}
			return ti.Before(&tj)
		}
	}
	sort.SliceStable(objs, func(i, j int) bool {
		if desc {
			return less(j, i)
		}
		return less(i, j)
	})
}

// sortByCell orders objects by one projected column.
//
// The value has to be computed for every object — that is what sorting means —
// but only the requested page is ever rendered, so the row each value came out
// of is dropped immediately. The previous version built a row map per object
// and held all of them live in order to return fifty: on a fifty-thousand
// object list that is sixty megabytes alive at once, per request, to page a
// table.
//
// Ties break on name, so paging is deterministic, which is what the row-based
// version did by comparing the projected "name" cell.
func sortByCell(objs []*unstructured.Unstructured, set columnSet, key string, desc bool) {
	if key == "" {
		key = "name"
	}
	cells := make([]any, len(objs))
	for i, o := range objs {
		// set.row allocates a map; taking one value out of it and letting it
		// go is the whole point.
		cells[i] = set.row(o)[key]
	}

	order := make([]int, len(objs))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		i, j := order[a], order[b]
		c := compareCell(cells[i], cells[j])
		if c == 0 {
			return objs[i].GetName() < objs[j].GetName()
		}
		if desc {
			return c > 0
		}
		return c < 0
	})

	sorted := make([]*unstructured.Unstructured, len(objs))
	for dst, src := range order {
		sorted[dst] = objs[src]
	}
	copy(objs, sorted)
}

// rowLess builds the comparison used for both row sorting paths.
func rowLess(rows []map[string]any, key string, desc bool) func(i, j int) bool {
	if key == "" {
		key = "name"
	}
	return func(i, j int) bool {
		c := compareCell(rows[i][key], rows[j][key])
		if c == 0 {
			// Ties break on name so paging is deterministic.
			return fmt.Sprint(rows[i]["name"]) < fmt.Sprint(rows[j]["name"])
		}
		if desc {
			return c > 0
		}
		return c < 0
	}
}

// compareCell orders two cells: numerically when both are numbers (zero-padded
// strings would misorder negatives), false-before-true for booleans, and
// case-insensitive text otherwise.
func compareCell(x, y any) int {
	switch xv := x.(type) {
	case int64:
		if yv, ok := y.(int64); ok {
			return cmp.Compare(xv, yv)
		}
	case float64:
		if yv, ok := y.(float64); ok {
			return cmp.Compare(xv, yv)
		}
	case bool:
		if yv, ok := y.(bool); ok {
			switch {
			case xv == yv:
				return 0
			case !xv:
				return -1
			default:
				return 1
			}
		}
	}
	return strings.Compare(strings.ToLower(fmt.Sprint(x)), strings.ToLower(fmt.Sprint(y)))
}

// listFilter is the parsed narrowing criteria shared by the list endpoint and
// the watch stream, so both agree on what "matches" means.
type listFilter struct {
	q        string
	labelSel labels.Selector
	fieldSel fields.Selector
}

func (f listFilter) empty() bool {
	return f.q == "" && f.labelSel == nil && f.fieldSel == nil
}

// unstructuredLabels adapts an object's raw label map to labels.Labels without
// copying it.
//
// GetLabels allocates a fresh map[string]string on every call, so filtering a
// fifty-thousand-object list by label selector cost fifty thousand map
// allocations for what is only ever a read-only membership test. Selector
// matching needs Has and Get, not ownership, so the raw map serves directly.
type unstructuredLabels map[string]any

func (l unstructuredLabels) Has(key string) bool {
	_, ok := l[key]
	return ok
}

func (l unstructuredLabels) Get(key string) string {
	s, _ := l[key].(string)
	return s
}

func (l unstructuredLabels) Lookup(key string) (string, bool) {
	v, ok := l[key]
	if !ok {
		return "", false
	}
	s, _ := v.(string)
	return s, true
}

// labelsOf returns a no-copy view of an object's labels.
func labelsOf(o *unstructured.Unstructured) unstructuredLabels {
	raw, found, err := unstructured.NestedFieldNoCopy(o.Object, "metadata", "labels")
	if !found || err != nil {
		return nil
	}
	m, _ := raw.(map[string]any)
	return unstructuredLabels(m)
}

// objectFields is the same trick for field selectors: fieldSetFor builds a
// map per object, which the matcher then only reads. This computes each value
// on demand instead, and mirrors fieldSetFor's key set exactly — name and
// namespace are always present, the rest only when non-empty.
type objectFields struct{ o *unstructured.Unstructured }

func (f objectFields) Get(field string) string {
	switch field {
	case "metadata.name":
		return f.o.GetName()
	case "metadata.namespace":
		return f.o.GetNamespace()
	case "status.phase":
		return str(f.o, "status", "phase")
	case "spec.nodeName":
		return str(f.o, "spec", "nodeName")
	case "type":
		return str(f.o, "type")
	case "involvedObject.name":
		return str(f.o, "involvedObject", "name")
	case "involvedObject.kind":
		return str(f.o, "involvedObject", "kind")
	case "involvedObject.namespace":
		return str(f.o, "involvedObject", "namespace")
	case "involvedObject.uid":
		return str(f.o, "involvedObject", "uid")
	default:
		return ""
	}
}

func (f objectFields) Has(field string) bool {
	switch field {
	case "metadata.name", "metadata.namespace":
		// fieldSetFor sets both unconditionally, empty or not.
		return true
	default:
		return f.Get(field) != ""
	}
}

func (f listFilter) matches(o *unstructured.Unstructured) bool {
	if f.q != "" && !matchesQuery(o, f.q) {
		return false
	}
	if f.labelSel != nil && !f.labelSel.Matches(labelsOf(o)) {
		return false
	}
	if f.fieldSel != nil && !f.fieldSel.Matches(objectFields{o}) {
		return false
	}
	return true
}

// matchesQuery is the free-text filter: name, namespace, or any label as
// "key=value", so typing "app=web" narrows by label without selector syntax.
//
// The label scan reads the raw map rather than GetLabels for the same reason
// the selector path does — a fresh map per object is a lot of garbage for a
// read-only walk — and never builds the "key=value" string it is searching.
// A query the name happens to satisfy returns on the first line and none of
// this runs; a query that has to reach the labels used to cost two allocations
// per object, which is 16 MB on a fifty-thousand-object list, on every
// keystroke the search box sends.
func matchesQuery(o *unstructured.Unstructured, q string) bool {
	if strings.Contains(strings.ToLower(o.GetName()), q) {
		return true
	}
	if ns := o.GetNamespace(); ns != "" && strings.Contains(strings.ToLower(ns), q) {
		return true
	}
	for k, raw := range labelsOf(o) {
		v, _ := raw.(string)
		if matchesLabelPair(k, v, q) {
			return true
		}
	}
	return false
}

// matchesLabelPair reports whether q occurs in the lower-cased "key=value"
// pair, without ever building that string. q is already lower-cased by
// parseListFilter.
//
// strings.ToLower returns its argument untouched when there is nothing to
// lower, and label keys and values are ASCII and almost always already
// lower-case, so the two calls here are free in the ordinary case. What is
// never free is the concatenation, which is why the three places a match can
// fall are handled separately: inside the key, inside the value, or straddling
// the "=" — and the last one can only line up with the pair's single "=", so
// it is a suffix test against the key and a prefix test against the value.
func matchesLabelPair(k, v, q string) bool {
	lk, lv := strings.ToLower(k), strings.ToLower(v)
	if strings.Contains(lk, q) || strings.Contains(lv, q) {
		return true
	}
	for e := 0; e < len(q); e++ {
		if q[e] != '=' {
			continue
		}
		if strings.HasSuffix(lk, q[:e]) && strings.HasPrefix(lv, q[e+1:]) {
			return true
		}
	}
	return false
}

func parseListFilter(r *http.Request) (listFilter, error) {
	f := listFilter{q: strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))}

	if raw := r.URL.Query().Get("labelSelector"); raw != "" {
		sel, err := labels.Parse(raw)
		if err != nil {
			return f, badRequest("labelSelector: %v", err)
		}
		f.labelSel = sel
	}

	if raw := r.URL.Query().Get("fieldSelector"); raw != "" {
		sel, err := fields.ParseSelector(raw)
		if err != nil {
			return f, badRequest("fieldSelector: %v", err)
		}
		// A field this server never projects would silently match nothing,
		// which reads as "no such objects" and sends people debugging the
		// wrong thing. Refuse it instead.
		for _, req := range sel.Requirements() {
			if !supportedFieldKeys[req.Field] {
				return f, badRequest("fieldSelector: unsupported field %q (supported: %s)",
					req.Field, strings.Join(supportedFieldKeyList(), ", "))
			}
		}
		f.fieldSel = sel
	}
	return f, nil
}

// filterObjects applies the free-text, label and field filters in one pass.
func filterObjects(objs []*unstructured.Unstructured, r *http.Request) ([]*unstructured.Unstructured, error) {
	f, err := parseListFilter(r)
	if err != nil {
		return nil, err
	}
	if f.empty() {
		return objs, nil
	}
	out := objs[:0:0]
	for _, o := range objs {
		if f.matches(o) {
			out = append(out, o)
		}
	}
	return out, nil
}

// supportedFieldKeys mirrors exactly what fieldSetFor projects.
var supportedFieldKeys = map[string]bool{
	"metadata.name":            true,
	"metadata.namespace":       true,
	"status.phase":             true,
	"spec.nodeName":            true,
	"type":                     true,
	"involvedObject.name":      true,
	"involvedObject.kind":      true,
	"involvedObject.namespace": true,
	"involvedObject.uid":       true,
}

func supportedFieldKeyList() []string {
	out := make([]string, 0, len(supportedFieldKeys))
	for k := range supportedFieldKeys {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// fieldSetFor exposes the field selectors the API server supports for the
// common resources, so a saved kubectl-style filter behaves the same here.
func fieldSetFor(o *unstructured.Unstructured) fields.Set {
	set := fields.Set{
		"metadata.name":      o.GetName(),
		"metadata.namespace": o.GetNamespace(),
	}
	if v := str(o, "status", "phase"); v != "" {
		set["status.phase"] = v
	}
	if v := str(o, "spec", "nodeName"); v != "" {
		set["spec.nodeName"] = v
	}
	if v := str(o, "type"); v != "" {
		set["type"] = v
	}
	if v := str(o, "involvedObject", "name"); v != "" {
		set["involvedObject.name"] = v
		set["involvedObject.kind"] = str(o, "involvedObject", "kind")
		set["involvedObject.namespace"] = str(o, "involvedObject", "namespace")
		set["involvedObject.uid"] = str(o, "involvedObject", "uid")
	}
	return set
}

// getResource returns a single object, always read live rather than from the
// cache: detail views need freshness for an edit round trip, and secrets are
// deliberately redacted in the cache.
func (a *API) getResource(w http.ResponseWriter, r *http.Request) {
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
	if err := a.authorize(ctx, res, "get", namespace, name, ""); err != nil {
		a.writeErr(w, r, err)
		return
	}

	obj, err := res.clients.Dynamic.
		Resource(res.resource.GVR()).Namespace(namespace).
		Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	if !queryBool(r, "managedFields", false) {
		obj = cluster.TrimForResponse(obj)
	}

	if a.writeObjectYAML(w, r, obj) {
		return
	}
	writeJSON(w, http.StatusOK, obj)
}

// decodeManifest accepts JSON or YAML, which is what a paste-a-manifest box
// needs to be useful.
func decodeManifest(r *http.Request) (*unstructured.Unstructured, error) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
	if err != nil {
		return nil, badRequest("read body: %v", err)
	}
	if len(raw) == 0 {
		return nil, badRequest("empty request body")
	}
	jsonBytes, err := yaml.YAMLToJSON(raw)
	if err != nil {
		return nil, badRequest("body is neither valid JSON nor YAML: %v", err)
	}
	obj := &unstructured.Unstructured{}
	if err := obj.UnmarshalJSON(jsonBytes); err != nil {
		return nil, badRequest("decode manifest: %v", err)
	}
	if obj.GetKind() == "" {
		return nil, badRequest("manifest has no kind")
	}
	return obj, nil
}

func (a *API) createResource(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	res, err := a.resolve(r)
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	obj, err := decodeManifest(r)
	if err != nil {
		a.writeErr(w, r, err)
		return
	}

	namespace := obj.GetNamespace()
	if namespace == "" {
		namespace = r.URL.Query().Get("namespace")
	}
	if !res.resource.Namespaced {
		namespace = ""
	} else if namespace == "" {
		a.writeErr(w, r, badRequest("namespace is required for %s", res.resource.Kind))
		return
	}
	if res.resource.Namespaced {
		obj.SetNamespace(namespace)
	}

	if err := a.authorize(ctx, res, "create", namespace, obj.GetName(), ""); err != nil {
		a.writeErr(w, r, err)
		return
	}

	created, err := res.clients.Dynamic.
		Resource(res.resource.GVR()).Namespace(namespace).
		Create(ctx, obj, metav1.CreateOptions{DryRun: dryRunList(r)})
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	if !dryRunRequested(r) {
		a.invalidateIfCRD(res)
	}
	writeJSON(w, http.StatusCreated, cluster.TrimForResponse(created))
}

func (a *API) updateResource(w http.ResponseWriter, r *http.Request) {
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
	obj, err := decodeManifest(r)
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	// Trust the URL over the body so a mangled manifest cannot rename or
	// relocate the object being edited.
	obj.SetName(name)
	if res.resource.Namespaced {
		obj.SetNamespace(namespace)
	}

	if err := a.authorize(ctx, res, "update", namespace, name, ""); err != nil {
		a.writeErr(w, r, err)
		return
	}

	updated, err := res.clients.Dynamic.
		Resource(res.resource.GVR()).Namespace(namespace).
		Update(ctx, obj, metav1.UpdateOptions{DryRun: dryRunList(r)})
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	if !dryRunRequested(r) {
		a.invalidateIfCRD(res)
	}
	trimmed := cluster.TrimForResponse(updated)
	if a.writeObjectYAML(w, r, trimmed) {
		return
	}
	writeJSON(w, http.StatusOK, trimmed)
}

// patchTypeFor maps the request's content type onto a Kubernetes patch type.
func patchTypeFor(contentType string) (types.PatchType, error) {
	switch strings.TrimSpace(strings.Split(contentType, ";")[0]) {
	case "application/merge-patch+json", "":
		return types.MergePatchType, nil
	case "application/json-patch+json":
		return types.JSONPatchType, nil
	case "application/strategic-merge-patch+json":
		return types.StrategicMergePatchType, nil
	case "application/apply-patch+yaml":
		return types.ApplyPatchType, nil
	default:
		return "", badRequest("unsupported patch content type %q", contentType)
	}
}

func (a *API) patchResource(w http.ResponseWriter, r *http.Request) {
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
	pt, err := patchTypeFor(r.Header.Get("Content-Type"))
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
	if err != nil {
		a.writeErr(w, r, badRequest("read body: %v", err))
		return
	}
	if err := a.authorize(ctx, res, "patch", namespace, name, ""); err != nil {
		a.writeErr(w, r, err)
		return
	}

	opts := metav1.PatchOptions{DryRun: dryRunList(r)}
	if pt == types.ApplyPatchType {
		opts.FieldManager = "orrery"
		// Force is opt-in: silently stealing fields from another manager is
		// exactly the conflict server-side apply exists to surface.
		if queryBool(r, "force", false) {
			opts.Force = ptr(true)
		}
	}
	updated, err := res.clients.Dynamic.
		Resource(res.resource.GVR()).Namespace(namespace).
		Patch(ctx, name, pt, body, opts)
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, cluster.TrimForResponse(updated))
}

func (a *API) deleteResource(w http.ResponseWriter, r *http.Request) {
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
	if err := a.authorize(ctx, res, "delete", namespace, name, ""); err != nil {
		a.writeErr(w, r, err)
		return
	}

	opts := metav1.DeleteOptions{}
	switch r.URL.Query().Get("propagationPolicy") {
	case "Orphan":
		opts.PropagationPolicy = propagation(metav1.DeletePropagationOrphan)
	case "Background":
		opts.PropagationPolicy = propagation(metav1.DeletePropagationBackground)
	case "Foreground":
		opts.PropagationPolicy = propagation(metav1.DeletePropagationForeground)
	}
	if g := queryInt(r, "gracePeriodSeconds", -1, -1, 86400); g >= 0 {
		g64 := int64(g)
		opts.GracePeriodSeconds = &g64
	}

	if err := res.clients.Dynamic.
		Resource(res.resource.GVR()).Namespace(namespace).
		Delete(ctx, name, opts); err != nil {
		a.writeErr(w, r, err)
		return
	}
	if !dryRunRequested(r) {
		a.invalidateIfCRD(res)
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "name": name, "namespace": namespace})
}

// writeObjectYAML serves an object as YAML when the caller asked for
// format=yaml, and reports whether it did. Shared by the read path and by
// dry-run writes, which the editor diffs as text.
func (a *API) writeObjectYAML(w http.ResponseWriter, r *http.Request, obj *unstructured.Unstructured) bool {
	if r.URL.Query().Get("format") != "yaml" {
		return false
	}
	raw, err := yaml.Marshal(obj.Object)
	if err != nil {
		a.writeErr(w, r, err)
		return true
	}
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(raw)
	return true
}

// dryRunRequested reports whether the caller asked for a dry run. A dry-run
// write is sent to the API server with DryRun=All: it runs admission, mutating
// webhooks, validation and defaulting, then discards the result instead of
// persisting it. That is what makes a truthful diff possible — the object it
// returns is what would actually be stored, defaults and webhook edits
// included, not what the client guessed would be.
//
// Permission is unchanged: a dry run still needs the same verb on the same
// object, so this cannot be used to probe what a write would do without being
// allowed to do it.
func dryRunRequested(r *http.Request) bool {
	v := r.URL.Query().Get("dryRun")
	return v == "true" || v == "All" || v == "1"
}

// dryRunList is the DryRun field value for a write, empty when the caller did
// not ask for one.
func dryRunList(r *http.Request) []string {
	if dryRunRequested(r) {
		return []string{metav1.DryRunAll}
	}
	return nil
}

// invalidateIfCRD drops cached discovery after a CRD changes, so a new custom
// resource becomes browsable immediately instead of after the TTL.
func (a *API) invalidateIfCRD(res *resolved) {
	if res.resource.Kind == "CustomResourceDefinition" {
		res.cluster.Discovery.Invalidate()
	}
}

func propagation(p metav1.DeletionPropagation) *metav1.DeletionPropagation { return &p }

func ptr[T any](v T) *T { return &v }
