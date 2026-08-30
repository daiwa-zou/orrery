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

// Counting predicates bounds how many patterns run, not how much each one
// costs. The cost of matching is proportional to the length of the pattern as
// well as the length of the cell, and one predicate can carry the whole request
// line: measured against a 224-byte event message, a megabyte of pattern is a
// little over a second per row, which is hours across a busy cluster's events.
// The filter runs before the limit and never looks at the request context, and
// writeTimeout is deliberately zero, so nothing downstream ends it either.
func TestListRefusesAPatternLargerThanItWillRun(t *testing.T) {
	rig := hndNewRig(t)

	huge := strings.Repeat("a", maxPatternBytes+1)
	rec := rig.get(t, "/api/v1/clusters/fake/resources/_/v1/pods?where=name%3D~"+huge)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, "pattern") {
		t.Errorf("body = %s, want it to say which term was too long", body)
	}
}

// And sixteen patterns each just inside the per-pattern cap cost sixteen times
// one, so the total is bounded separately. Without this the first cap buys
// nothing: it is the product that runs against every row.
func TestListRefusesMorePatternBytesInTotalThanItWillRun(t *testing.T) {
	rig := hndNewRig(t)

	// Each term is legal on its own; together they are past the budget.
	per := maxPatternBytes / 2
	terms := make([]string, 0, maxWherePredicates)
	for i := range maxWherePredicates {
		terms = append(terms, fmt.Sprintf("where=name%%3D~%s%d", strings.Repeat("a", per), i))
	}
	rec := rig.get(t, "/api/v1/clusters/fake/resources/_/v1/pods?"+strings.Join(terms, "&"))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}

// Neither cap may narrow what the console actually sends. These are the
// patterns this parameter exists for.
func TestListAcceptsThePatternsAConsoleAsksFor(t *testing.T) {
	rig := hndNewRig(t)
	for _, q := range []string{
		"where=name%3D~%5Eweb-",
		"where=name%3D~canary%24%7C1%24",
		"where=name%3D~%5Eweb-&where=name%21~canary",
	} {
		rec := rig.get(t, "/api/v1/clusters/fake/resources/_/v1/pods?"+q)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200: %s", q, rec.Code, rec.Body.String())
		}
	}
}

// Every word in the box is looked for in every searched column of every event
// in scope, and the scan runs before the limit — so the parameter is a product
// and the caller sets the only term of it that is unbounded. A megabyte of
// query string is half a million single-character words.
func TestEventsRefusesMoreSearchWordsThanItWillRun(t *testing.T) {
	rig := hndNewRig(t)

	words := make([]string, 0, maxSearchTerms*4)
	for i := range maxSearchTerms * 4 {
		words = append(words, fmt.Sprintf("w%d", i))
	}
	rec := rig.get(t, "/api/v1/clusters/fake/events?q="+strings.Join(words, "+"))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}

// The queries this grammar was built around are two and three words.
func TestEventsAcceptsTheSearchAConsoleAsksFor(t *testing.T) {
	rig := hndNewRig(t)
	rec := rig.get(t, "/api/v1/clusters/fake/events?q=back-off+%22failed+to+mount%22+-Pulled")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

// childResource is the most expensive repeated parameter here: a name that
// resolves costs a discovery lookup, an access review, and a read through the
// shared cache, which starts an informer — a cluster-wide LIST and a WATCH held
// open afterwards. maxRelated bounds the answer and not the work, so a request
// naming resources that own nothing scans every one of them and never reaches
// it. The refusal has to land before the first scan.
func TestRelatedRefusesMoreChildResourcesThanItWillScan(t *testing.T) {
	rig := hndNewRig(t)

	var lists atomic.Int64
	rig.fake.setTrace(func(path string) {
		if strings.Contains(path, "/configmaps") || strings.Contains(path, "/secrets") {
			lists.Add(1)
		}
	})

	terms := make([]string, 0, maxChildResources*4)
	for i := range maxChildResources * 4 {
		terms = append(terms, fmt.Sprintf("childResource=v1/configmaps%d", i))
	}
	rec := rig.get(t,
		"/api/v1/clusters/fake/resources/apps/v1/deployments/demo/web/related?"+strings.Join(terms, "&"))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "childResource") {
		t.Errorf("body = %s, want it to name the parameter that was refused", rec.Body.String())
	}
	if n := lists.Load(); n != 0 {
		t.Errorf("%d scans ran for a request that was refused", n)
	}
}

// A custom controller has one or two extra edges, and naming the same one twice
// is not a second scan.
func TestRelatedAcceptsTheChildResourcesAConsoleAsksFor(t *testing.T) {
	rig := hndNewRig(t)
	rec := rig.get(t,
		"/api/v1/clusters/fake/resources/apps/v1/deployments/demo/web/related"+
			"?childResource=v1/configmaps&childResource=v1/configmaps&childResource=v1/secrets")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}
