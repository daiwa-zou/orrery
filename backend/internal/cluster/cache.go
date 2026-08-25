package cluster

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/cache"

	"github.com/daiwa-zou/orrery/backend/internal/config"
)

// EventType mirrors the Kubernetes watch verbs plus two control signals the
// UI needs in order to stay honest about what it is showing.
type EventType string

const (
	EventAdded    EventType = "ADDED"
	EventModified EventType = "MODIFIED"
	EventDeleted  EventType = "DELETED"
	// EventSynced is emitted once the initial cache fill is complete.
	EventSynced EventType = "SYNCED"
	// EventOverflow tells a subscriber it fell behind and must reload; it is
	// the only honest answer when we have dropped changes on the floor.
	EventOverflow EventType = "OVERFLOW"
)

// Event is one change to one object.
type Event struct {
	Type   EventType
	Object *unstructured.Unstructured
}

// Broadcaster fans one informer's events out to many WebSocket subscribers.
// One upstream watch serves every viewer of a resource, which is the whole
// reason a hundred open tabs do not translate into a hundred watches.
type Broadcaster struct {
	mu     sync.RWMutex
	subs   map[int64]chan Event
	nextID int64
	closed bool
}

func newBroadcaster() *Broadcaster {
	return &Broadcaster{subs: make(map[int64]chan Event)}
}

// Subscribe registers a consumer and returns its id and channel.
func (b *Broadcaster) Subscribe(buffer int) (int64, <-chan Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.nextID++
	id := b.nextID
	ch := make(chan Event, buffer)
	if b.closed {
		close(ch)
		return id, ch
	}
	b.subs[id] = ch
	return id, ch
}

// Unsubscribe removes and closes a consumer's channel.
func (b *Broadcaster) Unsubscribe(id int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if ch, ok := b.subs[id]; ok {
		delete(b.subs, id)
		close(ch)
	}
}

// Publish delivers an event to every subscriber without ever blocking the
// informer. A consumer that cannot keep up is sent OVERFLOW and dropped: it is
// better to make one client reload than to stall the shared cache for all.
func (b *Broadcaster) Publish(ev Event) {
	b.mu.RLock()
	slow := make([]int64, 0)
	for id, ch := range b.subs {
		select {
		case ch <- ev:
		default:
			slow = append(slow, id)
		}
	}
	b.mu.RUnlock()

	for _, id := range slow {
		b.mu.Lock()
		if ch, ok := b.subs[id]; ok {
			delete(b.subs, id)
			// Best-effort notice; the buffer is full so this may not land,
			// but closing the channel is itself a signal to reload.
			select {
			case ch <- Event{Type: EventOverflow}:
			default:
			}
			close(ch)
		}
		b.mu.Unlock()
	}
}

// Count reports the number of live subscribers.
func (b *Broadcaster) Count() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subs)
}

func (b *Broadcaster) close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	for id, ch := range b.subs {
		delete(b.subs, id)
		close(ch)
	}
}

// informerEntry is one running reflector plus its fan-out. The entry does not
// carry its own GVR; the manager's map key is the single source of that truth.
type informerEntry struct {
	informer cache.SharedIndexInformer
	bc       *Broadcaster

	stop     chan struct{}
	stopOnce sync.Once

	// lastUsed is a unix-nano timestamp touched by every read; the evictor
	// uses it to decide what to shut down.
	lastUsed atomic.Int64

	synced  chan struct{}
	failed  chan struct{}
	failErr atomic.Pointer[string]

	startedAt time.Time
}

func (e *informerEntry) touch() { e.lastUsed.Store(time.Now().UnixNano()) }

func (e *informerEntry) idleFor() time.Duration {
	return time.Since(time.Unix(0, e.lastUsed.Load()))
}

func (e *informerEntry) shutdown() {
	e.stopOnce.Do(func() {
		close(e.stop)
		e.bc.close()
	})
}

// InformerManager starts, shares and retires per-resource caches for one
// cluster. Informers are created on first use and stopped once nobody is
// looking, so a cluster with three hundred CRDs costs memory only for the
// handful of resources actually being viewed.
type InformerManager struct {
	dyn    dynamic.Interface
	cfg    config.CacheConfig
	log    *slog.Logger
	filter TransformFunc

	mu      sync.Mutex
	entries map[schema.GroupVersionResource]*informerEntry

	stop     chan struct{}
	stopOnce sync.Once
}

// TransformFunc trims objects before they enter the cache.
type TransformFunc func(obj *unstructured.Unstructured) *unstructured.Unstructured

// NewInformerManager builds a manager and starts its eviction loop.
func NewInformerManager(dyn dynamic.Interface, cfg config.CacheConfig, log *slog.Logger) *InformerManager {
	m := &InformerManager{
		dyn:     dyn,
		cfg:     cfg,
		log:     log,
		filter:  TrimForCache,
		entries: make(map[schema.GroupVersionResource]*informerEntry),
		stop:    make(chan struct{}),
	}
	go m.evictLoop()
	return m
}

