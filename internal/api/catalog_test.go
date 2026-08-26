package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/daiwa-zou/orrery/internal/config"
)

// notReadOnly are the GET routes the capability document deliberately omits,
// each with the reason it is not a read. Anything else that appears on the
// router without an entry is drift, and the test below says so.
var notReadOnly = map[string]string{
	"/api/v1/clusters/{cluster}/ws/exec": "opens a shell inside a container; nothing about it is read-only",
	"/api/v1/auth/login":                 "starts a redirect flow rather than serving data",
	"/api/v1/auth/callback":              "completes that flow and mutates the session",
}

func catalogPaths() map[string]bool {
	out := make(map[string]bool, len(readOnlyEndpoints))
	for _, e := range readOnlyEndpoints {
		out[e.Path] = true
	}
	return out
}

// TestCapabilitiesCoversEveryReadRoute is the pin that keeps a hand-written
// document honest: add a GET route without describing it and this fails.
func TestCapabilitiesCoversEveryReadRoute(t *testing.T) {
	rig := hndNewRig(t)
	mux, ok := rig.router.(chi.Routes)
	if !ok {
		t.Fatal("router is not a chi.Routes; the walk below cannot run")
	}
	described := catalogPaths()

	err := chi.Walk(mux, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if method != http.MethodGet || !strings.HasPrefix(route, "/api/v1/") {
			return nil
		}
		if _, excused := notReadOnly[route]; excused {
			return nil
		}
		if !described[route] {
			t.Errorf("GET %s is served but missing from readOnlyEndpoints", route)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the router: %v", err)
	}
}

// TestCapabilitiesDescribesOnlyRealRoutes catches the other direction: an
// entry for a route that was renamed or removed.
func TestCapabilitiesDescribesOnlyRealRoutes(t *testing.T) {
	rig := hndNewRig(t)
	mux := rig.router.(chi.Routes)

	served := map[string]bool{}
	_ = chi.Walk(mux, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if method == http.MethodGet {
			served[route] = true
		}
		return nil
	})
	for _, e := range readOnlyEndpoints {
		if !served[e.Path] {
			t.Errorf("readOnlyEndpoints describes %s %s, which the router does not serve", e.Method, e.Path)
		}
	}
}

func TestCapabilitiesEveryEntryIsUsable(t *testing.T) {
	seen := map[string]bool{}
	for _, e := range readOnlyEndpoints {
		if seen[e.Path] {
			t.Errorf("%s is described twice", e.Path)
		}
		seen[e.Path] = true

		if e.Summary == "" {
			t.Errorf("%s has no summary", e.Path)
		}
		if e.Method != http.MethodGet {
			t.Errorf("%s is listed as %s; the read-only surface is GET only", e.Path, e.Method)
		}
		// A path placeholder nobody documented reaches a caller as an empty
		// description, which is worse than not listing it.
		for _, p := range pathParams(e.Path) {
			if p.Description == "" {
				t.Errorf("%s: path parameter %q has no description", e.Path, p.Name)
			}
		}
		for _, p := range e.Params {
			if p.In != "query" {
				t.Errorf("%s: parameter %q declares in=%q; only query params are hand-written", e.Path, p.Name, p.In)
			}
			if p.Description == "" {
				t.Errorf("%s: query parameter %q has no description", e.Path, p.Name)
			}
		}
	}
}

func TestCapabilitiesEndpoint(t *testing.T) {
	rig := hndNewRig(t)
	rec := rig.get(t, "/api/v1/capabilities")
	hndWantStatus(t, rec, http.StatusOK)

	var body capabilitiesResponse
	hndDecode(t, rec, &body)

	if body.BasePath != "/api/v1" {
		t.Errorf("basePath = %q", body.BasePath)
	}
	if len(body.ReadOnly) == 0 {
		t.Fatal("no endpoints described")
	}
	if len(body.Notes) == 0 || len(body.Placeholders) == 0 {
		t.Error("a caller needs the notes and placeholders to use the paths")
	}

	byPath := map[string]endpoint{}
	for _, e := range body.ReadOnly {
		byPath[e.Path] = e
	}
	for _, want := range []string{
		"/api/v1/clusters/{cluster}/resources/{group}/{version}/{resource}",
		"/api/v1/clusters/{cluster}/resources/{group}/{version}/{resource}/{namespace}/{name}/related",
		"/api/v1/clusters/{cluster}/access",
		"/api/v1/clusters/{cluster}/logs",
		"/api/v1/search",
	} {
		if _, ok := byPath[want]; !ok {
			t.Errorf("%s is not described", want)
		}
	}

	// Path parameters are derived, so the list route must carry all four of
	// its placeholders without anyone having typed them.
	list := byPath["/api/v1/clusters/{cluster}/resources/{group}/{version}/{resource}"]
	got := map[string]endpointParam{}
	for _, p := range list.Params {
		got[p.Name] = p
	}
	for _, name := range []string{"cluster", "group", "version", "resource"} {
		p, ok := got[name]
		if !ok {
			t.Errorf("list route is missing path parameter %q", name)
			continue
		}
		if p.In != "path" || !p.Required {
			t.Errorf("path parameter %q = %+v, want a required path param", name, p)
		}
	}
	if p := got["pageSize"]; p.In != "query" || p.Default != "50" {
		t.Errorf("pageSize = %+v, want a query param defaulting to 50", p)
	}

	// Writes must not appear: this document is handed to callers that must
	// not change anything.
	for _, e := range body.ReadOnly {
		if e.Method != http.MethodGet {
			t.Errorf("%s %s is not a read", e.Method, e.Path)
		}
		if strings.Contains(e.Path, "/actions/") {
			t.Errorf("%s is an action, not a read", e.Path)
		}
	}
	if _, ok := byPath["/api/v1/clusters/{cluster}/ws/exec"]; ok {
		t.Error("exec is described as read-only")
	}
}

// TestCapabilitiesFollowsTheBuild checks that a server which does not serve
// the proxy does not advertise it — the reason this is served rather than
// written down once in the docs.
func TestCapabilitiesFollowsTheBuild(t *testing.T) {
	on := hndNewRig(t)
	var enabled capabilitiesResponse
	hndDecode(t, on.get(t, "/api/v1/capabilities"), &enabled)
	if !enabled.Features["proxy"] {
		t.Fatal("the proxy is on by default; capabilities says otherwise")
	}
	if !describes(enabled, "/api/v1/clusters/{cluster}/proxy/{namespace}/{ptype}/{name}/*") {
		t.Error("the proxy route is served but not described")
	}

	off := hndNewRigWith(t, func(c *config.Config) { c.Proxy.Enabled = ptr(false) })
	var disabled capabilitiesResponse
	hndDecode(t, off.get(t, "/api/v1/capabilities"), &disabled)
	if disabled.Features["proxy"] {
		t.Error("the proxy is disabled but capabilities advertises it")
	}
	if describes(disabled, "/api/v1/clusters/{cluster}/proxy/{namespace}/{ptype}/{name}/*") {
		t.Error("a disabled proxy is still described; a caller would get a 404 from a documented route")
	}
	// The rest of the surface is unaffected.
	if !describes(disabled, "/api/v1/clusters/{cluster}/overview") {
		t.Error("disabling the proxy dropped unrelated endpoints")
	}
}

func describes(resp capabilitiesResponse, path string) bool {
	for _, e := range resp.ReadOnly {
		if e.Path == path {
			return true
		}
	}
	return false
}
