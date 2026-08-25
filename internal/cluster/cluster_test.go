package cluster

import (
	"net/http"
	"strings"
	"testing"

	"github.com/daiwa-zou/orrery/internal/config"
)

func TestNewBuildsClusterAndProbes(t *testing.T) {
	f := newFakeAPI(t)
	c := newTestCluster(t, f, config.AuthModeImpersonation)

	// probe() is called directly for determinism instead of waiting on the
	// probe loop's ticker.
	c.probe()
	h := c.Health()
	if h.Status != HealthOK {
		t.Fatalf("status = %s (%s), want %s", h.Status, h.Message, HealthOK)
	}
	if h.Version != fakeGitVersion {
		t.Errorf("version = %q, want %q", h.Version, fakeGitVersion)
	}
	if h.CheckedAt.IsZero() {
		t.Error("CheckedAt was not set")
	}
	if c.OpenAPIClient() == nil {
		t.Error("OpenAPIClient returned nil")
	}
}

func TestProbeDegradedAndUnreachable(t *testing.T) {
	f := newFakeAPI(t)
	c := newTestCluster(t, f, config.AuthModeImpersonation)

	f.setReadyz(http.StatusOK, "no")
	c.probe()
	if h := c.Health(); h.Status != HealthDegraded || h.Message != "API server readyz is not ok" {
		t.Errorf("readyz body 'no': got %s / %q", h.Status, h.Message)
	}

	f.setReadyz(http.StatusInternalServerError, "boom")
	c.probe()
	if h := c.Health(); h.Status != HealthDegraded || h.Message == "" {
		t.Errorf("readyz 500: got %s / %q, want degraded with message", h.Status, h.Message)
	}

	// A dead API server must read as unreachable, not as an error page.
	f.srv.Close()
	c.probe()
	if h := c.Health(); h.Status != HealthUnreachable || h.Message == "" {
		t.Errorf("dead server: got %s / %q, want unreachable with message", h.Status, h.Message)
	}
}

func TestHealthDefaultsToUnknown(t *testing.T) {
	// A cluster whose probe has never run must still answer, not crash the
	// switcher.
	var c Cluster
	if h := c.Health(); h.Status != HealthUnknown {
		t.Errorf("status = %s, want %s", h.Status, HealthUnknown)
	}
}

func TestNewErrors(t *testing.T) {
	f := newFakeAPI(t)
	kc := writeKubeconfig(t, f.srv.URL)

	t.Run("missing kubeconfig", func(t *testing.T) {
		cfg := testClusterConfig(t.TempDir()+"/absent", config.AuthModeImpersonation)
		if _, err := New(cfg, config.Default(), testLogger()); err == nil {
			t.Fatal("expected an error for a missing kubeconfig")
		}
	})

	t.Run("unknown context", func(t *testing.T) {
		cfg := testClusterConfig(kc, config.AuthModeImpersonation)
		cfg.Context = "no-such-context"
		_, err := New(cfg, config.Default(), testLogger())
		if err == nil || !strings.Contains(err.Error(), "no-such-context") {
			t.Fatalf("err = %v, want mention of the bad context", err)
		}
	})

	t.Run("in-cluster outside a cluster", func(t *testing.T) {
		// Blank the env InClusterConfig reads so the test passes even inside a
		// pod.
		t.Setenv("KUBERNETES_SERVICE_HOST", "")
		t.Setenv("KUBERNETES_SERVICE_PORT", "")
		cfg := testClusterConfig("", config.AuthModeImpersonation)
		cfg.InCluster = true
		_, err := New(cfg, config.Default(), testLogger())
		if err == nil || !strings.Contains(err.Error(), "in-cluster config") {
			t.Fatalf("err = %v, want in-cluster config error", err)
		}
	})

	t.Run("client construction fails", func(t *testing.T) {
		// QPS without Burst is rejected by client-go's rate limiter setup,
		// which is the cheapest way to reach clientsFor's error path.
		cfg := testClusterConfig(kc, config.AuthModeImpersonation)
		cfg.QPS, cfg.Burst = 1, 0
		_, err := New(cfg, config.Default(), testLogger())
		if err == nil || !strings.Contains(err.Error(), "typed client") {
			t.Fatalf("err = %v, want typed client error", err)
		}
	})
}

func TestRestConfigForSelectsContext(t *testing.T) {
	f := newFakeAPI(t)
	kc := writeKubeconfig(t, f.srv.URL)

	def, err := restConfigFor(testClusterConfig(kc, config.AuthModeImpersonation))
	if err != nil {
		t.Fatalf("default context: %v", err)
	}
	if def.Host != f.srv.URL {
		t.Errorf("default context host = %q, want %q", def.Host, f.srv.URL)
	}

	cfg := testClusterConfig(kc, config.AuthModeImpersonation)
	cfg.Context = "other"
	other, err := restConfigFor(cfg)
	if err != nil {
		t.Fatalf("other context: %v", err)
	}
	if other.Host != "http://127.0.0.1:1" {
		t.Errorf("other context host = %q, want the dead server", other.Host)
	}
}

func TestClientsForServiceAccount(t *testing.T) {
	f := newFakeAPI(t)
	c := newTestCluster(t, f, config.AuthModeServiceAccount)

	cl, err := c.ClientsFor(Identity{Username: "anyone"})
	if err != nil {
		t.Fatalf("ClientsFor: %v", err)
	}
	if cl != c.base {
		t.Error("service account mode must hand out the base clients unchanged")
	}
}

