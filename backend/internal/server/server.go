// Package server assembles the application: configuration into clusters, auth
// and the HTTP surface, with graceful startup and shutdown.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/daiwazou/clusterlens/backend/internal/api"
	"github.com/daiwazou/clusterlens/backend/internal/auth"
	"github.com/daiwazou/clusterlens/backend/internal/cluster"
	"github.com/daiwazou/clusterlens/backend/internal/config"
)

// Server owns the listeners and the resources behind them.
type Server struct {
	cfg      *config.Config
	log      *slog.Logger
	registry *cluster.Registry
	store    auth.Store
	http     *http.Server
	metrics  *http.Server
}

// anonymousUser is the identity used when OIDC is disabled. It is deliberately
// namespaced under a prefix that cannot collide with a real OIDC username, so
// binding it in RBAC is an explicit, visible decision.
var anonymousUser = &auth.User{
	Username: "clusterlens:anonymous",
	Name:     "Local user",
	Groups:   []string{"system:authenticated"},
}

// New assembles the server from configuration.
func New(ctx context.Context, cfg *config.Config, log *slog.Logger) (*Server, error) {
	if cfg.EphemeralSessionKey() && cfg.OIDC.Enabled {
		log.Warn("session.encryptionKey was generated at startup; " +
			"sessions will not survive a restart and will not work across replicas")
	}

	registry, err := cluster.NewRegistry(cfg, log)
	if err != nil {
		return nil, err
	}

	store := auth.Store(auth.NewMemoryStore(cfg.Session.IdleTimeout))
	if cfg.Session.Store == "redis" {
		registry.Close()
		return nil, errors.New("session.store=redis is configured but this build has no Redis backend compiled in")
	}

	sessions, err := auth.NewSessionManager(cfg, store)
	if err != nil {
		registry.Close()
		return nil, err
	}

	var authn *auth.Authenticator
	var anon *auth.User
	if cfg.OIDC.Enabled {
		discoveryCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		authn, err = auth.NewAuthenticator(discoveryCtx, cfg, sessions)
		cancel()
		if err != nil {
			registry.Close()
			return nil, err
		}
		log.Info("OIDC enabled", "issuer", cfg.OIDC.Issuer, "clientID", cfg.OIDC.ClientID)
	} else {
		anon = anonymousUser
		log.Warn("OIDC is disabled: every request runs as " + anonymousUser.Username +
			"; do not expose this deployment to a network you do not control")
	}

	mw := auth.NewMiddleware(sessions, authn, anon)
	apiSrv := api.New(cfg, registry, sessions, authn, mw, log)

	prometheus.MustRegister(api.NewCacheCollector(apiSrv))

	s := &Server{
		cfg:      cfg,
		log:      log,
		registry: registry,
		store:    store,
		http: &http.Server{
			Addr:        cfg.Server.Addr,
			Handler:     apiSrv.Router(),
			ReadTimeout: cfg.Server.ReadTimeout,
			// WriteTimeout is deliberately left at zero: log follows, watches
			// and exec sessions are long-lived by design and a write deadline
			// would sever them mid-stream.
			WriteTimeout:      cfg.Server.WriteTimeout,
			ReadHeaderTimeout: 15 * time.Second,
			IdleTimeout:       120 * time.Second,
		},
	}

	if cfg.Server.MetricsAddr != "" {
		mux := http.NewServeMux()
		mux.Handle("/metrics", promhttp.Handler())
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		})
		s.metrics = &http.Server{
			Addr:              cfg.Server.MetricsAddr,
			Handler:           mux,
			ReadHeaderTimeout: 10 * time.Second,
		}
	}
	return s, nil
}

// Run serves until the context is cancelled, then shuts down gracefully.
func (s *Server) Run(ctx context.Context) error {
	errCh := make(chan error, 2)

	go func() {
		s.log.Info("listening", "addr", s.cfg.Server.Addr, "publicURL", s.cfg.Server.PublicURL)
		if err := s.http.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("http server: %w", err)
		}
	}()

	if s.metrics != nil {
		go func() {
			s.log.Info("metrics listening", "addr", s.cfg.Server.MetricsAddr)
			if err := s.metrics.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errCh <- fmt.Errorf("metrics server: %w", err)
			}
		}()
	}

	for _, e := range s.registry.Entries() {
		status := "ready"
		if e.Cluster == nil {
			status = "unavailable"
		}
		s.log.Info("cluster registered", "name", e.Name, "authMode", e.AuthMode, "status", status)
	}

	select {
	case err := <-errCh:
		s.shutdown()
		return err
	case <-ctx.Done():
		s.log.Info("shutting down")
		s.shutdown()
		return nil
	}
}

func (s *Server) shutdown() {
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.Server.ShutdownTimeout)
	defer cancel()

	if err := s.http.Shutdown(ctx); err != nil {
		s.log.Warn("http shutdown", "err", err)
	}
	if s.metrics != nil {
		_ = s.metrics.Shutdown(ctx)
	}
	s.registry.Close()
	_ = s.store.Close()
}
