package api

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// rs builds a ReplicaSet carrying one pod template.
func rs(template map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{"template": template},
	}}
}

func template(containers ...map[string]any) map[string]any {
	list := make([]any, 0, len(containers))
	for _, c := range containers {
		list = append(list, c)
	}
	return map[string]any{
		"metadata": map[string]any{"labels": map[string]any{"app": "web"}},
		"spec":     map[string]any{"containers": list},
	}
}

func container(name, image string, extra map[string]any) map[string]any {
	c := map[string]any{"name": name, "image": image}
	for k, v := range extra {
		c[k] = v
	}
	return c
}

func TestRevisionChangesReportsAnIdenticalTemplate(t *testing.T) {
	// The case that prompted this: two revisions, same image, same age,
	// nothing on screen to choose between them. Rolling back to this one would
	// change nothing at all, and only the server can say so.
	current := rs(template(container("web", "nginx:1.25", nil)))
	target := rs(template(container("web", "nginx:1.25", nil)))

	changes, identical := revisionChanges(current, target)
	if !identical {
		t.Error("two equal templates should report identical")
	}
	if len(changes) != 0 {
		t.Errorf("an identical template has no changes: %v", changes)
	}
}

func TestRevisionChangesNamesTheImageMove(t *testing.T) {
	current := rs(template(container("web", "nginx:1.25", nil)))
	target := rs(template(container("web", "nginx:1.24", nil)))

	changes, identical := revisionChanges(current, target)
	if identical {
		t.Fatal("different images are not identical")
	}
	// Phrased in the direction a rollback travels: what is running now, then
	// what this revision would put back.
	if len(changes) != 1 || changes[0] != "web: nginx:1.25 → nginx:1.24" {
		t.Errorf("changes = %v", changes)
	}
}

func TestRevisionChangesNamesWhatElseDiffers(t *testing.T) {
	current := rs(template(container("web", "nginx:1.25", map[string]any{
		"env":       []any{map[string]any{"name": "MODE", "value": "fast"}},
		"resources": map[string]any{"limits": map[string]any{"cpu": "1"}},
	})))
	target := rs(template(container("web", "nginx:1.25", map[string]any{
		"env": []any{map[string]any{"name": "MODE", "value": "slow"}},
	})))

	changes, identical := revisionChanges(current, target)
	if identical {
		t.Fatal("templates differing in env are not identical")
	}
	got := strings.Join(changes, ",")
	// Named, not diffed: "env" is what tells someone whether to go and look.
	if !strings.Contains(got, "env") || !strings.Contains(got, "resources") {
		t.Errorf("changes = %v, want env and resources named", changes)
	}
	// And the image did not move, so it is not claimed to have.
	if strings.Contains(got, "nginx") {
		t.Errorf("changes = %v, want no image change", changes)
	}
}

func TestRevisionChangesReportsAddedAndRemovedContainers(t *testing.T) {
	current := rs(template(
		container("web", "nginx:1.25", nil),
		container("sidecar", "envoy:1.30", nil),
	))
	target := rs(template(container("web", "nginx:1.25", nil)))

	changes, _ := revisionChanges(current, target)
	joined := strings.Join(changes, ", ")
	if !strings.Contains(joined, "removes container sidecar") {
		t.Errorf("changes = %v, want the dropped sidecar named", changes)
	}

	back, _ := revisionChanges(target, current)
	if !strings.Contains(strings.Join(back, ", "), "adds container sidecar (envoy:1.30)") {
		t.Errorf("reverse changes = %v, want the added sidecar named", back)
	}
}

func TestRevisionChangesSaysNothingItCannotSee(t *testing.T) {
	// A ReplicaSet with no template at all — and the honest answer is neither
	// "identical" nor a list of differences.
	changes, identical := revisionChanges(rs(template()), &unstructured.Unstructured{Object: map[string]any{}})
	if identical || len(changes) != 0 {
		t.Errorf("changes = %v, identical = %v — want nothing claimed", changes, identical)
	}
}

// A difference the field lists do not name still makes the templates
// different, and must not come back as "identical".
func TestRevisionChangesNeverCallsADifferentTemplateIdentical(t *testing.T) {
	current := rs(template(container("web", "nginx:1.25", nil)))
	target := rs(template(container("web", "nginx:1.25", map[string]any{
		"somethingThisServerDoesNotName": "x",
	})))

	changes, identical := revisionChanges(current, target)
	if identical {
		t.Error("an unnamed difference is still a difference")
	}
	if len(changes) != 0 {
		t.Errorf("changes = %v, want none named", changes)
	}
}

func TestSummarizeChangesKeepsTheCountHonest(t *testing.T) {
	changes := []string{"a", "b", "c", "d", "e"}
	if got := summarizeChanges(changes, 5); got != "a, b, c, d, e" {
		t.Errorf("summarizeChanges = %q", got)
	}
	if got := summarizeChanges(changes, 2); got != "a, b, +3 more" {
		t.Errorf("summarizeChanges = %q, want the remainder counted", got)
	}
}

// The Deployment controller writes pod-template-hash onto every template, so
// it differs between any two revisions by construction. Reporting it would put
// "template labels" on every row of the history and mean nothing anyone did.
func TestRevisionChangesIgnoresTheControllersOwnLabel(t *testing.T) {
	withHash := func(hash string) *unstructured.Unstructured {
		return rs(map[string]any{
			"metadata": map[string]any{
				"labels": map[string]any{"app": "web", "pod-template-hash": hash},
			},
			"spec": map[string]any{
				"containers": []any{map[string]any{"name": "web", "image": "nginx:1.25"}},
			},
		})
	}

	changes, identical := revisionChanges(withHash("abc123"), withHash("def456"))
	if len(changes) != 0 {
		t.Errorf("changes = %v, want nothing but the hash to differ", changes)
	}
	// And identical, because the hash is derived from everything else in the
	// template: two revisions equal apart from it would deploy the same pods.
	// Counting it as a difference would make "identical" unreachable — every
	// revision is a different ReplicaSet with a different hash.
	if !identical {
		t.Error("templates equal apart from the controller's hash deploy the same pods")
	}

	// A real label change is still reported.
	real := rs(map[string]any{
		"metadata": map[string]any{
			"labels": map[string]any{"app": "web", "tier": "canary", "pod-template-hash": "def456"},
		},
		"spec": map[string]any{
			"containers": []any{map[string]any{"name": "web", "image": "nginx:1.25"}},
		},
	})
	changes, _ = revisionChanges(withHash("abc123"), real)
	if len(changes) != 1 || changes[0] != "template labels" {
		t.Errorf("changes = %v, want the label change named", changes)
	}
}
