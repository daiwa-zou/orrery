// Package cluster owns everything that is per-Kubernetes-cluster: credentials,
// API discovery, shared informer caches and health.
package cluster

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
)

// APIResource is the trimmed view of a discovered resource the UI needs.
type APIResource struct {
	Group        string   `json:"group"`
	Version      string   `json:"version"`
	Name         string   `json:"name"`
	SingularName string   `json:"singularName"`
	Kind         string   `json:"kind"`
	Namespaced   bool     `json:"namespaced"`
	Verbs        []string `json:"verbs"`
	ShortNames   []string `json:"shortNames,omitempty"`
	Categories   []string `json:"categories,omitempty"`
	// Preferred marks the group's storage version, which is the one the UI
	// should default to when several versions are served.
	Preferred bool `json:"preferred"`
}

// GVR returns the resource's group-version-resource triple.
func (r APIResource) GVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: r.Group, Version: r.Version, Resource: r.Name}
}

// Supports reports whether the API server advertises a verb for this resource.
func (r APIResource) Supports(verb string) bool {
	for _, v := range r.Verbs {
		if v == verb {
			return true
		}
	}
	return false
}

// DiscoveryCache memoises a cluster's API surface. Discovery is a burst of
// requests proportional to the number of API groups, so re-running it per
// user request would dominate the load a dashboard puts on an API server.
type DiscoveryCache struct {
	client discovery.DiscoveryInterface
	ttl    time.Duration

	mu        sync.RWMutex
	resources []APIResource
	byGVR     map[schema.GroupVersionResource]APIResource
	// aliases maps every accepted spelling (name, singular, short name,
	// kind, name.group) to a canonical GVR.
	aliases   map[string]schema.GroupVersionResource
	fetchedAt time.Time
	// missRefreshAt rate-limits the refresh-on-miss path below.
	missRefreshAt time.Time

	sf singleflight.Group
}

// NewDiscoveryCache builds a cache over a discovery client.
func NewDiscoveryCache(client discovery.DiscoveryInterface, ttl time.Duration) *DiscoveryCache {
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	return &DiscoveryCache{client: client, ttl: ttl}
}

