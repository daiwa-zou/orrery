package server

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/daiwa-zou/orrery/internal/api"
	"github.com/daiwa-zou/orrery/internal/config"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// testConfig is a minimal valid configuration: one cluster whose kubeconfig
// does not exist. The registry tolerates an unavailable cluster (it records
// the error and keeps retrying), so New still assembles the whole server with
// no network and no real API server.
func testConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := config.Default()
	cfg.Server.Addr = "127.0.0.1:0"
	cfg.Server.ShutdownTimeout = 2 * time.Second
	cfg.Session.EncryptionKey = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="
	cfg.Session.Secure = false
	cfg.Clusters = []config.ClusterConfig{{
		Name:       "dead",
		Kubeconfig: filepath.Join(t.TempDir(), "missing-kubeconfig"),
		AuthMode:   config.AuthModeImpersonation,
	}}
	return cfg
}

// newServer wraps New and, on success, unregisters the cache collector New
// installs in the process-global Prometheus registry. Without this a second
// successful New in the same test binary would panic in MustRegister.
func newServer(t *testing.T, ctx context.Context, cfg *config.Config) (*Server, error) {
	t.Helper()
	s, err := New(ctx, cfg, discardLogger())
	if err == nil {
		t.Cleanup(func() { prometheus.Unregister(api.NewCacheCollector(nil)) })
	}
	return s, err
}

func TestNewFailsWithoutClusters(t *testing.T) {
	cfg := testConfig(t)
	cfg.Clusters = nil
	_, err := newServer(t, context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "no clusters configured") {
		t.Errorf("err = %v", err)
	}
}

func TestNewFailsOnBadContextExpansion(t *testing.T) {
	// A context wildcard needs a readable kubeconfig, so this is the one
	// cluster problem that is a configuration error rather than a runtime one.
	cfg := testConfig(t)
	cfg.Clusters[0].Context = "*"
	if _, err := newServer(t, context.Background(), cfg); err == nil {
		t.Error("expanding contexts from a missing kubeconfig must fail")
	}
}

func TestNewFailsOnBadRedisURL(t *testing.T) {
	cfg := testConfig(t)
	cfg.Session.Store = "redis"
	cfg.Session.RedisURL = "not-a-redis-url"
	_, err := newServer(t, context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "redisURL") {
		t.Errorf("err = %v", err)
	}
}

func TestNewFailsOnBadSessionKey(t *testing.T) {
	cfg := testConfig(t)
	cfg.Session.EncryptionKey = "!!not base64!!"
	if _, err := newServer(t, context.Background(), cfg); err == nil {
		t.Error("an undecodable session key must fail startup")
	}
}

func TestNewFailsWhenOIDCDiscoveryFails(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()

	// Build through config.Load so the session key is generated (ephemeral):
	// with OIDC enabled that combination must at least warn, since sessions
	// will not survive a restart or work across replicas.
	for _, env := range []string{"ORRERY_SESSION_KEY", "ORRERY_KUBECONFIG", "ORRERY_OIDC_ENABLED", "ORRERY_REDIS_URL"} {
		t.Setenv(env, "")
	}
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Clusters = testConfig(t).Clusters
	cfg.OIDC.Enabled = true
	cfg.OIDC.Issuer = srv.URL
	cfg.OIDC.ClientID = "orrery"
	cfg.OIDC.RedirectURL = srv.URL + "/callback"
	_, err = newServer(t, context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "oidc discovery") {
		t.Errorf("err = %v", err)
	}
}

func TestNewAndRunServeUntilCancelled(t *testing.T) {
	cfg := testConfig(t)
	cfg.Server.MetricsAddr = "127.0.0.1:0"

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, err := newServer(t, ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if s.metrics == nil {
		t.Fatal("a metrics address must produce a metrics listener")
	}

	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	// A clean nil return after cancellation is the contract; ordering against
	// the listener goroutines does not matter, so no synchronisation is needed.
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run after cancellation = %v, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
}

func TestRunReportsListenFailure(t *testing.T) {
	// Occupy a port first so ListenAndServe fails immediately and Run takes
	// the error branch instead of waiting for cancellation.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	cfg := testConfig(t)
	cfg.Server.Addr = l.Addr().String()

	ctx := context.Background()
	s, err := newServer(t, ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}

	err = s.Run(ctx)
	if err == nil || !strings.Contains(err.Error(), "http server") {
		t.Errorf("Run = %v, want a wrapped listen failure", err)
	}
}