// Stop shuts down every informer.
func (m *InformerManager) Stop() {
	m.stopOnce.Do(func() {
		close(m.stop)
		m.mu.Lock()
		defer m.mu.Unlock()
		for gvr, e := range m.entries {
			e.shutdown()
			delete(m.entries, gvr)
		}
	})
}

// evictLoop retires informers that have gone idle and enforces the per-cluster
// informer cap.
func (m *InformerManager) evictLoop() {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-t.C:
			m.evictIdle()
		}
	}
}

func (m *InformerManager) evictIdle() {
	if m.cfg.IdleTimeout <= 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for gvr, e := range m.entries {
		// A live watcher keeps its informer alive regardless of read activity.
		if e.bc.Count() > 0 {
			e.touch()
			continue
		}
		if e.idleFor() > m.cfg.IdleTimeout {
			m.log.Debug("stopping idle informer", "gvr", gvr.String(), "idle", e.idleFor().String())
			e.shutdown()
			delete(m.entries, gvr)
		}
	}
}

// enforceCapLocked stops the least recently used unwatched informer when the
// cap is exceeded. Callers must hold m.mu.
func (m *InformerManager) enforceCapLocked() {
	max := m.cfg.MaxInformersPerCluster
	if max <= 0 || len(m.entries) < max {
		return
	}
	type cand struct {
		gvr  schema.GroupVersionResource
		idle time.Duration
	}
	var cands []cand
	for gvr, e := range m.entries {
		if e.bc.Count() > 0 {
			continue
		}
		cands = append(cands, cand{gvr, e.idleFor()})
	}
	if len(cands) == 0 {
		return
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].idle > cands[j].idle })
	victim := cands[0]
	m.log.Debug("evicting informer at cap", "gvr", victim.gvr.String())
	m.entries[victim.gvr].shutdown()
	delete(m.entries, victim.gvr)
}

// entry returns a started informer for a resource, waiting for its first sync.
func (m *InformerManager) entry(ctx context.Context, ar APIResource) (*informerEntry, error) {
	gvr := ar.GVR()

	m.mu.Lock()
	e, ok := m.entries[gvr]
	if !ok {
		m.enforceCapLocked()
		e = m.start(ar)
		m.entries[gvr] = e
	}
	m.mu.Unlock()

	e.touch()

	syncTimeout := m.cfg.SyncTimeout
	if syncTimeout <= 0 {
		syncTimeout = 15 * time.Second
	}
	timer := time.NewTimer(syncTimeout)
	defer timer.Stop()

	select {
	case <-e.synced:
		return e, nil
	case <-e.failed:
		msg := "watch failed"
		if p := e.failErr.Load(); p != nil {
			msg = *p
		}
		// Do not keep a broken informer around; the next request retries and
		// a transient RBAC or CRD-rollout blip then heals on its own.
		m.mu.Lock()
		if cur, ok := m.entries[gvr]; ok && cur == e {
			delete(m.entries, gvr)
		}
		m.mu.Unlock()
		e.shutdown()
		return nil, fmt.Errorf("cache for %s could not start: %s", gvr.Resource, msg)
	case <-timer.C:
		return nil, fmt.Errorf("timed out building cache for %s", gvr.Resource)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// start constructs and launches an informer. Callers must hold m.mu.
func (m *InformerManager) start(ar APIResource) *informerEntry {
	gvr := ar.GVR()
	e := &informerEntry{
		bc:        newBroadcaster(),
		stop:      make(chan struct{}),
		synced:    make(chan struct{}),
		failed:    make(chan struct{}),
		startedAt: time.Now(),
	}
	e.touch()

	lw := &cache.ListWatch{
		ListWithContextFunc: func(ctx context.Context, opts metav1.ListOptions) (runtime.Object, error) {
			return m.dyn.Resource(gvr).Namespace(metav1.NamespaceAll).List(ctx, opts)
		},
		WatchFuncWithContext: func(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
			return m.dyn.Resource(gvr).Namespace(metav1.NamespaceAll).Watch(ctx, opts)
		},
	}

	informer := cache.NewSharedIndexInformer(lw, &unstructured.Unstructured{}, m.cfg.ResyncPeriod,
		cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})

	// Trim before the object is stored, not after: the saving is in what the
	// cache holds, not in what a handler copies.
	if m.filter != nil {
		_ = informer.SetTransform(func(obj any) (any, error) {
			u, ok := obj.(*unstructured.Unstructured)
			if !ok {
				return obj, nil
			}
			return m.filter(u), nil
		})
	}

	_ = informer.SetWatchErrorHandler(func(_ *cache.Reflector, err error) {
		msg := err.Error()
		e.failErr.Store(&msg)
		m.log.Warn("informer watch error", "gvr", gvr.String(), "err", msg)
		select {
		case <-e.synced:
			// Already serving; a transient watch error is the reflector's
			// problem to retry, not a reason to tear the cache down.
		case <-e.failed:
		default:
			close(e.failed)
		}
	})

	_, _ = informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			if u, ok := obj.(*unstructured.Unstructured); ok {
				e.bc.Publish(Event{Type: EventAdded, Object: u})
			}
		},
		UpdateFunc: func(_, obj any) {
			if u, ok := obj.(*unstructured.Unstructured); ok {
				e.bc.Publish(Event{Type: EventModified, Object: u})
			}
		},
		DeleteFunc: func(obj any) {
			if tomb, ok := obj.(cache.DeletedFinalStateUnknown); ok {
				obj = tomb.Obj
			}
			if u, ok := obj.(*unstructured.Unstructured); ok {
				e.bc.Publish(Event{Type: EventDeleted, Object: u})
			}
		},
	})

	e.informer = informer

	go informer.Run(e.stop)
	go func() {
		if cache.WaitForCacheSync(e.stop, informer.HasSynced) {
			select {
			case <-e.synced:
			default:
				close(e.synced)
			}
			e.bc.Publish(Event{Type: EventSynced})
		}
	}()

	return e
}