// Resources returns the cluster's discoverable resources, refreshing on TTL.
//
// The slice is the cache's own and must be treated as read-only, along with
// the slices inside each APIResource. A refresh replaces it wholesale rather
// than editing it, which is what makes handing it out safe — and what a caller
// sorting or filtering it in place would quietly undo, for every other reader
// at once.
func (d *DiscoveryCache) Resources(ctx context.Context) ([]APIResource, error) {
	d.mu.RLock()
	fresh := time.Since(d.fetchedAt) < d.ttl && d.resources != nil
	res := d.resources
	d.mu.RUnlock()
	if fresh {
		return res, nil
	}
	if err := d.refresh(ctx); err != nil {
		// Serve stale data rather than failing the page: a partially reachable
		// API server should degrade, not break the console.
		d.mu.RLock()
		defer d.mu.RUnlock()
		if d.resources != nil {
			return d.resources, nil
		}
		return nil, err
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.resources, nil
}

// refresh forces a discovery reload, collapsing concurrent callers.
//
// The reload itself cannot be cancelled — client-go's ServerGroupsAndResources
// takes no context — so ctx bounds the waiting rather than the work: a caller
// that has gone away stops waiting, and the reload it started carries on for
// whoever is still here. The parameter was previously accepted and ignored
// outright, which on a cold or slow cluster meant every reader of discovery
// was held for the full round trip no matter who had already left.
func (d *DiscoveryCache) refresh(ctx context.Context) error {
	ch := d.sf.DoChan("discovery", func() (any, error) {
		// ServerGroupsAndResources returns partial results alongside an error
		// when an aggregated API is unavailable; that is common (a broken
		// metrics-server) and must not blank out the whole resource list.
		groups, lists, err := d.client.ServerGroupsAndResources()
		if len(lists) == 0 && err != nil {
			return nil, fmt.Errorf("discovery: %w", err)
		}

		preferred := make(map[string]string, len(groups)) // group -> preferred version
		for _, g := range groups {
			preferred[g.Name] = g.PreferredVersion.Version
		}

		var out []APIResource
		byGVR := make(map[schema.GroupVersionResource]APIResource)
		aliases := make(map[string]schema.GroupVersionResource)

		for _, list := range lists {
			if list == nil {
				continue
			}
			gv, parseErr := schema.ParseGroupVersion(list.GroupVersion)
			if parseErr != nil {
				continue
			}
			for _, r := range list.APIResources {
				// Subresources (pods/log, deployments/scale) are addressed
				// through their parent, not listed as resources.
				if strings.Contains(r.Name, "/") {
					continue
				}
				ar := APIResource{
					Group:        gv.Group,
					Version:      gv.Version,
					Name:         r.Name,
					SingularName: r.SingularName,
					Kind:         r.Kind,
					Namespaced:   r.Namespaced,
					Verbs:        []string(r.Verbs),
					ShortNames:   r.ShortNames,
					Categories:   r.Categories,
					Preferred:    preferred[gv.Group] == gv.Version,
				}
				out = append(out, ar)
				byGVR[ar.GVR()] = ar

				if ar.Preferred {
					register(aliases, ar)
				} else if _, exists := aliases[aliasKey(ar.Name, ar.Group)]; !exists {
					// Still addressable by its fully qualified name.
					aliases[aliasKey(ar.Name, ar.Group)] = ar.GVR()
				}
			}
		}

		sort.Slice(out, func(i, j int) bool {
			if out[i].Group != out[j].Group {
				return out[i].Group < out[j].Group
			}
			if out[i].Name != out[j].Name {
				return out[i].Name < out[j].Name
			}
			return out[i].Version < out[j].Version
		})

		d.mu.Lock()
		d.resources, d.byGVR, d.aliases, d.fetchedAt = out, byGVR, aliases, time.Now()
		d.mu.Unlock()
		return nil, nil
	})

	select {
	case <-ctx.Done():
		return ctx.Err()
	case r := <-ch:
		return r.Err
	}
}

// register records every spelling that should resolve to this resource.
func register(aliases map[string]schema.GroupVersionResource, ar APIResource) {
	gvr := ar.GVR()
	keys := []string{
		aliasKey(ar.Name, ar.Group),
		aliasKey(ar.SingularName, ar.Group),
		aliasKey(strings.ToLower(ar.Kind), ar.Group),
	}
	for _, sn := range ar.ShortNames {
		keys = append(keys, aliasKey(sn, ar.Group))
	}
	// Unqualified spellings are only claimed by core-group resources and by
	// the first group to register them, so "deployments" means apps/v1 and
	// never some CRD that happens to reuse the name.
	unqualified := []string{ar.Name, ar.SingularName, strings.ToLower(ar.Kind)}
	unqualified = append(unqualified, ar.ShortNames...)

	for _, k := range keys {
		if k != "|" {
			aliases[k] = gvr
		}
	}
	for _, k := range unqualified {
		if k == "" {
			continue
		}
		if _, taken := aliases[k]; !taken || ar.Group == "" {
			aliases[k] = gvr
		}
	}
}

func aliasKey(name, group string) string { return name + "|" + group }

// Resolve turns a user-supplied group/version/resource into a discovered
// resource. Version may be empty to mean "the preferred version", and group
// accepts the literal "core" for the legacy group.
//
// A miss forces one discovery refresh before giving up. Without that, a CRD
// installed by anything other than this dashboard stays invisible until the
// TTL expires, which is a confusing several minutes for whoever just ran
// kubectl apply. The refresh is rate-limited so a stale bookmark pointing at a
// resource that genuinely does not exist cannot turn into a hot loop.
func (d *DiscoveryCache) Resolve(ctx context.Context, group, version, resource string) (APIResource, error) {
	if _, err := d.Resources(ctx); err != nil {
		return APIResource{}, err
	}
	group = NormalizeGroup(group)

	if ar, ok := d.lookup(group, version, resource); ok {
		return ar, nil
	}

	if !d.shouldRefreshOnMiss() {
		return APIResource{}, &UnknownResourceError{Group: group, Version: version, Resource: resource}
	}
	if err := d.refresh(ctx); err != nil {
		// A refresh that failed has not established anything about the
		// resource. UnknownResourceError says "this cluster does not serve
		// X" and reaches the reader as a 404, and it would be saying that on
		// the strength of a cache already known to be stale — which is why the
		// refresh was attempted — plus a lookup that never happened. Whoever
		// just ran kubectl apply then goes and debugs a CRD they installed
		// perfectly well, which is the several confusing minutes this whole
		// refresh-on-miss path exists to prevent.
		return APIResource{}, fmt.Errorf(
			"could not confirm whether %q is served by this cluster: %w", resource, err)
	}
	if ar, ok := d.lookup(group, version, resource); ok {
		return ar, nil
	}
	return APIResource{}, &UnknownResourceError{Group: group, Version: version, Resource: resource}
}

// shouldRefreshOnMiss reports whether a miss may trigger a discovery reload,
// claiming the slot if so.
func (d *DiscoveryCache) shouldRefreshOnMiss() bool {
	const minInterval = 10 * time.Second
	d.mu.Lock()
	defer d.mu.Unlock()
	if time.Since(d.missRefreshAt) < minInterval {
		return false
	}
	d.missRefreshAt = time.Now()
	return true
}

// lookup resolves against the current snapshot without refreshing.
func (d *DiscoveryCache) lookup(group, version, resource string) (APIResource, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	if version != "" && version != "_" {
		gvr := schema.GroupVersionResource{Group: group, Version: version, Resource: resource}
		if ar, ok := d.byGVR[gvr]; ok {
			return ar, true
		}
		// Fall through to alias resolution so a stale bookmark still works.
	}
	if gvr, ok := d.aliases[aliasKey(resource, group)]; ok {
		return d.byGVR[gvr], true
	}
	if gvr, ok := d.aliases[resource]; ok {
		return d.byGVR[gvr], true
	}
	return APIResource{}, false
}

// NormalizeGroup maps the URL-friendly placeholders ("core", "_", "-") onto
// the empty core group. It is the single spelling authority: every endpoint
// that accepts a group parameter must agree on which placeholders work.
func NormalizeGroup(group string) string {
	if group == "core" || group == "_" || group == "-" {
		return ""
	}
	return group
}

// UnknownResourceError signals that a cluster does not serve a resource.
type UnknownResourceError struct {
	Group, Version, Resource string
}

func (e *UnknownResourceError) Error() string {
	gv := e.Group
	if gv == "" {
		gv = "core"
	}
	if e.Version != "" {
		gv += "/" + e.Version
	}
	return fmt.Sprintf("resource %q is not served by this cluster (group %s)", e.Resource, gv)
}

// ServerVersion reports the cluster's reported version string.
func (d *DiscoveryCache) ServerVersion() (string, error) {
	v, err := d.client.ServerVersion()
	if err != nil {
		return "", err
	}
	return v.GitVersion, nil
}

// Invalidate drops cached discovery, used after a CRD is created or deleted.
func (d *DiscoveryCache) Invalidate() {
	d.mu.Lock()
	d.fetchedAt = time.Time{}
	d.missRefreshAt = time.Time{}
	d.mu.Unlock()
	if inv, ok := d.client.(discovery.CachedDiscoveryInterface); ok {
		inv.Invalidate()
	}
}
