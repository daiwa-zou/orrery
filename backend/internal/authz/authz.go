// Package authz answers "may this user do this?" by asking the Kubernetes API
// server, and caches the answer briefly.
//
// Clusterlens deliberately does not reimplement RBAC. Every verdict comes from
// a SubjectAccessReview, so a cluster's Roles, ClusterRoles, webhook
// authorizers and admission plugins remain the single source of truth.
package authz

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
	"golang.org/x/sync/singleflight"
	authzv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Attributes describe one access question.
type Attributes struct {
	Verb        string
	Group       string
	Version     string
	Resource    string
	Subresource string
	Namespace   string
	Name        string
}

func (a Attributes) key() string {
	return strings.Join([]string{a.Verb, a.Group, a.Version, a.Resource, a.Subresource, a.Namespace, a.Name}, "\x1f")
}

// Subject is who is asking.
type Subject struct {
	// User and Groups identify the end user for a SubjectAccessReview.
	User   string
	Groups []string
	// Self selects SelfSubjectAccessReview instead, for clusters where the
	// client already carries the user's own credentials (passthrough mode).
	Self bool
}

func (s Subject) key() string {
	if s.Self {
		return "self"
	}
	g := append([]string(nil), s.Groups...)
	sort.Strings(g)
	return s.User + "\x1e" + strings.Join(g, ",")
}

