package api

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
)

// unstructuredLabels and objectFields exist to let selector matching read an
// object without copying it. Both are therefore adapters whose only job is to
// answer exactly what the maps they stand in for would have answered — and a
// no-copy view that answers differently is worse than the copy it replaced,
// because it silently changes which objects a filter selects.

func TestLabelsOfReadsThroughToTheObject(t *testing.T) {
	o := obj("p", "d", map[string]string{"app": "web", "tier": ""}, nil)
	l := labelsOf(o)

	if !l.Has("app") {
		t.Error(`Has("app") = false, want true`)
	}
	if got := l.Get("app"); got != "web" {
		t.Errorf(`Get("app") = %q, want "web"`, got)
	}
	// An empty value is a label that is set. Reporting it absent would make
	// `app=` and `!app` mean the same thing, which they do not.
	if !l.Has("tier") {
		t.Error(`Has("tier") = false, want true: an empty label is still set`)
	}
	if got, ok := l.Lookup("tier"); got != "" || !ok {
		t.Errorf(`Lookup("tier") = %q, %v; want "", true`, got, ok)
	}
	if l.Has("absent") {
		t.Error(`Has("absent") = true, want false`)
	}
	if got := l.Get("absent"); got != "" {
		t.Errorf(`Get("absent") = %q, want ""`, got)
	}
	if got, ok := l.Lookup("absent"); got != "" || ok {
		t.Errorf(`Lookup("absent") = %q, %v; want "", false`, got, ok)
	}
}

// An object with no labels at all gives a nil view, which every method still
// has to answer for — this is the common case for cluster-scoped objects, and
// a panic here would take down any list filtered by label selector.
func TestLabelsOfWithoutLabels(t *testing.T) {
	for _, o := range []*unstructured.Unstructured{
		obj("p", "d", nil, nil),
		{Object: map[string]any{"metadata": map[string]any{"labels": "not-a-map"}}},
		{Object: map[string]any{}},
	} {
		l := labelsOf(o)
		if l.Has("app") {
			t.Errorf("Has on %v reported a label", o.Object)
		}
		if got := l.Get("app"); got != "" {
			t.Errorf("Get on %v = %q, want \"\"", o.Object, got)
		}
		if _, ok := l.Lookup("app"); ok {
			t.Errorf("Lookup on %v found a label", o.Object)
		}
	}
}

// A label whose value is not a string cannot come from the API server, but the
// view reads raw JSON and must not hand a caller something that is not one.
func TestLabelsOfIgnoresNonStringValues(t *testing.T) {
	o := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"labels": map[string]any{"n": int64(3)}},
	}}
	l := labelsOf(o)
	if !l.Has("n") {
		t.Error(`Has("n") = false, want true: the key is present`)
	}
	if got := l.Get("n"); got != "" {
		t.Errorf(`Get("n") = %q, want "": a non-string label has no string value`, got)
	}
}

// The no-copy view has to select the same objects a labels.Set would.
func TestLabelSelectorMatchesThroughTheView(t *testing.T) {
	o := obj("p", "d", map[string]string{"app": "web"}, nil)
	for _, c := range []struct {
		sel  string
		want bool
	}{
		{"app=web", true},
		{"app=api", false},
		{"app", true},
		{"!app", false},
		{"app in (web,api)", true},
		{"app notin (web)", false},
		{"other=x", false},
	} {
		sel, err := labels.Parse(c.sel)
		if err != nil {
			t.Fatalf("labels.Parse(%q): %v", c.sel, err)
		}
		if got := sel.Matches(labelsOf(o)); got != c.want {
			t.Errorf("%q matched %v, want %v", c.sel, got, c.want)
		}
	}
}

// fieldObject carries a distinct value at every path objectFields knows, so a
// key that the parser accepts but the matcher has forgotten reads back empty.
func fieldObject() (*unstructured.Unstructured, map[string]string) {
	want := map[string]string{
		"metadata.name":            "obj-1",
		"metadata.namespace":       "team-a",
		"status.phase":             "Running",
		"spec.nodeName":            "node-7",
		"type":                     "Warning",
		"involvedObject.name":      "web-abc",
		"involvedObject.kind":      "Pod",
		"involvedObject.namespace": "team-b",
		"involvedObject.uid":       "0f3c",
	}
	return obj(want["metadata.name"], want["metadata.namespace"], nil, map[string]any{
		"type":   want["type"],
		"status": map[string]any{"phase": want["status.phase"]},
		"spec":   map[string]any{"nodeName": want["spec.nodeName"]},
		"involvedObject": map[string]any{
			"name":      want["involvedObject.name"],
			"kind":      want["involvedObject.kind"],
			"namespace": want["involvedObject.namespace"],
			"uid":       want["involvedObject.uid"],
		},
	}), want
}

