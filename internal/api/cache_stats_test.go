package api

// /stats reports which caches are running and how many objects they hold, and
// it filters that list through an access review because the set of running
// informers is itself information about the cluster.
//
// The filter used to drop any resource whose review did not come back allowed,
// which folds "you may not list this" together with "the review could not be
// performed". The console renders the result as two plain numbers — "Cached
// objects: 4,102", "Informers: 11" — and a reader is looking at that panel
// precisely because they doubt the memory figure. A total quietly missing a
// cache is the one answer worse than no total.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/daiwa-zou/orrery/internal/config"
)

type statsBody struct {
	Cluster   string `json:"cluster"`
	Informers []struct {
		GVR     string `json:"gvr"`
		Objects int    `json:"objects"`
	} `json:"informers"`
	TotalObjects int    `json:"totalObjects"`
	Unchecked    int    `json:"unchecked"`
	Warning      string `json:"warning"`
}

func statsOf(t *testing.T, rig *hndRig) statsBody {
	t.Helper()
	rec := rig.get(t, "/api/v1/clusters/fake/stats")
	hndWantStatus(t, rec, http.StatusOK)
	var body statsBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode stats: %v (%s)", err, rec.Body.String())
	}
	return body
}

func TestCacheStatsSaysWhenACacheCouldNotBeChecked(t *testing.T) {
	// Verdicts must not be served from the checker's cache here: the pods
	// review has to succeed once, to start the informer, and then fail.
	rig := hndNewRigWith(t, func(c *config.Config) { c.Authz.TTL = time.Nanosecond })

	// Start the pods informer, so there is a cache for /stats to report.
	hndWantStatus(t, rig.get(t, "/api/v1/clusters/fake/resources/core/v1/pods"), http.StatusOK)

	before := statsOf(t, rig)
	if before.Unchecked != 0 || before.Warning != "" {
		t.Fatalf("a healthy cluster reported gaps: %+v", before)
	}
	if before.TotalObjects == 0 || len(before.Informers) == 0 {
		t.Fatalf("stats reported nothing with an informer running: %+v", before)
	}

	// The review for pods now errors rather than answering.
	rig.fake.set(func(f *hndFake) { f.failReviewResource = "pods" })

	after := statsOf(t, rig)
	if after.Unchecked == 0 {
		t.Errorf("a review that could not be performed was not counted: %+v", after)
	}
	if after.Warning == "" {
		t.Error("the short list was served with nothing said about why it is short")
	}
	// The sentence has to keep the reader away from their RBAC, which is not
	// where the fault is.
	if !strings.Contains(after.Warning, "not a permission problem") {
		t.Errorf("warning = %q, want it to say this is not about permissions", after.Warning)
	}
	// And it must not claim a denial.
	if strings.Contains(after.Warning, "You may not") {
		t.Errorf("an unperformable review was reported as a refusal: %q", after.Warning)
	}
	for _, inf := range after.Informers {
		if strings.Contains(inf.GVR, "pods") {
			t.Errorf("a cache whose review failed was listed as permitted: %+v", after.Informers)
		}
	}
}

// A denial is still a denial: the resource is omitted, and nothing is said,
// because "you may not see this" is a complete answer.
func TestCacheStatsStaysSilentAboutADenial(t *testing.T) {
	rig := hndNewRigWith(t, func(c *config.Config) { c.Authz.TTL = time.Nanosecond })
	hndWantStatus(t, rig.get(t, "/api/v1/clusters/fake/resources/core/v1/pods"), http.StatusOK)

	rig.fake.set(func(f *hndFake) { f.denyResource = "pods" })

	got := statsOf(t, rig)
	if got.Unchecked != 0 || got.Warning != "" {
		t.Errorf("a plain denial was reported as an unanswered question: %+v", got)
	}
	for _, inf := range got.Informers {
		if strings.Contains(inf.GVR, "pods") {
			t.Errorf("a denied resource was listed: %+v", got.Informers)
		}
	}
}
