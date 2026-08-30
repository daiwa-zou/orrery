package api

import (
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

// Every repeated ?namespace= costs a SubjectAccessReview against the real API
// server, issued one at a time, for a name that does not have to exist. The
// verdict cache does not help — a name nobody has asked about before is always
// a miss — so the parameter turns query-string bytes into API-server round
// trips, and evicts the verdicts that were worth keeping on its way through.
//
// The refusal has to land before the first review, not after some of them.
func TestListRefusesMoreNamespacesThanItWillReview(t *testing.T) {
	rig := hndNewRig(t)

	var reviews atomic.Int64
	rig.fake.setTrace(func(path string) {
		if strings.Contains(path, "subjectaccessreviews") {
			reviews.Add(1)
		}
	})

	terms := make([]string, 0, maxQueryNamespaces*4)
	for i := range maxQueryNamespaces * 4 {
		terms = append(terms, fmt.Sprintf("namespace=ns-%d", i))
	}
	rec := rig.get(t, "/api/v1/clusters/fake/resources/_/v1/pods?"+strings.Join(terms, "&"))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if n := reviews.Load(); n != 0 {
		t.Errorf("%d access reviews were issued for a request that was refused", n)
	}
}

// The cap must not narrow what the console actually asks for.
func TestListAcceptsTheNamespacesAConsoleAsksFor(t *testing.T) {
	rig := hndNewRig(t)
	rec := rig.get(t, "/api/v1/clusters/fake/resources/_/v1/pods?namespace=demo&namespace=kube-system")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

// A where predicate is compiled once and then run against every object in the
// list, so the parameter multiplies rather than adds.
func TestListRefusesMoreWherePredicatesThanItWillRun(t *testing.T) {
	rig := hndNewRig(t)

	terms := make([]string, 0, maxWherePredicates*4)
	for i := range maxWherePredicates * 4 {
		terms = append(terms, fmt.Sprintf("where=name%%3D~x%d", i))
	}
	rec := rig.get(t, "/api/v1/clusters/fake/resources/_/v1/pods?"+strings.Join(terms, "&"))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "where") {
		t.Errorf("body = %s, want it to name the parameter that was refused", rec.Body.String())
	}
}
