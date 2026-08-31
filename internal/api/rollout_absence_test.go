package api

import (
	"net/http"
	"strings"
	"testing"
)

// InformerManager.Get returns (nil, nil) for an object that is not in the
// cache and (nil, err) for a cache that could not be read. Both were folded
// into `if err != nil || dep == nil`, which answers 404 "deployment demo/web"
// — a definite statement that the object does not exist — to a question that
// was never asked.
//
// Rollout history and undo are both built on this walk, so the moment it lies
// is the moment somebody is trying to roll back a bad deploy and is told the
// deployment they are looking at does not exist.

func TestRolloutHistoryDoesNotCallAnUnreadableCacheAMissingDeployment(t *testing.T) {
	rig := hndNewRig(t)
	rig.fake.breakCacheResource = "apps/deployments"

	rec := rig.get(t, "/api/v1/clusters/fake/rollout/history?namespace=demo&name=web")
	if rec.Code == http.StatusNotFound {
		t.Fatalf("an unreadable cache was reported as a missing deployment: %s", rec.Body.String())
	}
	if rec.Code == http.StatusOK {
		t.Fatalf("history was served from a cache that could not be built: %s", rec.Body.String())
	}
	if body := decodeErrBody(t, rec); !strings.Contains(body.Reason, "deployments") {
		t.Errorf("reason = %q, want it to name what could not be read", body.Reason)
	}
}

func TestRolloutUndoDoesNotCallAnUnreadableCacheAMissingDeployment(t *testing.T) {
	rig := hndNewRig(t)
	rig.fake.breakCacheResource = "apps/deployments"

	rec := rig.do(t, http.MethodPost, "/api/v1/clusters/fake/actions/rollout-undo",
		`{"namespace":"demo","name":"web"}`, nil)
	if rec.Code == http.StatusNotFound {
		t.Fatalf("a rollback was refused as 'no such deployment' when the cache was unreadable: %s",
			rec.Body.String())
	}
	if rec.Code == http.StatusOK {
		t.Fatalf("a rollback proceeded from a cache that could not be built: %s", rec.Body.String())
	}
}

// A deployment that genuinely is not there is still a 404.
func TestRolloutHistoryStillReportsAMissingDeployment(t *testing.T) {
	rig := hndNewRig(t)

	rec := rig.get(t, "/api/v1/clusters/fake/rollout/history?namespace=demo&name=absent")
	if rec.Code != http.StatusNotFound {
		t.Errorf("history for a deployment that does not exist = %d: %s", rec.Code, rec.Body.String())
	}
}
