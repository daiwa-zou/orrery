package cluster

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/daiwa-zou/orrery/backend/internal/config"
)

var (
	cmAR = APIResource{Version: "v1", Name: "configmaps", SingularName: "configmap",
		Kind: "ConfigMap", Namespaced: true, Verbs: []string{"get", "list", "watch"}}
	secretAR = APIResource{Version: "v1", Name: "secrets", SingularName: "secret",
		Kind: "Secret", Namespaced: true, Verbs: []string{"get", "list", "watch"}}
)

func cmObj(ns, name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]any{"namespace": ns, "name": name},
	}}
}

func testCacheConfig() config.CacheConfig {
	return config.CacheConfig{
		IdleTimeout:            time.Minute,
		DiscoveryTTL:           time.Minute,
		MaxInformersPerCluster: 64,
		SyncTimeout:            5 * time.Second,
	}
}

func newTestInformerManager(t *testing.T, cfg config.CacheConfig, objs ...runtime.Object) (*InformerManager, *dynfake.FakeDynamicClient) {
	t.Helper()
	listKinds := map[schema.GroupVersionResource]string{
		{Version: "v1", Resource: "configmaps"}: "ConfigMapList",
		{Version: "v1", Resource: "secrets"}:    "SecretList",
	}
	dyn := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds, objs...)
	m := NewInformerManager(dyn, cfg, testLogger())
	t.Cleanup(m.Stop)
	return m, dyn
}

func TestInformerManagerListAndGet(t *testing.T) {
	m, _ := newTestInformerManager(t, testCacheConfig(),
		cmObj("ns1", "a"), cmObj("ns1", "b"), cmObj("ns2", "c"))
	ctx := context.Background()

	all, err := m.List(ctx, cmAR, "")
	if err != nil {
		t.Fatalf("List all: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("List all = %d objects, want 3", len(all))
	}

	ns1, err := m.List(ctx, cmAR, "ns1")
	if err != nil {
		t.Fatalf("List ns1: %v", err)
	}
	if len(ns1) != 2 {
		t.Errorf("List ns1 = %d objects, want 2", len(ns1))
	}

	got, err := m.Get(ctx, cmAR, "ns1", "a")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil || got.GetName() != "a" {
		t.Errorf("Get(ns1/a) = %v", got)
	}

	// Absence is an answer, not an error: the caller decides whether a
	// missing object is a 404.
	missing, err := m.Get(ctx, cmAR, "ns1", "nope")
	if err != nil || missing != nil {
		t.Errorf("Get(missing) = %v, %v, want nil, nil", missing, err)
	}
}

func TestInformerManagerWatch(t *testing.T) {
	m, dyn := newTestInformerManager(t, testCacheConfig(), cmObj("ns1", "existing"))
	ctx := context.Background()

	sub, initial, err := m.Watch(ctx, cmAR, "ns1", 16)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if len(initial) != 1 || initial[0].GetName() != "existing" {
		t.Fatalf("initial snapshot = %v, want the pre-existing object", initial)
	}

	if _, err := dyn.Resource(cmAR.GVR()).Namespace("ns1").Create(ctx, cmObj("ns1", "fresh"), metav1.CreateOptions{}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-sub.Events:
			if !ok {
				t.Fatal("event channel closed before the ADDED event arrived")
			}
			if ev.Type == EventAdded && ev.Object != nil && ev.Object.GetName() == "fresh" {
				goto done
			}
		case <-deadline:
			t.Fatal("timed out waiting for the ADDED event")
		}
	}
done:
	sub.Close()
	sub.Close() // closing twice must be safe

	m.mu.Lock()
	e := m.entries[cmAR.GVR()]
	m.mu.Unlock()
	if n := e.bc.Count(); n != 0 {
		t.Errorf("subscriber count after Close = %d, want 0", n)
	}

	// A Subscription with no cancel (zero value) must be inert, not a panic.
	(&Subscription{}).Close()
}

func TestInformerManagerStats(t *testing.T) {
	m, _ := newTestInformerManager(t, testCacheConfig(), cmObj("ns1", "a"), cmObj("ns2", "b"))
	ctx := context.Background()

	if got := m.Stats(); len(got) != 0 {
		t.Fatalf("a fresh manager reports %d informers, want 0", len(got))
	}
	if _, err := m.List(ctx, cmAR, ""); err != nil {
		t.Fatalf("List: %v", err)
	}

	stats := m.Stats()
	if len(stats) != 1 {
		t.Fatalf("stats = %+v, want exactly one informer", stats)
	}
	s := stats[0]
	if s.Resource != "configmaps" || s.Version != "v1" || s.Objects != 2 || s.Subscribers != 0 {
		t.Errorf("stat = %+v", s)
	}
}

func TestInformerManagerStopIsIdempotent(t *testing.T) {
	m, _ := newTestInformerManager(t, testCacheConfig(), cmObj("ns1", "a"))
	if _, err := m.List(context.Background(), cmAR, ""); err != nil {
		t.Fatalf("List: %v", err)
	}
	m.Stop()
	m.Stop() // second stop must not double-close channels
	if got := m.Stats(); len(got) != 0 {
		t.Errorf("informers survived Stop: %+v", got)
	}
}

