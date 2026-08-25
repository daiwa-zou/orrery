package main

import (
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/daiwa-zou/orrery/backend/internal/config"
)

func TestNewLoggerLevels(t *testing.T) {
	cases := []struct {
		level   string
		enabled slog.Level
		muted   slog.Level
	}{
		{"debug", slog.LevelDebug, slog.LevelDebug - 1},
		{"warn", slog.LevelWarn, slog.LevelInfo},
		{"error", slog.LevelError, slog.LevelWarn},
		{"", slog.LevelInfo, slog.LevelDebug},
		{"bogus", slog.LevelInfo, slog.LevelDebug}, // unknown levels fall back to info
	}
	for _, tc := range cases {
		t.Run("level "+tc.level, func(t *testing.T) {
			log := newLogger(config.LogConfig{Level: tc.level})
			if !log.Enabled(t.Context(), tc.enabled) {
				t.Errorf("level %v should be enabled", tc.enabled)
			}
			if log.Enabled(t.Context(), tc.muted) {
				t.Errorf("level %v should be muted", tc.muted)
			}
		})
	}
}

func TestNewLoggerFormat(t *testing.T) {
	if _, ok := newLogger(config.LogConfig{Format: "json"}).Handler().(*slog.JSONHandler); !ok {
		t.Error("format json must build a JSON handler")
	}
	if _, ok := newLogger(config.LogConfig{Format: "text"}).Handler().(*slog.TextHandler); !ok {
		t.Error("format text must build a text handler")
	}
	if _, ok := newLogger(config.LogConfig{}).Handler().(*slog.TextHandler); !ok {
		t.Error("the default format must be text")
	}
}

func TestOrNone(t *testing.T) {
	if got := orNone(""); got != "<none>" {
		t.Errorf("orNone(\"\") = %q", got)
	}
	if got := orNone("/srv/web"); got != "/srv/web" {
		t.Errorf("orNone kept nothing: %q", got)
	}
}

func TestMaskNeverEchoesTheSecret(t *testing.T) {
	if got := mask(""); got != "<none>" {
		t.Errorf("mask(\"\") = %q", got)
	}
	if got := mask("hunter2"); got != "<set>" {
		t.Errorf("mask leaked or mangled the secret: %q", got)
	}
}

func TestKeyOrigin(t *testing.T) {
	// Default() leaves the key unset without marking it ephemeral; only Load
	// mints one, so this stands in for an operator-configured key.
	if got := keyOrigin(config.Default()); got != "<configured>" {
		t.Errorf("keyOrigin = %q", got)
	}

	// Loading with no key configured generates one and must say so, because a
	// generated key silently breaks sessions across restarts and replicas.
	for _, env := range []string{"ORRERY_SESSION_KEY", "ORRERY_KUBECONFIG", "ORRERY_OIDC_ENABLED", "ORRERY_REDIS_URL"} {
		t.Setenv(env, "")
	}
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	if got := keyOrigin(cfg); !strings.Contains(got, "generated at startup") {
		t.Errorf("keyOrigin for an ephemeral key = %q", got)
	}
}

// captureStdout runs f with os.Stdout redirected into a pipe and returns what
// it printed. printResolved writes straight to stdout by design — it is a
// debugging command — so the test meets it there.
func captureStdout(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	f()
	_ = w.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestPrintResolvedMasksSecretsAndListsClusters(t *testing.T) {
	cfg := config.Default()
	cfg.OIDC.Enabled = true
	cfg.OIDC.Issuer = "https://issuer.example"
	cfg.OIDC.ClientID = "orrery"
	cfg.OIDC.ClientSecret = "super-secret"
	cfg.Clusters = []config.ClusterConfig{
		{Name: "prod", AuthMode: config.AuthModeImpersonation, Kubeconfig: "/etc/prod.yaml"},
		{Name: "dev", AuthMode: config.AuthModePassthrough},
	}

	out := captureStdout(t, func() { printResolved(cfg) })

	if strings.Contains(out, "super-secret") {
		t.Error("the client secret must never be printed")
	}
	if !strings.Contains(out, "oidc.clientSecret:    <set>") {
		t.Error("a configured secret should print as <set>")
	}
	if !strings.Contains(out, "clusters:             2") {
		t.Errorf("cluster count missing:\n%s", out)
	}
	if !strings.Contains(out, "prod") || !strings.Contains(out, "dev") {
		t.Errorf("clusters not listed:\n%s", out)
	}
	// A cluster with no kubeconfig (in-cluster) renders <none>, not blank.
	if !strings.Contains(out, "kubeconfig=<none>") {
		t.Errorf("empty kubeconfig not rendered as <none>:\n%s", out)
	}
}

func TestPrintResolvedOmitsOIDCDetailWhenDisabled(t *testing.T) {
	out := captureStdout(t, func() { printResolved(config.Default()) })
	if !strings.Contains(out, "oidc.enabled:         false") {
		t.Errorf("oidc.enabled line missing:\n%s", out)
	}
	if strings.Contains(out, "oidc.issuer") {
		t.Error("issuer detail should not print when OIDC is disabled")
	}
}
