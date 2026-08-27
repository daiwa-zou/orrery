package api

import (
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
func (a *API) cacheStats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	res, err := a.clusterOnly(r)
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	// The stats come straight from the shared cache, and the set of running
	// informers reveals which CRDs exist and how many objects each holds. Only
	// report the resources this user could list cluster-wide themselves.
	stats := res.cluster.Informers.Stats()
	visible := stats[:0]
	totalObjects := 0
	for _, s := range stats {
		check := *res
		check.resource = cluster.APIResource{Group: s.Group, Version: s.Version, Name: s.Resource}
		if err := a.authorize(ctx, &check, "list", "", "", ""); err != nil {
			continue
		}
		visible = append(visible, s)
		totalObjects += s.Objects
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"cluster":      res.cluster.Cfg.Name,
		"informers":    visible,
		"totalObjects": totalObjects,
	})
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

	namespace := r.URL.Query().Get("namespace")
	if namespace != "" {
		if err := a.authorize(ctx, res, "list", namespace, "", ""); err != nil {
			a.writeErr(w, r, err)
			return
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
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))

	set := a.tableFor(ctx, res.cluster, eventRes)
	rows := make([]map[string]any, 0, 64)
	for _, e := range objs {
		if namespace != "" && e.GetNamespace() != namespace {
			continue
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
		// Free text scans the projected columns the table shows, so what
		// matches is exactly what the reader can see.
		if q != "" && !rowMatchesQuery(row, q, eventSearchKeys) {
			continue
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		return strings.Compare(asString(rows[j]["lastSeen"]), asString(rows[i]["lastSeen"])) < 0
	})
	limit := queryInt(r, "limit", 200, 1, 2000)
	if len(rows) > limit {
		rows = rows[:limit]
	}
	writeJSON(w, http.StatusOK, listResponse{
		Items: &rows, Columns: set.columns, Total: len(rows),
		Page: 1, PageSize: limit, Resource: metaOf(eventRes),
	})
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

var eventSearchKeys = []string{"object", "reason", "message", "namespace"}

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
