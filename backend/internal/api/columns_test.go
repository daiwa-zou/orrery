package api

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// pod builds an unstructured pod from a status fragment.
func pod(t *testing.T, status map[string]any, spec map[string]any, meta map[string]any) *unstructured.Unstructured {
	t.Helper()
	obj := map[string]any{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata":   map[string]any{"name": "p", "namespace": "n"},
	}
	for k, v := range meta {
		obj["metadata"].(map[string]any)[k] = v
	}
	if spec != nil {
		obj["spec"] = spec
	}
	if status != nil {
		obj["status"] = status
	}
	return &unstructured.Unstructured{Object: obj}
}

func TestPodStatus(t *testing.T) {
	tests := []struct {
		name   string
		status map[string]any
		spec   map[string]any
		meta   map[string]any
		want   string
	}{
		{
			name:   "running pod reports its phase",
			status: map[string]any{"phase": "Running"},
			want:   "Running",
		},
		{
			// The whole point of not just reading status.phase: a pod stuck
			// pulling an image is "Pending" by phase, which tells nobody
			// anything useful.
			name: "waiting container reason wins over phase",
			status: map[string]any{
				"phase": "Pending",
				"containerStatuses": []any{
					map[string]any{"state": map[string]any{
						"waiting": map[string]any{"reason": "ImagePullBackOff"},
					}},
				},
			},
			want: "ImagePullBackOff",
		},
		{
			name: "crashloop is surfaced",
			status: map[string]any{
				"phase": "Running",
				"containerStatuses": []any{
					map[string]any{"state": map[string]any{
						"waiting": map[string]any{"reason": "CrashLoopBackOff"},
					}},
				},
			},
			want: "CrashLoopBackOff",
		},
		{
			name: "failing init container is prefixed",
			status: map[string]any{
				"phase": "Pending",
				"initContainerStatuses": []any{
					map[string]any{"state": map[string]any{
						"waiting": map[string]any{"reason": "ImagePullBackOff"},
					}},
				},
			},
			want: "Init:ImagePullBackOff",
		},
		{
			name: "completed init container does not mask the pod status",
			status: map[string]any{
				"phase": "Running",
				"initContainerStatuses": []any{
					map[string]any{"state": map[string]any{
						"terminated": map[string]any{"exitCode": int64(0), "reason": "Completed"},
					}},
				},
			},
			want: "Running",
		},
		{
			name: "failed init container reports its exit reason",
			status: map[string]any{
				"phase": "Pending",
				"initContainerStatuses": []any{
					map[string]any{"state": map[string]any{
						"terminated": map[string]any{"exitCode": int64(1), "reason": "Error"},
					}},
				},
			},
			want: "Init:Error",
		},
		{
			name:   "deleting pod is terminating regardless of phase",
			status: map[string]any{"phase": "Running"},
			meta:   map[string]any{"deletionTimestamp": "2026-01-01T00:00:00Z"},
			want:   "Terminating",
		},
		{
			name:   "evicted pods report the status reason",
			status: map[string]any{"phase": "Failed", "reason": "Evicted"},
			want:   "Evicted",
		},
		{
			name:   "no status at all is honest about it",
			status: map[string]any{},
			want:   "Unknown",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := podStatus(pod(t, tc.status, tc.spec, tc.meta))
			if got != tc.want {
				t.Errorf("podStatus() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPodContainerSummary(t *testing.T) {
	p := pod(t, map[string]any{
		"containerStatuses": []any{
			map[string]any{"ready": true, "restartCount": int64(2)},
			map[string]any{"ready": false, "restartCount": int64(5)},
			map[string]any{"ready": true, "restartCount": int64(0)},
		},
	}, nil, nil)

	ready, total, restarts := podContainerSummary(p)
	if ready != 2 || total != 3 || restarts != 7 {
		t.Errorf("got ready=%d total=%d restarts=%d, want 2/3 and 7 restarts", ready, total, restarts)
	}
}

func TestPodContainerSummaryFallsBackToSpec(t *testing.T) {
	// A pod that has not been scheduled yet has no container statuses, but the
	// table still needs a denominator.
	p := pod(t, map[string]any{"phase": "Pending"}, map[string]any{
		"containers": []any{map[string]any{"name": "a"}, map[string]any{"name": "b"}},
	}, nil)

	ready, total, _ := podContainerSummary(p)
	if ready != 0 || total != 2 {
		t.Errorf("got %d/%d, want 0/2", ready, total)
	}
}

func TestNodeStatus(t *testing.T) {
	node := func(unschedulable bool, readyStatus string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "v1",
			"kind":       "Node",
			"metadata":   map[string]any{"name": "n1"},
			"spec":       map[string]any{"unschedulable": unschedulable},
			"status": map[string]any{"conditions": []any{
				map[string]any{"type": "MemoryPressure", "status": "False"},
				map[string]any{"type": "Ready", "status": readyStatus},
			}},
		}}
	}

	cases := map[string]struct {
		obj  *unstructured.Unstructured
		want string
	}{
		"ready":                 {node(false, "True"), "Ready"},
		"not ready":             {node(false, "False"), "NotReady"},
		"unknown":               {node(false, "Unknown"), "Unknown"},
		"cordon beats ready":    {node(true, "True"), "SchedulingDisabled"},
		"cordon beats notready": {node(true, "False"), "SchedulingDisabled"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := nodeStatus(tc.obj); got != tc.want {
				t.Errorf("nodeStatus() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNodeRoles(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "n", "labels": map[string]any{
			"node-role.kubernetes.io/control-plane": "",
			"node-role.kubernetes.io/worker":        "",
			"kubernetes.io/os":                      "linux",
		}},
	}}
	roles := nodeRoles(obj)
	if len(roles) != 2 || roles[0] != "control-plane" || roles[1] != "worker" {
		t.Errorf("nodeRoles() = %v, want [control-plane worker]", roles)
	}

	bare := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "n"},
	}}
	if got := nodeRoles(bare); len(got) != 1 || got[0] != "<none>" {
		t.Errorf("nodeRoles() on unlabelled node = %v, want [<none>]", got)
	}
}

