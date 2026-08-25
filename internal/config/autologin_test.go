package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeAutologinConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestOIDCAutoLogin(t *testing.T) {
	// The oidc block comes last so a test can append one more key to it.
	base := `
server:
  publicURL: https://orrery.example.com
clusters:
  - name: a
    kubeconfig: /tmp/kc
oidc:
  enabled: true
  issuer: https://issuer.example.com
  clientID: orrery
`
	cfg, err := Load(writeAutologinConfig(t, base))
	if err != nil {
		t.Fatal(err)
	}
	// Off unless asked for: bouncing every visitor to the IdP is a policy
	// choice, not a default.
	if cfg.OIDC.AutoLogin {
		t.Error("autoLogin should default to false")
	}

	cfg, err = Load(writeAutologinConfig(t, base+`  autoLogin: true`))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.OIDC.AutoLogin {
		t.Error("autoLogin: true in yaml not honoured")
	}

	t.Setenv("ORRERY_OIDC_AUTOLOGIN", "true")
	cfg, err = Load(writeAutologinConfig(t, base))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.OIDC.AutoLogin {
		t.Error("ORRERY_OIDC_AUTOLOGIN=true not honoured")
	}
}
