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

// streamClosedBecause is the sentence a live stream leaves behind when a
// re-authorization check ends it.
//
// Closing is right either way: a review that could not be performed is not a
// pass, and a socket held open on one is a socket serving data nobody
// re-checked. The sentence is what was wrong. "Access was revoked" names a
// cause the reader can act on, and the action it points at is asking whoever
// administers their RBAC about a permission that was never withdrawn — while
// the real fault, an API server that could not answer for a moment, would have
// been fixed by reconnecting.
//
// These streams stay open for hours, which is exactly where a transient
// failure is most likely to be met, and it was the one place the distinction
// this console is built on had not reached.
func streamClosedBecause(err error, revoked string) string {
	if isForbidden(err) {
		return revoked
	}
	return "your access could not be re-checked, so this stream was closed; " +
		"reconnecting will try again (" + err.Error() + ")"
}

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

	namespaces, err := queryNamespaces(r)
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
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
	// Read once, not once per event: queryBool reparses the request's whole
	// query string, and this loop runs for as long as the socket is open.
	withLabels := queryBool(r, "labels", true)

	// Authorize before upgrading: a plain HTTP error is far easier for the
	// client to handle than a socket that opens and immediately closes.
	attrs := authz.Attributes{
		Verb: "watch", Group: res.resource.Group, Version: res.resource.Version,
		Resource: res.resource.Name, Namespace: watched,
	}
	visible, warnings, err := a.watchScope(r.Context(), res, attrs, namespaces)
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
		items = append(items, buildRow(o, set, withLabels))
	}
	// The snapshot says what it is missing, in the same sentences the list
	// endpoint uses. A stream that quietly dropped a namespace shows a table
	// that is wrong for as long as it stays open — and wrong in the one
	// direction a reader takes as reassurance, since the missing rows are the
	// ones nothing further will ever arrive for.
	init := map[string]any{
		"type":     "INIT",
		"columns":  set.columns,
		"items":    items,
		"resource": metaOf(res.resource),
	}
	if len(warnings) > 0 {
		init["warnings"] = warnings
	}
	if err := ws.WriteJSON(init); err != nil {
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
			next, _, err := a.watchScope(ctx, res, attrs, namespaces)
			if err != nil {
				ws.wsError(streamClosedBecause(err, "access to this resource was revoked"))
				return
			}
			// A scope that has *changed* is not something adopting the filter
			// can finish. This stream is a snapshot plus the deltas since, and
			// both halves are now wrong in a way no delta describes.
			//
			// Narrower: the rows for the lost namespace were already sent, and
			// silence is not a retraction — they sit on the page, frozen at the
			// moment access went away, looking like objects that have stopped
			// changing.
			//
			// Wider: the namespace that came back was excluded from INIT, so
			// its objects are ones the client has never seen. The next edit to
			// one of them arrives as MODIFIED for a row that does not exist.
			// That case is not hypothetical — it is exactly what a scan that
			// failed on some namespaces and succeeded on the rest produces
			// sixty seconds later, when the API server is no longer busy.
			//
			// OVERFLOW is the message this protocol already has for "what you
			// are holding cannot be repaired from here"; the client reloads and
			// gets a snapshot that matches its permissions.
			if !visible.sameAs(next) {
				_ = ws.WriteJSON(map[string]any{"type": "OVERFLOW"})
				ws.closeWith(1000, "reload required: your access to these namespaces changed")
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
					"item": buildRow(ev.Object, set, withLabels),
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

// sameAs reports whether two scopes reveal the same thing.
//
// It exists because a scope that has changed makes the snapshot the client is
// holding wrong, and wrong in a way the delta stream cannot put right — see the
// note at the re-authorization tick.
func (v watchVisibility) sameAs(o watchVisibility) bool {
	if v.all != o.all || v.namespaced != o.namespaced {
		return false
	}
	if len(v.namespaces) != len(o.namespaces) {
		return false
	}
	for ns := range v.namespaces {
		if _, ok := o.namespaces[ns]; !ok {
			return false
		}
	}
	return true
}

// watchScope authorizes the stream and returns the namespace filter to apply,
// together with any partial-answer warnings.
//
// The warnings are returned rather than dropped for the reason the list
// endpoint returns them, which authz.VisibleNamespaces states outright: a scan
// that could not ask every question has measured a lower bound on a scope and
// not the scope, and an answer narrowed by a busy API server is indistinguishable
// from one narrowed by RBAC unless it says so. This used to consult the scan
// error only when *nothing* came back, so a stream that lost some namespaces to
// a hiccup and kept the rest opened in silence.
func (a *API) watchScope(ctx context.Context, res *resolved, attrs authz.Attributes, namespaces []string) (watchVisibility, []string, error) {
	vis := watchVisibility{namespaced: res.resource.Namespaced}

	if !res.resource.Namespaced {
		if err := a.authorize(ctx, res, "watch", "", "", ""); err != nil {
			return vis, nil, err
		}
		vis.all = true
		return vis, nil, nil
	}

	if len(namespaces) > 0 {
		// Authorized one at a time, like the list endpoint: permission is
		// granted per namespace, and being allowed two of the three asked for
		// is a narrower stream rather than a refused one.
		access := a.authorizeNamespaces(ctx, res, "watch", namespaces)
		if len(access.allowed) == 0 {
			return vis, nil, access.firstErr
		}
		// Every allowed namespace goes in the filter, including when there is
		// only one. Asking the informer for a single namespace looks like it
		// makes the filter redundant, and it does for the INIT snapshot, which
		// is a namespace-indexed List. It does nothing for the events after
		// it: there is one informer per resource, listing and watching at
		// NamespaceAll, and its broadcaster fans every change out to every
		// subscriber. Without this filter a viewer of one namespace was sent
		// live objects from all of them.
		vis.namespaces = make(map[string]struct{}, len(access.allowed))
		for _, ns := range access.allowed {
			vis.namespaces[ns] = struct{}{}
		}
		return vis, access.warnings(res.resource.Name), nil
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
		return vis, nil, nil
	}
	if len(allowed) == 0 {
		if scanErr != nil {
			return vis, nil, scanErr
		}
		return vis, nil, &forbiddenError{verb: "watch", resource: res.resource.Name}
	}
	vis.namespaces = make(map[string]struct{}, len(allowed))
	for _, ns := range allowed {
		vis.namespaces[ns] = struct{}{}
	}
	var warnings []string
	if scanErr != nil {
		warnings = append(warnings, scanErr.Error())
	}
	return vis, warnings, nil
}
