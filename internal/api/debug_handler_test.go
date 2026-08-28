package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/daiwa-zou/orrery/internal/config"
)

// debugPod injects a container into somebody's running pod. It was the one
// action handler with no test at all, which is an odd gap for the most
// privileged thing on the surface: it is one-way (Kubernetes cannot remove an
// ephemeral container), it runs an operator-chosen image inside another
// workload's namespaces, and several of its guards exist precisely to keep the
// browser from choosing what runs.

func hndDebugPost(t *testing.T, rig *hndRig, body string) *httptest.ResponseRecorder {
	t.Helper()
	return rig.do(t, http.MethodPost, "/api/v1/clusters/fake/actions/debug", body,
		map[string]string{"Content-Type": "application/json"})
}

func TestDebugPodAttachesAnEphemeralContainer(t *testing.T) {
	rig := hndNewRig(t)

	rec := hndDebugPost(t, rig, `{"namespace":"demo","pod":"web-1","targetContainer":"app"}`)
	hndWantStatus(t, rec, http.StatusOK)

	var body debugResponse
	hndDecode(t, rec, &body)

	if body.Pod != "web-1" || body.Namespace != "demo" {
		t.Errorf("response names %s/%s", body.Namespace, body.Pod)
	}
	// The generated name is the whole point of the response: it is what the
	// caller has to exec into, and nothing else can tell them what it is.
	if !strings.HasPrefix(body.Container, "debugger-") || len(body.Container) <= len("debugger-") {
		t.Errorf("container = %q, want a generated debugger- name", body.Container)
	}
	// The image comes from configuration, never from the request.
	if body.Image != config.Default().Debug.Image {
		t.Errorf("image = %q, want the configured one", body.Image)
	}

	sent := rig.fake.ephemeralContainers()
	if len(sent) != 1 {
		t.Fatalf("cluster received %v", sent)
	}
	want := "demo/web-1/" + body.Container + ":" + body.Image + ":app"
	if sent[0] != want {
		t.Errorf("cluster received %q, want %q", sent[0], want)
	}
}

// Each attempt needs its own name: an ephemeral container cannot be removed,
// so a repeated name collides with the container already there.
func TestDebugPodGeneratesADistinctNameEachTime(t *testing.T) {
	rig := hndNewRig(t)

	seen := map[string]bool{}
	for i := 0; i < 5; i++ {
		rec := hndDebugPost(t, rig, `{"namespace":"demo","pod":"web-1"}`)
		hndWantStatus(t, rec, http.StatusOK)
		var body debugResponse
		hndDecode(t, rec, &body)
		if seen[body.Container] {
			t.Fatalf("name %q was generated twice", body.Container)
		}
		seen[body.Container] = true
	}
}

// An empty target attaches to the pod without joining any container's process
// namespace, which is a different and legitimate request — not a missing field.
func TestDebugPodAllowsNoTargetContainer(t *testing.T) {
	rig := hndNewRig(t)

	rec := hndDebugPost(t, rig, `{"namespace":"demo","pod":"web-1"}`)
	hndWantStatus(t, rec, http.StatusOK)

	sent := rig.fake.ephemeralContainers()
	if len(sent) != 1 || !strings.HasSuffix(sent[0], ":") {
		t.Errorf("cluster received %v, want an empty targetContainerName", sent)
	}
}

// The API server would reject this too, with a message about the pod spec.
// Catching it here says which name was wrong and what the choices were.
func TestDebugPodRejectsAnUnknownTargetContainer(t *testing.T) {
	rig := hndNewRig(t)

	rec := hndDebugPost(t, rig, `{"namespace":"demo","pod":"web-1","targetContainer":"ghost"}`)
	hndWantStatus(t, rec, http.StatusBadRequest)

	body := decodeErrBody(t, rec)
	if !strings.Contains(body.Reason, "ghost") || !strings.Contains(body.Reason, "app") {
		t.Errorf("reason = %q, want it to name the bad container and the real ones", body.Reason)
	}
	if got := rig.fake.ephemeralContainers(); len(got) != 0 {
		t.Errorf("a rejected request still reached the cluster: %v", got)
	}
}

