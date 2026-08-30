package api

// Router-level tests: CORS, the SPA fallback, the JSON 404, the read-only
// proxy, and the cheap pre-upgrade error paths of the streaming endpoints.
// Full WebSocket sessions are deliberately out of scope — everything after a
// successful upgrade needs a live socket, which httptest recorders cannot be.

import (
	"net/http"
	"strings"
	"testing"
)

func TestRouterAndStreamErrorPathsHTTP(t *testing.T) {
	rig := hndNewRig(t)

	t.Run("apiNotFoundIsJSON", func(t *testing.T) {
		rec := rig.get(t, "/api/v1/nonsense")
		hndWantStatus(t, rec, 404)
		var body errorBody
		hndDecode(t, rec, &body)
		if body.Error != "not_found" {
			t.Errorf("error = %q", body.Error)
		}
	})

	t.Run("spaFallback", func(t *testing.T) {
		rec := rig.get(t, "/")
		hndWantStatus(t, rec, 200)
		if !strings.Contains(rec.Body.String(), "orrery-index") {
			t.Errorf("index body = %q", rec.Body.String())
		}

		// Deep links into client-side routes must serve the shell, uncached.
		rec = rig.get(t, "/clusters/fake/pods")
		hndWantStatus(t, rec, 200)
		if !strings.Contains(rec.Body.String(), "orrery-index") {
			t.Errorf("deep link body = %q", rec.Body.String())
		}
		if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
			t.Errorf("index cache-control = %q", cc)
		}
	})

	t.Run("spaHashedAssetsAreImmutable", func(t *testing.T) {
		rec := rig.get(t, "/assets/app.js")
		hndWantStatus(t, rec, 200)
		if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
			t.Errorf("asset cache-control = %q", cc)
		}
	})

	t.Run("cors", func(t *testing.T) {
		rec := rig.do(t, http.MethodGet, "/api/v1/healthz", "", map[string]string{"Origin": "http://cors.example"})
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://cors.example" {
			t.Errorf("allowed origin echoed %q", got)
		}
		if rec.Header().Get("Access-Control-Allow-Credentials") != "true" {
			t.Error("credentials header missing for allowed origin")
		}

		rec = rig.do(t, http.MethodGet, "/api/v1/healthz", "", map[string]string{"Origin": "http://evil.example"})
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Errorf("disallowed origin got %q", got)
		}

		rec = rig.do(t, http.MethodOptions, "/api/v1/clusters", "", map[string]string{"Origin": "http://cors.example"})
		hndWantStatus(t, rec, 204)
	})

	// Refusing to put CORS headers on the response only stops the attacker
	// reading the answer. The write has already happened by then, and with
	// authentication off there is no CSRF token in the way — the rig runs
	// anonymous, which is exactly the mode this guards.
	//
	// text/plain is the shape that matters: it is a CORS "simple request", so
	// the browser sends it with no preflight to refuse, and the manifest
	// decoder never looks at Content-Type anyway.
	t.Run("crossOriginWriteRefused", func(t *testing.T) {
		body := `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"pwned","namespace":"demo"}}`
		rec := rig.do(t, http.MethodPost, "/api/v1/clusters/fake/resources/_/v1/configmaps", body,
			map[string]string{
				"Origin":       "http://evil.example",
				"Content-Type": "text/plain",
			})
		hndWantStatus(t, rec, 403)
		if !strings.Contains(rec.Body.String(), "cross_origin") {
			t.Errorf("body = %s, want it to name the cross-origin refusal", rec.Body.String())
		}
	})

	// A client with no Origin at all is not a browser page, and the console's
	// own page must still be able to write.
	t.Run("sameOriginAndOriginlessWritesPass", func(t *testing.T) {
		body := `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"ok","namespace":"demo"}}`
		for _, hdr := range []map[string]string{
			nil,
			{"Origin": "http://cors.example"},
		} {
			rec := rig.do(t, http.MethodPost, "/api/v1/clusters/fake/resources/_/v1/configmaps", body, hdr)
			if rec.Code == http.StatusForbidden &&
				strings.Contains(rec.Body.String(), "cross_origin") {
				t.Errorf("headers %v were refused as cross-origin: %s", hdr, rec.Body.String())
			}
		}
	})

	t.Run("watchPreUpgradeErrors", func(t *testing.T) {
		// A bad selector must be a plain 400 before any upgrade is attempted.
		rec := rig.get(t, "/api/v1/clusters/fake/ws/watch/_/v1/pods?labelSelector=%3D%3Dbad")
		hndWantStatus(t, rec, 400)

		// Unknown resources 404 the same way the list endpoint does.
		rec = rig.get(t, "/api/v1/clusters/fake/ws/watch/_/v1/gizmos")
		hndWantStatus(t, rec, 404)

		// Authorized but not a WebSocket handshake: the upgrader answers 400.
		// This still walks resolve + the watch access review.
		rec = rig.get(t, "/api/v1/clusters/fake/ws/watch/_/v1/pods")
		hndWantStatus(t, rec, 400)
	})

	t.Run("watchOriginRejected", func(t *testing.T) {
		// A cross-site handshake dies on the Origin check — the CSRF stand-in
		// for streams — before any subscription is created.
		rec := rig.do(t, http.MethodGet, "/api/v1/clusters/fake/ws/watch/_/v1/pods", "", map[string]string{
			"Origin":                "http://evil.example",
			"Connection":            "Upgrade",
			"Upgrade":               "websocket",
			"Sec-WebSocket-Version": "13",
			"Sec-WebSocket-Key":     "dGhlIHNhbXBsZSBub25jZQ==",
		})
		hndWantStatus(t, rec, 403)
	})

	t.Run("logsStreamParamErrors", func(t *testing.T) {
		rec := rig.get(t, "/api/v1/clusters/fake/ws/logs")
		hndWantStatus(t, rec, 400)
		rec = rig.get(t, "/api/v1/clusters/fake/ws/logs?namespace=demo")
		hndWantStatus(t, rec, 400)
	})

	t.Run("execParamErrors", func(t *testing.T) {
		rec := rig.get(t, "/api/v1/clusters/fake/ws/exec?pod=web-1")
		hndWantStatus(t, rec, 400)
	})

	t.Run("proxy", func(t *testing.T) {
		rec := rig.do(t, http.MethodPost, "/api/v1/clusters/fake/proxy/demo/services/svc/index.html", "x", nil)
		hndWantStatus(t, rec, 405)

		rec = rig.get(t, "/api/v1/clusters/fake/proxy/demo/widgets/svc/index.html")
		hndWantStatus(t, rec, 400)

		rec = rig.get(t, "/api/v1/clusters/fake/proxy/demo/services/svc:80/index.html")
		hndWantStatus(t, rec, 200)
		if rec.Body.String() != "hello-from-proxy" {
			t.Errorf("proxied body = %q", rec.Body.String())
		}
		// Whatever the workload serves must not script against the dashboard.
		if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "sandbox") {
			t.Errorf("proxy CSP = %q", csp)
		}
	})
}
