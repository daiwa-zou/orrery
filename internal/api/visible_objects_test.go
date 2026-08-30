package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// visibleObjects promises to tell "you may not read this" apart from "we could
// not read this" — its own doc comment says an informer timeout must never
// reach the user as an RBAC problem. The per-namespace fallback path used to
// break that promise in the other direction: it skipped any namespace whose
// cache read failed and returned what was left with a nil error.
//
// InformerManager.List fails at the informer, which is per resource and not
// per namespace, so "some namespaces failed" is never the real case — when one
// fails they all do, and the caller received an empty slice and a nil error.
// The overview page then rendered every affected tile as a confident zero:
// neither Forbidden nor Unavailable, just "there is nothing here".
//
// Only a narrowly bound user reaches that path, so the bug was invisible to
// anyone with cluster-wide read.

func TestOverviewReportsAnUnreadableCacheRatherThanZero(t *testing.T) {
	rig := hndNewRig(t)

	// Deny only the cluster-wide review, which is what forces the
	// per-namespace fallback and puts the loop in play at all.
	rig.fake.nsOnlyResource = "pods"
	// Discovery still advertises pods; it is the cache that cannot be built.
	rig.fake.breakCacheResource = "pods"

	rec := rig.get(t, "/api/v1/clusters/fake/overview")
	if rec.Code != http.StatusOK {
		t.Fatalf("overview = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	var body overviewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode overview: %v", err)
	}

	if body.Pods.Unavailable {
		return // the answer we want: we could not say.
	}
	if body.Pods.Forbidden {
		t.Fatalf("an unreadable cache was reported as an RBAC denial")
	}
	t.Fatalf("pods came back as a definite total of %d with no reason given; "+
		"an unreadable cache must not render as a count", body.Pods.Total)
}

// The same distinction on the list endpoint, which shares listAcross. A
// namespace-scoped list whose cache cannot be built must fail loudly rather
// than serve an empty page that reads as "this namespace is empty".
func TestListReportsAnUnreadableCacheRatherThanAnEmptyPage(t *testing.T) {
	rig := hndNewRig(t)
	rig.fake.nsOnlyResource = "pods"
	rig.fake.breakCacheResource = "pods"

	rec := rig.get(t, "/api/v1/clusters/fake/resources/core/v1/pods")
	if rec.Code == http.StatusOK {
		t.Fatalf("an unreadable cache returned a page anyway: %s", rec.Body.String())
	}
	if rec.Code == http.StatusForbidden {
		t.Fatalf("an unreadable cache was reported as an RBAC denial: %s", rec.Body.String())
	}
	if body := decodeErrBody(t, rec); !strings.Contains(body.Reason, "pods") {
		t.Errorf("reason = %q, want it to name what could not be read", body.Reason)
	}
}