func TestClientsForPassthrough(t *testing.T) {
	f := newFakeAPI(t)
	c := newTestCluster(t, f, config.AuthModePassthrough)

	if _, err := c.ClientsFor(Identity{Username: "alice"}); err == nil {
		t.Fatal("passthrough without a token must fail")
	}

	id := Identity{Username: "alice", BearerToken: "tok-a"}
	cl, err := c.ClientsFor(id)
	if err != nil {
		t.Fatalf("ClientsFor: %v", err)
	}
	if cl == c.base {
		t.Error("passthrough must not reuse the dashboard's own credential")
	}
	if cl.Rest.BearerToken != "tok-a" {
		t.Errorf("bearer token = %q, want the user's own", cl.Rest.BearerToken)
	}
	if cl.Rest.UserAgent != c.base.Rest.UserAgent {
		t.Errorf("user agent %q not carried over", cl.Rest.UserAgent)
	}

	again, err := c.ClientsFor(id)
	if err != nil {
		t.Fatalf("second ClientsFor: %v", err)
	}
	if again != cl {
		t.Error("same token should hit the client cache")
	}

	other, err := c.ClientsFor(Identity{Username: "alice", BearerToken: "tok-b"})
	if err != nil {
		t.Fatalf("other token: %v", err)
	}
	if other == cl {
		t.Error("a different token must build different clients")
	}
}

func TestClientsForImpersonation(t *testing.T) {
	f := newFakeAPI(t)
	c := newTestCluster(t, f, config.AuthModeImpersonation)

	if _, err := c.ClientsFor(Identity{}); err == nil {
		t.Fatal("impersonation without a username must fail")
	}

	id := Identity{Username: "alice", Groups: []string{"dev", "ops"}}
	cl, err := c.ClientsFor(id)
	if err != nil {
		t.Fatalf("ClientsFor: %v", err)
	}
	if cl.Rest.Impersonate.UserName != "alice" {
		t.Errorf("impersonated user = %q", cl.Rest.Impersonate.UserName)
	}
	if len(cl.Rest.Impersonate.Groups) != 2 || cl.Rest.Impersonate.Groups[0] != "dev" {
		t.Errorf("impersonated groups = %v", cl.Rest.Impersonate.Groups)
	}

	again, err := c.ClientsFor(id)
	if err != nil {
		t.Fatalf("second ClientsFor: %v", err)
	}
	if again != cl {
		t.Error("same identity should hit the client cache")
	}

	// "a b" + no groups and "a" + group "b" must never share a cache key; the
	// quoting in the key exists exactly for this.
	x, err := c.ClientsFor(Identity{Username: "a b"})
	if err != nil {
		t.Fatalf("user 'a b': %v", err)
	}
	y, err := c.ClientsFor(Identity{Username: "a", Groups: []string{"b"}})
	if err != nil {
		t.Fatalf("user 'a' group 'b': %v", err)
	}
	if x == y {
		t.Error("distinct identities collided on one cached client")
	}
}

func TestAuthSubject(t *testing.T) {
	f := newFakeAPI(t)
	sa := newTestCluster(t, f, config.AuthModeServiceAccount)
	pt := newTestCluster(t, f, config.AuthModePassthrough)
	imp := newTestCluster(t, f, config.AuthModeImpersonation)

	if s := sa.AuthSubject(Identity{Username: "alice"}); !s.Self || s.SelfID != "" {
		t.Errorf("serviceaccount subject = %+v, want anonymous self", s)
	}

	if s := pt.AuthSubject(Identity{Username: "alice", BearerToken: "tok"}); !s.Self || s.SelfID != "alice" {
		t.Errorf("passthrough subject = %+v, want self keyed by username", s)
	}
	// Without a username the cache key must still distinguish users, so it
	// falls back to a token digest.
	s := pt.AuthSubject(Identity{BearerToken: "tok"})
	if !s.Self || !strings.HasPrefix(s.SelfID, "tok:") {
		t.Errorf("tokened subject = %+v, want tok: digest SelfID", s)
	}

	if s := imp.AuthSubject(Identity{Username: "alice", Groups: []string{"dev"}}); s.Self || s.User != "alice" || len(s.Groups) != 1 {
		t.Errorf("impersonation subject = %+v, want explicit user+groups", s)
	}
}

func TestAuthzClient(t *testing.T) {
	f := newFakeAPI(t)
	imp := newTestCluster(t, f, config.AuthModeImpersonation)
	pt := newTestCluster(t, f, config.AuthModePassthrough)

	user, err := pt.ClientsFor(Identity{BearerToken: "tok"})
	if err != nil {
		t.Fatalf("ClientsFor: %v", err)
	}

	// Impersonation reviews run with the dashboard's own standing.
	if got := imp.AuthzClient(user); got != imp.base.Kube {
		t.Error("impersonation must review with the base client")
	}
	// Passthrough reviews run as the caller.
	if got := pt.AuthzClient(user); got != user.Kube {
		t.Error("passthrough must review with the user's client")
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	f := newFakeAPI(t)
	c := newTestCluster(t, f, config.AuthModeImpersonation)
	c.Close()
	c.Close() // second close must not panic on the stop channel
}
