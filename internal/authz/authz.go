// Package authz answers "may this user do this?" by asking the Kubernetes API
// server, and caches the answer briefly.
//
// Orrery deliberately does not reimplement RBAC. Every verdict comes from
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
	// SelfID discriminates cached Self verdicts between users. Without it every
	// passthrough user shares one cache key and one user's allow is served to
	// the next — a cross-user authorization bypass. Leave it empty only when
	// every caller genuinely shares one identity (serviceaccount mode).
	SelfID string
}

func (s Subject) key() string {
	if s.Self {
		return "self\x1e" + s.SelfID
	}
	g := append([]string(nil), s.Groups...)
	sort.Strings(g)
	return s.User + "\x1e" + strings.Join(g, ",")
}

// Decision is a cached verdict.
type Decision struct {
	Allowed bool `json:"allowed"`
	Denied  bool `json:"denied,omitempty"`
	// Unavailable marks a verdict that was never reached: the review itself
	// failed. Allowed is false either way, and that is the whole difficulty —
	// a batch of questions has to answer every one of them, so the answer to
	// an unaskable question used to be the same false as a refusal, and the
	// console greyed a button out with "you cannot list this" because the API
	// server was busy for a moment. Reason then carries the failure.
	//
	// It is the distinction countSummary draws for counts, in the one place
	// the UI decides what a user may do.
	Unavailable bool   `json:"unavailable,omitempty"`
	Reason      string `json:"reason,omitempty"`
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
	// truncated records that the scan hit scanLimit, so a cache hit reports
	// the same partial-answer error the original scan did. Losing it turned
	// "we could not check everything" into a flat (and wrong) "forbidden".
	truncated bool
	expires   time.Time
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

// reviewTimeout bounds one deduplicated access review. It exists because the
// review outlives the caller that started it, so nothing else would ever stop
// it; it is deliberately longer than any page load is willing to wait, since
// its job is to bound a leak and not to decide how patient a caller is.
const reviewTimeout = 30 * time.Second

// Allowed reports whether the subject may perform the action. client must be
// the dashboard's own client for SubjectAccessReview, or the user's client
// when Subject.Self is set.
func (c *Checker) Allowed(ctx context.Context, client kubernetes.Interface, subj Subject, attrs Attributes) (Decision, error) {
	key := subj.key() + "\x1d" + attrs.key()

	if e, ok := c.cache.Get(key); ok && time.Now().Before(e.expires) {
		return e.decision, nil
	}

	// Collapse the stampede that a table of fifty rows would otherwise cause.
	//
	// The shared review runs on a context detached from whichever caller
	// happened to start it, and every caller waits on its own. Running it on
	// the first caller's context makes one request's cancellation the other
	// requests' answer: a tab closed mid-flight cancels the review that two
	// other tabs are waiting on, and they are handed "context canceled" for a
	// question RBAC would have answered. That reads downstream as "we could
	// not check", which is at least honest, but it is a failure invented
	// entirely by the deduplication — and it costs the cache entry too, so
	// the next request pays for the round trip again.
	ch := c.sf.DoChan(key, func() (any, error) {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), reviewTimeout)
		defer cancel()
		d, err := c.review(rctx, client, subj, attrs)
		if err != nil {
			return Decision{}, err
		}
		c.cache.Add(key, cacheEntry{decision: d, expires: time.Now().Add(c.ttl)})
		return d, nil
	})

	select {
	case <-ctx.Done():
		return Decision{}, ctx.Err()
	case r := <-ch:
		if r.Err != nil {
			return Decision{}, r.Err
		}
		return r.Val.(Decision), nil
	}
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

	// Taken before the goroutine starts, so a batch of sixty-four questions is
	// eight goroutines rather than sixty-four waiting for eight.
	sem := make(chan struct{}, 8)
	for _, attrs := range list {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			d, err := c.Allowed(ctx, client, subj, attrs)
			if err != nil {
				// A question that could not be put is not a "no". Recording it
				// as one is how a busy API server greys out a button and tells
				// the user they lack a permission they hold — and the console
				// asks these in batches precisely where it decides what a user
				// may do.
				d = Decision{Unavailable: true, Reason: err.Error()}
			}
			mu.Lock()
			out[attrs.key()] = d
			mu.Unlock()
		}()
	}
	wg.Wait()
	return out
}