// With no image configured there is nothing to run, and the operator is the
// only one who may choose it — so this is a configuration error stated plainly,
// not an invitation for the caller to supply one.
func TestDebugPodRefusesWithNoConfiguredImage(t *testing.T) {
	rig := hndNewRigWith(t, func(c *config.Config) { c.Debug.Image = "" })

	rec := hndDebugPost(t, rig, `{"namespace":"demo","pod":"web-1"}`)
	hndWantStatus(t, rec, http.StatusBadRequest)

	if body := decodeErrBody(t, rec); !strings.Contains(body.Reason, "debug.image") {
		t.Errorf("reason = %q, want it to name the setting", body.Reason)
	}
	if got := rig.fake.ephemeralContainers(); len(got) != 0 {
		t.Errorf("a container was attached with no image: %v", got)
	}
}

// The image is not a request field. A console that accepted one would be a way
// to run arbitrary code inside another workload's namespaces, so an attempt to
// name one must be ignored rather than honoured.
func TestDebugPodIgnoresAnImageFromTheRequest(t *testing.T) {
	rig := hndNewRig(t)

	rec := hndDebugPost(t, rig,
		`{"namespace":"demo","pod":"web-1","image":"evil.example/backdoor:latest"}`)
	hndWantStatus(t, rec, http.StatusOK)

	var body debugResponse
	hndDecode(t, rec, &body)
	if body.Image != config.Default().Debug.Image {
		t.Errorf("image = %q, want the configured one", body.Image)
	}
	for _, sent := range rig.fake.ephemeralContainers() {
		if strings.Contains(sent, "evil.example") {
			t.Fatalf("a request-supplied image reached the cluster: %q", sent)
		}
	}
}

func TestDebugPodRejections(t *testing.T) {
	rig := hndNewRig(t)
	cases := []struct {
		name, body string
		want       int
	}{
		{"no namespace", `{"pod":"web-1"}`, http.StatusBadRequest},
		{"no pod", `{"namespace":"demo"}`, http.StatusBadRequest},
		{"empty body", `{}`, http.StatusBadRequest},
		{"garbage", `not json`, http.StatusBadRequest},
		{"no such pod", `{"namespace":"demo","pod":"ghost"}`, http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hndWantStatus(t, hndDebugPost(t, rig, tc.body), tc.want)
		})
	}
}

// Gated on patch of pods/ephemeralcontainers — the same permission kubectl
// debug needs, decided by the cluster rather than by the dashboard.
func TestDebugPodRefusedWhenNotPermitted(t *testing.T) {
	rig := hndNewRig(t)
	rig.fake.denyResource = "pods"

	rec := hndDebugPost(t, rig, `{"namespace":"demo","pod":"web-1"}`)
	hndWantStatus(t, rec, http.StatusForbidden)

	if got := rig.fake.ephemeralContainers(); len(got) != 0 {
		t.Errorf("a forbidden request still reached the cluster: %v", got)
	}
}

// A pod that has finished will never start another container: the kubelet is
// done with it. The API server accepts the update anyway, so without this the
// call succeeded, the container was added to a pod that was over, and the
// console sat on "waiting for the node to start it" until whoever asked gave
// up. An answer beats a spinner.
func TestDebugPodRefusesAFinishedPod(t *testing.T) {
	rig := hndNewRig(t)

	rec := hndDebugPost(t, rig, `{"namespace":"demo","pod":"done-1"}`)
	hndWantStatus(t, rec, http.StatusBadRequest)

	if body := rec.Body.String(); !strings.Contains(body, "Succeeded") {
		t.Errorf("the refusal should name the phase that caused it: %s", body)
	}
	if sent := rig.fake.ephemeralContainers(); len(sent) != 0 {
		t.Errorf("nothing should have been added to a finished pod, got %v", sent)
	}
}
