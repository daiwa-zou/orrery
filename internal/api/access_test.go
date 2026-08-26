package api

import (
	"net/http"
	"reflect"
	"testing"
)

func TestAccessProbeDefaultsToTheReadVerbs(t *testing.T) {
	rig := hndNewRig(t)
	rec := rig.get(t, "/api/v1/clusters/fake/access?resource=pods&namespace=demo")
	hndWantStatus(t, rec, http.StatusOK)

	var body accessProbeResponse
	hndDecode(t, rec, &body)

	if want := []string{"get", "list", "watch"}; !reflect.DeepEqual(body.Allowed, want) {
		t.Errorf("allowed = %v, want %v", body.Allowed, want)
	}
	if len(body.Results) != 3 {
		t.Errorf("results = %v, want one entry per read verb", body.Results)
	}
	if body.Resource.Name != "pods" || body.Resource.Kind != "Pod" || body.Resource.Version != "v1" {
		t.Errorf("resource = %+v", body.Resource)
	}
	if body.Namespace != "demo" || body.Cluster != "fake" {
		t.Errorf("cluster/namespace = %q/%q", body.Cluster, body.Namespace)
	}
}

// Two ways to ask one question is two ways to get different answers. The GET
// exists so a caller with no CSRF token can ask; it would be worthless if it
// disagreed with the batch POST the console uses.
func TestAccessProbeAgreesWithTheBatchPOST(t *testing.T) {
	rig := hndNewRig(t)
	rig.fake.denyResource = "secrets"

	for _, resource := range []string{"pods", "secrets"} {
		probe := rig.get(t, "/api/v1/clusters/fake/access?namespace=demo&verb=list&resource="+resource)
		hndWantStatus(t, probe, http.StatusOK)
		var got accessProbeResponse
		hndDecode(t, probe, &got)

		batch := rig.do(t, http.MethodPost, "/api/v1/clusters/fake/access",
			`{"checks":[{"verb":"list","version":"v1","resource":"`+resource+`","namespace":"demo"}]}`,
			map[string]string{"Content-Type": "application/json"})
		hndWantStatus(t, batch, http.StatusOK)
		var want struct {
			Results map[string]struct {
				Allowed bool `json:"allowed"`
			} `json:"results"`
		}
		hndDecode(t, batch, &want)

		if got.Results["list"].Allowed != want.Results["0"].Allowed {
			t.Errorf("%s: GET says allowed=%v, POST says allowed=%v",
				resource, got.Results["list"].Allowed, want.Results["0"].Allowed)
		}
	}
}

func TestAccessProbeReportsDenial(t *testing.T) {
	rig := hndNewRig(t)
	rig.fake.denyResource = "secrets"

	rec := rig.get(t, "/api/v1/clusters/fake/access?resource=secrets&namespace=demo&verb=get,list")
	hndWantStatus(t, rec, http.StatusOK)

	var body accessProbeResponse
	hndDecode(t, rec, &body)

	// A denial is an answer, not an error: the caller asked a question and
	// gets "no", which is exactly what it needs to say so to a user.
	if len(body.Allowed) != 0 {
		t.Errorf("allowed = %v, want nothing", body.Allowed)
	}
	for _, verb := range []string{"get", "list"} {
		d, ok := body.Results[verb]
		if !ok {
			t.Fatalf("no verdict for %q", verb)
		}
		if d.Allowed {
			t.Errorf("%s came back allowed against a denying cluster", verb)
		}
	}
}

func TestAccessProbeAcceptsAnySpelling(t *testing.T) {
	rig := hndNewRig(t)
	// A kind, the way an owner reference or a manifest spells it.
	rec := rig.get(t, "/api/v1/clusters/fake/access?resource=Deployment&verb=get")
	hndWantStatus(t, rec, http.StatusOK)

	var body accessProbeResponse
	hndDecode(t, rec, &body)
	if body.Resource.Name != "deployments" || body.Resource.Group != "apps" {
		t.Errorf("resource = %+v, want apps/deployments", body.Resource)
	}
}

func TestAccessProbeIgnoresNamespaceForClusterScope(t *testing.T) {
	rig := hndNewRig(t)
	rec := rig.get(t, "/api/v1/clusters/fake/access?resource=nodes&namespace=demo&verb=list")
	hndWantStatus(t, rec, http.StatusOK)

	var body accessProbeResponse
	hndDecode(t, rec, &body)
	// Asking about a namespace for a cluster-scoped resource is a question
	// with no meaning; answering the cluster-wide one is the honest reading.
	if body.Namespace != "" {
		t.Errorf("namespace = %q, want it dropped for a cluster-scoped resource", body.Namespace)
	}
}

