// Package config defines Clusterlens' runtime configuration and its loading
// rules: a YAML file, overlaid with environment variables, then validated.
package config

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the fully-resolved server configuration.
type Config struct {
	Server   ServerConfig    `yaml:"server"`
	OIDC     OIDCConfig      `yaml:"oidc"`
	Session  SessionConfig   `yaml:"session"`
	Clusters []ClusterConfig `yaml:"clusters"`
	Cache    CacheConfig     `yaml:"cache"`
	Authz    AuthzConfig     `yaml:"authz"`
	Log      LogConfig       `yaml:"log"`
}

type ServerConfig struct {
	Addr string `yaml:"addr"`
	// PublicURL is the externally reachable base URL. It anchors the OIDC
	// redirect URI and post-logout redirect when those are not set explicitly.
	PublicURL string `yaml:"publicURL"`
	// WebRoot serves the compiled SPA. Empty disables static serving, which is
	// what you want when the frontend is behind its own CDN.
	WebRoot         string        `yaml:"webRoot"`
	CORSOrigins     []string      `yaml:"corsOrigins"`
	ReadTimeout     time.Duration `yaml:"readTimeout"`
	WriteTimeout    time.Duration `yaml:"writeTimeout"`
	ShutdownTimeout time.Duration `yaml:"shutdownTimeout"`
	// MetricsAddr exposes Prometheus metrics on a separate listener so the
	// scrape endpoint never sits behind the auth middleware.
	MetricsAddr string `yaml:"metricsAddr"`
}

type OIDCConfig struct {
	Enabled      bool     `yaml:"enabled"`
	Issuer       string   `yaml:"issuer"`
	ClientID     string   `yaml:"clientID"`
	ClientSecret string   `yaml:"clientSecret"`
	RedirectURL  string   `yaml:"redirectURL"`
	Scopes       []string `yaml:"scopes"`

	// Claim mapping. These mirror the kube-apiserver's own OIDC flags so a
	// cluster and the dashboard can be pointed at one provider with one story
	// about who the user is.
	UsernameClaim  string `yaml:"usernameClaim"`
	GroupsClaim    string `yaml:"groupsClaim"`
	UsernamePrefix string `yaml:"usernamePrefix"`
	GroupsPrefix   string `yaml:"groupsPrefix"`

	// RequiredClaims gates login on exact claim values (e.g. hd: example.com).
	RequiredClaims map[string]string `yaml:"requiredClaims"`
	// AllowedGroups, when non-empty, restricts login to members of a group.
	AllowedGroups []string `yaml:"allowedGroups"`

	// InsecureSkipIssuerVerify tolerates providers whose discovery document
	// advertises a different issuer than the URL it was fetched from.
	InsecureSkipIssuerVerify bool `yaml:"insecureSkipIssuerVerify"`
	// OfflineAccess requests a refresh token so long sessions survive short
	// ID token lifetimes.
	OfflineAccess bool `yaml:"offlineAccess"`
	// EndSessionURL triggers RP-initiated logout at the provider.
	EndSessionURL string `yaml:"endSessionURL"`
}

type SessionConfig struct {
	// Store is "memory" (single replica / dev) or "redis" (HA).
	Store      string        `yaml:"store"`
	RedisURL   string        `yaml:"redisURL"`
	CookieName string        `yaml:"cookieName"`
	TTL        time.Duration `yaml:"ttl"`
	// IdleTimeout expires sessions that stop being used before TTL elapses.
	IdleTimeout time.Duration `yaml:"idleTimeout"`
	Secure      bool          `yaml:"secure"`
	SameSite    string        `yaml:"sameSite"`
	Domain      string        `yaml:"domain"`
	// EncryptionKey is 32 raw bytes, base64-encoded. Generated at boot when
	// empty, which is fine for one replica and wrong for several.
	EncryptionKey string `yaml:"encryptionKey"`

	// ephemeralKey records that EncryptionKey was minted at boot rather than
	// configured, so the server can warn about it.
	ephemeralKey bool `yaml:"-"`
}

// AuthMode decides which credential actually talks to a cluster's API server.
type AuthMode string

const (
	// AuthModeImpersonation calls the API server with the dashboard's own
	// credential plus Impersonate-User/-Group headers. RBAC is evaluated
	// against the real end user and the audit log names them, while a single
	// shared informer cache still serves every reader. This is the default
	// because it is the only mode that is both correct and cheap at scale.
	AuthModeImpersonation AuthMode = "impersonation"
	// AuthModePassthrough forwards the user's own OIDC ID token as the bearer
	// token. Requires the cluster's API server to trust the same issuer.
	AuthModePassthrough AuthMode = "passthrough"
	// AuthModeServiceAccount uses the configured credential directly with no
	// per-user identity. Read-only demo clusters only.
	AuthModeServiceAccount AuthMode = "serviceaccount"
)

