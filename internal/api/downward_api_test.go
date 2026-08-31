package api

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The env panel resolves a container's environment the way the kubelet would,
// because the point of the panel is to show what the process actually sees. A
// path the kubelet accepts and this does not is reported to the reader as an
// error against their pod, so the two have to agree on the whole set.

func downwardPod() *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "web-0",
			Namespace:   "team-a",
			UID:         "8b1f-uid",
			Labels:      map[string]string{"tier": "front", "app": "web"},
			Annotations: map[string]string{"note": "two\nlines", "owner": "sre"},
		},
		Spec: corev1.PodSpec{
			NodeName:           "node-3",
			ServiceAccountName: "web-sa",
		},
		Status: corev1.PodStatus{
			HostIP: "10.0.0.3",
			PodIP:  "10.1.2.3",
			PodIPs: []corev1.PodIP{{IP: "10.1.2.3"}, {IP: "fd00::3"}},
		},
	}
}

func TestPodFieldValueResolvesEveryPathTheKubeletAccepts(t *testing.T) {
	pod := downwardPod()
	for _, c := range []struct{ path, want string }{
		{"metadata.name", "web-0"},
		{"metadata.namespace", "team-a"},
		{"metadata.uid", "8b1f-uid"},
		{"spec.nodeName", "node-3"},
		{"spec.serviceAccountName", "web-sa"},
		{"status.hostIP", "10.0.0.3"},
		{"status.podIP", "10.1.2.3"},
		{"status.podIPs", "10.1.2.3,fd00::3"},
		{"metadata.labels['app']", "web"},
		{"metadata.annotations['owner']", "sre"},
	} {
		got, err := podFieldValue(pod, c.path)
		if err != nil {
			t.Errorf("podFieldValue(%q) errored: %v", c.path, err)
			continue
		}
		if got != c.want {
			t.Errorf("podFieldValue(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}

// An unsubscripted metadata.labels or metadata.annotations is the whole map,
// rendered as the kubelet renders it: sorted `key="value"` lines. Before this
// the panel called a valid pod spec an unsupported fieldRef, which describes a
// perfectly ordinary container as misconfigured.
func TestPodFieldValueRendersAWholeMap(t *testing.T) {
	pod := downwardPod()

	labels, err := podFieldValue(pod, "metadata.labels")
	if err != nil {
		t.Fatalf("metadata.labels errored: %v", err)
	}
	if want := "app=\"web\"\ntier=\"front\""; labels != want {
		t.Errorf("metadata.labels = %q, want %q", labels, want)
	}

	annotations, err := podFieldValue(pod, "metadata.annotations")
	if err != nil {
		t.Fatalf("metadata.annotations errored: %v", err)
	}
	// The quoting is the load-bearing part: "note" holds a newline, and
	// unquoted it would be indistinguishable from a third annotation.
	if want := "note=\"two\\nlines\"\nowner=\"sre\""; annotations != want {
		t.Errorf("metadata.annotations = %q, want %q", annotations, want)
	}
	if strings.Count(annotations, "\n") != 1 {
		t.Errorf("metadata.annotations = %q: a value's newline leaked into the line structure",
			annotations)
	}
}

func TestPodFieldValueOnAnEmptyMap(t *testing.T) {
	pod := &corev1.Pod{}
	for _, path := range []string{"metadata.labels", "metadata.annotations"} {
		got, err := podFieldValue(pod, path)
		if err != nil || got != "" {
			t.Errorf("podFieldValue(%q) = %q, %v; want \"\", nil", path, got, err)
		}
	}
}

// A key that is not set is empty, not an error: the kubelet injects an empty
// variable, and calling it an error would report the pod as broken.
func TestPodFieldValueAbsentSubscriptIsEmpty(t *testing.T) {
	pod := downwardPod()
	for _, path := range []string{"metadata.labels['nope']", "metadata.annotations['nope']"} {
		got, err := podFieldValue(pod, path)
		if err != nil || got != "" {
			t.Errorf("podFieldValue(%q) = %q, %v; want \"\", nil", path, got, err)
		}
	}
}

// A path this server cannot resolve says so, rather than resolving to the
// empty string — an env var reported as empty when it is really unknown is the
// difference between "the process sees nothing here" and "we did not look".
func TestPodFieldValueRejectsWhatItCannotResolve(t *testing.T) {
	pod := downwardPod()
	for _, path := range []string{
		"spec.containers[0].name",
		"status.qosClass",
		"metadata.labels[\"app\"]", // double quotes: not the kubelet's syntax
		"metadata.labels['app'",
		"",
	} {
		got, err := podFieldValue(pod, path)
		if err == nil {
			t.Errorf("podFieldValue(%q) = %q with no error, want an error", path, got)
		}
	}
}

func TestSubscript(t *testing.T) {
	for _, c := range []struct {
		path, prefix, want string
		ok                 bool
	}{
		{"metadata.labels['app']", "metadata.labels", "app", true},
		{"metadata.labels['a.b/c']", "metadata.labels", "a.b/c", true},
		{"metadata.labels['']", "metadata.labels", "", true},
		{"metadata.labels", "metadata.labels", "", false},
		{"metadata.annotations['x']", "metadata.labels", "", false},
		{"metadata.labels['x'", "metadata.labels", "", false},
	} {
		got, ok := subscript(c.path, c.prefix)
		if got != c.want || ok != c.ok {
			t.Errorf("subscript(%q, %q) = %q, %v; want %q, %v",
				c.path, c.prefix, got, ok, c.want, c.ok)
		}
	}
}

func resourceContainer() corev1.Container {
	return corev1.Container{
		Name: "app",
		Resources: corev1.ResourceRequirements{
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1500m"),
				corev1.ResourceMemory: resource.MustParse("512Mi"),
			},
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("250m"),
				corev1.ResourceMemory: resource.MustParse("128Mi"),
			},
		},
	}
}

func TestContainerResourceValue(t *testing.T) {
	c := resourceContainer()
	for _, tc := range []struct {
		name, res, divisor, want string
	}{
		// No divisor is a divisor of one, and the kubelet rounds up: 1.5 CPUs
		// becomes 2, because a whole-core count that rounded down would say a
		// container limited to 1.5 cores may use one.
		{"cpu limit rounds up to whole cores", "limits.cpu", "", "2"},
		{"cpu request rounds up to whole cores", "requests.cpu", "", "1"},
		{"cpu limit in millicores", "limits.cpu", "1m", "1500"},
		{"cpu request in millicores", "requests.cpu", "1m", "250"},
		{"memory limit in bytes", "limits.memory", "", "536870912"},
		{"memory limit in Mi", "limits.memory", "1Mi", "512"},
		{"memory request in Mi", "requests.memory", "1Mi", "128"},
		{"memory limit in Ki", "limits.memory", "1Ki", "524288"},
		// A divisor larger than the quantity still rounds up, so a container
		// with a limit is never reported as having none.
		{"memory limit in Gi rounds up", "limits.memory", "1Gi", "1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ref := &corev1.ResourceFieldSelector{Resource: tc.res}
			if tc.divisor != "" {
				ref.Divisor = resource.MustParse(tc.divisor)
			}
			got, err := containerResourceValue(c, ref)
			if err != nil {
				t.Fatalf("containerResourceValue(%q, %q) errored: %v", tc.res, tc.divisor, err)
			}
			if got != tc.want {
				t.Errorf("containerResourceValue(%q, %q) = %q, want %q",
					tc.res, tc.divisor, got, tc.want)
			}
		})
	}
}

// The kubelet substitutes the node's allocatable when a container declares no
// limit. The node is not in hand here, so the omission is reported rather than
// guessed at — a number invented from nothing would be read as the container's
// own declaration.
func TestContainerResourceValueReportsWhatItCannotAnswer(t *testing.T) {
	bare := corev1.Container{Name: "app"}
	for _, tc := range []struct {
		name    string
		c       corev1.Container
		ref     corev1.ResourceFieldSelector
		wantSub string
	}{
		{"no limit declared", bare,
			corev1.ResourceFieldSelector{Resource: "limits.cpu"}, "declares no"},
		{"no request declared", bare,
			corev1.ResourceFieldSelector{Resource: "requests.memory"}, "declares no"},
		{"resource absent from a declared list", resourceContainer(),
			corev1.ResourceFieldSelector{Resource: "limits.ephemeral-storage"}, "declares no"},
		{"unqualified resource", resourceContainer(),
			corev1.ResourceFieldSelector{Resource: "cpu"}, "unsupported"},
		{"neither limits nor requests", resourceContainer(),
			corev1.ResourceFieldSelector{Resource: "capacity.cpu"}, "unsupported"},
		{"non-positive divisor", resourceContainer(),
			corev1.ResourceFieldSelector{
				Resource: "limits.memory",
				Divisor:  resource.MustParse("-1"),
			}, "bad divisor"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ref := tc.ref
			got, err := containerResourceValue(tc.c, &ref)
			if err == nil {
				t.Fatalf("containerResourceValue(%q) = %q with no error, want one",
					tc.ref.Resource, got)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q does not say %q", err, tc.wantSub)
			}
		})
	}
}