func TestAccessProbeRejections(t *testing.T) {
	rig := hndNewRig(t)
	cases := []struct {
		name, query string
		want        int
	}{
		{"no resource", "verb=get", http.StatusBadRequest},
		{"unknown resource", "resource=widgetses", http.StatusNotFound},
		{
			"too many verbs",
			"resource=pods&verb=a,b,c,d,e,f,g,h,i,j,k,l,m,n,o,p,q",
			http.StatusBadRequest,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := rig.get(t, "/api/v1/clusters/fake/access?"+tc.query)
			hndWantStatus(t, rec, tc.want)
		})
	}
}

func TestProbeVerbs(t *testing.T) {
	got, err := probeVerbs(nil)
	if err != nil || !reflect.DeepEqual(got, readVerbs) {
		t.Errorf("no verbs = %v, %v; want the read verbs", got, err)
	}

	// Repeated and comma-separated spellings are the same ask, and a
	// duplicate must not buy a second review.
	got, err = probeVerbs([]string{"get,list", "list", " WATCH "})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"get", "list", "watch"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}

	if _, err := probeVerbs([]string{"a,b,c,d,e,f,g,h,i,j,k,l,m,n,o,p,q"}); err == nil {
		t.Error("seventeen verbs were accepted")
	}
}

func TestNamespaceAccessClusterWide(t *testing.T) {
	rig := hndNewRig(t)
	rec := rig.get(t, "/api/v1/clusters/fake/access/namespaces?resource=pods")
	hndWantStatus(t, rec, http.StatusOK)

	var body namespaceAccessResponse
	hndDecode(t, rec, &body)
	if !body.AllNamespaces {
		t.Errorf("allNamespaces = false for an unrestricted caller: %+v", body)
	}
	// Enumerating every namespace when the answer is "all of them" is noise,
	// and goes stale the moment a namespace is created.
	if len(body.Namespaces) != 0 {
		t.Errorf("namespaces = %v, want them omitted when the grant is cluster-wide", body.Namespaces)
	}
	if body.Verb != "list" {
		t.Errorf("verb = %q, want list by default", body.Verb)
	}
}

// The point of the endpoint: a caller refused cluster-wide learns where it may
// look instead, rather than concluding there is nothing to see.
func TestNamespaceAccessEnumeratesWhenNarrowlyBound(t *testing.T) {
	rig := hndNewRig(t)
	rig.fake.nsOnlyResource = "pods"

	rec := rig.get(t, "/api/v1/clusters/fake/access/namespaces?resource=pods")
	hndWantStatus(t, rec, http.StatusOK)

	var body namespaceAccessResponse
	hndDecode(t, rec, &body)
	if body.AllNamespaces {
		t.Fatal("allNamespaces = true although the cluster-wide review was denied")
	}
	if len(body.Namespaces) == 0 {
		t.Fatalf("no namespaces returned: %+v", body)
	}
	found := false
	for _, ns := range body.Namespaces {
		if ns == "demo" {
			found = true
		}
	}
	if !found {
		t.Errorf("namespaces = %v, want demo among them", body.Namespaces)
	}
}

func TestNamespaceAccessClusterScopedResource(t *testing.T) {
	rig := hndNewRig(t)
	rec := rig.get(t, "/api/v1/clusters/fake/access/namespaces?resource=nodes")
	hndWantStatus(t, rec, http.StatusOK)

	var body namespaceAccessResponse
	hndDecode(t, rec, &body)
	if !body.AllNamespaces || len(body.Namespaces) != 0 {
		t.Errorf("body = %+v, want the cluster-wide verdict and no namespace list", body)
	}
}

func TestNamespaceAccessDenied(t *testing.T) {
	rig := hndNewRig(t)
	rig.fake.denyResource = "secrets"

	rec := rig.get(t, "/api/v1/clusters/fake/access/namespaces?resource=secrets")
	hndWantStatus(t, rec, http.StatusOK)

	var body namespaceAccessResponse
	hndDecode(t, rec, &body)
	// "Nowhere" is an answer. Returning 403 here would make the caller unable
	// to distinguish it from a broken request.
	if body.AllNamespaces || len(body.Namespaces) != 0 {
		t.Errorf("body = %+v, want an empty scope", body)
	}
}

func TestNamespaceAccessRejections(t *testing.T) {
	rig := hndNewRig(t)
	hndWantStatus(t, rig.get(t, "/api/v1/clusters/fake/access/namespaces"), http.StatusBadRequest)
	hndWantStatus(t, rig.get(t, "/api/v1/clusters/fake/access/namespaces?resource=nope"), http.StatusNotFound)
	hndWantStatus(t, rig.get(t, "/api/v1/clusters/nope/access?resource=pods"), http.StatusNotFound)
}