type ClusterConfig struct {
	Name        string `yaml:"name"`
	DisplayName string `yaml:"displayName"`
	// Kubeconfig + Context select a credential from a kubeconfig file.
	Kubeconfig string `yaml:"kubeconfig"`
	Context    string `yaml:"context"`
	// InCluster uses the pod's own service account.
	InCluster bool `yaml:"inCluster"`

	AuthMode AuthMode `yaml:"authMode"`

	// Labels are free-form tags (region, env, tier) used for grouping in the UI.
	Labels map[string]string `yaml:"labels"`

	// QPS/Burst bound the load this dashboard can put on one API server.
	QPS   float32 `yaml:"qps"`
	Burst int     `yaml:"burst"`

	// Enabled allows keeping a cluster defined but dormant.
	Enabled *bool `yaml:"enabled"`
}

type CacheConfig struct {
	// IdleTimeout stops an informer whose resource nobody has read recently.
	// This is what keeps memory bounded when a cluster has 300 CRDs and users
	// look at four of them.
	IdleTimeout time.Duration `yaml:"idleTimeout"`
	// ResyncPeriod is the informer's full relist interval. Zero disables it;
	// watch is authoritative and resync mostly costs memory churn.
	ResyncPeriod time.Duration `yaml:"resyncPeriod"`
	// DiscoveryTTL bounds how stale the API resource list may be.
	DiscoveryTTL time.Duration `yaml:"discoveryTTL"`
	// MaxInformersPerCluster caps concurrent informers; the least recently
	// used one is stopped past the cap.
	MaxInformersPerCluster int `yaml:"maxInformersPerCluster"`
	// SyncTimeout bounds how long a request waits for a cold cache to fill.
	SyncTimeout time.Duration `yaml:"syncTimeout"`
}

type AuthzConfig struct {
	// TTL caches SubjectAccessReview verdicts. Short enough that revoking a
	// RoleBinding takes effect promptly, long enough that a table render is
	// not N round trips to the API server.
	TTL       time.Duration `yaml:"ttl"`
	CacheSize int           `yaml:"cacheSize"`
	// NamespaceScanLimit bounds the fallback that probes per-namespace access
	// when a user lacks cluster-wide list permission.
	NamespaceScanLimit int `yaml:"namespaceScanLimit"`
}

type LogConfig struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

// Default returns a configuration that runs sensibly with nothing set.
func Default() *Config {
	return &Config{
		Server: ServerConfig{
			Addr:            ":8080",
			PublicURL:       "http://localhost:8080",
			ReadTimeout:     30 * time.Second,
			WriteTimeout:    0, // streaming endpoints must not be cut off
			ShutdownTimeout: 20 * time.Second,
			MetricsAddr:     "",
		},
		OIDC: OIDCConfig{
			Scopes:         []string{"openid", "profile", "email", "groups"},
			UsernameClaim:  "email",
			GroupsClaim:    "groups",
			UsernamePrefix: "oidc:",
			GroupsPrefix:   "oidc:",
			OfflineAccess:  true,
		},
		Session: SessionConfig{
			Store:       "memory",
			CookieName:  "clusterlens_session",
			TTL:         12 * time.Hour,
			IdleTimeout: 2 * time.Hour,
			Secure:      true,
			SameSite:    "lax",
		},
		Cache: CacheConfig{
			IdleTimeout:            10 * time.Minute,
			ResyncPeriod:           0,
			DiscoveryTTL:           5 * time.Minute,
			MaxInformersPerCluster: 64,
			SyncTimeout:            15 * time.Second,
		},
		Authz: AuthzConfig{
			TTL:                30 * time.Second,
			CacheSize:          65536,
			NamespaceScanLimit: 200,
		},
		Log: LogConfig{Level: "info", Format: "text"},
	}
}

