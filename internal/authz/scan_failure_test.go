package authz

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	authzv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

// The per-namespace scan asks one SubjectAccessReview per candidate, and a
// review can fail for reasons that have nothing to do with the answer: the API
// server is throttling, the request that started the scan was cancelled, a
// network blip. The loop used to keep only `err == nil && dec.Allowed`, which
// silently folds every one of those into "not allowed".
//
// That is the same conflation the rest of this package is written to avoid,
// and here it was worse than a wrong render: the narrowed scope was written to
// nsCache and served for the full TTL, so one hiccup produced a standing,
// self-healing-in-30-seconds "you may not list pods" for a user who could.
//
// A failed review now makes the scan report itself as incomplete and keeps it
// out of the cache. Truncation is still cached, because hitting the same limit
// on the same list is a fact rather than a hiccup.

// failingClient answers reviews with decide, except for the namespaces in
// broken, whose reviews return an error. Flipping healed makes them succeed,
// which is what a transient failure looks like from the next request's side.
func failingClient(decide allowFunc, broken map[string]bool, healed *atomic.Bool) *fake.Clientset {
	client := fake.NewSimpleClientset()

	react := func(action ktesting.Action) (bool, runtime.Object, error) {
		create, ok := action.(ktesting.CreateAction)
		if !ok {
			return false, nil, nil
		}
		review, ok := create.GetObject().(*authzv1.SubjectAccessReview)
		if !ok {
			return false, nil, nil
		}
		ns := review.Spec.ResourceAttributes.Namespace
		if broken[ns] && (healed == nil || !healed.Load()) {
			return true, nil, errors.New("the API server is too busy")
		}
		review.Status = authzv1.SubjectAccessReviewStatus{
			Allowed: decide(review.Spec.ResourceAttributes),
			Reason:  "test",
		}
		return true, review, nil
	}

	client.PrependReactor("create", "subjectaccessreviews", react)
	return client
}

func TestVisibleNamespacesReportsReviewsItCouldNotPerform(t *testing.T) {
	// Cluster-wide denied, so the scan runs; every namespace would be allowed
	// if it could be asked, but two of them cannot.
	client := failingClient(
		func(ra *authzv1.ResourceAttributes) bool { return ra.Namespace != "" },
		map[string]bool{"team-b": true, "team-d": true}, nil)
	c, _ := NewChecker(512, time.Minute, 50)

	all, namespaces, err := c.VisibleNamespaces(context.Background(), client,
		Subject{User: "alice"}, Attributes{Verb: "list", Resource: "pods"},
		nsList([]string{"team-a", "team-b", "team-c", "team-d"}))

	if all {
		t.Fatal("a narrowly bound user must not be treated as cluster-wide")
	}
	if err == nil {
		t.Fatal("two unanswerable reviews came back as two denials, with no error")
	}
	if msg := err.Error(); !strings.Contains(msg, "2 of 4") {
		t.Errorf("error = %q, want it to say how much of the scan is missing", msg)
	}
	// The namespaces that did answer are still worth returning: this is a
	// partial answer, not a failure.
	if len(namespaces) != 2 || namespaces[0] != "team-a" || namespaces[1] != "team-c" {
		t.Errorf("namespaces = %v, want the two that answered", namespaces)
	}
}

func TestVisibleNamespacesDoesNotCacheAScanThatFailed(t *testing.T) {
	var healed atomic.Bool
	client := failingClient(
		func(ra *authzv1.ResourceAttributes) bool { return ra.Namespace != "" },
		map[string]bool{"team-a": true, "team-b": true}, &healed)
	// A TTL far longer than the test, so a cached wrong answer could not
	// expire its way to correctness.
	c, _ := NewChecker(512, time.Hour, 50)
	subj := Subject{User: "alice"}
	attrs := Attributes{Verb: "list", Resource: "pods"}
	candidates := nsList([]string{"team-a", "team-b"})

	if _, ns, err := c.VisibleNamespaces(context.Background(), client, subj, attrs, candidates); err == nil {
		t.Fatalf("a scan where every review failed reported success with %v", ns)
	}

	// The blip passes. The very next request must ask again rather than be
	// served the empty scope the failed scan produced.
	healed.Store(true)

	all, namespaces, err := c.VisibleNamespaces(context.Background(), client, subj, attrs, candidates)
	if err != nil {
		t.Fatalf("the healed scan failed: %v", err)
	}
	if all {
		t.Fatal("cluster-wide access is denied here")
	}
	if len(namespaces) != 2 {
		t.Errorf("namespaces = %v, want both — the failed scan was cached", namespaces)
	}
}
