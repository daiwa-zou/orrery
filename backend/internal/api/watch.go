package api

import (
	"context"
	"net/http"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/daiwazou/clusterlens/backend/internal/authz"
	"github.com/daiwazou/clusterlens/backend/internal/cluster"
)

// watchBuffer is how far one subscriber may fall behind before it is dropped
// and told to reload. Large enough to absorb a rollout, small enough that a
// stalled tab does not hold megabytes.
const watchBuffer = 512

// reauthorizeInterval re-checks a long-lived stream's permissions, so revoking
// a RoleBinding closes open watches rather than waiting for the user to
// refresh.
const reauthorizeInterval = 60 * time.Second

// watchResources streams a resource's changes over a WebSocket, projected into
// the same rows the table endpoint returns so the client can apply them
// directly.
//
// Every subscriber attaches to the cluster's single shared informer: a hundred
// open tabs on the same namespace still cost the API server exactly one watch.
func (a *API) watchResources(w http.ResponseWriter, r *http.Request) {
	res, err := a.resolve(r)
	if err != nil {
		a.writeErr(w, r, err)
		return
	}

	namespace := r.URL.Query().Get("namespace")
	if !res.resource.Namespaced {
		namespace = ""
	}

	// Authorize before upgrading: a plain HTTP error is far easier for the
	// client to handle than a socket that opens and immediately closes.
	attrs := authz.Attributes{
		Verb: "watch", Group: res.resource.Group, Version: res.resource.Version,
		Resource: res.resource.Name, Namespace: namespace,
	}
	visible, err := a.watchScope(r.Context(), res, attrs, namespace)
	if err != nil {
		a.writeErr(w, r, err)
		return
	}

	conn, err := a.upgrader().Upgrade(w, r, nil)
	if err != nil {
		// Upgrade already wrote a response.
		return
	}
	ws := newWSConn(conn)
	defer ws.close()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go ws.ping(ctx)
	go ws.drain()

	sub, initial, err := res.cluster.Informers.Watch(ctx, res.resource, namespace, watchBuffer)
	if err != nil {
		ws.wsError(err.Error())
		return
	}
	defer sub.Close()

	set := a.tableFor(ctx, res.cluster, res.resource)

	items := make([]map[string]any, 0, len(initial))
	for _, o := range initial {
		if !visible.permits(o) {
			continue
		}
		items = append(items, buildRow(o, set, r))
	}
	if err := ws.WriteJSON(map[string]any{
		"type":     "INIT",
		"columns":  set.columns,
		"items":    items,
		"resource": metaOf(res.resource),
	}); err != nil {
		return
	}

	reauth := time.NewTicker(reauthorizeInterval)
	defer reauth.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ws.Done():
			return

		case <-reauth.C:
			if _, err := a.watchScope(ctx, res, attrs, namespace); err != nil {
				ws.wsError("access to this resource was revoked")
				return
			}

		case ev, ok := <-sub.Events:
			if !ok {
				// The broadcaster dropped us, or the informer stopped.
				_ = ws.WriteJSON(map[string]any{"type": "OVERFLOW"})
				ws.closeWith(1000, "reload required")
				return
			}
			switch ev.Type {
			case cluster.EventSynced:
				_ = ws.WriteJSON(map[string]any{"type": "SYNCED"})
			case cluster.EventOverflow:
				_ = ws.WriteJSON(map[string]any{"type": "OVERFLOW"})
				ws.closeWith(1000, "reload required")
				return
			case cluster.EventError:
				ws.wsError(ev.Err)
				return
			default:
				if ev.Object == nil || !visible.permits(ev.Object) {
					continue
				}
				if err := ws.WriteJSON(map[string]any{
					"type": string(ev.Type),
					"item": buildRow(ev.Object, set, r),
				}); err != nil {
					return
				}
			}
		}
	}
}

// watchVisibility encodes which namespaces a stream may reveal.
type watchVisibility struct {
	all        bool
	namespaces map[string]struct{}
	namespaced bool
}

func (v watchVisibility) permits(o *unstructured.Unstructured) bool {
	if v.all || !v.namespaced {
		return true
	}
	_, ok := v.namespaces[o.GetNamespace()]
	return ok
}

// watchScope authorizes the stream and returns the namespace filter to apply.
func (a *API) watchScope(ctx context.Context, res *resolved, attrs authz.Attributes, namespace string) (watchVisibility, error) {
	vis := watchVisibility{namespaced: res.resource.Namespaced}

	if namespace != "" || !res.resource.Namespaced {
		if err := a.authorize(ctx, res, "watch", namespace, "", ""); err != nil {
			return vis, err
		}
		vis.all = true
		return vis, nil
	}

	all, allowed, scanErr := res.cluster.Authz.VisibleNamespaces(
		ctx,
		res.cluster.AuthzClient(res.clients),
		res.cluster.AuthSubject(res.identity),
		attrs,
		a.namespaceNames(ctx, res.cluster),
	)
	if all {
		vis.all = true
		return vis, nil
	}
	if len(allowed) == 0 {
		if scanErr != nil {
			return vis, scanErr
		}
		return vis, &forbiddenError{verb: "watch", resource: res.resource.Name}
	}
	vis.namespaces = make(map[string]struct{}, len(allowed))
	for _, ns := range allowed {
		vis.namespaces[ns] = struct{}{}
	}
	return vis, nil
}
