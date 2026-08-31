package api

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestDebugSuffixIsUsableAsAContainerName(t *testing.T) {
	seen := map[string]bool{}
	for range 200 {
		s, err := debugSuffix()
		if err != nil {
			t.Fatalf("debugSuffix: %v", err)
		}
		if len(s) != 5 {
			t.Fatalf("suffix %q is %d chars, want 5", s, len(s))
		}
		// A container name must be a DNS label: lowercase alphanumerics and
		// dashes. The alphabet also drops vowels and lookalikes so a generated
		// name is neither an accidental word nor ambiguous when read aloud.
		for _, r := range s {
			if !strings.ContainsRune("bcdfghjklmnpqrstvwxz2456789", r) {
				t.Fatalf("suffix %q contains %q, which is outside the alphabet", s, r)
			}
		}
		seen[s] = true
	}
	// Collisions are possible but 200 draws from 27^5 should not all coincide;
	// this catches a suffix that is accidentally constant.
	if len(seen) < 150 {
		t.Errorf("only %d distinct suffixes in 200 draws — not random enough", len(seen))
	}
}

func TestHasContainerCoversInitContainers(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "demo"},
		Spec: corev1.PodSpec{
			Containers:     []corev1.Container{{Name: "app"}, {Name: "sidecar"}},
			InitContainers: []corev1.Container{{Name: "migrate"}},
		},
	}

	for _, name := range []string{"app", "sidecar", "migrate"} {
		if !hasContainer(pod, name) {
			t.Errorf("hasContainer(%q) = false, want true", name)
		}
	}
	if hasContainer(pod, "absent") {
		t.Error("hasContainer reported a container that is not in the spec")
	}

	// The error message names the real containers so a typo is self-correcting
	// — which means it has to name every container the check above accepts.
	// Listing only Spec.Containers answered a misspelt init container with a
	// parenthesis that omitted init containers entirely, read as "those cannot
	// be targeted" rather than "you have spelt this one wrong".
	if got, want := containerNames(pod), "app, sidecar, migrate"; got != want {
		t.Errorf("containerNames = %q, want %q", got, want)
	}
	for _, name := range targetableContainers(pod) {
		if !hasContainer(pod, name) {
			t.Errorf("the refusal offers %q as a choice that hasContainer rejects", name)
		}
	}
}
