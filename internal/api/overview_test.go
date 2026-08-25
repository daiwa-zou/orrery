package api

import (
	"errors"
	"fmt"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestCountSummaryMark(t *testing.T) {
	// A denied read and a broken informer must not look the same in the UI, or
	// operators chase RBAC problems that do not exist.
	var denied countSummary
	denied.mark(&forbiddenError{verb: "list", resource: "pods"})
	if !denied.Forbidden || denied.Unavailable {
		t.Errorf("forbidden marked as %+v", denied)
	}

	var broken countSummary
	broken.mark(errors.New("informer not synced"))
	if broken.Forbidden || !broken.Unavailable {
		t.Errorf("unavailable marked as %+v", broken)
	}
}

func TestIsForbidden(t *testing.T) {
	if !isForbidden(&forbiddenError{verb: "get", resource: "pods"}) {
		t.Error("a forbiddenError should be forbidden")
	}
	// Wrapping must not hide it.
	if !isForbidden(fmt.Errorf("scan: %w", &forbiddenError{verb: "list", resource: "pods"})) {
		t.Error("a wrapped forbiddenError should still be forbidden")
	}
	if isForbidden(errors.New("timeout")) {
		t.Error("an ordinary error is not forbidden")
	}
	if isForbidden(nil) {
		t.Error("nil is not forbidden")
	}
}

func TestPodRequests(t *testing.T) {
	p := mkObj(t, nil, map[string]any{
		"spec": map[string]any{"containers": []any{
			map[string]any{"resources": map[string]any{"requests": map[string]any{
				"cpu": "250m", "memory": "128Mi",
			}}},
			map[string]any{"resources": map[string]any{"requests": map[string]any{
				"cpu": "1", "memory": "1Gi",
			}}},
			map[string]any{}, // a container with no requests contributes nothing
		}},
	})
	cpu, mem := podRequests(p)
	if cpu != 1250 {
		t.Errorf("cpu = %dm, want 1250m", cpu)
	}
	if mem != 128+1024 {
		t.Errorf("mem = %dMi, want 1152Mi", mem)
	}
}

func TestWorkloadHealth(t *testing.T) {
	wl := func(kind string, extra map[string]any) *unstructured.Unstructured {
		o := map[string]any{
			"kind":     kind,
			"metadata": map[string]any{"name": "w"},
		}
		for k, v := range extra {
			o[k] = v
		}
		return &unstructured.Unstructured{Object: o}
	}
	replicas := func(desired, ready int64) map[string]any {
		return map[string]any{
			"spec":   map[string]any{"replicas": desired},
			"status": map[string]any{"readyReplicas": ready},
		}
	}
	daemon := func(desired, ready int64) map[string]any {
		return map[string]any{
			"status": map[string]any{"desiredNumberScheduled": desired, "numberReady": ready},
		}
	}

	cases := []struct {
		name string
		obj  *unstructured.Unstructured
		want string
	}{
		{"deployment healthy", wl("Deployment", replicas(3, 3)), "Healthy"},
		{"deployment progressing", wl("Deployment", replicas(3, 1)), "Progressing"},
		{"deployment degraded", wl("Deployment", replicas(3, 0)), "Degraded"},
		// Deliberately scaled down is not an outage.
		{"deployment scaled to zero", wl("Deployment", replicas(0, 0)), "Scaled to zero"},
		{"statefulset healthy", wl("StatefulSet", replicas(2, 2)), "Healthy"},
		{"daemonset healthy", wl("DaemonSet", daemon(4, 4)), "Healthy"},
		{"daemonset degraded", wl("DaemonSet", daemon(4, 0)), "Degraded"},
		{"daemonset progressing", wl("DaemonSet", daemon(4, 2)), "Progressing"},
		{"daemonset unscheduled", wl("DaemonSet", daemon(0, 0)), "Not scheduled"},
		{"job defers to jobStatus", wl("Job", map[string]any{
			"status": map[string]any{"active": int64(1)},
		}), "Running"},
		// Kinds without a health model (Services, Ingresses) count as healthy.
		{"service", wl("Service", nil), "Healthy"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := workloadHealth(tc.obj); got != tc.want {
				t.Errorf("workloadHealth = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestEventTime(t *testing.T) {
	if got := eventTime(mkObj(t, nil, map[string]any{
		"lastTimestamp": "2024-06-01T00:00:00Z",
		"eventTime":     "2024-06-02T00:00:00Z",
	})); got != "2024-06-01T00:00:00Z" {
		t.Errorf("lastTimestamp should win, got %q", got)
	}
	if got := eventTime(mkObj(t, nil, map[string]any{
		"eventTime": "2024-06-02T00:00:00Z",
	})); got != "2024-06-02T00:00:00Z" {
		t.Errorf("eventTime fallback = %q", got)
	}
	if got := eventTime(mkObj(t, nil, map[string]any{
		"firstTimestamp": "2024-06-03T00:00:00Z",
	})); got != "2024-06-03T00:00:00Z" {
		t.Errorf("firstTimestamp fallback = %q", got)
	}
	// With no event timestamps at all, creation time is the last resort.
	if got := eventTime(mkObj(t, map[string]any{
		"creationTimestamp": "2024-06-04T00:00:00Z",
	}, nil)); got != "2024-06-04T00:00:00Z" {
		t.Errorf("creation fallback = %q", got)
	}
}

func TestParseCPUMilli(t *testing.T) {
	cases := map[string]int64{
		"":     0,
		"250m": 250,
		"1":    1000,
		"2500": 2500000,
		"junk": 0, // unparseable input degrades to zero, never panics
	}
	for in, want := range cases {
		if got := parseCPUMilli(in); got != want {
			t.Errorf("parseCPUMilli(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestParseMemMiB(t *testing.T) {
	cases := map[string]int64{
		"":      0,
		"128Mi": 128,
		"1Gi":   1024,
		"junk":  0,
	}
	for in, want := range cases {
		if got := parseMemMiB(in); got != want {
			t.Errorf("parseMemMiB(%q) = %d, want %d", in, got, want)
		}
	}
}
