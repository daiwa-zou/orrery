package cluster

import (
	"encoding/base64"
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

	obj = TrimForCache(obj)

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

	obj = TrimForCache(obj)

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

	obj = TrimForCache(obj)

	if _, found, _ := unstructured.NestedFieldNoCopy(obj.Object, "data"); found {
		t.Fatal("secret data must never reach the shared cache")
	}
	sizes, found, _ := unstructured.NestedMap(obj.Object, "orrery.io/redacted", "data")
	if !found {
		t.Fatal("expected redacted key metadata to be recorded")
	}
	if len(sizes) != 2 {
		t.Errorf("expected both key names preserved, got %v", sizes)
	}
	if _, ok := sizes["token"]; !ok {
		t.Error("key names should survive so list views can show them")
	}
	// The size is what the console labels each key with, so it has to be the
	// decoded length. "c3VwZXItc2VjcmV0" is twelve bytes and "eA==" is one;
	// counting the padding as data reported them as twelve and three.
	if sizes["token"] != int64(len("super-secret")) {
		t.Errorf("token size = %v, want %d", sizes["token"], len("super-secret"))
	}
	if sizes["other"] != int64(1) {
		t.Errorf("other size = %v, want 1", sizes["other"])
	}
}

// base64 encodes three bytes as four characters and pads the final group with
// "=", so the encoded length overstates the value by however many pad
// characters there are — up to two bytes on every secret in the cluster.
func TestBase64DecodedLen(t *testing.T) {
	cases := []struct {
		plain string
		b64   string
	}{
		{"", ""},
		{"x", "eA=="},   // two pad characters
		{"xy", "eHk="},  // one
		{"xyz", "eHl6"}, // none
		{"super-secret", "c3VwZXItc2VjcmV0"},
		{"SUPERSECRETVALUE123", "U1VQRVJTRUNSRVRWQUxVRTEyMw=="},
		{"abcdefghijklmnop", "YWJjZGVmZ2hpamtsbW5vcA=="},
	}
	for _, tc := range cases {
		// The fixture itself has to be right, or the test proves nothing.
		if got := base64.StdEncoding.EncodeToString([]byte(tc.plain)); got != tc.b64 {
			t.Fatalf("fixture wrong: %q encodes to %q, not %q", tc.plain, got, tc.b64)
		}
		if got := base64DecodedLen(tc.b64); got != int64(len(tc.plain)) {
			t.Errorf("base64DecodedLen(%q) = %d, want %d", tc.b64, got, len(tc.plain))
		}
	}
}

// The transform runs on whatever the API server sent, which is not always what
// this expects. Nothing here may panic or report a negative size.
func TestBase64DecodedLenSurvivesMalformedInput(t *testing.T) {
	for _, in := range []string{"=", "==", "===", "a", "ab", "abc", "!!!!", "a===b"} {
		if got := base64DecodedLen(in); got < 0 {
			t.Errorf("base64DecodedLen(%q) = %d, want a non-negative size", in, got)
		}
	}
}

func TestTrimForCacheLeavesNonSecretsAlone(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]any{"name": "c"},
		"data":       map[string]any{"mode": "production"},
	}}

	trimmed := TrimForCache(obj)

	// Nothing to trim means the very same object comes back: with a resync
	// period configured the transform re-runs over objects already stored in
	// the indexer, and copying (or mutating) them there would be wrong.
	if trimmed != obj {
		t.Error("an already-trimmed object should be returned unchanged, not copied")
	}
	data, found, _ := unstructured.NestedMap(trimmed.Object, "data")
	if !found || data["mode"] != "production" {
		t.Errorf("ConfigMap data should be untouched, got %v", data)
	}
}

func TestTrimForCacheDoesNotMutateInput(t *testing.T) {
	// The input may be an object already stored in the indexer (resync path),
	// so trimming must never write through the pointer it was handed.
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata": map[string]any{
			"name":          "s",
			"managedFields": []any{map[string]any{"manager": "kubectl"}},
		},
		"data": map[string]any{"token": "c3VwZXItc2VjcmV0"},
	}}

	trimmed := TrimForCache(obj)

	if trimmed == obj {
		t.Fatal("a trimmed object must be a copy")
	}
	if _, found, _ := unstructured.NestedFieldNoCopy(obj.Object, "metadata", "managedFields"); !found {
		t.Error("the input object was mutated in place")
	}
	if _, found, _ := unstructured.NestedFieldNoCopy(obj.Object, "data"); !found {
		t.Error("the input object's secret data was removed in place")
	}
	if _, found, _ := unstructured.NestedFieldNoCopy(trimmed.Object, "data"); found {
		t.Error("the trimmed copy still carries secret data")
	}
	// Trimming the result again must be a no-op returning the same pointer.
	if again := TrimForCache(trimmed); again != trimmed {
		t.Error("TrimForCache is not idempotent")
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