// candidates supplies the namespaces to scan, and is called only when a scan
// is actually needed — never for a cluster-wide subject and never on a cache
// hit. An error from it aborts the answer rather than being read as an empty
// scope; see the note at the call.
//
// VisibleNamespaces returns the namespaces in which the subject may perform a
// verb on a resource. It first tries the cluster-wide question, which is one
// round trip and covers most users, and only falls back to a per-namespace
// scan for users with narrowly scoped bindings.
func (c *Checker) VisibleNamespaces(
	ctx context.Context,
	client kubernetes.Interface,
	subj Subject,
	attrs Attributes,
	candidates func() ([]string, error),
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
		if e.truncated {
			return false, e.namespaces, fmt.Errorf("namespace scan truncated at %d namespaces", c.scanLimit)
		}
		return false, e.namespaces, nil
	}
	c.nsMu.Unlock()

	// Asked for only now. A subject with cluster-wide access never reaches
	// here, and neither does a cache hit, so the namespace list is a cost only
	// the scan actually pays.
	//
	// It is also the one input whose absence must not be mistaken for an
	// answer. Scanning an empty candidate list finds nothing allowed and looks
	// exactly like a subject permitted nowhere — and that verdict would then be
	// cached and served for the rest of the TTL. Failing here keeps "we could
	// not ask" out of the cache and out of the caller's hands.
	allNamespaces, err := candidates()
	if err != nil {
		return false, nil, err
	}

	scan := allNamespaces
	truncated := false
	if len(scan) > c.scanLimit {
		scan = scan[:c.scanLimit]
		truncated = true
	}

	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		allowed  = make([]string, 0, 8)
		failed   int
		firstErr error
	)
	sem := make(chan struct{}, 16)

	for _, ns := range scan {
		wg.Add(1)
		go func(ns string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			a := attrs
			a.Namespace = ns
			dec, err := c.Allowed(ctx, client, subj, a)

			mu.Lock()
			defer mu.Unlock()
			// A review that could not be performed is not a denial. Counting
			// it as one is how a throttled API server, or a caller who
			// navigated away mid-scan, becomes a permanent-looking "you are
			// allowed nowhere" — see the note below the loop.
			switch {
			case err != nil:
				failed++
				if firstErr == nil {
					firstErr = err
				}
			case dec.Allowed:
				allowed = append(allowed, ns)
			}
		}(ns)
	}
	wg.Wait()
	sort.Strings(allowed)

	// A scan that could not ask every question has not measured a scope; it has
	// measured a lower bound on one. Two things follow, and the code above the
	// loop only ever guarded the first.
	//
	// It must not be cached. Truncation is deterministic — the same scan hits
	// the same limit — so a truncated entry is worth keeping and is kept, with
	// its flag. A failed review is a hiccup: caching it freezes the narrowed
	// scope for the whole TTL, and the next request, which would have
	// succeeded, is answered from the bad one instead.
	//
	// And it must be reported. Every caller already knows what to do with a
	// scan error — surface it as a warning beside a partial answer, or as the
	// unavailability it is when nothing came back — because that is how
	// truncation is handled. Silence is the one answer that misleads: an empty
	// `allowed` with no error is read as a definite "you may not", and the user
	// is sent to their cluster administrator over a timeout.
	if failed > 0 {
		return false, allowed, fmt.Errorf(
			"could not check %d of %d namespaces, so this may not be everywhere you can look: %w",
			failed, len(scan), firstErr)
	}

	c.nsMu.Lock()
	// Expired entries are never read again; sweep them here so the map cannot
	// grow without bound on a multi-tenant deployment.
	now := time.Now()
	for k, e := range c.nsCache {
		if now.After(e.expires) {
			delete(c.nsCache, k)
		}
	}
	c.nsCache[cacheKey] = nsEntry{namespaces: allowed, truncated: truncated, expires: now.Add(c.ttl)}
	c.nsMu.Unlock()

	if truncated {
		return false, allowed, fmt.Errorf("namespace scan truncated at %d namespaces", c.scanLimit)
	}
	return false, allowed, nil
}

// AttributesKey exposes the canonical key so callers can index AllowedMany's
// result without knowing the encoding.
func AttributesKey(a Attributes) string { return a.key() }
