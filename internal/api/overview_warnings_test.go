package api

import (
	"net/http"
	"strings"
	"testing"
)

// The recent-warnings feed is the one field on the overview that a reader takes
// as reassurance: the console renders an empty one as "No warning events. That
// is a good sign." An access review coming back no produced exactly the same
// empty list, so a user who simply may not read events was told their cluster
// was healthy.

func hndOverview(t *testing.T, rig *hndRig) overviewResponse {
	t.Helper()
	rec := rig.get(t, "/api/v1/clusters/fake/overview")
	hndWantStatus(t, rec, http.StatusOK)
	var body overviewResponse
	hndDecode(t, rec, &body)
	return body
}

func TestOverviewWarningsAreReportedWhenReadable(t *testing.T) {
	rig := hndNewRig(t)
	body := hndOverview(t, rig)

	if len(body.Warnings) == 0 {
		t.Fatalf("the fixture has a Warning event; got %+v", body.Warnings)
	}
	// A readable, genuinely quiet cluster must not be flagged: that is the
	// case the console is entitled to call a good sign.
	if body.WarningsForbidden || body.WarningsUnavailable {
		t.Errorf("readable warnings were marked unreadable: %+v", body)
	}
}

func TestOverviewSaysWhenWarningsAreForbidden(t *testing.T) {
	rig := hndNewRig(t)
	rig.fake.denyResource = "events"

	body := hndOverview(t, rig)

	if len(body.Warnings) != 0 {
		t.Errorf("events were returned despite being denied: %+v", body.Warnings)
	}
	if !body.WarningsForbidden {
		t.Error("a denied event scan came back as an ordinary empty feed")
	}
	if body.WarningsUnavailable {
		t.Error("a permission problem was reported as an availability problem")
	}
	// The rest of the overview is unaffected: one unreadable resource must not
	// take the page down with it.
	if body.Nodes.Total == 0 {
		t.Error("denying events also lost the node count")
	}
}

// An empty feed must marshal as [] rather than null, so a client can render it
// without a special case — and the flags stay absent when there is nothing to
// report, keeping the common response small.
func TestOverviewWarningsShapeIsStable(t *testing.T) {
	rig := hndNewRig(t)
	rec := rig.get(t, "/api/v1/clusters/fake/overview")
	hndWantStatus(t, rec, http.StatusOK)

	if body := rec.Body.String(); !strings.Contains(body, `"warnings":[`) {
		t.Errorf("warnings did not marshal as a list: %.300s", body)
	}
	// Absent, not false: the flags are omitempty so a healthy answer does not
	// carry two fields saying nothing went wrong.
	if body := rec.Body.String(); strings.Contains(body, "warningsForbidden") {
		t.Errorf("a clean response carried the forbidden flag: %.300s", body)
	}
}
