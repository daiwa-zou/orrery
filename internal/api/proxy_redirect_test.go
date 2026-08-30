package api

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// The proxy relays a request into a workload under the cluster's credentials.
// Following a redirect out of that workload would carry them along: net/http
// drops Authorization when a redirect changes host, but client-go's transport
// puts it back — it adds the header inside RoundTrip to every request without
// one, and a redirected request is one of those. The impersonation headers ride
// the same path.
//
// So the workload gets to choose where the dashboard's bearer token goes, and
// on a shared cluster the workload can be one the caller deployed.
func TestProxyDoesNotFollowAWorkloadsRedirect(t *testing.T) {
	var reached atomic.Bool
	var sawAuth atomic.Bool
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Store(true)
		if r.Header.Get("Authorization") != "" {
			sawAuth.Store(true)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer elsewhere.Close()

	rig := hndNewRig(t)
	rig.fake.proxyRedirectTo = elsewhere.URL + "/collect"

	rec := rig.get(t, "/api/v1/clusters/fake/proxy/demo/services/svc:80/index.html")

	// Not being reached at all is the property under test. The rig's
	// kubeconfig carries no bearer token, so the Authorization check below can
	// only ever confirm what this one already establishes — it is here to fail
	// loudly if the fixture ever grows credentials and the redirect is
	// followed again.
	if reached.Load() {
		t.Error("the proxy followed the workload's redirect off-cluster")
	}
	if sawAuth.Load() {
		t.Error("the dashboard's credentials were sent to the redirect target")
	}

	// Handed to the browser instead, which is what the Location pass-through
	// below the round trip has always been for.
	if rec.Code != http.StatusFound {
		t.Errorf("proxy returned %d, want the workload's 302 passed through", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != elsewhere.URL+"/collect" {
		t.Errorf("Location = %q, want the workload's own", got)
	}
}