// Load reads a YAML config from path (optional), applies environment overrides,
// fills defaults and validates the result.
func Load(path string) (*Config, error) {
	cfg := Default()

	if path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read config %s: %w", path, err)
		}
		raw = []byte(os.ExpandEnv(string(raw)))
		if err := yaml.Unmarshal(raw, cfg); err != nil {
			return nil, fmt.Errorf("parse config %s: %w", path, err)
		}
	}

	cfg.applyEnv()
	if err := cfg.finalize(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// applyEnv lets a container image be configured without a config file at all.
func (c *Config) applyEnv() {
	str := func(env string, dst *string) {
		if v := os.Getenv(env); v != "" {
			*dst = v
		}
	}
	str("CLUSTERLENS_ADDR", &c.Server.Addr)
	str("CLUSTERLENS_PUBLIC_URL", &c.Server.PublicURL)
	str("CLUSTERLENS_WEB_ROOT", &c.Server.WebRoot)
	str("CLUSTERLENS_METRICS_ADDR", &c.Server.MetricsAddr)
	str("CLUSTERLENS_OIDC_ISSUER", &c.OIDC.Issuer)
	str("CLUSTERLENS_OIDC_CLIENT_ID", &c.OIDC.ClientID)
	str("CLUSTERLENS_OIDC_CLIENT_SECRET", &c.OIDC.ClientSecret)
	str("CLUSTERLENS_OIDC_REDIRECT_URL", &c.OIDC.RedirectURL)
	str("CLUSTERLENS_SESSION_KEY", &c.Session.EncryptionKey)
	str("CLUSTERLENS_REDIS_URL", &c.Session.RedisURL)
	str("CLUSTERLENS_LOG_LEVEL", &c.Log.Level)
	str("CLUSTERLENS_LOG_FORMAT", &c.Log.Format)

	if v := os.Getenv("CLUSTERLENS_OIDC_ENABLED"); v != "" {
		c.OIDC.Enabled = v == "true" || v == "1"
	}
	if v := os.Getenv("CLUSTERLENS_SESSION_INSECURE"); v == "true" || v == "1" {
		c.Session.Secure = false
	}
	// A single cluster from a kubeconfig path is the common dev case.
	if v := os.Getenv("CLUSTERLENS_KUBECONFIG"); v != "" && len(c.Clusters) == 0 {
		c.Clusters = append(c.Clusters, ClusterConfig{
			Name:       firstNonEmpty(os.Getenv("CLUSTERLENS_CLUSTER_NAME"), "default"),
			Kubeconfig: v,
			Context:    os.Getenv("CLUSTERLENS_CONTEXT"),
		})
	}
}

func (c *Config) finalize() error {
	if c.Server.PublicURL != "" {
		c.Server.PublicURL = strings.TrimRight(c.Server.PublicURL, "/")
	}
	if c.OIDC.Enabled && c.OIDC.RedirectURL == "" {
		c.OIDC.RedirectURL = c.Server.PublicURL + "/api/v1/auth/callback"
	}
	if c.OIDC.OfflineAccess && !contains(c.OIDC.Scopes, "offline_access") {
		c.OIDC.Scopes = append(c.OIDC.Scopes, "offline_access")
	}
	if c.Session.EncryptionKey == "" {
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return fmt.Errorf("generate session key: %w", err)
		}
		c.Session.EncryptionKey = base64.StdEncoding.EncodeToString(key)
		c.Session.ephemeralKey = true
	}

	seen := make(map[string]bool, len(c.Clusters))
	for i := range c.Clusters {
		cl := &c.Clusters[i]
		if cl.Name == "" {
			return fmt.Errorf("clusters[%d]: name is required", i)
		}
		if seen[cl.Name] {
			return fmt.Errorf("duplicate cluster name %q", cl.Name)
		}
		seen[cl.Name] = true
		if cl.DisplayName == "" {
			cl.DisplayName = cl.Name
		}
		if cl.AuthMode == "" {
			cl.AuthMode = AuthModeImpersonation
		}
		switch cl.AuthMode {
		case AuthModeImpersonation, AuthModePassthrough, AuthModeServiceAccount:
		default:
			return fmt.Errorf("cluster %q: unknown authMode %q", cl.Name, cl.AuthMode)
		}
		if cl.AuthMode == AuthModePassthrough && !c.OIDC.Enabled {
			return fmt.Errorf("cluster %q: authMode passthrough requires oidc.enabled", cl.Name)
		}
		if !cl.InCluster && cl.Kubeconfig == "" {
			return fmt.Errorf("cluster %q: set kubeconfig or inCluster", cl.Name)
		}
		if cl.QPS == 0 {
			cl.QPS = 50
		}
		if cl.Burst == 0 {
			cl.Burst = 100
		}
	}

	if c.OIDC.Enabled {
		if c.OIDC.Issuer == "" || c.OIDC.ClientID == "" {
			return fmt.Errorf("oidc: issuer and clientID are required when enabled")
		}
	}
	if c.Session.Store == "redis" && c.Session.RedisURL == "" {
		return fmt.Errorf("session: redisURL is required for store=redis")
	}
	if _, err := c.SessionKey(); err != nil {
		return err
	}
	return nil
}

// SessionKey decodes the configured key and enforces its length.
func (c *Config) SessionKey() ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(c.Session.EncryptionKey)
	if err != nil {
		return nil, fmt.Errorf("session.encryptionKey: not valid base64: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("session.encryptionKey: need 32 bytes, got %d", len(key))
	}
	return key, nil
}

// EphemeralSessionKey reports whether the key was generated at boot, which
// invalidates every session on restart and breaks multi-replica deployments.
func (c *Config) EphemeralSessionKey() bool { return c.Session.ephemeralKey }

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}