func TestInformerManagerEvictIdle(t *testing.T) {
	m, _ := newTestInformerManager(t, testCacheConfig(), cmObj("ns1", "a"))
	ctx := context.Background()
	if _, err := m.List(ctx, cmAR, ""); err != nil {
		t.Fatalf("List: %v", err)
	}

	// Age the entry in place instead of waiting out the idle timeout, then run
	// one eviction pass directly rather than waiting on the loop's ticker.
	m.mu.Lock()
	e := m.entries[cmAR.GVR()]
	m.mu.Unlock()
	e.lastUsed.Store(time.Now().Add(-time.Hour).UnixNano())

	m.evictIdle()
	if got := m.Stats(); len(got) != 0 {
		t.Errorf("idle informer survived eviction: %+v", got)
	}
}

func TestInformerManagerEvictIdleSparesWatched(t *testing.T) {
	m, _ := newTestInformerManager(t, testCacheConfig(), cmObj("ns1", "a"))
	ctx := context.Background()

	sub, _, err := m.Watch(ctx, cmAR, "", 4)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	defer sub.Close()

	m.mu.Lock()
	e := m.entries[cmAR.GVR()]
	m.mu.Unlock()
	e.lastUsed.Store(time.Now().Add(-time.Hour).UnixNano())

	// A live subscriber keeps the informer alive no matter how stale reads are.
	m.evictIdle()
	if got := m.Stats(); len(got) != 1 {
		t.Errorf("watched informer was evicted: %+v", got)
	}
}

func TestInformerManagerEntryFailure(t *testing.T) {
	m, dyn := newTestInformerManager(t, testCacheConfig())
	dyn.PrependReactor("list", "configmaps", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("rbac denied")
	})

	_, err := m.List(context.Background(), cmAR, "")
	if err == nil || !strings.Contains(err.Error(), "could not start") || !strings.Contains(err.Error(), "rbac denied") {
		t.Fatalf("err = %v, want a start failure carrying the watch error", err)
	}
	// The broken informer must not linger: the next request should rebuild it
	// so a transient RBAC blip heals on its own.
	if got := m.Stats(); len(got) != 0 {
		t.Errorf("a failed informer was kept: %+v", got)
	}
}

func TestInformerManagerEntryContextCanceled(t *testing.T) {
	m, dyn := newTestInformerManager(t, testCacheConfig())
	// A slow list keeps sync (and failure) from racing the canceled context.
	dyn.PrependReactor("list", "configmaps", func(k8stesting.Action) (bool, runtime.Object, error) {
		time.Sleep(200 * time.Millisecond)
		return true, nil, errors.New("slow")
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.List(ctx, cmAR, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestInformerManagerEntrySyncTimeout(t *testing.T) {
	cfg := testCacheConfig()
	cfg.SyncTimeout = 20 * time.Millisecond
	m, dyn := newTestInformerManager(t, cfg)
	dyn.PrependReactor("list", "configmaps", func(k8stesting.Action) (bool, runtime.Object, error) {
		time.Sleep(500 * time.Millisecond)
		return true, nil, errors.New("slow")
	})

	_, err := m.List(context.Background(), cmAR, "")
	if err == nil || !strings.Contains(err.Error(), "timed out building cache") {
		t.Fatalf("err = %v, want a sync timeout", err)
	}
}

func TestInformerManagerEvictIdleDisabled(t *testing.T) {
	cfg := testCacheConfig()
	cfg.IdleTimeout = 0
	// Also exercise the SyncTimeout default: zero must mean "sane default",
	// not "fail instantly".
	cfg.SyncTimeout = 0
	m, _ := newTestInformerManager(t, cfg, cmObj("ns1", "a"))
	if _, err := m.List(context.Background(), cmAR, ""); err != nil {
		t.Fatalf("List: %v", err)
	}

	m.mu.Lock()
	e := m.entries[cmAR.GVR()]
	m.mu.Unlock()
	e.lastUsed.Store(time.Now().Add(-time.Hour).UnixNano())

	m.evictIdle()
	if got := m.Stats(); len(got) != 1 {
		t.Errorf("eviction ran despite being disabled: %+v", got)
	}
}

func TestInformerManagerEnforcesCap(t *testing.T) {
	cfg := testCacheConfig()
	cfg.MaxInformersPerCluster = 1
	m, _ := newTestInformerManager(t, cfg, cmObj("ns1", "a"))
	ctx := context.Background()

	if _, err := m.List(ctx, cmAR, ""); err != nil {
		t.Fatalf("List configmaps: %v", err)
	}
	if _, err := m.List(ctx, secretAR, ""); err != nil {
		t.Fatalf("List secrets: %v", err)
	}

	stats := m.Stats()
	if len(stats) != 1 || stats[0].Resource != "secrets" {
		t.Errorf("stats = %+v, want only the newest informer at cap 1", stats)
	}
}

func TestInformerManagerCapSparesWatched(t *testing.T) {
	cfg := testCacheConfig()
	cfg.MaxInformersPerCluster = 1
	m, _ := newTestInformerManager(t, cfg, cmObj("ns1", "a"))
	ctx := context.Background()

	sub, _, err := m.Watch(ctx, cmAR, "", 4)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	defer sub.Close()

	if _, err := m.List(ctx, secretAR, ""); err != nil {
		t.Fatalf("List secrets: %v", err)
	}

	// A watched informer is never a cap victim, so the cap is allowed to be
	// exceeded rather than cutting off a live stream.
	if stats := m.Stats(); len(stats) != 2 {
		t.Errorf("stats = %+v, want both informers to survive", stats)
	}
}
