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

func slice(u *unstructured.Unstructured, fields ...string) []any {
	v, _, _ := unstructured.NestedSlice(u.Object, fields...)
	return v
}

func strSlice(u *unstructured.Unstructured, fields ...string) []string {
	v, _, _ := unstructured.NestedStringSlice(u.Object, fields...)
	return v
}

func mapOf(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
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
