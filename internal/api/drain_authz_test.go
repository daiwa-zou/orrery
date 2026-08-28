package api

import (
	"net/http"
	"strings"
	"testing"
)

// A drain checks eviction permission per pod, because permission is granted
// that way, and reports the pods it may not evict as a count — never a list,
// since their names are exactly what the caller may not see.
//
// The count was reached by `if err != nil`, which is two answers. One is a
// denial. The other is a SubjectAccessReview the API server did not answer,
// and a drain is precisely the moment an API server is busy. Those pods were
// not evicted either — the node is not drained — but the operator was told
// they were outside their permissions, and went to ask for access they already
// had while the node stayed up.

func TestDrainSeparatesUnaskedChecksFromDenials(t *testing.T) {
	rig := hndNewRig(t)
	// The reviews for the node itself still answer; only the per-pod eviction
	// reviews fail, which is the shape of a busy API server mid-drain.
	rig.fake.failReviewResource = "pods"

	rec := rig.do(t, http.MethodPost, "/api/v1/clusters/fake/actions/drain",
		`{"node":"node-1","ignoreDaemonSets":true,"deleteEmptyDirData":true}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("drain = %d: %s", rec.Code, rec.Body.String())
	}

	var body drainResult
	hndDecode(t, rec, &body)

	if body.NotPermitted > 0 {
		t.Errorf("%d pod(s) were reported as outside the caller's permissions; "+
			"the reviews failed, they were not denied", body.NotPermitted)
	}
	if body.NotChecked == 0 {
		t.Fatalf("unaskable reviews vanished from the result entirely: %+v", body)
	}
	if len(body.Evicted) != 0 {
		t.Errorf("evicted = %v, want none: no pod's permission was established", body.Evicted)
	}
}

// The denial path still says what it always said.
func TestDrainStillCountsRealDenials(t *testing.T) {
	rig := hndNewRig(t)
	rig.fake.denyResource = "pods"

	rec := rig.do(t, http.MethodPost, "/api/v1/clusters/fake/actions/drain",
		`{"node":"node-1","ignoreDaemonSets":true,"deleteEmptyDirData":true}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("drain = %d: %s", rec.Code, rec.Body.String())
	}

	var body drainResult
	hndDecode(t, rec, &body)

	if body.NotPermitted == 0 {
		t.Errorf("a real denial was not counted: %+v", body)
	}
	if body.NotChecked > 0 {
		t.Errorf("a denial was reported as an unaskable question: %+v", body)
	}
	// Whichever bucket they land in, the names stay out of the response.
	if names := strings.Join(append(body.Evicted, body.Skipped...), " "); strings.Contains(names, "web-1") {
		t.Errorf("a pod the caller may not evict was named: %v", names)
	}
}
