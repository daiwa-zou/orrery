package api

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

// The per-namespace fallback scan asks "where may this user read?" by checking
// each namespace in turn. Its candidate list comes from namespaceNames, and an
// empty candidate list is indistinguishable, downstream, from a user allowed
// nowhere: VisibleNamespaces scans nothing, finds nothing, reports success,
// and caches that for its TTL. Every caller then renders a 403.
//
// So a hiccup listing namespaces used to become "you are not allowed to list
// pods" — cached, and wrong. On a deployment whose own service account cannot
// list namespaces it was not a hiccup but a permanent state, and no RBAC
// change made for the *user* would ever fix it.

func TestNamespaceNamesReportsFailureRatherThanEmptiness(t *testing.T) {
	rig := hndNewRig(t)

	c, err := rig.api.registry.Get("fake")
	if err != nil {
		t.Fatal(err)
	}

	names, err := rig.api.namespaceNames(t.Context(), c)
	if err != nil {
		t.Fatalf("listing namespaces failed: %v", err)
	}
	if len(names) == 0 {
		t.Fatal("the fixture has namespaces; got none")
	}

	// Now the cluster stops serving them.
	rig.fake.hideResource = "namespaces"
	c.Discovery.Invalidate()

	names, err = rig.api.namespaceNames(t.Context(), c)
	if err == nil {
		t.Fatal("an unreadable namespace list came back as a successful empty list")
	}
	if !errors.Is(err, errNoNamespaceScan) {
		t.Errorf("error = %v, want it to carry errNoNamespaceScan", err)
	}
	if len(names) != 0 {
		t.Errorf("names = %v, want none alongside the error", names)
	}
}

// The end-to-end shape of the bug: a narrowly bound user, whose answer depends
// entirely on the fallback scan, must not be told they lack a permission when
// the truth is that the question could not be asked.
func TestListDoesNotBlameRBACWhenNamespacesCannotBeListed(t *testing.T) {
	rig := hndNewRig(t)
	// Deny only the cluster-wide review, which is what forces the fallback,
	// and take the namespace list away before anything has been cached. A
	// warm authz cache would answer from a previous good scan — correctly —
	// and would not exercise this path at all.
	rig.fake.nsOnlyResource = "pods"
	rig.fake.hideResource = "namespaces"

	rec := rig.get(t, "/api/v1/clusters/fake/resources/core/v1/pods")
	if rec.Code == http.StatusForbidden {
		t.Fatalf("a namespace-listing failure was reported as an RBAC denial: %s", rec.Body.String())
	}
	if rec.Code == http.StatusOK {
		t.Fatalf("an unanswerable scope question returned a list anyway: %s", rec.Body.String())
	}
	body := decodeErrBody(t, rec)
	if !strings.Contains(body.Reason, "namespaces") {
		t.Errorf("reason = %q, want it to name what could not be read", body.Reason)
	}
}

// The same question asked directly. access/namespaces exists to tell a client
// where it may read; answering "nowhere" when the scan could not run would
// send it to exactly the wrong conclusion.
func TestNamespaceAccessDoesNotReportNowhereOnFailure(t *testing.T) {
	rig := hndNewRig(t)
	rig.fake.nsOnlyResource = "pods"
	rig.fake.hideResource = "namespaces"
	c, err := rig.api.registry.Get("fake")
	if err != nil {
		t.Fatal(err)
	}
	c.Discovery.Invalidate()

	rec := rig.get(t, "/api/v1/clusters/fake/access/namespaces?resource=pods")
	if rec.Code == http.StatusOK {
		var body namespaceAccessResponse
		hndDecode(t, rec, &body)
		if !body.AllNamespaces && len(body.Namespaces) == 0 {
			t.Fatal("reported an empty scope for a scan that could not run")
		}
	}
	if rec.Code == http.StatusForbidden {
		t.Errorf("a scan failure was reported as a permission problem: %s", rec.Body.String())
	}
}

// A cluster-wide reader never reaches the fallback scan, so losing the
// namespace list must not affect them at all.
func TestClusterWideReaderUnaffectedByNamespaceScan(t *testing.T) {
	rig := hndNewRig(t)
	rig.fake.hideResource = "namespaces"
	c, err := rig.api.registry.Get("fake")
	if err != nil {
		t.Fatal(err)
	}
	c.Discovery.Invalidate()

	rec := rig.get(t, "/api/v1/clusters/fake/resources/core/v1/pods")
	hndWantStatus(t, rec, http.StatusOK)
}
