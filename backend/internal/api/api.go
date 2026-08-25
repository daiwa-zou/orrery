// Package api exposes the HTTP surface: one uniform, cluster-scoped REST API
// over every Kubernetes resource, plus the streaming endpoints for logs,
// exec and live updates.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/daiwa-zou/orrery/backend/internal/auth"
	"github.com/daiwa-zou/orrery/backend/internal/cluster"
	"github.com/daiwa-zou/orrery/backend/internal/config"
)

// API holds the dependencies every handler needs. Sessions are reached only
// through the middleware, which owns refresh and liveness policy.
type API struct {
	cfg      *config.Config
	registry *cluster.Registry
	authn    *auth.Authenticator
	mw       *auth.Middleware
	log      *slog.Logger

	// tables memoises resolved column sets, including the JSONPath programs
	// compiled from CRD printer columns.
	tables *tableCache
}

// New builds the API.
func New(
	cfg *config.Config,
	registry *cluster.Registry,
	authn *auth.Authenticator,
	mw *auth.Middleware,
	log *slog.Logger,
) *API {
	return &API{
		cfg: cfg, registry: registry,
		authn: authn, mw: mw, log: log,
		tables: newTableCache(cfg.Cache.DiscoveryTTL),
	}
}

// errorBody is the single error shape every endpoint returns, so the frontend
// has exactly one thing to render.
type errorBody struct {
	Error  string `json:"error"`
	Reason string `json:"reason,omitempty"`
	Code   int    `json:"code"`
	// Details carries structured extras such as the offending resource.
	Details map[string]any `json:"details,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	// Responses are per-user (authorization-gated) and can contain live secret
	// values; nothing on this surface may land in a browser or proxy cache.
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		// The status line is already written; all that is left is a log line.
		slog.Debug("write response", "err", err)
	}
}

// writeErr translates Go and Kubernetes errors into a consistent HTTP shape.
// Kubernetes status codes are passed through, so a 403 from the API server
// reaches the browser as a 403 with the API server's own explanation.
func (a *API) writeErr(w http.ResponseWriter, r *http.Request, err error) {
	if err == nil {
		return
	}
	// The browser cancelling a request it no longer needs is routine, not a
	// server failure; logging it as a 500 buries real errors in noise.
	if errors.Is(err, context.Canceled) {
		writeJSON(w, 499, errorBody{Error: "client_closed_request", Code: 499})
		return
	}
	status := http.StatusInternalServerError
	reason := err.Error()
	errKind := "internal"

	var statusErr apierrors.APIStatus
	var unknownCluster *cluster.UnknownClusterError
	var unknownResource *cluster.UnknownResourceError
	var forbidden *forbiddenError

	switch {
	case errors.As(err, &forbidden):
		status = http.StatusForbidden
		errKind = "forbidden"
		reason = forbidden.Error()
	case errors.As(err, &statusErr):
		s := statusErr.Status()
		status = int(s.Code)
		if status == 0 {
			status = http.StatusInternalServerError
		}
		errKind = strings.ToLower(string(s.Reason))
		if errKind == "" {
			errKind = "kubernetes"
		}
		reason = s.Message
	case errors.As(err, &unknownCluster):
		status = http.StatusNotFound
		errKind = "unknown_cluster"
	case errors.As(err, &unknownResource):
		status = http.StatusNotFound
		errKind = "unknown_resource"
	case errors.Is(err, errBadRequest):
		status = http.StatusBadRequest
		errKind = "bad_request"
	case errors.Is(err, errNotFound):
		status = http.StatusNotFound
		errKind = "not_found"
	}

	if status >= 500 {
		a.log.Error("request failed", "path", r.URL.Path, "err", err)
	}
	writeJSON(w, status, errorBody{Error: errKind, Reason: reason, Code: status})
}

var errBadRequest = errors.New("bad request")

func badRequest(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errBadRequest, fmt.Sprintf(format, args...))
}

var errNotFound = errors.New("not found")

func notFound(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errNotFound, fmt.Sprintf(format, args...))
}

// forbiddenError is raised when our own access review says no, before any call
// to the API server is attempted.
type forbiddenError struct {
	verb, resource, namespace, reason string
}

func (e *forbiddenError) Error() string {
	scope := "cluster-wide"
	if e.namespace != "" {
		scope = "in namespace " + e.namespace
	}
	msg := fmt.Sprintf("you are not allowed to %s %s %s", e.verb, e.resource, scope)
	if e.reason != "" {
		msg += " (" + e.reason + ")"
	}
	return msg
}

// identity converts the request's authenticated user into the cluster layer's
// identity, attaching the raw ID token for passthrough clusters.
func identityFrom(r *http.Request) (cluster.Identity, error) {
	u, ok := auth.UserFrom(r.Context())
	if !ok {
		return cluster.Identity{}, errors.New("unauthenticated")
	}
	id := cluster.Identity{Username: u.Username, Groups: u.Groups}
	if s, ok := auth.SessionFrom(r.Context()); ok {
		id.BearerToken = s.IDToken
	}
	return id, nil
}

// refreshStreamIdentity re-resolves a stream's identity from the session
// store, called on each re-authorization cycle. It renews OIDC tokens that
// have gone stale since the handshake — without this, a passthrough cluster's
// re-authorization starts presenting an expired token and healthy streams die
// — and it returns an error when the session itself is gone (signed out or
// expired), because a watch or shell must not outlive the login that opened
// it. With OIDC disabled it is a no-op.
func (a *API) refreshStreamIdentity(ctx context.Context, r *http.Request, res *resolved) error {
	sess, ok := auth.SessionFrom(r.Context())
	if !ok {
		return nil // anonymous mode: the identity cannot go stale
	}
	fresh, err := a.mw.FreshSession(ctx, sess.ID)
	if err != nil {
		return err
	}
	id := cluster.Identity{
		Username:    fresh.User.Username,
		Groups:      fresh.User.Groups,
		BearerToken: fresh.IDToken,
	}
	clients, err := res.cluster.ClientsFor(id)
	if err != nil {
		return err
	}
	res.identity, res.clients = id, clients
	return nil
}

// resolved bundles everything a resource handler needs after routing.
type resolved struct {
	cluster  *cluster.Cluster
	clients  *cluster.Clients
	identity cluster.Identity
	resource cluster.APIResource
}

// resolve looks up the cluster, the user's clients and the target resource in
// one step, since every resource handler needs all three.
func (a *API) resolve(r *http.Request) (*resolved, error) {
	name := chi.URLParam(r, "cluster")
	c, err := a.registry.Get(name)
	if err != nil {
		return nil, err
	}
	id, err := identityFrom(r)
	if err != nil {
		return nil, err
	}
	clients, err := c.ClientsFor(id)
	if err != nil {
		return nil, err
	}
	out := &resolved{cluster: c, clients: clients, identity: id}

	group := chi.URLParam(r, "group")
	version := chi.URLParam(r, "version")
	resource := chi.URLParam(r, "resource")
	if resource != "" {
		ar, err := c.Discovery.Resolve(r.Context(), group, version, resource)
		if err != nil {
			return nil, err
		}
		out.resource = ar
	}
	return out, nil
}

// clusterOnly resolves just the cluster and the caller's clients.
func (a *API) clusterOnly(r *http.Request) (*resolved, error) {
	name := chi.URLParam(r, "cluster")
	c, err := a.registry.Get(name)
	if err != nil {
		return nil, err
	}
	id, err := identityFrom(r)
	if err != nil {
		return nil, err
	}
	clients, err := c.ClientsFor(id)
	if err != nil {
		return nil, err
	}
	return &resolved{cluster: c, clients: clients, identity: id}, nil
}

// query helpers

func queryInt(r *http.Request, key string, def, min, max int) int {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

func queryBool(r *http.Request, key string, def bool) bool {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return def
	}
	return raw == "true" || raw == "1" || raw == "yes"
}
