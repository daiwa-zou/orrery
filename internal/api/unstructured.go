package api

import (
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Small typed accessors over unstructured objects. The generated helpers in
// apimachinery return (value, found, err) triples, which makes column
// extractors unreadable; these collapse that to a zero value.

func str(u *unstructured.Unstructured, fields ...string) string {
	v, _, _ := unstructured.NestedString(u.Object, fields...)
	return v
}

func i64(u *unstructured.Unstructured, fields ...string) int64 {
	v, _, _ := unstructured.NestedInt64(u.Object, fields...)
	return v
}

func i64ok(u *unstructured.Unstructured, fields ...string) (int64, bool) {
	v, ok, _ := unstructured.NestedInt64(u.Object, fields...)
	return v, ok
}

func boolean(u *unstructured.Unstructured, fields ...string) bool {
	v, _, _ := unstructured.NestedBool(u.Object, fields...)
	return v
}

// slice reads a list field without copying it.
//
// unstructured.NestedSlice deep-copies everything it hands back — for
// spec.containers that is each container's whole spec: env, mounts, probes,
// resources, the lot — and every caller in this package iterates the result
// read-only and drops it. The pod projector reads two of these per row, so a
// page of a thousand pods copied two thousand container lists to count how
// many were ready; the overview walks spec.containers for every pod in the
// cluster to sum their requests.
//
// The elements belong to the shared cache and are not ours to change, which is
// exactly what makes not copying them safe. It is the same bargain labelsOf
// and objectFields strike a few files over: a read-only walk is not a reason
// to own anything.
func slice(u *unstructured.Unstructured, fields ...string) []any {
	v, _, _ := unstructured.NestedFieldNoCopy(u.Object, fields...)
	s, _ := v.([]any)
	return s
}

func strSlice(u *unstructured.Unstructured, fields ...string) []string {
	v, _, _ := unstructured.NestedStringSlice(u.Object, fields...)
	return v
}

func mapOf(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// mapAt reads an object field without copying it, reporting whether the field
// was there at all.
//
// unstructured.NestedMap deep-copies, for the same reason NestedSlice does and
// with the same consequence: a ConfigMap's data is up to a megabyte, and the
// projector that reads it only ever wanted the number of keys. A page of fifty
// copied fifty megabytes to produce fifty integers, and sorting on that column
// did it for every object in the namespace rather than for the page.
//
// found is separate from the map being empty, because "no such field" and "a
// field holding nothing" are different answers — the secret projector tells
// them apart to know whether it is looking at a redacted object.
func mapAt(u *unstructured.Unstructured, fields ...string) (m map[string]any, found bool) {
	v, ok, _ := unstructured.NestedFieldNoCopy(u.Object, fields...)
	if !ok {
		return nil, false
	}
	m, ok = v.(map[string]any)
	return m, ok
}

func mstr(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

func mint(m map[string]any, key string) int64 {
	switch v := m[key].(type) {
	case int64:
		return v
	case float64:
		return int64(v)
	}
	return 0
}

func mbool(m map[string]any, key string) bool {
	b, _ := m[key].(bool)
	return b
}

// containerImages lists the images of a pod template or pod spec.
func containerImages(u *unstructured.Unstructured, specPath ...string) []string {
	var out []string
	for _, field := range []string{"initContainers", "containers"} {
		path := append(append([]string{}, specPath...), field)
		for _, c := range slice(u, path...) {
			if img := mstr(mapOf(c), "image"); img != "" {
				out = append(out, img)
			}
		}
	}
	return out
}

// joinLimit renders a list compactly, summarising the tail so a pod with
// forty ports does not blow out a table cell.
func joinLimit(items []string, max int) string {
	if len(items) == 0 {
		return ""
	}
	if len(items) <= max {
		return strings.Join(items, ", ")
	}
	return strings.Join(items[:max], ", ") + ", +" + strconv.Itoa(len(items)-max) + " more"
}
