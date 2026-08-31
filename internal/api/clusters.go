package api

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/daiwa-zou/orrery/internal/auth"
	"github.com/daiwa-zou/orrery/internal/authz"
	"github.com/daiwa-zou/orrery/internal/cluster"
	"github.com/daiwa-zou/orrery/internal/config"
)

// clusterSummary is the cluster switcher's view of one cluster.
type clusterSummary struct {
	Name        string            `json:"name"`
	DisplayName string            `json:"displayName"`
	Labels      map[string]string `json:"labels,omitempty"`
	AuthMode    config.AuthMode   `json:"authMode"`
	Health      cluster.Health    `json:"health"`
	Available   bool              `json:"available"`
	Error       string            `json:"error,omitempty"`
}

func (a *API) summarize(res *resolved) clusterSummary {
	c := res.cluster
	return clusterSummary{
		Name:        c.Cfg.Name,
		DisplayName: c.Cfg.DisplayName,
		Labels:      c.Cfg.Labels,
		AuthMode:    c.Cfg.AuthMode,
		Health:      c.Health(),
		Available:   true,
	}
}

// listClusters returns every registered cluster, including the ones that are
// currently unreachable. Hiding a broken cluster would make an outage look
// like a configuration change.
func (a *API) listClusters(w http.ResponseWriter, r *http.Request) {
	entries := a.registry.Entries()
	out := make([]clusterSummary, 0, len(entries))
	for _, e := range entries {
		s := clusterSummary{
			Name:        e.Name,
			DisplayName: e.DisplayName,
			Labels:      e.Labels,
			AuthMode:    e.AuthMode,
			Available:   e.Cluster != nil,
		}
		if e.Cluster != nil {
			s.Health = e.Cluster.Health()
		} else {
			s.Health = cluster.Health{Status: cluster.HealthUnreachable}
			if e.Err != nil {
				s.Error = e.Err.Error()
			}
		}
		out = append(out, s)
	}
	writeJSON(w, http.StatusOK, map[string]any{"clusters": out})
}

// discoveryGroup bundles a group's resources for the navigation tree.
type discoveryGroup struct {
	Group     string                `json:"group"`
	Resources []cluster.APIResource `json:"resources"`
}

// listAPIResources returns the cluster's browsable API surface. Only preferred
// versions are returned by default: showing v1beta1 alongside v1 for every
// resource makes the navigation useless.
func (a *API) listAPIResources(w http.ResponseWriter, r *http.Request) {
	res, err := a.clusterOnly(r)
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	all, err := res.cluster.Discovery.Resources(r.Context())
	if err != nil {
		a.writeErr(w, r, err)
		return
	}

	includeAll := queryBool(r, "allVersions", false)
	listableOnly := queryBool(r, "listable", true)

	byGroup := map[string][]cluster.APIResource{}
	for _, ar := range all {
		if !includeAll && !ar.Preferred {
			continue
		}
		if listableOnly && !ar.Supports("list") {
			continue
		}
		byGroup[ar.Group] = append(byGroup[ar.Group], ar)
	}

	groups := make([]discoveryGroup, 0, len(byGroup))
	for g, rs := range byGroup {
		sort.Slice(rs, func(i, j int) bool { return rs[i].Kind < rs[j].Kind })
		groups = append(groups, discoveryGroup{Group: g, Resources: rs})
	}
	sort.Slice(groups, func(i, j int) bool {
		// The core group leads; everything else is alphabetical.
		if (groups[i].Group == "") != (groups[j].Group == "") {
			return groups[i].Group == ""
		}
		return groups[i].Group < groups[j].Group
	})

	version, _ := res.cluster.Discovery.ServerVersion()
	writeJSON(w, http.StatusOK, map[string]any{
		"groups":        groups,
		"serverVersion": version,
	})
}

// accessCheck is one question in a batch permission query.
type accessCheck struct {
	Verb        string `json:"verb"`
	Group       string `json:"group"`
	Version     string `json:"version"`
	Resource    string `json:"resource"`
	Subresource string `json:"subresource,omitempty"`
	Namespace   string `json:"namespace,omitempty"`
	Name        string `json:"name,omitempty"`
}

