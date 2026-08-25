package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func postJSON(body string) *http.Request {
	return httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(body))
}

func TestDecodeBody(t *testing.T) {
	got, err := decodeBody[scaleRequest](postJSON(`{"resource":"deployments","name":"web","replicas":3}`))
	if err != nil {
		t.Fatal(err)
	}
	if got.Resource != "deployments" || got.Name != "web" || got.Replicas != 3 {
		t.Errorf("decoded = %+v", got)
	}
}

func TestDecodeBodyRejectsGarbage(t *testing.T) {
	_, err := decodeBody[scaleRequest](postJSON(`{not json`))
	if err == nil {
		t.Fatal("malformed JSON should be rejected")
	}
	// The failure must surface as a 400, not a 500.
	if !errors.Is(err, errBadRequest) {
		t.Errorf("decode failure is not a bad request: %v", err)
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("connection dropped") }

func TestDecodeBodyReadFailure(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/x", failingReader{})
	_, err := decodeBody[targetRef](r)
	if err == nil {
		t.Fatal("a failed body read should be an error")
	}
	// The client hung up mid-upload; that is their 400, not our 500.
	if !errors.Is(err, errBadRequest) {
		t.Errorf("read failure is not a bad request: %v", err)
	}
}

// drainPod builds a pod shaped the way skipDrain inspects it.
func drainPod(t *testing.T, mutate func(o map[string]any)) *unstructured.Unstructured {
	t.Helper()
	o := map[string]any{
		"metadata": map[string]any{"name": "p", "namespace": "demo"},
		"spec":     map[string]any{},
		"status":   map[string]any{"phase": "Running"},
	}
	if mutate != nil {
		mutate(o)
	}
	return &unstructured.Unstructured{Object: o}
}

func TestSkipDrain(t *testing.T) {
	cases := []struct {
		name       string
		mutate     func(o map[string]any)
		req        drainRequest
		wantSkip   bool
		wantReason string
	}{
		{
			name:     "ordinary pod is evicted",
			wantSkip: false,
		},
		{
			name: "terminating pod",
			mutate: func(o map[string]any) {
				o["metadata"].(map[string]any)["deletionTimestamp"] = "2024-01-01T00:00:00Z"
			},
			wantSkip:   true,
			wantReason: "already terminating",
		},
		{
			name: "succeeded pod",
			mutate: func(o map[string]any) {
				o["status"].(map[string]any)["phase"] = "Succeeded"
			},
			wantSkip:   true,
			wantReason: "already finished",
		},
		{
			name: "failed pod",
			mutate: func(o map[string]any) {
				o["status"].(map[string]any)["phase"] = "Failed"
			},
			wantSkip:   true,
			wantReason: "already finished",
		},
		{
			// Mirror pods are kubelet-owned; evicting them does nothing.
			name: "mirror pod",
			mutate: func(o map[string]any) {
				o["metadata"].(map[string]any)["annotations"] = map[string]any{
					"kubernetes.io/config.mirror": "abc",
				}
			},
			wantSkip:   true,
			wantReason: "mirror pod",
		},
		{
			name: "daemonset pod with the flag",
			mutate: func(o map[string]any) {
				o["metadata"].(map[string]any)["ownerReferences"] = []any{
					map[string]any{"kind": "DaemonSet", "name": "ds"},
				}
			},
			req:        drainRequest{IgnoreDaemonSets: true},
			wantSkip:   true,
			wantReason: "daemonset-managed",
		},
		{
			// Without the flag the skip reason must teach the fix, like kubectl.
			name: "daemonset pod without the flag",
			mutate: func(o map[string]any) {
				o["metadata"].(map[string]any)["ownerReferences"] = []any{
					map[string]any{"kind": "DaemonSet", "name": "ds"},
				}
			},
			wantSkip:   true,
			wantReason: "daemonset-managed (set ignoreDaemonSets to proceed)",
		},
		{
			name: "emptyDir pod without consent",
			mutate: func(o map[string]any) {
				o["spec"].(map[string]any)["volumes"] = []any{
					map[string]any{"name": "scratch", "emptyDir": map[string]any{}},
				}
			},
			wantSkip:   true,
			wantReason: "uses emptyDir (set deleteEmptyDirData to proceed)",
		},
		{
			name: "emptyDir pod with consent",
			mutate: func(o map[string]any) {
				o["spec"].(map[string]any)["volumes"] = []any{
					map[string]any{"name": "scratch", "emptyDir": map[string]any{}},
				}
			},
			req:      drainRequest{DeleteEmptyDirData: true},
			wantSkip: false,
		},
		{
			name: "replicaset pod is not daemonset-managed",
			mutate: func(o map[string]any) {
				o["metadata"].(map[string]any)["ownerReferences"] = []any{
					map[string]any{"kind": "ReplicaSet", "name": "rs"},
				}
			},
			wantSkip: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason, skip := skipDrain(drainPod(t, tc.mutate), tc.req)
			if skip != tc.wantSkip {
				t.Fatalf("skip = %v (reason %q), want %v", skip, reason, tc.wantSkip)
			}
			if skip && reason != tc.wantReason {
				t.Errorf("reason = %q, want %q", reason, tc.wantReason)
			}
		})
	}
}
