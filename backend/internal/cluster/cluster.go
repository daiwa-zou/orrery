package cluster

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	metricsv "k8s.io/metrics/pkg/client/clientset/versioned"

	"github.com/daiwazou/clusterlens/backend/internal/authz"
	"github.com/daiwazou/clusterlens/backend/internal/config"
)

// Identity is the end user as the cluster layer needs to see them, decoupled
// from HTTP sessions so this package stays independent of the auth flow.
type Identity struct {
	Username string
	Groups   []string
	// BearerToken is the user's own OIDC ID token, used only by clusters in
	// passthrough mode.
	BearerToken string
}

// Clients bundles the typed, dynamic and metrics clients built from one
// credential.
type Clients struct {
	Rest    *rest.Config
	Dynamic dynamic.Interface
	Kube    kubernetes.Interface
	Metrics metricsv.Interface
	// Self is true when these clients carry the user's own credentials, which
	// selects SelfSubjectAccessReview over SubjectAccessReview.
	Self bool
}

// HealthStatus is the coarse state shown in the cluster switcher.
type HealthStatus string

const (
	HealthOK          HealthStatus = "healthy"
	HealthDegraded    HealthStatus = "degraded"
	HealthUnreachable HealthStatus = "unreachable"
	HealthUnknown     HealthStatus = "unknown"
)

// Health is a probe result.
type Health struct {
	Status    HealthStatus `json:"status"`
	Message   string       `json:"message,omitempty"`
	Version   string       `json:"version,omitempty"`
	LatencyMS int64        `json:"latencyMs"`
	CheckedAt time.Time    `json:"checkedAt"`
}

// Cluster is one managed Kubernetes cluster: its credentials, its shared
// caches and its health.
type Cluster struct {
	Cfg config.ClusterConfig
	log *slog.Logger

	base      *Clients
	Discovery *DiscoveryCache
	Informers *InformerManager
	Authz     *authz.Checker

	// userClients memoises per-identity clients. Building a clientset per
	// request would churn TLS transports; keyed reuse keeps one connection
	// pool per distinct caller.
	userMu      sync.Mutex
	userClients *lru.Cache[string, *Clients]

	health atomic.Pointer[Health]

	stop     chan struct{}
	stopOnce sync.Once
}

// New builds a cluster from its configuration and starts health probing.
func New(cfg config.ClusterConfig, appCfg *config.Config, log *slog.Logger) (*Cluster, error) {
	log = log.With("cluster", cfg.Name)

	restCfg, err := restConfigFor(cfg)
	if err != nil {
		return nil, err
	}
	restCfg.QPS = cfg.QPS
	restCfg.Burst = cfg.Burst
	restCfg.UserAgent = "clusterlens/1.0"

	base, err := clientsFor(restCfg, false)
	if err != nil {
		return nil, fmt.Errorf("cluster %s: %w", cfg.Name, err)
	}

	checker, err := authz.NewChecker(appCfg.Authz.CacheSize, appCfg.Authz.TTL, appCfg.Authz.NamespaceScanLimit)
	if err != nil {
		return nil, err
	}
	userCache, err := lru.New[string, *Clients](512)
	if err != nil {
		return nil, err
	}

	c := &Cluster{
		Cfg:         cfg,
		log:         log,
		base:        base,
		Discovery:   NewDiscoveryCache(base.Kube.Discovery(), appCfg.Cache.DiscoveryTTL),
		Informers:   NewInformerManager(base.Dynamic, appCfg.Cache, log),
		Authz:       checker,
		userClients: userCache,
		stop:        make(chan struct{}),
	}
	c.health.Store(&Health{Status: HealthUnknown, CheckedAt: time.Now()})
	go c.probeLoop()
	return c, nil
}

// restConfigFor resolves a cluster's own credential.
func restConfigFor(cfg config.ClusterConfig) (*rest.Config, error) {
	if cfg.InCluster {
		rc, err := rest.InClusterConfig()
		if err != nil {
			return nil, fmt.Errorf("cluster %s: in-cluster config: %w", cfg.Name, err)
		}
		return rc, nil
	}
	rc, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: cfg.Kubeconfig},
		&clientcmd.ConfigOverrides{CurrentContext: cfg.Context},
	).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("cluster %s: kubeconfig %s (context %q): %w", cfg.Name, cfg.Kubeconfig, cfg.Context, err)
	}
	return rc, nil
}

func clientsFor(rc *rest.Config, self bool) (*Clients, error) {
	kube, err := kubernetes.NewForConfig(rc)
	if err != nil {
		return nil, fmt.Errorf("typed client: %w", err)
	}
	dyn, err := dynamic.NewForConfig(rc)
	if err != nil {
		return nil, fmt.Errorf("dynamic client: %w", err)
	}
	// A cluster without metrics-server is normal; the client is built anyway
	// and its calls fail individually rather than blocking startup.
	mc, err := metricsv.NewForConfig(rc)
	if err != nil {
		return nil, fmt.Errorf("metrics client: %w", err)
	}
	return &Clients{Rest: rc, Dynamic: dyn, Kube: kube, Metrics: mc, Self: self}, nil
}

