package api

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// A Deployment as one actually arrives from a cluster that anything has ever
// applied to: carrying the bookkeeping the API server writes and the reader
// did not.
//
// The fixtures elsewhere in this package are hand-built and carry none of it,
// which is why nothing here noticed. managedFields in particular is not a
// small block — one entry per controller that has touched the object, each
// listing every field it owns — and on a Deployment managed by Helm or Argo it
// runs to hundreds of lines ahead of the spec.
func yamlViewObject() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata": map[string]any{
			"name":              "web",
			"namespace":         "demo",
			"uid":               "uid-web",
			"resourceVersion":   "12345",
			"generation":        int64(4),
			"creationTimestamp": "2024-01-01T00:00:00Z",
			"labels":            map[string]any{"app": "web"},
			"managedFields": []any{
				map[string]any{
					"manager":    "kubectl-client-side-apply",
					"operation":  "Update",
					"apiVersion": "apps/v1",
					"time":       "2024-01-01T00:00:00Z",
					"fieldsType": "FieldsV1",
					"fieldsV1": map[string]any{
						"f:spec": map[string]any{
							"f:replicas": map[string]any{},
							"f:template": map[string]any{"f:spec": map[string]any{"f:containers": map[string]any{}}},
						},
					},
				},
				map[string]any{
					"manager":     "kube-controller-manager",
					"operation":   "Update",
					"subresource": "status",
					"apiVersion":  "apps/v1",
					"fieldsV1":    map[string]any{"f:status": map[string]any{"f:replicas": map[string]any{}}},
				},
			},
		},
		"spec":   map[string]any{"replicas": int64(2)},
		"status": map[string]any{"replicas": int64(2)},
	}}
}

// The YAML tab is where someone reads an object to understand it, and where
// they edit it. The editor says outright that server-managed fields are
// stripped from the view; that has to be true.
func TestYAMLViewOmitsManagedFields(t *testing.T) {
	out := string(mustYAML(t, yamlViewObject()))

	for _, unwanted := range []string{"managedFields", "fieldsV1", "kubectl-client-side-apply", "f:replicas"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("the YAML view still contains %q:\n%s", unwanted, out)
		}
	}
}

// Stripping the noise must not strip the object. Everything a reader came for
// stays, including the identifying metadata an editor needs to apply.
func TestYAMLViewKeepsTheObject(t *testing.T) {
	out := string(mustYAML(t, yamlViewObject()))

	for _, wanted := range []string{
		"apiVersion: apps/v1",
		"kind: Deployment",
		"name: web",
		"namespace: demo",
		"app: web",
		"replicas: 2",
		"creationTimestamp:",
		"resourceVersion:",
	} {
		if !strings.Contains(out, wanted) {
			t.Errorf("the YAML view lost %q:\n%s", wanted, out)
		}
	}
}

// The view must not alter the object it was given: the same pointer is served
// to the JSON path and held in the informer cache.
func TestYAMLViewDoesNotMutateTheCachedObject(t *testing.T) {
	obj := yamlViewObject()
	_ = mustYAML(t, obj)

	meta, _ := obj.Object["metadata"].(map[string]any)
	if _, still := meta["managedFields"]; !still {
		t.Fatal("yamlForView removed managedFields from the object it was handed; " +
			"that object is the one in the informer cache")
	}
}

func TestYAMLViewHandlesAnObjectWithoutMetadata(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{"kind": "Weird"}}
	if out := string(mustYAML(t, obj)); !strings.Contains(out, "kind: Weird") {
		t.Errorf("got %q", out)
	}
}

func mustYAML(t *testing.T, obj *unstructured.Unstructured) []byte {
	t.Helper()
	raw, err := yamlForView(obj)
	if err != nil {
		t.Fatalf("yamlForView: %v", err)
	}
	return raw
}
