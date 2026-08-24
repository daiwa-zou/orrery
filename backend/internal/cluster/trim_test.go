package cluster

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestTrimForCacheRemovesManagedFields(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata": map[string]any{
			"name": "c",
			"managedFields": []any{
				map[string]any{"manager": "kubectl", "operation": "Apply"},
			},
			"annotations": map[string]any{
				lastAppliedAnnotation: `{"apiVersion":"v1","kind":"ConfigMap"}`,
				"keep-me":             "yes",
			},
		},
	}}

	TrimForCache(obj)

	if _, found, _ := unstructured.NestedFieldNoCopy(obj.Object, "metadata", "managedFields"); found {
		t.Error("managedFields survived the trim")
	}
	anns := obj.GetAnnotations()
	if _, found := anns[lastAppliedAnnotation]; found {
		t.Error("last-applied-configuration survived the trim")
	}
	if anns["keep-me"] != "yes" {
		t.Errorf("unrelated annotations were dropped: %v", anns)
	}
}

func TestTrimForCacheDropsEmptyAnnotationMap(t *testing.T) {
	// If the only annotation was the one we strip, leaving an empty map behind
	// makes every object carry a pointless allocation.
	obj := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{
			"name":        "c",
			"annotations": map[string]any{lastAppliedAnnotation: "{}"},
		},
	}}

	TrimForCache(obj)

	if _, found, _ := unstructured.NestedFieldNoCopy(obj.Object, "metadata", "annotations"); found {
		t.Error("expected the now-empty annotations map to be removed")
	}
}

func TestTrimForCacheRedactsSecretValues(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata":   map[string]any{"name": "s", "namespace": "n"},
		"type":       "Opaque",
		// "c3VwZXItc2VjcmV0" is base64 for "super-secret".
		"data": map[string]any{"token": "c3VwZXItc2VjcmV0", "other": "eA=="},
	}}

	TrimForCache(obj)

	if _, found, _ := unstructured.NestedFieldNoCopy(obj.Object, "data"); found {
		t.Fatal("secret data must never reach the shared cache")
	}
	sizes, found, _ := unstructured.NestedMap(obj.Object, "clusterlens.io/redacted", "data")
	if !found {
		t.Fatal("expected redacted key metadata to be recorded")
	}
	if len(sizes) != 2 {
		t.Errorf("expected both key names preserved, got %v", sizes)
	}
	if _, ok := sizes["token"]; !ok {
		t.Error("key names should survive so list views can show them")
	}
}

func TestTrimForCacheLeavesNonSecretsAlone(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]any{"name": "c"},
		"data":       map[string]any{"mode": "production"},
	}}

	TrimForCache(obj)

	data, found, _ := unstructured.NestedMap(obj.Object, "data")
	if !found || data["mode"] != "production" {
		t.Errorf("ConfigMap data should be untouched, got %v", data)
	}
}

func TestTrimForResponseDoesNotMutateInput(t *testing.T) {
	// Response trimming runs on objects that other requests may be reading
	// out of the shared cache at the same instant.
	original := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{
			"name":          "c",
			"managedFields": []any{map[string]any{"manager": "kubectl"}},
		},
	}}

	trimmed := TrimForResponse(original)

	if _, found, _ := unstructured.NestedFieldNoCopy(trimmed.Object, "metadata", "managedFields"); found {
		t.Error("response copy should not carry managedFields")
	}
	if _, found, _ := unstructured.NestedFieldNoCopy(original.Object, "metadata", "managedFields"); !found {
		t.Error("the cached object was mutated; other readers would see a torn object")
	}
}

func TestTrimHandlesNil(t *testing.T) {
	if TrimForCache(nil) != nil || TrimForResponse(nil) != nil {
		t.Error("nil should pass through both trims")
	}
}
