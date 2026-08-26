package api

import (
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

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
			r.Get("/clusters", a.listClusters)

			r.Route("/clusters/{cluster}", func(r chi.Router) {
				// ---- reads ----
				r.Get("/discovery", a.listAPIResources)
				r.Get("/overview", a.clusterOverview)
				r.Get("/stats", a.cacheStats)
				r.Get("/events", a.listEvents)
				r.Get("/metrics/nodes", a.nodeMetrics)
				r.Get("/metrics/pods", a.podMetrics)
				r.Get("/pods/{namespace}/{name}/logs", a.getPodLogs)
				r.Get("/pods/{namespace}/{name}/env", a.podEnv)

				r.Get("/resources/{group}/{version}/{resource}", a.listResources)
				r.Get("/resources/{group}/{version}/{resource}/facets", a.listFacets)
				r.Get("/resources/{group}/{version}/{resource}/{namespace}/{name}", a.getResource)
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
func (a *API) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil && rec != http.ErrAbortHandler {
				a.log.Error("panic serving request",
					"path", r.URL.Path, "method", r.Method, "panic", rec)
				writeJSON(w, http.StatusInternalServerError, errorBody{
					Error: "internal", Reason: "the server hit an unexpected error", Code: 500,
				})
			}
		}()
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

// spaHandler serves the built frontend from any filesystem — a directory on
// disk or the bundle embedded in release binaries — falling back to
// index.html so that deep links into client-side routes work on a hard
// refresh. fs.FS path rules reject ".." traversal by construction.
func spaHandler(fsys fs.FS) http.HandlerFunc {
	fileServer := http.FileServer(http.FS(fsys))

	return func(w http.ResponseWriter, r *http.Request) {
		clean := path.Clean("/" + r.URL.Path)
		name := strings.TrimPrefix(clean, "/")

		if info, err := fs.Stat(fsys, name); name != "" && err == nil && !info.IsDir() {
			// Hashed assets are immutable; index.html must never be cached or
			// a deploy leaves users on the old bundle.
			if strings.HasPrefix(clean, "/assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			fileServer.ServeHTTP(w, r)
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