// Decision is a cached verdict.
type Decision struct {
	Allowed bool   `json:"allowed"`
	Denied  bool   `json:"denied,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

type cacheEntry struct {
	decision Decision
	expires  time.Time
}

// Checker performs and caches access reviews for one cluster.
type Checker struct {
	cache *lru.Cache[string, cacheEntry]
	ttl   time.Duration
	sf    singleflight.Group

	// nsCache memoises the "which namespaces may this user list X in?" scan,
	// which is expensive enough to be worth its own entry.
	nsMu    sync.Mutex
	nsCache map[string]nsEntry
	// scanLimit bounds that scan.
	scanLimit int
}

type nsEntry struct {
	namespaces []string
	expires    time.Time
}

// NewChecker builds a checker with an LRU of the given size and TTL.
func NewChecker(size int, ttl time.Duration, scanLimit int) (*Checker, error) {
	if size <= 0 {
		size = 16384
	}
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	if scanLimit <= 0 {
		scanLimit = 200
	}
	c, err := lru.New[string, cacheEntry](size)
	if err != nil {
		return nil, err
	}
	return &Checker{cache: c, ttl: ttl, nsCache: make(map[string]nsEntry), scanLimit: scanLimit}, nil
}

// Allowed reports whether the subject may perform the action. client must be
// the dashboard's own client for SubjectAccessReview, or the user's client
// when Subject.Self is set.
func (c *Checker) Allowed(ctx context.Context, client kubernetes.Interface, subj Subject, attrs Attributes) (Decision, error) {
	key := subj.key() + "\x1d" + attrs.key()

	if e, ok := c.cache.Get(key); ok && time.Now().Before(e.expires) {
		return e.decision, nil
	}

	// Collapse the stampede that a table of fifty rows would otherwise cause.
	v, err, _ := c.sf.Do(key, func() (any, error) {
		d, err := c.review(ctx, client, subj, attrs)
		if err != nil {
			return Decision{}, err
		}
		c.cache.Add(key, cacheEntry{decision: d, expires: time.Now().Add(c.ttl)})
		return d, nil
	})
	if err != nil {
		return Decision{}, err
	}
	return v.(Decision), nil
}

func (c *Checker) review(ctx context.Context, client kubernetes.Interface, subj Subject, attrs Attributes) (Decision, error) {
	ra := &authzv1.ResourceAttributes{
		Namespace:   attrs.Namespace,
		Verb:        attrs.Verb,
		Group:       attrs.Group,
		Version:     attrs.Version,
		Resource:    attrs.Resource,
		Subresource: attrs.Subresource,
		Name:        attrs.Name,
	}

	if subj.Self {
		res, err := client.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx,
			&authzv1.SelfSubjectAccessReview{Spec: authzv1.SelfSubjectAccessReviewSpec{ResourceAttributes: ra}},
			metav1.CreateOptions{})
		if err != nil {
			return Decision{}, fmt.Errorf("selfsubjectaccessreview: %w", err)
		}
		return Decision{Allowed: res.Status.Allowed, Denied: res.Status.Denied, Reason: res.Status.Reason}, nil
	}

	res, err := client.AuthorizationV1().SubjectAccessReviews().Create(ctx,
		&authzv1.SubjectAccessReview{Spec: authzv1.SubjectAccessReviewSpec{
			ResourceAttributes: ra,
			User:               subj.User,
			Groups:             subj.Groups,
		}}, metav1.CreateOptions{})
	if err != nil {
		return Decision{}, fmt.Errorf("subjectaccessreview: %w", err)
	}
	return Decision{Allowed: res.Status.Allowed, Denied: res.Status.Denied, Reason: res.Status.Reason}, nil
}

// AllowedMany answers a batch of questions concurrently. The UI uses it to
// decide which buttons to render without a request per button.
func (c *Checker) AllowedMany(ctx context.Context, client kubernetes.Interface, subj Subject, list []Attributes) map[string]Decision {
	out := make(map[string]Decision, len(list))
	var mu sync.Mutex
	var wg sync.WaitGroup

	sem := make(chan struct{}, 8)
	for _, attrs := range list {
		wg.Add(1)
		go func(a Attributes) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			d, err := c.Allowed(ctx, client, subj, a)
			if err != nil {
				d = Decision{Allowed: false, Reason: err.Error()}
			}
			mu.Lock()
			out[a.key()] = d
			mu.Unlock()
		}(attrs)
	}
	wg.Wait()
	return out
}

// VisibleNamespaces returns the namespaces in which the subject may perform a
// verb on a resource. It first tries the cluster-wide question, which is one
// round trip and covers most users, and only falls back to a per-namespace
// scan for users with narrowly scoped bindings.
func (c *Checker) VisibleNamespaces(
	ctx context.Context,
	client kubernetes.Interface,
	subj Subject,
	attrs Attributes,
	allNamespaces []string,
) (all bool, namespaces []string, err error) {
	clusterWide := attrs
	clusterWide.Namespace = ""
	d, err := c.Allowed(ctx, client, subj, clusterWide)
	if err != nil {
		return false, nil, err
	}
	if d.Allowed {
		return true, nil, nil
	}

	cacheKey := subj.key() + "\x1d" + attrs.key() + "\x1dns"
	c.nsMu.Lock()
	if e, ok := c.nsCache[cacheKey]; ok && time.Now().Before(e.expires) {
		c.nsMu.Unlock()
		return false, e.namespaces, nil
	}
	c.nsMu.Unlock()

	scan := allNamespaces
	truncated := false
	if len(scan) > c.scanLimit {
		scan = scan[:c.scanLimit]
		truncated = true
	}

	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 16)
	allowed := make([]string, 0, 8)

	for _, ns := range scan {
		wg.Add(1)
		go func(ns string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			a := attrs
			a.Namespace = ns
			dec, err := c.Allowed(ctx, client, subj, a)
			if err == nil && dec.Allowed {
				mu.Lock()
				allowed = append(allowed, ns)
				mu.Unlock()
			}
		}(ns)
	}
	wg.Wait()
	sort.Strings(allowed)

	c.nsMu.Lock()
	c.nsCache[cacheKey] = nsEntry{namespaces: allowed, expires: time.Now().Add(c.ttl)}
	c.nsMu.Unlock()

	if truncated {
		return false, allowed, fmt.Errorf("namespace scan truncated at %d namespaces", c.scanLimit)
	}
	return false, allowed, nil
}

// Purge drops every cached verdict, used when an operator wants a permission
// change to take effect immediately.
func (c *Checker) Purge() {
	c.cache.Purge()
	c.nsMu.Lock()
	c.nsCache = make(map[string]nsEntry)
	c.nsMu.Unlock()
}

// AttributesKey exposes the canonical key so callers can index AllowedMany's
// result without knowing the encoding.
func AttributesKey(a Attributes) string { return a.key() }
