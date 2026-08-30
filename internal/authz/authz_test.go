package authz

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	authzv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

// allowFunc decides a review from its resource attributes.
type allowFunc func(ra *authzv1.ResourceAttributes) bool

// fakeClient returns a clientset that answers SubjectAccessReviews with decide,
// counting how many reviews actually reached the "API server".
func fakeClient(decide allowFunc) (*fake.Clientset, *atomic.Int64) {
	client := fake.NewSimpleClientset()
	var calls atomic.Int64

	react := func(action ktesting.Action) (bool, runtime.Object, error) {
		calls.Add(1)
		create, ok := action.(ktesting.CreateAction)
		if !ok {
			return false, nil, nil
		}
		switch review := create.GetObject().(type) {
		case *authzv1.SubjectAccessReview:
			review.Status = authzv1.SubjectAccessReviewStatus{
				Allowed: decide(review.Spec.ResourceAttributes),
				Reason:  "test",
			}
			return true, review, nil
		case *authzv1.SelfSubjectAccessReview:
			review.Status = authzv1.SubjectAccessReviewStatus{
				Allowed: decide(review.Spec.ResourceAttributes),
				Reason:  "test",
			}
			return true, review, nil
		}
		return false, nil, nil
	}

	client.PrependReactor("create", "subjectaccessreviews", react)
	client.PrependReactor("create", "selfsubjectaccessreviews", react)
	return client, &calls
}

