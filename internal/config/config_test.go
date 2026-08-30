package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadAppliesDefaults(t *testing.T) {
	path := writeConfig(t, `
clusters:
  - name: prod
    kubeconfig: /tmp/kubeconfig
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Server.Addr != ":8080" {
		t.Errorf("addr = %q", cfg.Server.Addr)
	}
	c := cfg.Clusters[0]
	// Impersonation is the default because it is the only mode that is both
	// correct per-user and cheap at scale.
	if c.AuthMode != AuthModeImpersonation {
		t.Errorf("default authMode = %q, want impersonation", c.AuthMode)
	}
	if c.DisplayName != "prod" {
		t.Errorf("displayName should default to the name, got %q", c.DisplayName)
	}
	if c.QPS == 0 || c.Burst == 0 {
		t.Error("client rate limits should have defaults so one dashboard cannot flood an API server")
	}
}

func TestLoadRejectsBadClusters(t *testing.T) {
	cases := map[string]string{
		"missing name": `
clusters:
  - kubeconfig: /tmp/k
`,
		"duplicate names": `
clusters:
  - {name: a, kubeconfig: /tmp/k}
  - {name: a, kubeconfig: /tmp/k2}
`,
		"no credential": `
clusters:
  - {name: a}
`,
		"unknown auth mode": `
clusters:
  - {name: a, kubeconfig: /tmp/k, authMode: magic}
`,
		"passthrough without oidc": `
oidc: {enabled: false}
clusters:
  - {name: a, kubeconfig: /tmp/k, authMode: passthrough}
`,
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(writeConfig(t, body)); err == nil {
				t.Error("expected a configuration error")
			}
		})
	}
}

func TestOIDCRequiresIssuerAndClientID(t *testing.T) {
	body := `
oidc:
  enabled: true
clusters:
  - {name: a, kubeconfig: /tmp/k, authMode: serviceaccount}
`
	_, err := Load(writeConfig(t, body))
	if err == nil {
		t.Fatal("expected an error when OIDC is enabled without an issuer")
	}
	if !strings.Contains(err.Error(), "issuer") {
		t.Errorf("the error should name the missing field, got: %v", err)
	}
}

func TestRedirectURLDerivedFromPublicURL(t *testing.T) {
	body := `
server:
  publicURL: 'https://console.example.com/'
oidc:
  enabled: true
  issuer: https://id.example.com
  clientID: abc
clusters:
  - {name: a, kubeconfig: /tmp/k, authMode: serviceaccount}
`
	cfg, err := Load(writeConfig(t, body))
	if err != nil {
		t.Fatal(err)
	}
	// The trailing slash must not survive into the redirect URI, which has to
	// match what is registered with the provider byte for byte.
	if cfg.OIDC.RedirectURL != "https://console.example.com/api/v1/auth/callback" {
		t.Errorf("redirectURL = %q", cfg.OIDC.RedirectURL)
	}
}

func TestOfflineAccessAddsScope(t *testing.T) {
	body := `
oidc:
  enabled: true
  issuer: https://id.example.com
  clientID: abc
  offlineAccess: true
clusters:
  - {name: a, kubeconfig: /tmp/k, authMode: serviceaccount}
`
	cfg, err := Load(writeConfig(t, body))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(cfg.OIDC.Scopes, "offline_access") {
		t.Errorf("offline_access was not requested: %v", cfg.OIDC.Scopes)
	}
}

func TestSessionKeyValidation(t *testing.T) {
	cfg := Default()
	cfg.Session.EncryptionKey = "not-base64!!"
	if _, err := cfg.SessionKey(); err == nil {
		t.Error("a non-base64 key should be rejected")
	}

	cfg.Session.EncryptionKey = "c2hvcnQ=" // "short"
	if _, err := cfg.SessionKey(); err == nil {
		t.Error("a key of the wrong length should be rejected")
	}
}

func TestEphemeralKeyIsFlagged(t *testing.T) {
	// An operator running several replicas needs to know their users will be
	// signed out at random, so this has to be observable.
	cfg, err := Load(writeConfig(t, `
clusters:
  - {name: a, kubeconfig: /tmp/k, authMode: serviceaccount}
`))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.EphemeralSessionKey() {
		t.Error("a generated session key should be flagged as ephemeral")
	}
	if _, err := cfg.SessionKey(); err != nil {
		t.Errorf("the generated key should still be valid: %v", err)
	}
}

func TestEnvironmentOverrides(t *testing.T) {
	t.Setenv("ORRERY_ADDR", ":9999")
	t.Setenv("ORRERY_OIDC_ENABLED", "true")
	t.Setenv("ORRERY_OIDC_ISSUER", "https://id.example.com")
	t.Setenv("ORRERY_OIDC_CLIENT_ID", "from-env")

	cfg, err := Load(writeConfig(t, `
clusters:
  - {name: a, kubeconfig: /tmp/k, authMode: serviceaccount}
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Addr != ":9999" {
		t.Errorf("addr = %q", cfg.Server.Addr)
	}
	if cfg.OIDC.ClientID != "from-env" {
		t.Errorf("clientID = %q", cfg.OIDC.ClientID)
	}
}

func TestKubeconfigFromEnvironmentCreatesACluster(t *testing.T) {
	// The zero-config path: point the binary at a kubeconfig and it runs.
	t.Setenv("ORRERY_KUBECONFIG", "/tmp/kubeconfig")
	t.Setenv("ORRERY_CONTEXT", "kind-dev")

	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Clusters) != 1 {
		t.Fatalf("got %d clusters", len(cfg.Clusters))
	}
	if cfg.Clusters[0].Context != "kind-dev" {
		t.Errorf("context = %q", cfg.Clusters[0].Context)
	}
}

