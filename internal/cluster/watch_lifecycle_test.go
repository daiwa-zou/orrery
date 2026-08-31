package cluster

// Watch's ordering guarantee, and the lifecycle around it.
//
// Watch subscribes and *then* snapshots, so a change landing between the two
// arrives twice — which a client keyed by object ignores — rather than falling
// in the gap and never arriving at all. Reversing those two lines is a change
// that reads as a tidy-up and silently drops objects, and nothing was holding
// the order in place.
//
// Watch also snapshots the entry it subscribed to rather than looking the
// resource up a second time, which matters when the two lookups could return
// different informers. That one is deliberately not asserted here: without a
// hook between the subscribe and the snapshot there is no way to make the entry
// change in between, so any test written for it would pass either way — and a
// test that cannot fail is worse than none, because it reads as cover.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stesting "k8s.io/client-go/testing"
)

// TestWatchLosesNothingBetweenSnapshotAndStream is the union property: every
// object that exists ends up either in the initial snapshot or in the event
// stream. An object that falls between the two is the failure the subscribe
// -then-snapshot order exists to prevent, and reversing that order makes it
// reachable.
func TestWatchLosesNothingBetweenSnapshotAndStream(t *testing.T) {
	m, dyn := newTestInformerManager(t, testCacheConfig(), cmObj("ns1", "seed"))
	ctx := context.Background()

	// The informer is warmed first, and that is what makes this test mean
	// anything. entry() blocks on the initial cache sync, which takes longer
	// than the writer below runs — so a cold Watch returns only once every
	// write has landed, the snapshot contains all of them, and the gap the
	// ordering protects is never opened. Warming it makes the second Watch
	// return immediately, into the middle of the writes.
	if _, err := m.List(ctx, cmAR, "ns1"); err != nil {
		t.Fatalf("warm the informer: %v", err)
	}

	// Writes land continuously, so some of them fall inside the handshake.
	const writes = 40
	written := make(chan string, writes)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(written)
		for i := range writes {
			name := fmt.Sprintf("racer-%02d", i)
			if _, err := dyn.Resource(cmAR.GVR()).Namespace("ns1").
				Create(ctx, cmObj("ns1", name), metav1.CreateOptions{}); err != nil {
				return
			}
			written <- name
			time.Sleep(time.Millisecond)
		}
	}()

	// Attach partway through the writes.
	time.Sleep(5 * time.Millisecond)
	sub, initial, err := m.Watch(ctx, cmAR, "ns1", 512)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	defer sub.Close()

	seen := map[string]bool{}
	for _, o := range initial {
		seen[o.GetName()] = true
	}

	wg.Wait()
	var want []string
	for name := range written {
		want = append(want, name)
	}

	// Drain until everything written has been accounted for, or time runs out.
	deadline := time.After(10 * time.Second)
	missing := func() []string {
		var out []string
		for _, n := range want {
			if !seen[n] {
				out = append(out, n)
			}
		}
		return out
	}
	for len(missing()) > 0 {
		select {
		case ev, ok := <-sub.Events:
			if !ok {
				t.Fatalf("stream closed with %v never delivered", missing())
			}
			if ev.Object != nil {
				seen[ev.Object.GetName()] = true
			}
		case <-deadline:
			t.Fatalf("objects written during the handshake reached neither the "+
				"snapshot nor the stream: %v", missing())
		}
	}
}

// A live subscriber pins its informer: evictIdle skips any entry with
// subscribers and touches it instead, which is what stops a watched cache being
// retired out from under an open socket. With the idle timeout at a nanosecond
// every unwatched entry is stale on sight, so the only thing keeping this one
// alive is that branch.
//
// Run under -race, this is also where a lock inversion between the evictor and
// the subscribe path would surface.
func TestEvictionSparesAWatchedInformerUnderChurn(t *testing.T) {
	cfg := testCacheConfig()
	cfg.IdleTimeout = time.Nanosecond // everything unwatched is instantly stale
	cfg.MaxInformersPerCluster = 1    // and the cap is always pressing
	m, dyn := newTestInformerManager(t, cfg, cmObj("ns1", "a"), cmObj("ns2", "b"))
	ctx := context.Background()

	sub, initial, err := m.Watch(ctx, cmAR, "ns1", 64)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	defer sub.Close()
	if len(initial) != 1 {
		t.Fatalf("initial = %v, want the one seeded object in ns1", initial)
	}

	m.mu.Lock()
	watched := m.entries[cmAR.GVR()]
	m.mu.Unlock()

	// Evict hard around the open subscription. The sweep is the cheap part and
	// the part under test, so it runs often; building the other informer costs
	// a full sync, so it runs just enough to put the cap in play — with the
	// watched entry the only resident one, enforceCapLocked has to decline to
	// evict it and admit the second anyway.
	for range 500 {
		m.evictIdle()
	}
	for range 3 {
		_, _ = m.List(ctx, secretAR, "")
		m.evictIdle()
	}

	m.mu.Lock()
	still := m.entries[cmAR.GVR()]
	m.mu.Unlock()
	if still != watched {
		t.Fatalf("the watched informer was evicted from under its subscriber "+
			"(entry %p -> %p)", watched, still)
	}

	// And it is still a working subscription, not merely a resident pointer.
	if _, err := dyn.Resource(cmAR.GVR()).Namespace("ns1").
		Create(ctx, cmObj("ns1", "after-churn"), metav1.CreateOptions{}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-sub.Events:
			if !ok {
				t.Fatal("the subscription was closed by the churn")
			}
			if ev.Object != nil && ev.Object.GetName() == "after-churn" {
				return
			}
		case <-deadline:
			t.Fatal("the pinned subscription stopped delivering events")
		}
	}
}

// Several callers waiting on one informer that then fails must each get the
// error, and the entry must be retired exactly once however many of them
// noticed — a double shutdown would close an already-closed channel and take
// the process with it.
func TestConcurrentCallersShareOneInformerFailure(t *testing.T) {
	m, dyn := newTestInformerManager(t, testCacheConfig())
	dyn.PrependReactor("list", "configmaps", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("rbac denied")
	})

	const callers = 16
	errs := make([]error, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = m.List(context.Background(), cmAR, "")
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err == nil {
			t.Errorf("caller %d was served from a cache that could not be built", i)
			continue
		}
		if !strings.Contains(err.Error(), "rbac denied") {
			t.Errorf("caller %d got %v, want the underlying reason", i, err)
		}
	}
	if got := m.Stats(); len(got) != 0 {
		t.Errorf("a failed informer was left resident: %+v", got)
	}
}
