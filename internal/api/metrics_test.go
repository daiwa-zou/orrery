package api

import (
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestUsageFrom(t *testing.T) {
	u := usageFrom(corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("500m"),
		corev1.ResourceMemory: resource.MustParse("256Mi"),
	})
	if u.CPUMilli != 500 || u.MemoryMiB != 256 {
		t.Errorf("usage = %+v", u)
	}

	if got := usageFrom(corev1.ResourceList{}); got != (usage{}) {
		t.Errorf("empty list = %+v, want zero usage", got)
	}
}

func TestMetricsUnavailable(t *testing.T) {
	if _, ok := metricsUnavailable(nil); ok {
		t.Error("nil error is not unavailability")
	}
	// A missing metrics-server is an honest "not installed", not a 500.
	for name, err := range map[string]error{
		"not found":   apierrors.NewNotFound(schema.GroupResource{Resource: "nodemetrics"}, "x"),
		"unavailable": apierrors.NewServiceUnavailable("down"),
		"timeout":     apierrors.NewTimeoutError("slow", 1),
	} {
		resp, ok := metricsUnavailable(err)
		if !ok {
			t.Errorf("%s should read as metrics unavailable", name)
			continue
		}
		if resp.Available || resp.Reason == "" {
			t.Errorf("%s produced %+v", name, resp)
		}
	}
	// Anything else (forbidden, network trouble) must surface as an error.
	if _, ok := metricsUnavailable(errors.New("boom")); ok {
		t.Error("an arbitrary error must not be reported as unavailability")
	}
	if _, ok := metricsUnavailable(apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "x", nil)); ok {
		t.Error("forbidden must not be masked as unavailability")
	}
}

func TestQuantityUsage(t *testing.T) {
	n := mkObj(t, nil, map[string]any{
		"status": map[string]any{
			"capacity":    map[string]any{"cpu": "4", "memory": "8Gi"},
			"allocatable": map[string]any{"cpu": "3500m", "memory": "7Gi"},
		},
	})
	if got := quantityUsage(n, "capacity"); got.CPUMilli != 4000 || got.MemoryMiB != 8192 {
		t.Errorf("capacity = %+v", got)
	}
	if got := quantityUsage(n, "allocatable"); got.CPUMilli != 3500 || got.MemoryMiB != 7168 {
		t.Errorf("allocatable = %+v", got)
	}
	// A node with no status block yields zeroes, not a panic.
	if got := quantityUsage(mkObj(t, nil, nil), "capacity"); got != (usage{}) {
		t.Errorf("statusless node = %+v", got)
	}
}

func TestPodLimits(t *testing.T) {
	p := mkObj(t, nil, map[string]any{
		"spec": map[string]any{"containers": []any{
			map[string]any{"resources": map[string]any{"limits": map[string]any{
				"cpu": "500m", "memory": "256Mi",
			}}},
			map[string]any{"resources": map[string]any{"limits": map[string]any{
				"cpu": "1",
			}}},
		}},
	})
	u, ok := podLimits(p)
	if !ok {
		t.Fatal("limits were declared but not found")
	}
	if u.CPUMilli != 1500 || u.MemoryMiB != 256 {
		t.Errorf("limits = %+v", u)
	}

	// No limits anywhere: found=false so the frontend can omit the field, as
	// opposed to drawing usage against a zero denominator.
	if _, ok := podLimits(mkObj(t, nil, map[string]any{
		"spec": map[string]any{"containers": []any{map[string]any{}}},
	})); ok {
		t.Error("limitless pod reported limits")
	}
}

func TestRound1(t *testing.T) {
	cases := map[float64]float64{
		0:     0,
		33.33: 33.3,
		12.36: 12.4,
		100:   100,
	}
	for in, want := range cases {
		if got := round1(in); got != want {
			t.Errorf("round1(%v) = %v, want %v", in, got, want)
		}
	}
}
