package api

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// docs/API.md opens with a listing of every route, and it is the first thing
// anyone reads before touching this surface. A listing that has drifted is
// worse than none: it sends people looking for endpoints that were renamed and
// hides ones that were added.
//
// This repository has had to correct that drift by hand before — there is a
// commit called "Bring the documentation back in step with the code" — which is
// the argument for checking it the same way the capability document is checked
// against the router rather than trusting a later reader to notice.

// docRoutePattern matches a line of the surface listing: a verb, then a path.
var docRoutePattern = regexp.MustCompile(`^([A-Z]+)\s+(/\S+)`)

// normaliseDocPath rewrites the listing's readable placeholders into the ones
// chi registers, so the two can be compared at all. The listing says {c} where
// the router says {cluster}, and spells the proxy's type parameter as the two
// values it accepts.
func normaliseDocPath(p string) string {
	p = strings.ReplaceAll(p, "/{c}/", "/{cluster}/")
	p = strings.ReplaceAll(p, "{pods|services}", "{ptype}")
	return p
}

func documentedRoutes(t *testing.T) map[string]bool {
	t.Helper()
	// The package's own directory is internal/api; the docs are two up.
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "API.md"))
	if err != nil {
		t.Fatalf("reading API.md: %v", err)
	}
	body := string(raw)

	const heading = "## The surface"
	i := strings.Index(body, heading)
	if i < 0 {
		t.Fatalf("API.md has no %q section; this test is checking nothing", heading)
	}
	parts := strings.SplitN(body[i:], "```", 3)
	if len(parts) < 3 {
		t.Fatal("the surface section has no fenced listing")
	}

	out := map[string]bool{}
	for _, line := range strings.Split(parts[1], "\n") {
		if m := docRoutePattern.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			out[m[1]+" "+normaliseDocPath(m[2])] = true
		}
	}
	if len(out) == 0 {
		t.Fatal("no routes parsed out of the listing; the format changed")
	}
	return out
}

func servedRoutes(t *testing.T, rig *hndRig) map[string]bool {
	t.Helper()
	mux, ok := rig.router.(chi.Routes)
	if !ok {
		t.Fatal("router is not chi.Routes")
	}
	out := map[string]bool{}
	_ = chi.Walk(mux, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if !strings.HasPrefix(route, "/api/v1/") {
			return nil
		}
		// The proxy is registered for every method by HandleFunc; the listing
		// documents the two it actually serves, and proxy.go enforces that.
		if strings.Contains(route, "/proxy/") && method != http.MethodGet && method != http.MethodHead {
			return nil
		}
		out[method+" "+route] = true
		return nil
	})
	return out
}

// Routes that exist only when OIDC is configured, which the anonymous test rig
// does not register. They are documented, and documented as conditional.
var oidcOnlyRoutes = map[string]bool{
	"GET /api/v1/auth/login":    true,
	"GET /api/v1/auth/callback": true,
	"POST /api/v1/auth/logout":  true,
}

// coveredBy reports whether the listing accounts for a served route.
//
// Two entries in the listing deliberately stand for more than one route, and
// enforcing a line each would make the document worse rather than better:
//
//   - the nine actions are one line reading /actions/{action}, with the nine
//     spelled out in the prose immediately below it;
//   - the proxy is one line under GET, and the prose says it serves GET and
//     HEAD and nothing else.
//
// Anything outside those two conventions has to be written down.
func coveredBy(documented map[string]bool, route string) bool {
	if documented[route] {
		return true
	}
	method, path, _ := strings.Cut(route, " ")
	if strings.Contains(path, "/actions/") {
		prefix, _, _ := strings.Cut(path, "/actions/")
		return documented[method+" "+prefix+"/actions/{action}"]
	}
	if method == http.MethodHead && strings.Contains(path, "/proxy/") {
		return documented[http.MethodGet+" "+path]
	}
	return false
}

func TestEveryServedRouteIsDocumented(t *testing.T) {
	rig := hndNewRig(t)
	documented := documentedRoutes(t)

	for route := range servedRoutes(t, rig) {
		if !coveredBy(documented, route) {
			t.Errorf("%s is served but missing from the surface listing in docs/API.md", route)
		}
	}
}

func TestEveryDocumentedRouteIsServed(t *testing.T) {
	rig := hndNewRig(t)
	served := servedRoutes(t, rig)

	for route := range documentedRoutes(t) {
		if oidcOnlyRoutes[route] {
			continue
		}
		if served[route] {
			continue
		}
		// The actions line stands for the nine concrete routes; it is
		// documented if any of them is served.
		if strings.HasSuffix(route, "/actions/{action}") {
			prefix := strings.TrimSuffix(route, "{action}")
			found := false
			for s := range served {
				if strings.HasPrefix(s, prefix) {
					found = true
					break
				}
			}
			if found {
				continue
			}
		}
		t.Errorf("docs/API.md lists %s, which the router does not serve", route)
	}
}

// HEAD on the proxy is real and worth documenting alongside GET, since the
// listing is where someone checks what the proxy will accept.
func TestProxyMethodsMatchTheListing(t *testing.T) {
	documented := documentedRoutes(t)
	const proxy = "/api/v1/clusters/{cluster}/proxy/{namespace}/{ptype}/{name}/*"
	if !documented["GET "+proxy] && !documented["GET|HEAD "+proxy] {
		t.Errorf("the proxy route is not documented under a GET verb: %v", documented)
	}
}