// checkAccess answers a batch of permission questions in one round trip. The
// UI uses it to decide which actions to offer, so a user is never shown a
// button that will fail.
func (a *API) checkAccess(w http.ResponseWriter, r *http.Request) {
	res, err := a.clusterOnly(r)
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	body, err := decodeBody[struct {
		Checks []accessCheck `json:"checks"`
	}](r)
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	if len(body.Checks) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"results": map[string]any{}})
		return
	}
	if len(body.Checks) > 64 {
		a.writeErr(w, r, badRequest("at most 64 checks per request"))
		return
	}

	attrs := make([]authz.Attributes, 0, len(body.Checks))
	keys := make([]string, 0, len(body.Checks))
	for _, c := range body.Checks {
		a := authz.Attributes{
			Verb: c.Verb, Group: cluster.NormalizeGroup(c.Group), Version: c.Version,
			Resource: c.Resource, Subresource: c.Subresource,
			Namespace: c.Namespace, Name: c.Name,
		}
		attrs = append(attrs, a)
		keys = append(keys, authz.AttributesKey(a))
	}

	decisions := res.cluster.Authz.AllowedMany(r.Context(),
		res.cluster.AuthzClient(res.clients),
		res.cluster.AuthSubject(res.identity),
		attrs)

	// Key the response by the caller's own index so the client does not have
	// to reconstruct our internal key encoding.
	results := make(map[string]any, len(keys))
	for i, k := range keys {
		results[strconv.Itoa(i)] = decisions[k]
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}

// cacheStats exposes what the dashboard is currently caching. It is the first
// thing to look at when a deployment's memory use is surprising.
//
// Every informer is filtered through an access review, because the set of
// running caches reveals which CRDs exist and how many objects each holds. The
// reviews are asked as one batch rather than one after another: a busy cluster
// runs dozens of informers, and a serial scan is dozens of round trips to the
// API server for a panel showing two numbers.
//
// A review that could not be *performed* is reported, not dropped. Silently
// omitting it is the same shape of mistake this package refuses everywhere
// else — the resource disappears from the list and its objects vanish from the
// total, and the console renders both as plain facts. "Cached objects: 4,102"
// is read as a measurement, so a number quietly missing a cache is worse than
// no number: the reader is looking at this page precisely because they doubt
// the memory figure, and the one thing it must not do is under-report without
// saying so.
func (a *API) cacheStats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	res, err := a.clusterOnly(r)
	if err != nil {
		a.writeErr(w, r, err)
		return
	}

	stats := res.cluster.Informers.Stats()
	attrs := make([]authz.Attributes, 0, len(stats))
	for _, s := range stats {
		attrs = append(attrs, authz.Attributes{
			Verb: "list", Group: s.Group, Version: s.Version, Resource: s.Resource,
		})
	}
	verdicts := res.cluster.Authz.AllowedMany(ctx,
		res.cluster.AuthzClient(res.clients),
		res.cluster.AuthSubject(res.identity),
		attrs)

	// Filtered in place: Stats returns a slice nothing else holds, and the
	// write index never overtakes the read index.
	visible := stats[:0]
	totalObjects := 0
	unchecked := 0
	for i, s := range stats {
		switch d := verdicts[authz.AttributesKey(attrs[i])]; {
		case d.Allowed:
			visible = append(visible, s)
			totalObjects += s.Objects
		case d.Unavailable:
			unchecked++
		}
	}

	out := map[string]any{
		"cluster":      res.cluster.Cfg.Name,
		"informers":    visible,
		"totalObjects": totalObjects,
	}
	if unchecked > 0 {
		// Named as the count it is, so the reader can tell a short list from a
		// narrow permission — and told that retrying is the fix, because
		// nothing about their RBAC is.
		out["unchecked"] = unchecked
		out["warning"] = fmt.Sprintf(
			"Your access to %d of the %d running caches could not be checked, so they are "+
				"not counted here. This is not a permission problem; try again.",
			unchecked, len(stats))
	}
	writeJSON(w, http.StatusOK, out)
}