// The header on objectFields states the invariant this pins: supportedFieldKeys
// is what the parser accepts and objectFields.Get is what the matcher reads, and
// a key in the first that is missing from the second matches nothing at all —
// which a reader sees as "there are no such objects" rather than as the
// unsupported filter it is. That is the absence-read-as-an-answer this codebase
// keeps having to fix, so the two lists are checked against each other rather
// than trusted to stay in step.
func TestEverySupportedFieldKeyIsReadable(t *testing.T) {
	o, want := fieldObject()
	f := objectFields{o}

	for key := range supportedFieldKeys {
		w, ok := want[key]
		if !ok {
			t.Errorf("supportedFieldKeys accepts %q but this test has no value for it; "+
				"add one so the key is actually exercised", key)
			continue
		}
		if got := f.Get(key); got != w {
			t.Errorf("Get(%q) = %q, want %q: the parser accepts this key and the "+
				"matcher cannot read it, so it selects nothing", key, got, w)
		}
		if !f.Has(key) {
			t.Errorf("Has(%q) = false on an object that carries it", key)
		}
	}

	// And the other direction: a key the matcher reads but the parser rejects
	// is a filter nobody can use.
	for key := range want {
		if !supportedFieldKeys[key] {
			t.Errorf("objectFields reads %q but supportedFieldKeys rejects it", key)
		}
	}
}

func TestObjectFieldsUnknownKey(t *testing.T) {
	o, _ := fieldObject()
	f := objectFields{o}
	if got := f.Get("spec.containers"); got != "" {
		t.Errorf(`Get("spec.containers") = %q, want ""`, got)
	}
	if f.Has("spec.containers") {
		t.Error(`Has("spec.containers") = true, want false`)
	}
}

// Identity is always present even when empty, which is what makes
// "metadata.namespace=" a usable way to ask for cluster-scoped objects rather
// than a selector over a field that is not there. Every other field is present
// only when it has a value.
func TestObjectFieldsHasOnAnEmptyObject(t *testing.T) {
	f := objectFields{&unstructured.Unstructured{Object: map[string]any{}}}
	for _, key := range []string{"metadata.name", "metadata.namespace"} {
		if !f.Has(key) {
			t.Errorf("Has(%q) = false, want true even when empty", key)
		}
	}
	for _, key := range []string{"status.phase", "spec.nodeName", "type", "involvedObject.name"} {
		if f.Has(key) {
			t.Errorf("Has(%q) = true on an object that does not carry it", key)
		}
		if got := f.Get(key); got != "" {
			t.Errorf("Get(%q) = %q, want \"\"", key, got)
		}
	}
}

// Cluster-scoped objects answer the empty namespace, so `metadata.namespace=`
// selects them and only them.
func TestFieldSelectorMatchesThroughTheView(t *testing.T) {
	namespaced, _ := fieldObject()
	clusterScoped := obj("node-7", "", nil, nil)

	for _, c := range []struct {
		sel              string
		wantNS, wantRoot bool
	}{
		{"metadata.namespace=team-a", true, false},
		{"metadata.namespace=", false, true},
		{"metadata.namespace!=team-a", false, true},
		{"metadata.name=obj-1", true, false},
		{"status.phase=Running", true, false},
		{"status.phase!=Running", false, true},
		{"involvedObject.kind=Pod,involvedObject.name=web-abc", true, false},
	} {
		sel, err := fields.ParseSelector(c.sel)
		if err != nil {
			t.Fatalf("fields.ParseSelector(%q): %v", c.sel, err)
		}
		if got := sel.Matches(objectFields{namespaced}); got != c.wantNS {
			t.Errorf("%q matched the namespaced object %v, want %v", c.sel, got, c.wantNS)
		}
		if got := sel.Matches(objectFields{clusterScoped}); got != c.wantRoot {
			t.Errorf("%q matched the cluster-scoped object %v, want %v", c.sel, got, c.wantRoot)
		}
	}
}
