package cluster

import (
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"k8s.io/client-go/tools/clientcmd"

	"github.com/daiwa-zou/orrery/internal/config"
)

// Entry is a registered cluster, which may not currently be usable.
type Entry struct {
	Name        string
	DisplayName string
	Labels      map[string]string
	AuthMode    config.AuthMode

	Cluster *Cluster
	// Err records why a cluster failed to initialise. A bad credential for one
	// cluster must never stop the dashboard from serving the other forty-nine.
	Err error
}

// Registry holds every configured cluster and keeps trying to revive the ones
// that failed to start.
type Registry struct {
	appCfg *config.Config
	log    *slog.Logger

	mu      sync.RWMutex
	entries map[string]*Entry
	order   []string
	// closed is set by Close under the write lock. retryLoop builds clusters
	// outside that lock, so without this a revival that finished during
	// shutdown was stored into a registry nobody would ever close again.
	closed bool

	stop     chan struct{}
	stopOnce sync.Once
}

// NewRegistry builds every configured cluster. It returns an error only when
// the configuration itself is unusable, not when a cluster is unreachable.
func NewRegistry(appCfg *config.Config, log *slog.Logger) (*Registry, error) {
	specs, err := expandClusters(appCfg.Clusters)
	if err != nil {
		return nil, err
	}
	if len(specs) == 0 {
		return nil, fmt.Errorf("no clusters configured")
	}

	r := &Registry{
		appCfg:  appCfg,
		log:     log,
		entries: make(map[string]*Entry, len(specs)),
		stop:    make(chan struct{}),
	}

	// Build in parallel: a cluster whose API server is slow to answer should
	// not serialise startup behind the others.
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, spec := range specs {
		if spec.Enabled != nil && !*spec.Enabled {
			continue
		}
		wg.Add(1)
		go func(spec config.ClusterConfig) {
			defer wg.Done()
			e := &Entry{
				Name:        spec.Name,
				DisplayName: spec.DisplayName,
				Labels:      spec.Labels,
				AuthMode:    spec.AuthMode,
			}
			c, err := New(spec, appCfg, log)
			if err != nil {
				e.Err = err
				log.Error("cluster unavailable at startup", "cluster", spec.Name, "err", err)
			} else {
				e.Cluster = c
			}
			mu.Lock()
			r.entries[spec.Name] = e
			mu.Unlock()
		}(spec)
	}
	wg.Wait()

	for _, spec := range specs {
		if _, ok := r.entries[spec.Name]; ok {
			r.order = append(r.order, spec.Name)
		}
	}

	go r.retryLoop(specs)
	return r, nil
}

// expandClusters resolves the context wildcard, which turns a developer's
// kubeconfig into a multi-cluster dashboard with no further configuration.
func expandClusters(in []config.ClusterConfig) ([]config.ClusterConfig, error) {
	var out []config.ClusterConfig
	for _, c := range in {
		if c.Context != "*" {
			out = append(out, c)
			continue
		}
		raw, err := clientcmd.LoadFromFile(c.Kubeconfig)
		if err != nil {
			return nil, fmt.Errorf("cluster %q: expand contexts: %w", c.Name, err)
		}
		names := make([]string, 0, len(raw.Contexts))
		for name := range raw.Contexts {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			cp := c
			cp.Context = name
			cp.Name = name
			cp.DisplayName = name
			out = append(out, cp)
		}
	}
	seen := map[string]bool{}
	for _, c := range out {
		if seen[c.Name] {
			return nil, fmt.Errorf("duplicate cluster name %q after context expansion", c.Name)
		}
		seen[c.Name] = true
	}
	return out, nil
}

// retryLoop revives clusters that were unavailable at startup.
func (r *Registry) retryLoop(specs []config.ClusterConfig) {
	byName := make(map[string]config.ClusterConfig, len(specs))
	for _, s := range specs {
		byName[s.Name] = s
	}
	t := time.NewTicker(60 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-r.stop:
			return
		case <-t.C:
			r.mu.RLock()
			var broken []string
			for name, e := range r.entries {
				if e.Cluster == nil {
					broken = append(broken, name)
				}
			}
			r.mu.RUnlock()

			for _, name := range broken {
				spec, ok := byName[name]
				if !ok {
					continue
				}
				c, err := New(spec, r.appCfg, r.log)
				if err != nil {
					r.recordFailure(name, err)
					continue
				}
				if !r.adopt(name, c) {
					c.Close()
					continue
				}
				r.log.Info("cluster recovered", "cluster", name)
			}
		}
	}
}

// recordFailure keeps the reason a cluster is unavailable current.
//
// Get reports whatever is in Err, and that used to be the failure from
// startup, however many retries ago. A cluster whose credentials were repaired
// but whose API server is now down went on being described by the credential
// error — an explanation nobody could act on any more, and one that hides the
// fault they could.
func (r *Registry) recordFailure(name string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.entries[name]; ok && e.Cluster == nil {
		e.Err = err
	}
}

// adopt installs a revived cluster, reporting whether the registry wanted it.
//
// New runs outside the lock — it dials an API server, which is not something
// to hold a registry-wide lock across — so by the time it returns the registry
// may have been closed, or the entry revived by someone else. A false return
// means the caller is holding a live cluster that nothing will ever shut down,
// and must close it: informers, watches and their goroutines otherwise outlive
// the registry that was supposed to own them.
func (r *Registry) adopt(name string, c *Cluster) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.entries[name]
	if !ok || r.closed || e.Cluster != nil {
		return false
	}
	e.Cluster, e.Err = c, nil
	return true
}

// Get returns a usable cluster by name.
func (r *Registry) Get(name string) (*Cluster, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.entries[name]
	if !ok {
		return nil, &UnknownClusterError{Name: name}
	}
	if e.Cluster == nil {
		return nil, fmt.Errorf("cluster %q is not available: %v", name, e.Err)
	}
	return e.Cluster, nil
}

// Entries returns a snapshot of every registered cluster in configuration
// order. Values, not pointers: retryLoop writes Cluster and Err under the
// write lock, so handing out the live structs would let callers read those
// fields unsynchronised.
func (r *Registry) Entries() []Entry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Entry, 0, len(r.order))
	for _, name := range r.order {
		if e, ok := r.entries[name]; ok {
			out = append(out, *e)
		}
	}
	return out
}

// Close shuts every cluster down.
func (r *Registry) Close() {
	r.stopOnce.Do(func() { close(r.stop) })
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	for _, e := range r.entries {
		if e.Cluster != nil {
			e.Cluster.Close()
		}
	}
}

// UnknownClusterError signals a cluster name that is not registered.
type UnknownClusterError struct{ Name string }

func (e *UnknownClusterError) Error() string {
	return fmt.Sprintf("unknown cluster %q", e.Name)
}
