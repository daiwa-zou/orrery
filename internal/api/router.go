package api

import (
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/daiwa-zou/orrery/internal/auth"
	"github.com/daiwa-zou/orrery/internal/webfs"
)

// pathNamespace reads the namespace path segment. Cluster-scoped resources use
// the "_" placeholder so one route shape serves both scopes.
func pathNamespace(r *http.Request) string {
	ns := chi.URLParam(r, "namespace")
	if ns == "_" || ns == "-" {
		return ""
	}
	return ns
}

// Router builds the complete HTTP surface.
func (a *API) Router() http.Handler {
	r := chi.NewRouter()

	// No RealIP middleware: it rewrites RemoteAddr from spoofable headers
	// (X-Forwarded-For et al.) whether or not a trusted proxy set them, and
	// nothing here consumes the client IP anyway.
	r.Use(middleware.RequestID)
	r.Use(a.recoverer)
	r.Use(a.observe)
	// Compression pays off heavily on list payloads, which are repetitive
	// JSON. WebSocket upgrades bypass it via their own negotiation.
	r.Use(middleware.Compress(5, "application/json", "text/plain", "application/yaml"))
	r.Use(a.cors)

	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		})
		r.Get("/auth/config", a.authConfig)

		if a.authn != nil {
			r.Get("/auth/login", a.authn.Login)
			r.Get("/auth/callback", a.authn.Callback)
		}

		r.Group(func(r chi.Router) {
			r.Use(a.mw.Authenticated)

			if a.authn != nil {
				// Logout is a state-changing POST like any other; without the
				// CSRF check any origin could force-logout a signed-in user.
				r.With(a.mw.CSRF).Post("/auth/logout", a.authn.Logout)
			}

			r.Get("/me", a.whoami)
			// The read-only surface, described for whoever has to call it
			// from a program rather than a browser.
			r.Get("/capabilities", a.capabilities)
			r.Get("/clusters", a.listClusters)
			// Cross-cluster, so it sits above the per-cluster routes rather
			// than inside them.
			r.Get("/search", a.searchResources)

			r.Route("/clusters/{cluster}", func(r chi.Router) {
				// ---- reads ----
				r.Get("/discovery", a.listAPIResources)
				r.Get("/overview", a.clusterOverview)
				r.Get("/stats", a.cacheStats)
				r.Get("/events", a.listEvents)
				// Asking whether a read is permitted is itself a read: it is a
				// GET so a client that never writes needs no CSRF token.
				r.Get("/access", a.accessProbe)
				r.Get("/access/namespaces", a.namespaceAccess)
				r.Get("/metrics/nodes", a.nodeMetrics)
				r.Get("/metrics/pods", a.podMetrics)
				r.Get("/pods/{namespace}/{name}/logs", a.getPodLogs)
				r.Get("/pods/{namespace}/{name}/env", a.podEnv)
				// The snapshot half of /ws/logs, for callers that ask a
				// question rather than watch a rollout.
				r.Get("/logs", a.podsLogSnapshot)

				r.Get("/resources/{group}/{version}/{resource}", a.listResources)
				r.Get("/resources/{group}/{version}/{resource}/facets", a.listFacets)
				r.Get("/resources/{group}/{version}/{resource}/{namespace}/{name}", a.getResource)
				r.Get("/resources/{group}/{version}/{resource}/{namespace}/{name}/related", a.relatedResources)
				r.Get("/rollout/history", a.rolloutHistory)
				r.Get("/explain", a.explainHandler)
				// Read-only HTTP proxy into pods and services — the browser's
				// kubectl port-forward. GET/HEAD only, enforced inside.
				// Registered only when enabled, so a disabled proxy is absent
				// rather than merely hidden: there is no route to reach by
				// typing the URL, and no handler to reason about.
				if a.cfg.Proxy.ProxyEnabled() {
					r.HandleFunc("/proxy/{namespace}/{ptype}/{name}/*", a.proxyHTTP)
				}

				// ---- streams ----
				// A browser cannot attach a CSRF header to a WebSocket
				// handshake, so these are guarded by the Origin check in the
				// upgrader instead.
				r.Get("/ws/watch/{group}/{version}/{resource}", a.watchResources)
				r.Get("/ws/logs", a.streamPodLogs)
				r.Get("/ws/exec", a.execIntoPod)

				// ---- writes ----
				r.Group(func(r chi.Router) {
					r.Use(a.sameOriginWrite)
					r.Use(a.mw.CSRF)

					r.Post("/access", a.checkAccess)

					r.Post("/resources/{group}/{version}/{resource}", a.createResource)
					r.Put("/resources/{group}/{version}/{resource}/{namespace}/{name}", a.updateResource)
					r.Patch("/resources/{group}/{version}/{resource}/{namespace}/{name}", a.patchResource)
					r.Delete("/resources/{group}/{version}/{resource}/{namespace}/{name}", a.deleteResource)

					r.Post("/actions/scale", a.scaleWorkload)
					r.Post("/actions/restart", a.restartWorkload)
					r.Post("/actions/rollout-undo", a.rolloutUndo)
					r.Post("/actions/trigger-cronjob", a.triggerCronJob)
					r.Post("/actions/suspend-cronjob", a.suspendCronJob)
					r.Post("/actions/cordon", a.cordonNode)
					r.Post("/actions/drain", a.drainNode)
					r.Post("/actions/evict", a.evictPod)
					r.Post("/actions/debug", a.debugPod)
				})
			})
		})

		r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusNotFound, errorBody{Error: "not_found", Code: 404})
		})
	})

	if a.cfg.Server.WebRoot != "" {
		r.NotFound(spaHandler(os.DirFS(a.cfg.Server.WebRoot)))
	} else if bundle := webfs.FS(); bundle != nil {
		// Release binaries carry the frontend; webRoot still wins when set so
		// a bundled binary can serve a newer or patched build from disk.
		r.NotFound(spaHandler(bundle))
	}
	return r
}