func TestServiceExternalIP(t *testing.T) {
	svc := func(typ string, ingress []any, external []any) *unstructured.Unstructured {
		obj := map[string]any{
			"metadata": map[string]any{"name": "s"},
			"spec":     map[string]any{"type": typ},
			"status":   map[string]any{"loadBalancer": map[string]any{"ingress": ingress}},
		}
		if external != nil {
			obj["spec"].(map[string]any)["externalIPs"] = external
		}
		return &unstructured.Unstructured{Object: obj}
	}

	if got := serviceExternalIP(svc("ClusterIP", nil, nil)); got != "" {
		t.Errorf("ClusterIP should have no external IP, got %q", got)
	}
	// A LoadBalancer with no address yet is pending, not absent — the
	// distinction matters when you are waiting for a cloud provider.
	if got := serviceExternalIP(svc("LoadBalancer", nil, nil)); got != "<pending>" {
		t.Errorf("pending LoadBalancer = %q, want <pending>", got)
	}
	got := serviceExternalIP(svc("LoadBalancer", []any{
		map[string]any{"ip": "1.2.3.4"},
		map[string]any{"hostname": "lb.example.com"},
	}, nil))
	if got != "1.2.3.4, lb.example.com" {
		t.Errorf("LoadBalancer addresses = %q", got)
	}
}

func TestJoinLimitSummarisesTail(t *testing.T) {
	if got := joinLimit([]string{"a", "b"}, 3); got != "a, b" {
		t.Errorf("short list = %q", got)
	}
	if got := joinLimit([]string{"a", "b", "c", "d", "e"}, 3); got != "a, b, c, +2 more" {
		t.Errorf("long list = %q", got)
	}
	if got := joinLimit(nil, 3); got != "" {
		t.Errorf("empty list = %q", got)
	}
}

func TestSanitizeKey(t *testing.T) {
	cases := map[string]string{
		"Size":            "size",
		"Ready Replicas":  "ready_replicas",
		"CPU (millicore)": "cpu__millicore",
		"---":             "",
	}
	for in, want := range cases {
		if got := sanitizeKey(in); got != want {
			t.Errorf("sanitizeKey(%q) = %q, want %q", in, got, want)
		}
	}
}
