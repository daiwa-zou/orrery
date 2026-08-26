package api

import (
	"net/http"
	"strings"
	"sync"
	"testing"
)

// The proxy relays one workload's HTTP through the API server's proxy
// subresource, gated on `get` of pods/proxy for the named pod. The suffix a
// caller supplies is joined onto that subresource path by client-go, and the
// join runs path.Clean — so a ".." segment does not stay inside the workload.
// It walks back up the API server's own path space:
//
//	.../pods/web-1/proxy + "a/../../../../secrets" -> /api/v1/namespaces/demo/secrets
//
// The request that then reaches the API server is a plain collection read. In
// serviceaccount auth mode it travels under the dashboard's own credentials,
// because that is what ClientsFor returns there, so the only thing that ever
// gated it was our review of one pod's proxy subresource. A caller allowed to
// read one pod's HTTP port could read every Secret the dashboard can read.

// hndTracedRig records every path the fake API server is asked for.
func hndTracedRig(t *testing.T) (*hndRig, func() []string) {
	t.Helper()
	rig := hndNewRig(t)
	var mu sync.Mutex
	var seen []string
	rig.fake.setTrace(func(p string) {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, p)
	})
	return rig, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), seen...)
	}
}

// upstreamOutsideProxy returns the paths that did not stay inside a proxy
// subresource, ignoring the discovery and access-review chatter every request
// makes.
func upstreamOutsideProxy(paths []string) []string {
	var out []string
	for _, p := range paths {
		switch {
		case strings.Contains(p, "/proxy"):
		case strings.HasPrefix(p, "/api/v1/namespaces/") && !strings.Contains(p, "/proxy"):
			out = append(out, p)
		}
	}
	return out
}

func TestProxyRefusesTraversalOutOfTheSubresource(t *testing.T) {
	cases := []struct {
		name, suffix string
	}{
		{"walks up to a sibling collection", "a/../../../../secrets"},
		{"leading dot-dot", "../../../../api/v1/nodes"},
		{"single dot-dot", ".."},
		{"dot-dot mid-path", "static/../../../../secrets"},
		{"current-directory segment", "./metrics"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rig, seen := hndTracedRig(t)

			rec := rig.get(t, "/api/v1/clusters/fake/proxy/demo/pods/web-1/"+tc.suffix)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400; body: %s", rec.Code, rec.Body.String())
			}
			// The status code is not the point. The point is that nothing left
			// the subresource, because what leaves it is read with the
			// dashboard's credentials.
			if escaped := upstreamOutsideProxy(seen()); len(escaped) > 0 {
				t.Fatalf("a request escaped the proxy subresource: %v", escaped)
			}
		})
	}
}

// The escape had to stay refused for the encoded spelling too, if the router
// ever starts decoding it before the handler sees it.
func TestProxyRefusesEncodedTraversal(t *testing.T) {
	rig, seen := hndTracedRig(t)

	rec := rig.get(t, "/api/v1/clusters/fake/proxy/demo/pods/web-1/..%2f..%2f..%2fsecrets")
	// Either refused outright or passed through as one literal segment; what
	// must not happen is landing on the secrets collection.
	if escaped := upstreamOutsideProxy(seen()); len(escaped) > 0 {
		t.Fatalf("encoded traversal escaped: %v (status %d)", escaped, rec.Code)
	}
}

// The ordinary case has to keep working, including the deep paths a real
// workload serves and the :port form.
func TestProxyStillRelaysOrdinaryPaths(t *testing.T) {
	rig, seen := hndTracedRig(t)

	for _, suffix := range []string{"", "metrics", "static/app.css", "a/b/c/d"} {
		rec := rig.get(t, "/api/v1/clusters/fake/proxy/demo/pods/web-1/"+suffix)
		if rec.Code != http.StatusOK {
			t.Errorf("suffix %q: status = %d, want 200; body: %s", suffix, rec.Code, rec.Body.String())
		}
		if body := rec.Body.String(); !strings.Contains(body, "hello-from-proxy") {
			t.Errorf("suffix %q: body = %q, want the proxied response", suffix, body)
		}
	}
	if escaped := upstreamOutsideProxy(seen()); len(escaped) > 0 {
		t.Fatalf("an ordinary path escaped the subresource: %v", escaped)
	}
}

func TestProxyRelaysThroughAPortedName(t *testing.T) {
	rig := hndNewRig(t)
	rec := rig.get(t, "/api/v1/clusters/fake/proxy/demo/pods/web-1:8080/metrics")
	hndWantStatus(t, rec, http.StatusOK)
}

// Unchanged, and worth keeping pinned next to the escape: the proxy is
// read-only because whatever it returns renders inside the console's origin.
func TestProxyRemainsReadOnly(t *testing.T) {
	rig, seen := hndTracedRig(t)

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		rec := rig.do(t, method, "/api/v1/clusters/fake/proxy/demo/pods/web-1/", "", nil)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: status = %d, want 405", method, rec.Code)
		}
	}
	if escaped := upstreamOutsideProxy(seen()); len(escaped) > 0 {
		t.Fatalf("a rejected method still reached the cluster: %v", escaped)
	}
}