// recoverer turns a handler panic into a 500 rather than killing the process,
// and logs it with the request path so it can be found again.
//
// http.ErrAbortHandler is passed back up rather than merely left unlogged.
// Recovering it at all is what breaks it: net/http gives that value its meaning
// in its own deferred recover, where it drops the connection without a reply
// and without a stack trace, and a panic this middleware has already caught
// never reaches there. Skipping the log and returning normally is therefore not
// "quietly aborting" — it is the opposite. The handler stops mid-response and
// the server finishes the response for it, so a client that was supposed to see
// a broken connection sees a complete and truncated one instead, which is the
// state ErrAbortHandler exists to avoid. Re-panicking is what chi's own
// Recoverer does, for this reason.
func (a *API) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			if rec == http.ErrAbortHandler {
				panic(rec)
			}
			a.log.Error("panic serving request",
				"path", r.URL.Path, "method", r.Method, "panic", rec)
			writeJSON(w, http.StatusInternalServerError, errorBody{
				Error: "internal", Reason: "the server hit an unexpected error", Code: 500,
			})
		}()
		next.ServeHTTP(w, r)
	})
}

// sameOriginWrite refuses a state-changing request sent by a page on another
// origin.
//
// The CSRF token is the defence on these routes, and it is skipped entirely
// when OIDC is off — on the reasoning that a deployment with no sessions has
// no session to forge. What that reasoning leaves out is which machine the
// request comes from. A dashboard on localhost is reachable by the developer's
// own browser and by nothing else on the internet, so a page they happen to
// have open is the one attacker positioned to reach it, and it needs no token
// to send a form post. Nor is a preflight in the way: none of these handlers
// looks at Content-Type, so text/plain carries a manifest perfectly well and
// text/plain is a request the browser sends without asking permission first.
//
// The Origin header is what separates the console's own page from somebody
// else's, and it is the same check the WebSocket upgrader already stands on
// for streams that cannot carry a token. A request with no Origin at all is
// not a browser page — curl, a script, a test — and is left alone; the ambient
// cookie is what a cross-site request has and a command-line client does not.
//
// In OIDC mode this is a second lock on a door the CSRF token already holds.
func (a *API) sameOriginWrite(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && !auth.OriginAllowed(origin, a.cfg.Server.PublicURL, a.cfg.Server.CORSOrigins) {
			// The origin and the expected one are both named, because the
			// other way to arrive here is a publicURL that does not match
			// where the console is actually served from — and a bare
			// "forbidden" sends that reader to look at RBAC, which is not
			// where the problem is.
			writeJSON(w, http.StatusForbidden, errorBody{
				Error: "cross_origin",
				Reason: fmt.Sprintf(
					"this write came from %s, which is not %q and not a configured allowed origin",
					origin, a.cfg.Server.PublicURL),
				Code: http.StatusForbidden,
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// cors permits the configured origins, which matters only when the SPA is
// served from a different host than the API.
func (a *API) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && originAllowed(origin, a.cfg.Server.CORSOrigins) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-CSRF-Token")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// originAllowed matches exact origins only. There is deliberately no "*"
// wildcard: every CORS response here carries Allow-Credentials, and
// reflecting arbitrary origins with credentials would let any website read
// cluster data with the visitor's session. Config validation rejects "*".
func originAllowed(origin string, allowed []string) bool {
	for _, a := range allowed {
		if strings.EqualFold(a, origin) {
			return true
		}
	}
	return false
}

// assetPrefix is where Vite writes hashed output; it is the build's own
// namespace rather than a path the client router may ever claim.
const assetPrefix = "/assets/"

// isSubresource reports whether the request is a page pulling in a script,
// stylesheet, image or font, as opposed to a browser navigating.
//
// It only says yes on positive evidence, and it reads that evidence from
// Sec-Fetch-Dest rather than from the path. Guessing from the path is not
// available here: the obvious test — a dot means a file — is wrong, because
// API groups and object names contain dots, so
// /c/lens-a/r/acme.example/v1/sprocketz/demo/sp-1 is an ordinary deep link.
//
// Anything that does not say what it wanted is treated as a navigation and
// gets the app, which keeps `curl /`, uptime checks and any client older than
// Sec-Fetch-Dest working exactly as before. Nothing is lost by that: every
// browser has sent the header since 2020, and the case this exists for — a
// stale bundle during a deploy — lives entirely under the asset directory,
// which spaHandler refuses on its own without consulting any header.
func isSubresource(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Dest") {
	case "", "document", "iframe", "frame":
		return false
	default:
		// script, style, image, font, audio, video, empty (fetch/XHR), ...
		return true
	}
}

// spaHandler serves the built frontend from any filesystem — a directory on
// disk or the bundle embedded in release binaries — falling back to
// index.html so that deep links into client-side routes work on a hard
// refresh. fs.FS path rules reject ".." traversal by construction.
//
// The fallback is deliberately not universal. Serving index.html for a
// subresource that is missing turns "this file is gone" into a 200 carrying
// HTML, which is the worst answer available: a stale <script> reports a syntax
// error pointing at the first "<" of a document it never asked for, and a
// stale stylesheet is dropped for a MIME mismatch and renders the page
// unstyled with nothing logged at all. Both are the deploy window between new
// HTML and old assets, which is exactly when a plain 404 would name the
// problem outright.
//
// So a miss under the asset directory is a 404 whoever asked, and a miss
// anywhere else is a 404 for a request that says it wanted a subresource.
// Everything else still gets the app.
func spaHandler(fsys fs.FS) http.HandlerFunc {
	fileServer := http.FileServer(http.FS(fsys))

	return func(w http.ResponseWriter, r *http.Request) {
		clean := path.Clean("/" + r.URL.Path)
		name := strings.TrimPrefix(clean, "/")
		inAssets := strings.HasPrefix(clean, assetPrefix)

		if info, err := fs.Stat(fsys, name); name != "" && err == nil && !info.IsDir() {
			// Hashed assets are immutable; index.html must never be cached or
			// a deploy leaves users on the old bundle.
			if inAssets {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			fileServer.ServeHTTP(w, r)
			return
		}

		// The build owns /assets, so a miss there is a miss however the
		// request was made.
		if inAssets || isSubresource(r) {
			http.NotFound(w, r)
			return
		}

		index, err := fs.ReadFile(fsys, "index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(index)
	}
}