func TestExpandsEnvironmentVariablesInFile(t *testing.T) {
	t.Setenv("TEST_CLIENT_SECRET", "s3cr3t")
	cfg, err := Load(writeConfig(t, `
oidc:
  enabled: true
  issuer: https://id.example.com
  clientID: abc
  clientSecret: ${TEST_CLIENT_SECRET}
clusters:
  - {name: a, kubeconfig: /tmp/k, authMode: serviceaccount}
`))
	if err != nil {
		t.Fatal(err)
	}
	// Secrets belong in the environment, not in a file that ends up in git.
	if cfg.OIDC.ClientSecret != "s3cr3t" {
		t.Errorf("clientSecret = %q", cfg.OIDC.ClientSecret)
	}
}

func TestDisabledClusterIsStillParsed(t *testing.T) {
	no := false
	cfg, err := Load(writeConfig(t, `
clusters:
  - {name: a, kubeconfig: /tmp/k, authMode: serviceaccount, enabled: false}
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Clusters[0].Enabled == nil || *cfg.Clusters[0].Enabled != no {
		t.Error("enabled: false should be preserved for the registry to act on")
	}
}

func TestWildcardCORSOriginIsRejected(t *testing.T) {
	body := `
server:
  corsOrigins: ['*']
clusters:
  - {name: a, kubeconfig: /tmp/k, authMode: serviceaccount}
`
	if _, err := Load(writeConfig(t, body)); err == nil ||
		!strings.Contains(err.Error(), "corsOrigins") {
		t.Errorf("a wildcard CORS origin must be rejected, got err=%v", err)
	}
}

// A value that looks accepted, is not the value that takes effect, and is never
// mentioned again is the failure this configuration is most careful about —
// finalize has refused an unknown authMode since it was written. These four
// settings sat beside it, each matched case-sensitively against a literal with
// a default underneath, so `Strict`, `Redis` and `DEBUG` were all quietly
// something else.
func TestUnknownEnumSettingsAreRefusedRatherThanDowngraded(t *testing.T) {
	cases := []struct {
		name  string
		body  string
		names string
	}{
		{
			// The protection asked for is not the one that was applied.
			"sameSite spelled as Go spells it",
			"session:\n  sameSite: Strict\n",
			"sameSite",
		},
		{
			// And an HA deployment then keeps sessions in one replica's memory.
			"store spelled with a capital",
			"session:\n  store: Redis\n  redisURL: redis://localhost:6379\n",
			"store",
		},
		{
			"a store nobody implements",
			"session:\n  store: postgres\n",
			"store",
		},
		{
			// Discovered while trying to debug something else.
			"level shouted",
			"log:\n  level: DEBUG\n",
			"log.level",
		},
		{
			"a format nobody implements",
			"log:\n  format: logfmt\n",
			"log.format",
		},
	}

	for _, c := range cases {
		body := c.body + "clusters:\n  - {name: a, kubeconfig: /tmp/k, authMode: serviceaccount}\n"
		_, err := Load(writeConfig(t, body))
		if err == nil {
			t.Errorf("%s: accepted silently", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.names) {
			t.Errorf("%s: err = %v, want it to name %q", c.name, err, c.names)
		}
	}
}

// A SameSite=None cookie that is not Secure is dropped by every current
// browser, so the session is never stored and sign-in bounces forever. Nothing
// in any response says so — the refusal happens in the browser — which makes it
// exactly the kind of misconfiguration worth refusing at boot.
func TestSameSiteNoneWithoutSecureIsRefused(t *testing.T) {
	body := `
session:
  sameSite: none
  secure: false
clusters:
  - {name: a, kubeconfig: /tmp/k, authMode: serviceaccount}
`
	_, err := Load(writeConfig(t, body))
	if err == nil || !strings.Contains(err.Error(), "sameSite") {
		t.Fatalf("err = %v, want a refusal naming sameSite", err)
	}
	if !strings.Contains(err.Error(), "secure") {
		t.Errorf("err = %v, want it to name the setting that fixes it", err)
	}
}

// The spellings that were always right stay right, and an unset value still
// means the default rather than a misread one.
func TestValidEnumSettingsAreAccepted(t *testing.T) {
	for _, body := range []string{
		"session:\n  sameSite: strict\n",
		"session:\n  sameSite: lax\n",
		"session:\n  sameSite: none\n  secure: true\n",
		"session:\n  store: memory\n",
		"session:\n  store: redis\n  redisURL: redis://localhost:6379\n",
		"log:\n  level: debug\n  format: json\n",
		"log: {}\n",
	} {
		full := body + "clusters:\n  - {name: a, kubeconfig: /tmp/k, authMode: serviceaccount}\n"
		if _, err := Load(writeConfig(t, full)); err != nil {
			t.Errorf("%q: %v", body, err)
		}
	}
}
