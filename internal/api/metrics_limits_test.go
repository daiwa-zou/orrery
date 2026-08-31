package api

// The pod metrics endpoint joins container limits in from the pod cache, and
// the console draws each pod's usage as a fraction of them. A pod with no
// declared limit has no honest denominator, so the bar stays empty and only the
// reading shows — which means "empty bar" is the console's way of saying
// "nobody set a limit on this".
//
// The cache read that supplies those limits could fail, and when it did the
// failure was dropped on the floor: every pod came back without limits, and the
// page said, of every workload at once, that none of them had any. That is not
// a gap a reader notices; it is a claim about the cluster, and someone acts on
// it.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func podMetricsOf(t *testing.T, rig *hndRig, path string) metricsResponse {
	t.Helper()
	rec := rig.get(t, path)
	hndWantStatus(t, rec, http.StatusOK)
	var body metricsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode metrics: %v (%s)", err, rec.Body.String())
	}
	return body
}

func TestPodMetricsSaysWhenLimitsCouldNotBeRead(t *testing.T) {
	rig := hndNewRig(t)
	// Discovery still advertises pods and the reviews still pass; it is the
	// cache behind the limits that cannot be built.
	rig.fake.breakCacheResource = "pods"

	body := podMetricsOf(t, rig, "/api/v1/clusters/fake/metrics/pods")

	// The usage readings are real and worth serving — this is a partial
	// answer, not a failure.
	if !body.Available || len(body.Pods) == 0 {
		t.Fatalf("usage was withheld over a missing denominator: %+v", body)
	}
	for _, p := range body.Pods {
		if p.Limits != nil {
			t.Fatalf("limits were served from a cache that could not be read: %+v", p)
		}
	}
	if len(body.Warnings) == 0 {
		t.Fatal("every pod came back without limits and nothing said why; " +
			"the console renders that as 'no limits are declared'")
	}
	joined := strings.Join(body.Warnings, "\n")
	if !strings.Contains(joined, "limits") {
		t.Errorf("warnings = %q, want them to name what is missing", joined)
	}
	// The sentence has to head off the conclusion the empty bars invite.
	if !strings.Contains(joined, "no limits are set") {
		t.Errorf("warnings = %q, want them to deny the reading a blank bar suggests", joined)
	}
}

// A healthy cluster says nothing, so the warning stays meaningful.
func TestPodMetricsIsSilentWhenLimitsAreReadable(t *testing.T) {
	rig := hndNewRig(t)

	body := podMetricsOf(t, rig, "/api/v1/clusters/fake/metrics/pods")
	if len(body.Warnings) != 0 {
		t.Errorf("a healthy read reported gaps: %v", body.Warnings)
	}
	limited := false
	for _, p := range body.Pods {
		if p.Limits != nil {
			limited = true
		}
	}
	if !limited {
		t.Error("no pod carried limits, so the test above proves nothing")
	}
}