// List returns cached objects, optionally restricted to one namespace.
func (m *InformerManager) List(ctx context.Context, ar APIResource, namespace string) ([]*unstructured.Unstructured, error) {
	e, err := m.entry(ctx, ar)
	if err != nil {
		return nil, err
	}
	var raw []any
	if namespace != "" && ar.Namespaced {
		raw, err = e.informer.GetIndexer().ByIndex(cache.NamespaceIndex, namespace)
		if err != nil {
			return nil, err
		}
	} else {
		raw = e.informer.GetIndexer().List()
	}
	out := make([]*unstructured.Unstructured, 0, len(raw))
	for _, o := range raw {
		if u, ok := o.(*unstructured.Unstructured); ok {
			out = append(out, u)
		}
	}
	return out, nil
}

// Get returns one cached object, or nil when it is absent.
func (m *InformerManager) Get(ctx context.Context, ar APIResource, namespace, name string) (*unstructured.Unstructured, error) {
	e, err := m.entry(ctx, ar)
	if err != nil {
		return nil, err
	}
	key := name
	if namespace != "" && ar.Namespaced {
		key = namespace + "/" + name
	}
	obj, exists, err := e.informer.GetIndexer().GetByKey(key)
	if err != nil || !exists {
		return nil, err
	}
	u, _ := obj.(*unstructured.Unstructured)
	return u, nil
}

// Subscription is a live feed of one resource's changes.
type Subscription struct {
	Events <-chan Event
	cancel func()
}

// Close detaches the subscriber.
func (s *Subscription) Close() {
	if s.cancel != nil {
		s.cancel()
	}
}

// Watch attaches a subscriber to a resource's shared informer and replays the
// current cache contents so the client starts from a complete picture.
func (m *InformerManager) Watch(ctx context.Context, ar APIResource, namespace string, buffer int) (*Subscription, []*unstructured.Unstructured, error) {
	e, err := m.entry(ctx, ar)
	if err != nil {
		return nil, nil, err
	}
	id, ch := e.bc.Subscribe(buffer)
	initial, err := m.List(ctx, ar, namespace)
	if err != nil {
		e.bc.Unsubscribe(id)
		return nil, nil, err
	}
	return &Subscription{Events: ch, cancel: func() { e.bc.Unsubscribe(id) }}, initial, nil
}

// InformerStat is a snapshot of one running cache, exposed for /metrics and
// the admin view.
type InformerStat struct {
	GVR         string `json:"gvr"`
	Group       string `json:"-"`
	Version     string `json:"-"`
	Resource    string `json:"-"`
	Objects     int    `json:"objects"`
	Subscribers int    `json:"subscribers"`
	IdleSeconds int64  `json:"idleSeconds"`
	AgeSeconds  int64  `json:"ageSeconds"`
}

// Stats reports every running informer.
func (m *InformerManager) Stats() []InformerStat {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]InformerStat, 0, len(m.entries))
	for gvr, e := range m.entries {
		out = append(out, InformerStat{
			GVR:         gvr.String(),
			Group:       gvr.Group,
			Version:     gvr.Version,
			Resource:    gvr.Resource,
			Objects:     len(e.informer.GetIndexer().ListKeys()),
			Subscribers: e.bc.Count(),
			IdleSeconds: int64(e.idleFor().Seconds()),
			AgeSeconds:  int64(time.Since(e.startedAt).Seconds()),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GVR < out[j].GVR })
	return out
}
