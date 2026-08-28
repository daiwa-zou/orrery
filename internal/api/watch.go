package api

import (
	"context"
	"net/http"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"github.com/daiwa-zou/orrery/internal/authz"
	"github.com/daiwa-zou/orrery/internal/cluster"
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

	namespaces := queryNamespaces(r)
	if !res.resource.Namespaced {
		namespaces = nil
	}
	// One namespace can be watched at the informer; several are watched
	// cluster-wide and filtered, because the cache holds one informer per
	// resource per namespace and opening three would cost three watches on the
	// API server to answer one question.
	watched := ""
	if len(namespaces) == 1 {
		watched = namespaces[0]
	}

	// The stream applies the same q/labelSelector/fieldSelector/where the list
	// endpoint does, so a narrowly filtered page is not woken by every change
	// elsewhere in the namespace — and, for the column predicates, is not sent
	// the rows it just excluded.
	//
	// The table is resolved here rather than after the upgrade because a
	// column predicate is checked against its column, and parsing has to stay
	// on this side of it: a bad filter is then a plain 400, same as on the
	// list endpoint, instead of a socket that opens and immediately closes.
	set := a.tableFor(r.Context(), res.cluster, res.resource)
	filter, err := parseListFilter(r, set)
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	events := newWatchEventFilter(filter)

	// Authorize before upgrading: a plain HTTP error is far easier for the
	// client to handle than a socket that opens and immediately closes.
	attrs := authz.Attributes{
		Verb: "watch", Group: res.resource.Group, Version: res.resource.Version,
		Resource: res.resource.Name, Namespace: watched,
	}
	visible, err := a.watchScope(r.Context(), res, attrs, namespaces)
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

	sub, initial, err := res.cluster.Informers.Watch(ctx, res.resource, watched, watchBuffer)
	if err != nil {
		ws.wsError(err.Error())
		return
	}
	defer sub.Close()

	items := make([]map[string]any, 0, len(initial))
	for _, o := range initial {
		if !visible.permits(o) || !events.admitInitial(o) {
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
			// A signed-out or expired session ends the stream; a token near
			// expiry is renewed here so the scope check below never presents
			// stale credentials.
			if err := a.refreshStreamIdentity(ctx, r, res); err != nil {
				ws.wsError("session expired; sign in again")
				return
			}
			// Adopt the recomputed scope, not just the yes/no: losing one
			// namespace out of several must stop that namespace's objects from
			// streaming, and only total revocation closes the socket.
			next, err := a.watchScope(ctx, res, attrs, namespaces)
			if err != nil {
				ws.wsError("access to this resource was revoked")
				return
			}
			visible = next

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
			default:
				if ev.Object == nil || !visible.permits(ev.Object) {
					continue
				}
				send, ok := events.translate(ev.Type, ev.Object)
				if !ok {
					continue
				}
				if err := ws.WriteJSON(map[string]any{
					"type": string(send),
					"item": buildRow(ev.Object, set, r),
				}); err != nil {
					return
				}
			}
		}
	}
}

// watchEventFilter translates raw informer events into the stream a filtered
// subscriber should see. The subtlety is edits across the filter boundary: an
// object modified so it no longer matches must leave the page as DELETED, and
// one modified into the filter must arrive as ADDED — a raw MODIFIED would be
// spliced into (or silently missing from) rows it does not belong to.
type watchEventFilter struct {
	filter listFilter
	// matched is the set of object UIDs the subscriber currently sees; nil
	// when no filter is active, making the whole type a pass-through.
	matched map[types.UID]struct{}
}

func newWatchEventFilter(f listFilter) *watchEventFilter {
	wf := &watchEventFilter{filter: f}
	if !f.empty() {
		wf.matched = make(map[types.UID]struct{})
	}
	return wf
}

// admitInitial reports whether an object belongs in the INIT snapshot, and
// records it so later events know it was shown.
func (wf *watchEventFilter) admitInitial(o *unstructured.Unstructured) bool {
	if wf.matched == nil {
		return true
	}
	if !wf.filter.matches(o) {
		return false
	}
	wf.matched[o.GetUID()] = struct{}{}
	return true
}

// translate maps an informer event to what the subscriber should be sent;
// ok=false means the event is invisible under the filter.
func (wf *watchEventFilter) translate(t cluster.EventType, o *unstructured.Unstructured) (cluster.EventType, bool) {
	if wf.matched == nil {
		return t, true
	}
	uid := o.GetUID()
	_, known := wf.matched[uid]

	if t == cluster.EventDeleted {
		delete(wf.matched, uid)
		// An unknown-but-matching delete can happen when the object raced in
		// before the INIT snapshot; a spurious DELETED only costs a refetch.
		return cluster.EventDeleted, known || wf.filter.matches(o)
	}
	if wf.filter.matches(o) {
		wf.matched[uid] = struct{}{}
		if known {
			return cluster.EventModified, true
		}
		return cluster.EventAdded, true
	}
	if known {
		delete(wf.matched, uid)
		return cluster.EventDeleted, true
	}
	return t, false
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
func (a *API) watchScope(ctx context.Context, res *resolved, attrs authz.Attributes, namespaces []string) (watchVisibility, error) {
	vis := watchVisibility{namespaced: res.resource.Namespaced}

	if !res.resource.Namespaced {
		if err := a.authorize(ctx, res, "watch", "", "", ""); err != nil {
			return vis, err
		}
		vis.all = true
		return vis, nil
	}

	if len(namespaces) > 0 {
		// Authorized one at a time, like the list endpoint: permission is
		// granted per namespace, and being allowed two of the three asked for
		// is a narrower stream rather than a refused one.
		var (
			allowed  []string
			firstErr error
		)
		for _, ns := range namespaces {
			if err := a.authorize(ctx, res, "watch", ns, "", ""); err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			allowed = append(allowed, ns)
		}
		if len(allowed) == 0 {
			return vis, firstErr
		}
		// A single namespace is already all the informer will deliver, so the
		// stream needs no filter of its own.
		if len(namespaces) == 1 {
			vis.all = true
			return vis, nil
		}
		vis.namespaces = make(map[string]struct{}, len(allowed))
		for _, ns := range allowed {
			vis.namespaces[ns] = struct{}{}
		}
		return vis, nil
	}

	all, allowed, scanErr := res.cluster.Authz.VisibleNamespaces(
		ctx,
		res.cluster.AuthzClient(res.clients),
		res.cluster.AuthSubject(res.identity),
		attrs,
		func() ([]string, error) { return a.namespaceNames(ctx, res.cluster) },
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
