// Command clusterlens serves a multi-cluster Kubernetes dashboard.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/daiwazou/clusterlens/backend/internal/config"
	"github.com/daiwazou/clusterlens/backend/internal/server"
)

func main() {
	var (
		configPath = flag.String("config", os.Getenv("CLUSTERLENS_CONFIG"), "path to the YAML configuration file")
		printCfg   = flag.Bool("print-config", false, "print the resolved configuration and exit")
	)
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "configuration error: %v\n", err)
		os.Exit(2)
	}

	log := newLogger(cfg.Log)
	slog.SetDefault(log)

	if *printCfg {
		printResolved(cfg)
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv, err := server.New(ctx, cfg, log)
	if err != nil {
		log.Error("startup failed", "err", err)
		os.Exit(1)
	}
	if err := srv.Run(ctx); err != nil {
		log.Error("server stopped", "err", err)
		os.Exit(1)
	}
}

func newLogger(cfg config.LogConfig) *slog.Logger {
	level := slog.LevelInfo
	switch cfg.Level {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	opts := &slog.HandlerOptions{Level: level}
	if cfg.Format == "json" {
		return slog.New(slog.NewJSONHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stdout, opts))
}

// printResolved shows the effective configuration with secrets masked, which
// is the fastest way to debug a deployment that is not behaving as its YAML
// suggests it should.
func printResolved(cfg *config.Config) {
	fmt.Printf("server.addr:          %s\n", cfg.Server.Addr)
	fmt.Printf("server.publicURL:     %s\n", cfg.Server.PublicURL)
	fmt.Printf("server.webRoot:       %s\n", orNone(cfg.Server.WebRoot))
	fmt.Printf("server.metricsAddr:   %s\n", orNone(cfg.Server.MetricsAddr))
	fmt.Printf("oidc.enabled:         %t\n", cfg.OIDC.Enabled)
	if cfg.OIDC.Enabled {
		fmt.Printf("oidc.issuer:          %s\n", cfg.OIDC.Issuer)
		fmt.Printf("oidc.clientID:        %s\n", cfg.OIDC.ClientID)
		fmt.Printf("oidc.clientSecret:    %s\n", mask(cfg.OIDC.ClientSecret))
		fmt.Printf("oidc.redirectURL:     %s\n", cfg.OIDC.RedirectURL)
		fmt.Printf("oidc.usernameClaim:   %s (prefix %q)\n", cfg.OIDC.UsernameClaim, cfg.OIDC.UsernamePrefix)
		fmt.Printf("oidc.groupsClaim:     %s (prefix %q)\n", cfg.OIDC.GroupsClaim, cfg.OIDC.GroupsPrefix)
	}
	fmt.Printf("session.store:        %s\n", cfg.Session.Store)
	fmt.Printf("session.key:          %s\n", keyOrigin(cfg))
	fmt.Printf("cache.idleTimeout:    %s\n", cfg.Cache.IdleTimeout)
	fmt.Printf("cache.maxInformers:   %d\n", cfg.Cache.MaxInformersPerCluster)
	fmt.Printf("authz.ttl:            %s\n", cfg.Authz.TTL)
	fmt.Printf("clusters:             %d\n", len(cfg.Clusters))
	for _, c := range cfg.Clusters {
		fmt.Printf("  - %-20s authMode=%-14s kubeconfig=%s context=%s\n",
			c.Name, c.AuthMode, orNone(c.Kubeconfig), orNone(c.Context))
	}
}

func orNone(s string) string {
	if s == "" {
		return "<none>"
	}
	return s
}

func mask(s string) string {
	if s == "" {
		return "<none>"
	}
	return "<set>"
}

func keyOrigin(cfg *config.Config) string {
	if cfg.EphemeralSessionKey() {
		return "<generated at startup — set session.encryptionKey for multi-replica>"
	}
	return "<configured>"
}