// Base returns the dashboard's own clients, used for informers and for
// SubjectAccessReview.
func (c *Cluster) Base() *Clients { return c.base }

// AuthSubject converts an identity into the subject of an access review.
func (c *Cluster) AuthSubject(id Identity) authz.Subject {
	if c.Cfg.AuthMode == config.AuthModePassthrough {
		return authz.Subject{Self: true}
	}
	if c.Cfg.AuthMode == config.AuthModeServiceAccount {
		// No end-user identity exists; reviews run as the dashboard itself.
		return authz.Subject{Self: true}
	}
	return authz.Subject{User: id.Username, Groups: id.Groups}
}

// AuthzClient returns the client that access reviews must be made with. For
// impersonation that is the dashboard's own client (it holds the right to
// create SubjectAccessReviews); otherwise it is the caller's own client.
func (c *Cluster) AuthzClient(user *Clients) kubernetes.Interface {
	if c.Cfg.AuthMode == config.AuthModeImpersonation {
		return c.base.Kube
	}
	return user.Kube
}

// ClientsFor returns clients that act as the given identity.
func (c *Cluster) ClientsFor(id Identity) (*Clients, error) {
	switch c.Cfg.AuthMode {
	case config.AuthModeServiceAccount:
		return c.base, nil

	case config.AuthModePassthrough:
		if id.BearerToken == "" {
			return nil, fmt.Errorf("cluster %s: passthrough mode requires an id_token", c.Cfg.Name)
		}
		sum := sha256.Sum256([]byte(id.BearerToken))
		key := "pt:" + hex.EncodeToString(sum[:8])
		return c.cachedClients(key, func() (*rest.Config, bool) {
			rc := rest.AnonymousClientConfig(c.base.Rest)
			rc.BearerToken = id.BearerToken
			rc.QPS, rc.Burst, rc.UserAgent = c.Cfg.QPS, c.Cfg.Burst, c.base.Rest.UserAgent
			return rc, true
		})

	default: // impersonation
		if id.Username == "" {
			return nil, fmt.Errorf("cluster %s: impersonation requires a username", c.Cfg.Name)
		}
		key := "imp:" + id.Username + "\x1e" + fmt.Sprint(id.Groups)
		return c.cachedClients(key, func() (*rest.Config, bool) {
			rc := rest.CopyConfig(c.base.Rest)
			rc.Impersonate = rest.ImpersonationConfig{
				UserName: id.Username,
				Groups:   id.Groups,
			}
			return rc, false
		})
	}
}

func (c *Cluster) cachedClients(key string, build func() (*rest.Config, bool)) (*Clients, error) {
	if cl, ok := c.userClients.Get(key); ok {
		return cl, nil
	}
	c.userMu.Lock()
	defer c.userMu.Unlock()
	if cl, ok := c.userClients.Get(key); ok {
		return cl, nil
	}
	rc, self := build()
	cl, err := clientsFor(rc, self)
	if err != nil {
		return nil, err
	}
	c.userClients.Add(key, cl)
	return cl, nil
}

// Health returns the most recent probe result.
func (c *Cluster) Health() Health {
	if h := c.health.Load(); h != nil {
		return *h
	}
	return Health{Status: HealthUnknown}
}

// probeLoop keeps cluster health fresh so the switcher can grey out a cluster
// that has gone away instead of hanging the page that selects it.
func (c *Cluster) probeLoop() {
	c.probe()
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-c.stop:
			return
		case <-t.C:
			c.probe()
		}
	}
}

func (c *Cluster) probe() {
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	h := Health{CheckedAt: start}
	version, err := c.base.Kube.Discovery().ServerVersion()
	h.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		h.Status = HealthUnreachable
		h.Message = err.Error()
		c.health.Store(&h)
		return
	}
	h.Version = version.GitVersion
	h.Status = HealthOK

	// A reachable API server whose readyz is failing is "degraded": the
	// dashboard works but the cluster is not well, and saying so is more
	// useful than a green dot.
	body, err := c.base.Kube.Discovery().RESTClient().Get().AbsPath("/readyz").DoRaw(ctx)
	if err != nil || string(body) != "ok" {
		h.Status = HealthDegraded
		if err != nil {
			h.Message = err.Error()
		} else {
			h.Message = "API server readyz is not ok"
		}
	}
	c.health.Store(&h)
}

// Close releases the cluster's caches.
func (c *Cluster) Close() {
	c.stopOnce.Do(func() {
		close(c.stop)
		c.Informers.Stop()
	})
}