// listEvents returns events, optionally narrowed to one object. Event lists
// are the most common thing a user wants next to a failing resource.
func (a *API) listEvents(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	res, err := a.clusterOnly(r)
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	eventRes, err := res.cluster.Discovery.Resolve(ctx, "", "v1", "events")
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	res.resource = eventRes

	// Authorized one at a time, like every other read narrowed to namespaces:
	// permission is granted that way, so being allowed two of the three asked
	// for is a narrower feed rather than a refused one.
	namespaces, err := queryNamespaces(r)
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	var (
		scoped   map[string]struct{}
		warnings []string
	)
	if len(namespaces) > 0 {
		access := a.authorizeNamespaces(ctx, res, "list", namespaces)
		if len(access.allowed) == 0 {
			a.writeErr(w, r, access.firstErr)
			return
		}
		// The feed was being narrowed in silence: ask for three namespaces,
		// be allowed two, and read the result as everything that happened.
		// The list endpoint has always said so; this one now says it too.
		warnings = access.warnings(eventRes.Name)
		scoped = make(map[string]struct{}, len(access.allowed))
		for _, ns := range access.allowed {
			scoped[ns] = struct{}{}
		}
	}

	objs, err := a.visibleObjects(ctx, res, "", "v1", "events")
	if err != nil {
		a.writeErr(w, r, err)
		return
	}

	uid := r.URL.Query().Get("involvedUID")
	name := r.URL.Query().Get("involvedName")
	kind := r.URL.Query().Get("involvedKind")
	onlyWarnings := queryBool(r, "warningsOnly", false)
	terms, err := parseSearchTerms(r.URL.Query().Get("q"))
	if err != nil {
		a.writeErr(w, r, err)
		return
	}

	set := a.tableFor(ctx, res.cluster, eventRes)
	// Column predicates are bound to the event table's own columns, so
	// count>3, reason=~^Failed and lastSeen<15m mean here exactly what they
	// mean on every other list. Binding them is also what lets a term naming a
	// column this table does not have be refused with a message, rather than
	// matching nothing on every row and reading as a quiet cluster.
	preds, err := parseWhere(r.URL.Query()["where"], set.columns)
	if err != nil {
		a.writeErr(w, r, err)
		return
	}

	rows := make([]map[string]any, 0, 64)
	for _, e := range objs {
		if scoped != nil {
			if _, ok := scoped[e.GetNamespace()]; !ok {
				continue
			}
		}
		if uid != "" && str(e, "involvedObject", "uid") != uid {
			continue
		}
		if name != "" && str(e, "involvedObject", "name") != name {
			continue
		}
		if kind != "" && !strings.EqualFold(str(e, "involvedObject", "kind"), kind) {
			continue
		}
		if onlyWarnings && str(e, "type") != "Warning" {
			continue
		}
		row := set.row(e)
		// Free text and predicates both read the projected row, so what
		// filters is exactly what the reader can see — and both are applied
		// before the limit below, so a match older than the newest few hundred
		// events still surfaces.
		if !rowMatchesSearch(row, terms, eventSearchKeys) {
			continue
		}
		if !matchesAll(preds, row) {
			continue
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		return strings.Compare(asString(rows[j]["lastSeen"]), asString(rows[i]["lastSeen"])) < 0
	})
	limit := queryInt(r, "limit", 200, 1, 2000)
	// Total counts what matched, not what fitted. Reporting the truncated
	// length as the total makes a capped feed describe itself as complete,
	// and the reader has no way left to discover that older events were
	// dropped — the rows are sorted newest-first, so what falls off the end
	// is exactly what they would have had to scroll to find.
	matched := len(rows)
	if len(rows) > limit {
		rows = rows[:limit]
	}
	writeJSON(w, http.StatusOK, listResponse{
		Items: &rows, Columns: set.columns, Total: matched,
		Page: 1, PageSize: limit, Resource: metaOf(eventRes),
		Warnings: warnings,
	})
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

// eventSearchKeys are the columns free text scans: everything an event says in
// words. "type" is among them because "warning" is a word people type, and a
// box that ignores it is answering a question it was not asked. The count and
// the timestamp are left out — they are compared, not read, and `count>3` says
// what searching for "3" never could.
var eventSearchKeys = []string{"object", "reason", "message", "namespace", "type"}

func rowMatchesQuery(row map[string]any, q string, keys []string) bool {
	for _, k := range keys {
		if v, ok := row[k]; ok && strings.Contains(strings.ToLower(asString(v)), q) {
			return true
		}
	}
	return false
}

// whoami reports the signed-in identity and how the server is configured, so
// the UI can render the right login affordances.
func (a *API) whoami(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFrom(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, errorBody{Error: "unauthenticated", Code: 401})
		return
	}
	body := map[string]any{
		"user":          u,
		"authenticated": true,
		"oidcEnabled":   a.cfg.OIDC.Enabled,
		"anonymous":     a.mw.Anonymous(),
		// Optional capabilities the console should not offer when the server
		// is not serving them.
		"features": map[string]any{
			"proxy": a.cfg.Proxy.ProxyEnabled(),
		},
	}
	if s, ok := auth.SessionFrom(r.Context()); ok {
		body["expiresAt"] = s.ExpiresAt
		body["csrfToken"] = s.CSRFToken
	}
	writeJSON(w, http.StatusOK, body)
}

// authConfig is served without authentication so the login page knows what to
// offer before anyone has signed in.
func (a *API) authConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"oidcEnabled": a.cfg.OIDC.Enabled,
		"anonymous":   a.mw.Anonymous(),
		"loginPath":   "/api/v1/auth/login",
		"autoLogin":   a.cfg.OIDC.Enabled && a.cfg.OIDC.AutoLogin,
	})
}
