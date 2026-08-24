package cluster

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// lastAppliedAnnotation duplicates an object's entire spec as a JSON string.
// It is pure weight in a cache and is never what the UI renders.
const lastAppliedAnnotation = "kubectl.kubernetes.io/last-applied-configuration"

// TrimForCache reduces an object to what the dashboard actually renders before
// it is stored. On a busy cluster managedFields alone is routinely a third of
// an object's bytes, so this is the difference between a cache that fits in a
// modest pod and one that does not.
//
// An already-trimmed object is returned untouched. That idempotence is not
// cosmetic: with a resync period configured, DeltaFIFO re-runs the transform
// over objects *already stored in the indexer* that concurrent readers are
// ranging over, so any in-place mutation there is a fatal map race. Fresh
// objects are copied before trimming for the same reason — "exclusive
// ownership of freshly decoded objects" stops being true on the resync path.
func TrimForCache(u *unstructured.Unstructured) *unstructured.Unstructured {
	if u == nil {
		return nil
	}
	if !needsTrim(u) {
		return u
	}
	u = u.DeepCopy()
	unstructured.RemoveNestedField(u.Object, "metadata", "managedFields")

	if anns, ok, _ := unstructured.NestedMap(u.Object, "metadata", "annotations"); ok {
		if _, present := anns[lastAppliedAnnotation]; present {
			delete(anns, lastAppliedAnnotation)
			if len(anns) == 0 {
				unstructured.RemoveNestedField(u.Object, "metadata", "annotations")
			} else {
				_ = unstructured.SetNestedMap(u.Object, anns, "metadata", "annotations")
			}
		}
	}

	// Secret payloads are never served from cache. Keeping only the key names
	// lets list views show shape and size without holding every credential in
	// the cluster in the dashboard's heap; opening a secret refetches it live
	// under the viewer's own identity.
	if isSecret(u) {
		redactSecretData(u)
	}
	return u
}

func isSecret(u *unstructured.Unstructured) bool {
	return u.GetKind() == "Secret" && u.GetAPIVersion() == "v1"
}

// needsTrim reports whether TrimForCache would change the object. It runs on
// every event for every cached object, so it must not copy anything.
func needsTrim(u *unstructured.Unstructured) bool {
	if _, ok, _ := unstructured.NestedFieldNoCopy(u.Object, "metadata", "managedFields"); ok {
		return true
	}
	if anns, ok, _ := unstructured.NestedFieldNoCopy(u.Object, "metadata", "annotations"); ok {
		if m, isMap := anns.(map[string]any); isMap {
			if _, present := m[lastAppliedAnnotation]; present {
				return true
			}
		}
	}
	if isSecret(u) {
		for _, field := range []string{"data", "stringData"} {
			if data, ok, _ := unstructured.NestedFieldNoCopy(u.Object, field); ok {
				if m, isMap := data.(map[string]any); isMap && len(m) > 0 {
					return true
				}
			}
		}
	}
	return false
}

// redactSecretData replaces each value with its byte length, preserving the
// key set the UI lists.
func redactSecretData(u *unstructured.Unstructured) {
	for _, field := range []string{"data", "stringData"} {
		data, ok, _ := unstructured.NestedMap(u.Object, field)
		if !ok || len(data) == 0 {
			continue
		}
		sizes := make(map[string]any, len(data))
		for k, v := range data {
			s, _ := v.(string)
			if field == "data" {
				// base64 expands by 4/3; report the decoded size.
				sizes[k] = int64(len(s) * 3 / 4)
			} else {
				// stringData is plaintext; its length is its size.
				sizes[k] = int64(len(s))
			}
		}
		unstructured.RemoveNestedField(u.Object, field)
		_ = unstructured.SetNestedMap(u.Object, sizes, "clusterlens.io/redacted", field)
	}
}

// TrimForResponse strips fields that are noise in an API response. Unlike
// TrimForCache it copies, because the input may be a shared cache object that
// other requests are reading concurrently.
func TrimForResponse(u *unstructured.Unstructured) *unstructured.Unstructured {
	if u == nil {
		return nil
	}
	out := u.DeepCopy()
	unstructured.RemoveNestedField(out.Object, "metadata", "managedFields")
	return out
}
