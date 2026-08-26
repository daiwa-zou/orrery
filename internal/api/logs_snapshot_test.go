package api

import (
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func snapshot(t *testing.T, rig *hndRig, query string) logSnapshotResponse {
	t.Helper()
	rec := rig.get(t, "/api/v1/clusters/fake/logs?"+query)
	hndWantStatus(t, rec, http.StatusOK)
	var body logSnapshotResponse
	hndDecode(t, rec, &body)
	return body
}

func TestLogSnapshotReadsSeveralPodsAtOnce(t *testing.T) {
	rig := hndNewRig(t)
	got := snapshot(t, rig, "namespace=demo&pod=web-1&pod=web-2")

	if got.Namespace != "demo" {
		t.Errorf("namespace = %q", got.Namespace)
	}
	if len(got.Pods) != 2 {
		t.Fatalf("pods = %+v, want two entries", got.Pods)
	}
	// Order follows the request, so a caller can line results up with what it
	// asked for instead of matching on names.
	if got.Pods[0].Pod != "web-1" || got.Pods[1].Pod != "web-2" {
		t.Errorf("pods came back as %q, %q", got.Pods[0].Pod, got.Pods[1].Pod)
	}
	for _, p := range got.Pods {
		if want := []string{"line-1", "line-2"}; !reflect.DeepEqual(p.Lines, want) {
			t.Errorf("%s lines = %v, want %v", p.Pod, p.Lines, want)
		}
		if p.Error != "" {
			t.Errorf("%s: %s", p.Pod, p.Error)
		}
	}
}

func TestLogSnapshotSinglePodMatchesThePlainTextRoute(t *testing.T) {
	rig := hndNewRig(t)

	text := rig.get(t, "/api/v1/clusters/fake/pods/demo/web-1/logs")
	hndWantStatus(t, text, http.StatusOK)
	want := strings.Split(strings.TrimRight(text.Body.String(), "\n"), "\n")

	got := snapshot(t, rig, "namespace=demo&pod=web-1")
	if len(got.Pods) != 1 {
		t.Fatalf("pods = %+v", got.Pods)
	}
	// Two routes onto one subresource must not disagree about what the log says.
	if !reflect.DeepEqual(got.Pods[0].Lines, want) {
		t.Errorf("snapshot = %v, plain text = %v", got.Pods[0].Lines, want)
	}
}

func TestLogSnapshotPassesThroughContainer(t *testing.T) {
	rig := hndNewRig(t)
	got := snapshot(t, rig, "namespace=demo&pod=web-1&container=app&tailLines=10")
	if got.Container != "app" {
		t.Errorf("container = %q, want it echoed back", got.Container)
	}
}

// Every pod is authorized on its own. Reading several at once is a convenience
// over the same checks, never a way around them.
func TestLogSnapshotRefusesWhenLogsAreDenied(t *testing.T) {
	rig := hndNewRig(t)
	rig.fake.denyResource = "pods"

	rec := rig.get(t, "/api/v1/clusters/fake/logs?namespace=demo&pod=web-1&pod=web-2")
	hndWantStatus(t, rec, http.StatusForbidden)
}

func TestLogSnapshotRejections(t *testing.T) {
	rig := hndNewRig(t)
	many := make([]string, 0, maxAggregatedPods+1)
	for i := 0; i <= maxAggregatedPods; i++ {
		many = append(many, "pod=p")
	}

	cases := []struct {
		name, query string
		want        int
	}{
		{"no namespace", "pod=web-1", http.StatusBadRequest},
		{"no pods", "namespace=demo", http.StatusBadRequest},
		{"over the cap", "namespace=demo&" + strings.Join(many, "&"), http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hndWantStatus(t, rig.get(t, "/api/v1/clusters/fake/logs?"+tc.query), tc.want)
		})
	}
	hndWantStatus(t, rig.get(t, "/api/v1/clusters/nope/logs?namespace=demo&pod=web-1"), http.StatusNotFound)
}

// The cap is shared with the streaming route on purpose: the two must not
// disagree about how many pods one caller may hold open at once.
func TestLogSnapshotSharesTheStreamCap(t *testing.T) {
	if maxAggregatedPods <= 0 {
		t.Fatal("the aggregation cap is not set")
	}
	rig := hndNewRig(t)
	over := make([]string, 0, maxAggregatedPods+1)
	for i := 0; i <= maxAggregatedPods; i++ {
		over = append(over, "pod=p")
	}
	query := "namespace=demo&" + strings.Join(over, "&")

	hndWantStatus(t, rig.get(t, "/api/v1/clusters/fake/logs?"+query), http.StatusBadRequest)
	hndWantStatus(t, rig.get(t, "/api/v1/clusters/fake/ws/logs?"+query), http.StatusBadRequest)
}
