package cluster

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daiwa-zou/orrery/internal/config"
)

func TestExpandClustersPassthrough(t *testing.T) {
	in := []config.ClusterConfig{{Name: "a", Context: "ctx-a"}, {Name: "b"}}
	out, err := expandClusters(in)
	if err != nil {
		t.Fatalf("expandClusters: %v", err)
	}
	if len(out) != 2 || out[0].Name != "a" || out[1].Name != "b" {
		t.Errorf("non-wildcard specs should pass through unchanged, got %+v", out)
	}
}

func TestExpandClustersWildcard(t *testing.T) {
	f := newFakeAPI(t)
	kc := writeKubeconfig(t, f.srv.URL)

	out, err := expandClusters([]config.ClusterConfig{{Name: "dev", Kubeconfig: kc, Context: "*"}})
	if err != nil {
		t.Fatalf("expandClusters: %v", err)
	}
	// The kubeconfig holds contexts "fake" and "other"; expansion is sorted so
	// the dashboard's cluster order is stable across restarts.
	if len(out) != 2 || out[0].Name != "fake" || out[1].Name != "other" {
		t.Fatalf("expanded to %+v, want fake then other", out)
	}
	for _, c := range out {
		if c.Context != c.Name || c.DisplayName != c.Name {
			t.Errorf("expanded cluster %q should be named after its context, got %+v", c.Name, c)
		}
		if c.Kubeconfig != kc {
			t.Errorf("expanded cluster %q lost its kubeconfig path", c.Name)
		}
	}
}

func TestExpandClustersErrors(t *testing.T) {
	f := newFakeAPI(t)
	kc := writeKubeconfig(t, f.srv.URL)

	t.Run("missing kubeconfig", func(t *testing.T) {
		_, err := expandClusters([]config.ClusterConfig{{Name: "x", Kubeconfig: filepath.Join(t.TempDir(), "absent"), Context: "*"}})
		if err == nil || !strings.Contains(err.Error(), "expand contexts") {
			t.Fatalf("err = %v, want expand contexts error", err)
		}
	})

	t.Run("duplicate names after expansion", func(t *testing.T) {
		_, err := expandClusters([]config.ClusterConfig{
			{Name: "a", Kubeconfig: kc, Context: "*"},
			{Name: "b", Kubeconfig: kc, Context: "*"},
		})
		if err == nil || !strings.Contains(err.Error(), "duplicate cluster name") {
			t.Fatalf("err = %v, want duplicate name error", err)
		}
	})
}

func TestNewRegistryRequiresClusters(t *testing.T) {
	appCfg := config.Default()
	if _, err := NewRegistry(appCfg, testLogger()); err == nil {
		t.Fatal("an empty cluster list must be a configuration error")
	}
}

func TestNewRegistryUnusableConfig(t *testing.T) {
	appCfg := config.Default()
	appCfg.Clusters = []config.ClusterConfig{
		{Name: "w", Kubeconfig: filepath.Join(t.TempDir(), "absent"), Context: "*"},
	}
	if _, err := NewRegistry(appCfg, testLogger()); err == nil {
		t.Fatal("a wildcard over a missing kubeconfig must fail registry construction")
	}
}

func TestRegistryLifecycle(t *testing.T) {
	f := newFakeAPI(t)
	kc := writeKubeconfig(t, f.srv.URL)
	off := false

	appCfg := config.Default()
	appCfg.Clusters = []config.ClusterConfig{
		{Name: "good", DisplayName: "Good", Kubeconfig: kc, AuthMode: config.AuthModeImpersonation, QPS: 50, Burst: 100,
			Labels: map[string]string{"env": "test"}},
		// A broken credential must not sink the registry; the entry records
		// the error instead.
		{Name: "bad", Kubeconfig: filepath.Join(t.TempDir(), "absent"), AuthMode: config.AuthModeImpersonation},
		{Name: "off", Kubeconfig: kc, Enabled: &off},
	}

	r, err := NewRegistry(appCfg, testLogger())
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer r.Close()

	entries := r.Entries()
	if len(entries) != 2 || entries[0].Name != "good" || entries[1].Name != "bad" {
		t.Fatalf("entries = %+v, want good then bad (off disabled)", entries)
	}
	if entries[0].Err != nil || entries[0].Cluster == nil || entries[0].DisplayName != "Good" || entries[0].Labels["env"] != "test" {
		t.Errorf("good entry malformed: %+v", entries[0])
	}
	if entries[1].Err == nil || entries[1].Cluster != nil {
		t.Errorf("bad entry should carry its startup error: %+v", entries[1])
	}

	if c, err := r.Get("good"); err != nil || c == nil {
		t.Errorf("Get(good) = %v, %v", c, err)
	}
	if _, err := r.Get("bad"); err == nil || !strings.Contains(err.Error(), "not available") {
		t.Errorf("Get(bad) err = %v, want 'not available'", err)
	}

	var unknown *UnknownClusterError
	if _, err := r.Get("off"); !errors.As(err, &unknown) {
		t.Errorf("Get(off) err = %v, want UnknownClusterError for a disabled cluster", err)
	}
	if _, err := r.Get("nope"); !errors.As(err, &unknown) {
		t.Errorf("Get(nope) err = %v, want UnknownClusterError", err)
	}

	r.Close()
	r.Close() // idempotent
}

func TestUnknownClusterErrorMessage(t *testing.T) {
	err := &UnknownClusterError{Name: "prod"}
	if got := err.Error(); got != `unknown cluster "prod"` {
		t.Errorf("Error() = %q", got)
	}
}
