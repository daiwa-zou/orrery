// Package config defines Orrery' runtime configuration and its loading
// rules: a YAML file, overlaid with environment variables, then validated.
package config

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"slices"
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
	Proxy    ProxyConfig     `yaml:"proxy"`
	Debug    DebugConfig     `yaml:"debug"`
	Log      LogConfig       `yaml:"log"`
}

// ProxyConfig governs the read-only HTTP proxy into pods and services.
type ProxyConfig struct {
	// Enabled exposes the proxy route and the console's Open button.
	//
	// On by default, because it is already narrow: GET and HEAD only, and
	// every request is gated on the pods/proxy or services/proxy subresource
	// under the caller's own identity.
	//
	// Worth turning off anyway when the workloads a cluster runs are not ones
	// you want rendered inside the console's origin, or when policy says a
	// dashboard may read the API server and nothing behind it. Disabling
	// removes the route entirely — not just the button — so it cannot be
	// reached by typing the URL.
	Enabled *bool `yaml:"enabled"`
}

// ProxyEnabled reports whether the HTTP proxy should be served. Absent means
// enabled, so an existing config keeps working after the flag was introduced.
func (c ProxyConfig) ProxyEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

// DebugConfig governs the ephemeral debug container action.
type DebugConfig struct {
	// Image the debug container runs.
	//
	// Chosen by the operator rather than the caller on purpose. kubectl debug
	// lets whoever runs it name any image, which is reasonable at a terminal
	// but not from a web console: a dashboard that accepts an arbitrary image
	// from the browser is a way to run arbitrary code inside another
	// workload's namespaces. RBAC still gates the action; this bounds what it
	// can start.
	Image string `yaml:"image"`
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
	Enabled bool `yaml:"enabled"`
	// AutoLogin sends unauthenticated visitors straight into the OIDC flow
	// instead of showing the sign-in button first. Sign-out and login errors
	// still land on the login page, or signing out would bounce right back in.
	AutoLogin    bool     `yaml:"autoLogin"`
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
			Scopes:        []string{"openid", "profile", "email", "groups"},
			UsernameClaim: "email",
			GroupsClaim:   "groups",
			// No default username prefix: a configured prefix always applies
			// (kube-apiserver semantics), and the apiserver's own default for
			// an email claim is the bare address. Defaulting to "oidc:" here
			// would silently impersonate identities no RBAC binding names.
			UsernamePrefix: "",
			GroupsPrefix:   "oidc:",
			OfflineAccess:  true,
		},
		Session: SessionConfig{
			Store:       "memory",
			CookieName:  "orrery_session",
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
		Debug: DebugConfig{
			Image: "busybox:1.37",
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
	str("ORRERY_ADDR", &c.Server.Addr)
	str("ORRERY_PUBLIC_URL", &c.Server.PublicURL)
	str("ORRERY_WEB_ROOT", &c.Server.WebRoot)
	str("ORRERY_METRICS_ADDR", &c.Server.MetricsAddr)
	str("ORRERY_OIDC_ISSUER", &c.OIDC.Issuer)
	str("ORRERY_OIDC_CLIENT_ID", &c.OIDC.ClientID)
	str("ORRERY_OIDC_CLIENT_SECRET", &c.OIDC.ClientSecret)
	str("ORRERY_OIDC_REDIRECT_URL", &c.OIDC.RedirectURL)
	str("ORRERY_SESSION_KEY", &c.Session.EncryptionKey)
	str("ORRERY_REDIS_URL", &c.Session.RedisURL)
	str("ORRERY_LOG_LEVEL", &c.Log.Level)
	str("ORRERY_LOG_FORMAT", &c.Log.Format)

	if v := os.Getenv("ORRERY_OIDC_ENABLED"); v != "" {
		c.OIDC.Enabled = v == "true" || v == "1"
	}
	if v := os.Getenv("ORRERY_OIDC_AUTOLOGIN"); v != "" {
		c.OIDC.AutoLogin = v == "true" || v == "1"
	}
	if v := os.Getenv("ORRERY_SESSION_INSECURE"); v == "true" || v == "1" {
		c.Session.Secure = false
	}
	// A single cluster from a kubeconfig path is the common dev case.
	if v := os.Getenv("ORRERY_KUBECONFIG"); v != "" && len(c.Clusters) == 0 {
		c.Clusters = append(c.Clusters, ClusterConfig{
			Name:       firstNonEmpty(os.Getenv("ORRERY_CLUSTER_NAME"), "default"),
			Kubeconfig: v,
			Context:    os.Getenv("ORRERY_CONTEXT"),
		})
	}
}

func (c *Config) finalize() error {
	if c.Server.PublicURL != "" {
		c.Server.PublicURL = strings.TrimRight(c.Server.PublicURL, "/")
	}
	for _, o := range c.Server.CORSOrigins {
		// CORS responses carry Allow-Credentials, and the same list feeds the
		// WebSocket Origin check that stands in for CSRF. A wildcard would let
		// any website act with a visitor's session, so origins are explicit.
		if o == "*" {
			return fmt.Errorf("server.corsOrigins: %q is not allowed; list each origin explicitly", o)
		}
	}
	if c.OIDC.Enabled && c.OIDC.RedirectURL == "" {
		c.OIDC.RedirectURL = c.Server.PublicURL + "/api/v1/auth/callback"
	}
	if c.OIDC.OfflineAccess && !slices.Contains(c.OIDC.Scopes, "offline_access") {
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
	if err := c.finalizeSession(); err != nil {
		return err
	}
	switch c.Log.Level {
	case "", "debug", "info", "warn", "error":
	default:
		return fmt.Errorf(
			"log.level: unknown level %q (use debug, info, warn or error)", c.Log.Level)
	}
	switch c.Log.Format {
	case "", "text", "json":
	default:
		return fmt.Errorf("log.format: unknown format %q (use text or json)", c.Log.Format)
	}
	if _, err := c.SessionKey(); err != nil {
		return err
	}
	return nil
}

// finalizeSession checks the settings that used to be matched case-sensitively
// against a literal and otherwise fell through to a default.
//
// Every one of these is spelled by hand in a YAML file, and every one of them
// had the same shape of failure: a value that looks accepted, is not the value
// that takes effect, and is never mentioned again. That is the failure this
// codebase is most concerned with, and finalize already refuses an unknown
// authMode for exactly this reason — the check simply never reached the four
// settings beside it.
//
// What each one silently did:
//
//   - sameSite: Strict is a downgrade to Lax, which is to say the protection
//     the operator asked for is not the one they got.
//   - store: Redis runs in memory, and the redisURL requirement below did not
//     fire either, so an HA deployment passed validation and then logged people
//     out at random as they moved between replicas.
//   - log.level: DEBUG stays at info, which is discovered while trying to debug
//     something.
//
// Empty is still accepted and still means the default; it is a value that was
// never set, not one that was set and misread.
func (c *Config) finalizeSession() error {
	switch c.Session.SameSite {
	case "", "lax", "strict", "none":
	default:
		return fmt.Errorf(
			"session.sameSite: unknown value %q (use lax, strict or none, lower-case)",
			c.Session.SameSite)
	}
	// A cookie that says SameSite=None and not Secure is rejected outright by
	// every current browser, so the session is never stored and the sign-in
	// bounces forever against a server with nothing wrong with it. Nothing in
	// the response says so; the refusal happens in the browser.
	if c.Session.SameSite == "none" && !c.Session.Secure {
		return fmt.Errorf(
			"session.sameSite=none requires session.secure=true; " +
				"browsers reject a SameSite=None cookie that is not Secure, " +
				"which appears as a sign-in loop rather than as an error")
	}
	switch c.Session.Store {
	case "", "memory", "redis":
	default:
		return fmt.Errorf(
			"session.store: unknown store %q (use memory or redis, lower-case)",
			c.Session.Store)
	}
	if c.Session.Store == "redis" && c.Session.RedisURL == "" {
		return fmt.Errorf("session: redisURL is required for store=redis")
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

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}
