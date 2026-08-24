package api

import (
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"k8s.io/client-go/rest"
)

// proxyHTTP is the browser's answer to `kubectl port-forward` for HTTP
// workloads: the request is relayed to a pod or service through the API
// server's proxy subresource, under the caller's own identity.
//
// Deliberately GET/HEAD only. The proxied page is rendered inside the
// dashboard's origin, and a state-changing proxy would let any site drive
// writes into cluster workloads with the viewer's session. Reading a
// dashboard, a queue depth or a health page — the things people actually
// port-forward for — needs no more.
func (a *API) proxyHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeJSON(w, http.StatusMethodNotAllowed, errorBody{
			Error: "method_not_allowed", Reason: "the proxy is read-only: only GET and HEAD pass through", Code: 405,
		})
		return
	}

	res, err := a.clusterOnly(r)
	if err != nil {
		a.writeErr(w, r, err)
		return
	}

	namespace := chi.URLParam(r, "namespace")
	ptype := chi.URLParam(r, "ptype") // "pods" or "services"
	name := chi.URLParam(r, "name")   // may carry ":port"
	rest_ := chi.URLParam(r, "*")

	if ptype != "pods" && ptype != "services" {
		a.writeErr(w, r, badRequest("proxy target must be pods or services"))
		return
	}

	proxyRes, err := res.cluster.Discovery.Resolve(ctx, "", "v1", ptype)
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	res.resource = proxyRes
	bare := name
	if i := strings.IndexByte(bare, ':'); i >= 0 {
		bare = bare[:i]
	}
	// The same review the API server itself would run for this URL.
	if err := a.authorize(ctx, res, "get", namespace, bare, "proxy"); err != nil {
		a.writeErr(w, r, err)
		return
	}

	req := res.clients.Kube.CoreV1().RESTClient().Get().
		Namespace(namespace).
		Resource(ptype).
		Name(name).
		SubResource("proxy").
		Suffix(strings.Split(rest_, "/")...)
	for k, vs := range r.URL.Query() {
		for _, v := range vs {
			req.Param(k, v)
		}
	}

	// A hand-rolled round trip instead of rest.Result: headers and status must
	// pass through untouched or stylesheets and redirects break.
	transport, err := rest.TransportFor(res.clients.Rest)
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	upstream, err := http.NewRequestWithContext(ctx, r.Method, req.URL().String(), nil)
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	if accept := r.Header.Get("Accept"); accept != "" {
		upstream.Header.Set("Accept", accept)
	}

	resp, err := (&http.Client{Transport: transport}).Do(upstream)
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	defer resp.Body.Close()

	for _, h := range []string{"Content-Type", "Content-Length", "Cache-Control", "Location", "Last-Modified", "ETag"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	// Whatever the workload serves, it must not script against the dashboard.
	w.Header().Set("Content-Security-Policy", "sandbox allow-same-origin; default-src 'self' 'unsafe-inline' data:")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}