func TestAllowedReflectsTheApiServerVerdict(t *testing.T) {
	client, _ := fakeClient(func(ra *authzv1.ResourceAttributes) bool {
		return ra.Verb == "list"
	})
	c, err := NewChecker(128, time.Minute, 50)
	if err != nil {
		t.Fatal(err)
	}

	subj := Subject{User: "alice@example.com", Groups: []string{"oidc:devs"}}
	ctx := context.Background()

	allowed, err := c.Allowed(ctx, client, subj, Attributes{Verb: "list", Resource: "pods", Namespace: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	if !allowed.Allowed {
		t.Error("list should have been allowed")
	}

	denied, err := c.Allowed(ctx, client, subj, Attributes{Verb: "delete", Resource: "pods", Namespace: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	if denied.Allowed {
		t.Error("delete should have been denied")
	}
}

func TestAllowedPassesTheImpersonatedIdentity(t *testing.T) {
	// The whole security model rests on the review naming the end user, not
	// the dashboard's service account.
	client := fake.NewSimpleClientset()
	var gotUser string
	var gotGroups []string

	client.PrependReactor("create", "subjectaccessreviews",
		func(action ktesting.Action) (bool, runtime.Object, error) {
			review := action.(ktesting.CreateAction).GetObject().(*authzv1.SubjectAccessReview)
			gotUser = review.Spec.User
			gotGroups = review.Spec.Groups
			review.Status = authzv1.SubjectAccessReviewStatus{Allowed: true}
			return true, review, nil
		})

	c, _ := NewChecker(128, time.Minute, 50)
	_, err := c.Allowed(context.Background(), client,
		Subject{User: "alice@example.com", Groups: []string{"oidc:devs", "oidc:sre"}},
		Attributes{Verb: "get", Resource: "pods"})
	if err != nil {
		t.Fatal(err)
	}

	if gotUser != "alice@example.com" {
		t.Errorf("review was made for %q", gotUser)
	}
	if len(gotGroups) != 2 {
		t.Errorf("groups were not forwarded: %v", gotGroups)
	}
}

func TestAllowedCachesVerdicts(t *testing.T) {
	client, calls := fakeClient(func(*authzv1.ResourceAttributes) bool { return true })
	c, _ := NewChecker(128, time.Minute, 50)

	subj := Subject{User: "alice"}
	attrs := Attributes{Verb: "list", Resource: "pods", Namespace: "demo"}

	for i := 0; i < 10; i++ {
		if _, err := c.Allowed(context.Background(), client, subj, attrs); err != nil {
			t.Fatal(err)
		}
	}
	// A fifty-row table must not become fifty round trips.
	if n := calls.Load(); n != 1 {
		t.Errorf("made %d reviews for the same question, want 1", n)
	}
}

func TestCacheKeyIncludesEverythingThatMatters(t *testing.T) {
	client, calls := fakeClient(func(*authzv1.ResourceAttributes) bool { return true })
	c, _ := NewChecker(128, time.Minute, 50)
	ctx := context.Background()

	base := Attributes{Verb: "list", Resource: "pods", Namespace: "demo"}
	variants := []struct {
		name  string
		subj  Subject
		attrs Attributes
	}{
		{"baseline", Subject{User: "alice"}, base},
		{"different user", Subject{User: "bob"}, base},
		{"different group set", Subject{User: "alice", Groups: []string{"g"}}, base},
		{"different verb", Subject{User: "alice"}, Attributes{Verb: "delete", Resource: "pods", Namespace: "demo"}},
		{"different namespace", Subject{User: "alice"}, Attributes{Verb: "list", Resource: "pods", Namespace: "other"}},
		{"different resource", Subject{User: "alice"}, Attributes{Verb: "list", Resource: "secrets", Namespace: "demo"}},
		{"subresource", Subject{User: "alice"}, Attributes{Verb: "list", Resource: "pods", Subresource: "log", Namespace: "demo"}},
		{"named object", Subject{User: "alice"}, Attributes{Verb: "list", Resource: "pods", Namespace: "demo", Name: "p1"}},
	}

	for _, v := range variants {
		if _, err := c.Allowed(ctx, client, v.subj, v.attrs); err != nil {
			t.Fatal(err)
		}
	}
	if n := calls.Load(); int(n) != len(variants) {
		t.Errorf("made %d reviews for %d distinct questions; cache keys collide", n, len(variants))
	}
}

func TestSubjectKeyIgnoresGroupOrder(t *testing.T) {
	// Group order varies between tokens from the same provider; treating the
	// two as different subjects would halve the cache hit rate for no reason.
	a := Subject{User: "alice", Groups: []string{"b", "a"}}
	b := Subject{User: "alice", Groups: []string{"a", "b"}}
	if a.key() != b.key() {
		t.Errorf("group order changed the subject key: %q vs %q", a.key(), b.key())
	}
}

func TestAllowedExpiresCachedVerdicts(t *testing.T) {
	client, calls := fakeClient(func(*authzv1.ResourceAttributes) bool { return true })
	c, _ := NewChecker(128, 20*time.Millisecond, 50)

	subj := Subject{User: "alice"}
	attrs := Attributes{Verb: "list", Resource: "pods"}
	ctx := context.Background()

	_, _ = c.Allowed(ctx, client, subj, attrs)
	time.Sleep(40 * time.Millisecond)
	_, _ = c.Allowed(ctx, client, subj, attrs)

	// Revoking a RoleBinding has to take effect promptly.
	if n := calls.Load(); n != 2 {
		t.Errorf("made %d reviews, want 2 (the cached verdict should have expired)", n)
	}
}

func TestConcurrentIdenticalChecksCollapse(t *testing.T) {
	client, calls := fakeClient(func(*authzv1.ResourceAttributes) bool { return true })
	// Make every review slow enough that the goroutines genuinely overlap;
	// otherwise early flights can finish before late goroutines even start,
	// and the count depends on scheduler timing rather than on singleflight.
	client.PrependReactor("create", "*", func(ktesting.Action) (bool, runtime.Object, error) {
		time.Sleep(20 * time.Millisecond)
		return false, nil, nil
	})
	c, _ := NewChecker(128, time.Minute, 50)

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = c.Allowed(context.Background(), client,
				Subject{User: "alice"}, Attributes{Verb: "list", Resource: "pods"})
		}()
	}
	wg.Wait()

	// singleflight should collapse the stampede a freshly rendered table causes.
	if n := calls.Load(); n > 2 {
		t.Errorf("made %d concurrent reviews for one question, want 1", n)
	}
}

// nsList adapts a fixed candidate list to the provider VisibleNamespaces takes.
func nsList(names []string) func() ([]string, error) {
	return func() ([]string, error) { return names, nil }
}

func TestVisibleNamespacesShortCircuitsOnClusterWideAccess(t *testing.T) {
	client, calls := fakeClient(func(*authzv1.ResourceAttributes) bool { return true })
	c, _ := NewChecker(128, time.Minute, 50)

	all, namespaces, err := c.VisibleNamespaces(context.Background(), client,
		Subject{User: "alice"}, Attributes{Verb: "list", Resource: "pods"},
		nsList([]string{"a", "b", "c", "d"}))
	if err != nil {
		t.Fatal(err)
	}
	if !all {
		t.Error("a cluster-wide reader should short-circuit to all namespaces")
	}
	if len(namespaces) != 0 {
		t.Errorf("no per-namespace list is needed when access is cluster-wide, got %v", namespaces)
	}
	// One review, not one per namespace.
	if n := calls.Load(); n != 1 {
		t.Errorf("made %d reviews, want 1", n)
	}
}

func TestVisibleNamespacesFallsBackToScan(t *testing.T) {
	client, _ := fakeClient(func(ra *authzv1.ResourceAttributes) bool {
		// Cluster-wide denied; only two namespaces permitted.
		return ra.Namespace == "team-a" || ra.Namespace == "team-c"
	})
	c, _ := NewChecker(512, time.Minute, 50)

	all, namespaces, err := c.VisibleNamespaces(context.Background(), client,
		Subject{User: "alice"}, Attributes{Verb: "list", Resource: "pods"},
		nsList([]string{"team-a", "team-b", "team-c", "kube-system"}))
	if err != nil {
		t.Fatal(err)
	}
	if all {
		t.Fatal("a narrowly bound user must not be treated as cluster-wide")
	}
	if len(namespaces) != 2 || namespaces[0] != "team-a" || namespaces[1] != "team-c" {
		t.Errorf("scan returned %v, want [team-a team-c] sorted", namespaces)
	}
}

func TestVisibleNamespacesReportsTruncation(t *testing.T) {
	client, _ := fakeClient(func(ra *authzv1.ResourceAttributes) bool { return ra.Namespace != "" })
	c, _ := NewChecker(1024, time.Minute, 3)

	_, namespaces, err := c.VisibleNamespaces(context.Background(), client,
		Subject{User: "alice"}, Attributes{Verb: "list", Resource: "pods"},
		nsList([]string{"a", "b", "c", "d", "e"}))

	// Silently showing three of five namespaces would look like the other two
	// are empty, so truncation has to be reported.
	if err == nil {
		t.Error("a truncated scan must be reported to the caller")
	}
	if len(namespaces) != 3 {
		t.Errorf("scanned %d namespaces, want the configured limit of 3", len(namespaces))
	}
}

func TestAllowedManyReturnsOneVerdictPerQuestion(t *testing.T) {
	client, _ := fakeClient(func(ra *authzv1.ResourceAttributes) bool { return ra.Verb == "get" })
	c, _ := NewChecker(128, time.Minute, 50)

	questions := []Attributes{
		{Verb: "get", Resource: "pods", Namespace: "demo"},
		{Verb: "delete", Resource: "pods", Namespace: "demo"},
		{Verb: "get", Resource: "secrets", Namespace: "demo"},
	}
	got := c.AllowedMany(context.Background(), client, Subject{User: "alice"}, questions)

	if len(got) != len(questions) {
		t.Fatalf("got %d verdicts for %d questions", len(got), len(questions))
	}
	if !got[AttributesKey(questions[0])].Allowed {
		t.Error("get pods should be allowed")
	}
	if got[AttributesKey(questions[1])].Allowed {
		t.Error("delete pods should be denied")
	}
}

func TestSelfSubjectReviewUsedForPassthrough(t *testing.T) {
	client := fake.NewSimpleClientset()
	var sawSelf bool
	client.PrependReactor("create", "selfsubjectaccessreviews",
		func(action ktesting.Action) (bool, runtime.Object, error) {
			sawSelf = true
			r := action.(ktesting.CreateAction).GetObject().(*authzv1.SelfSubjectAccessReview)
			r.Status = authzv1.SubjectAccessReviewStatus{Allowed: true}
			return true, r, nil
		})

	c, _ := NewChecker(128, time.Minute, 50)
	if _, err := c.Allowed(context.Background(), client,
		Subject{Self: true}, Attributes{Verb: "list", Resource: "pods"}); err != nil {
		t.Fatal(err)
	}
	if !sawSelf {
		t.Error("a Self subject must use SelfSubjectAccessReview")
	}
}

func TestSelfVerdictsAreCachedPerIdentity(t *testing.T) {
	// Two passthrough users ask the same question with different standing.
	// Each carries their own client; the checker must not serve one user's
	// cached verdict to the other.
	allowAll, _ := fakeClient(func(*authzv1.ResourceAttributes) bool { return true })
	denyAll, _ := fakeClient(func(*authzv1.ResourceAttributes) bool { return false })

	c, _ := NewChecker(128, time.Minute, 50)
	attrs := Attributes{Verb: "list", Resource: "secrets", Namespace: "prod"}

	d, err := c.Allowed(context.Background(), allowAll, Subject{Self: true, SelfID: "alice"}, attrs)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Allowed {
		t.Fatal("alice should be allowed")
	}

	d, err = c.Allowed(context.Background(), denyAll, Subject{Self: true, SelfID: "bob"}, attrs)
	if err != nil {
		t.Fatal(err)
	}
	if d.Allowed {
		t.Error("bob received alice's cached verdict: Self subjects must be cached per identity")
	}
}

// The candidate list is a real cost — a cache read at best, an API round trip
// at worst — and the subject who never needs it is the common one.
func TestVisibleNamespacesNeverAsksForCandidatesItDoesNotNeed(t *testing.T) {
	client, _ := fakeClient(func(*authzv1.ResourceAttributes) bool { return true })
	c, _ := NewChecker(128, time.Minute, 50)

	asked := 0
	all, _, err := c.VisibleNamespaces(context.Background(), client,
		Subject{User: "alice"}, Attributes{Verb: "list", Resource: "pods"},
		func() ([]string, error) { asked++; return []string{"a", "b"}, nil })
	if err != nil || !all {
		t.Fatalf("cluster-wide reader: all=%v err=%v", all, err)
	}
	if asked != 0 {
		t.Errorf("candidates were requested %d times for a cluster-wide subject", asked)
	}

	// Nor on a cache hit: the second narrow answer comes from the entry the
	// first one stored.
	client2, _ := fakeClient(func(ra *authzv1.ResourceAttributes) bool { return ra.Namespace == "team-a" })
	c2, _ := NewChecker(128, time.Minute, 50)
	asked = 0
	provider := func() ([]string, error) { asked++; return []string{"team-a", "team-b"}, nil }
	subj := Subject{User: "bob"}
	attrs := Attributes{Verb: "list", Resource: "pods"}
	if _, _, err := c2.VisibleNamespaces(context.Background(), client2, subj, attrs, provider); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c2.VisibleNamespaces(context.Background(), client2, subj, attrs, provider); err != nil {
		t.Fatal(err)
	}
	if asked != 1 {
		t.Errorf("candidates were requested %d times across two calls, want 1", asked)
	}
}

// The failure this whole shape exists to prevent. Scanning an empty candidate
// list finds nothing allowed and is indistinguishable from a subject permitted
// nowhere — and that verdict would be cached and served for the rest of the
// TTL, so one hiccup becomes a standing, wrong "you may not".
func TestVisibleNamespacesDoesNotCacheAFailureAsAnEmptyScope(t *testing.T) {
	client, _ := fakeClient(func(ra *authzv1.ResourceAttributes) bool { return ra.Namespace == "team-a" })
	c, _ := NewChecker(128, time.Minute, 50)
	subj := Subject{User: "alice"}
	attrs := Attributes{Verb: "list", Resource: "pods"}

	boom := errors.New("namespaces could not be listed")
	all, namespaces, err := c.VisibleNamespaces(context.Background(), client, subj, attrs,
		func() ([]string, error) { return nil, boom })
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the provider's failure", err)
	}
	if all || len(namespaces) != 0 {
		t.Errorf("a failed scan returned a scope: all=%v ns=%v", all, namespaces)
	}

	// The next call, with the namespaces readable again, must get the real
	// answer rather than a cached "nowhere".
	all, namespaces, err = c.VisibleNamespaces(context.Background(), client, subj, attrs,
		nsList([]string{"team-a", "team-b"}))
	if err != nil {
		t.Fatal(err)
	}
	if all {
		t.Fatal("narrow subject reported as cluster-wide")
	}
	if len(namespaces) != 1 || namespaces[0] != "team-a" {
		t.Errorf("recovered scope = %v, want [team-a] — a failure was cached", namespaces)
	}
}

// A batch has to return something for every question, and the answer to one
// that could not be asked used to be the same `allowed: false` as a refusal.
// The console reads these to decide which buttons exist, so a busy API server
// told users they lacked permissions they hold.
func TestAllowedManySaysWhenAQuestionCouldNotBePut(t *testing.T) {
	client := fake.NewSimpleClientset()
	client.PrependReactor("create", "subjectaccessreviews",
		func(ktesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New("apiserver is busy")
		})
	c, _ := NewChecker(128, time.Minute, 50)

	q := Attributes{Verb: "list", Resource: "pods", Namespace: "demo"}
	got := c.AllowedMany(context.Background(), client, Subject{User: "alice"}, []Attributes{q})

	d, ok := got[AttributesKey(q)]
	if !ok {
		t.Fatal("a question that failed got no verdict at all")
	}
	if d.Allowed {
		t.Error("a failed review was reported as permission")
	}
	if d.Denied {
		t.Error("a failed review was reported as an explicit denial")
	}
	if !d.Unavailable {
		t.Error("a failed review was indistinguishable from a refusal")
	}
	if d.Reason == "" {
		t.Error("nothing said why the review did not happen")
	}
}

// Deduplicating identical reviews must not tie one caller's fate to another's.
// The flight belongs to no single request: a caller that has given up waits no
// longer, and the review it walked away from still finishes for whoever else
// is waiting on it.
func TestACallerIsNotHeldByAnothersInFlightReview(t *testing.T) {
	client, _ := fakeClient(func(*authzv1.ResourceAttributes) bool { return true })

	entered := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	var once sync.Once
	client.PrependReactor("create", "*", func(ktesting.Action) (bool, runtime.Object, error) {
		once.Do(func() { close(entered) })
		<-release
		return false, nil, nil
	})

	c, _ := NewChecker(128, time.Minute, 50)
	subj := Subject{User: "alice"}
	attrs := Attributes{Verb: "list", Resource: "pods"}

	go func() { _, _ = c.Allowed(context.Background(), client, subj, attrs) }()
	<-entered

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := c.Allowed(ctx, client, subj, attrs)
		done <- err
	}()
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Allowed returned %v, want the caller's own cancellation", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a caller was held by a review it had already given up on")
	}
}
