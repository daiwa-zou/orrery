package cluster

// A minimal fake Kubernetes API server over HTTP. The production code builds
// real clients from a kubeconfig, so pointing a kubeconfig at an httptest
// server exercises the same client construction and wire decoding as a live
// cluster would, deterministically and offline.

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/version"

	"github.com/daiwa-zou/orrery/internal/config"
)

const fakeGitVersion = "v1.30.0-fake"

func TestMain(m *testing.M) {
	// client-go's WatchListClient feature makes reflectors open a streaming
	// watch that the fake dynamic client never completes (it sends no
	// initial-events-end bookmark), so informer sync would hang forever. The
	// gate is read from the environment exactly once, so it must be switched
	// off before the first informer starts.
	os.Setenv("KUBE_FEATURE_WatchListClient", "false")
	os.Exit(m.Run())
}

type fakeAPI struct {
	srv *httptest.Server

	mu            sync.Mutex
	readyzStatus  int
	readyzBody    string
	failDiscovery bool
	serveCRD      bool
	// groupHits counts GETs of /apis; each discovery refresh performs exactly
	// one, which makes cache-hit assertions precise.
	groupHits int
}

func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()
	f := &fakeAPI{readyzStatus: http.StatusOK, readyzBody: "ok"}

	mux := http.NewServeMux()
	mux.HandleFunc("/version", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, version.Info{Major: "1", Minor: "30", GitVersion: fakeGitVersion})
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		status, body := f.readyzStatus, f.readyzBody
		f.mu.Unlock()
		w.WriteHeader(status)
		io.WriteString(w, body)
	})
	mux.HandleFunc("/api", func(w http.ResponseWriter, _ *http.Request) {
		if f.discoveryDown(w) {
			return
		}
		writeJSON(w, metav1.APIVersions{Versions: []string{"v1"}})
	})
	mux.HandleFunc("/apis", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		f.groupHits++
		fail, crd := f.failDiscovery, f.serveCRD
		f.mu.Unlock()
		if fail {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		groups := []metav1.APIGroup{{
			Name: "apps",
			Versions: []metav1.GroupVersionForDiscovery{
				{GroupVersion: "apps/v1", Version: "v1"},
				{GroupVersion: "apps/v1beta1", Version: "v1beta1"},
			},
			PreferredVersion: metav1.GroupVersionForDiscovery{GroupVersion: "apps/v1", Version: "v1"},
		}}
		if crd {
			groups = append(groups, metav1.APIGroup{
				Name:             "example.io",
				Versions:         []metav1.GroupVersionForDiscovery{{GroupVersion: "example.io/v1", Version: "v1"}},
				PreferredVersion: metav1.GroupVersionForDiscovery{GroupVersion: "example.io/v1", Version: "v1"},
			})
		}
		writeJSON(w, metav1.APIGroupList{Groups: groups})
	})
	mux.HandleFunc("/api/v1", func(w http.ResponseWriter, _ *http.Request) {
		if f.discoveryDown(w) {
			return
		}
		writeJSON(w, metav1.APIResourceList{GroupVersion: "v1", APIResources: []metav1.APIResource{
			{Name: "pods", SingularName: "pod", Kind: "Pod", Namespaced: true,
				Verbs: metav1.Verbs{"get", "list", "watch", "delete"}, ShortNames: []string{"po"}, Categories: []string{"all"}},
			// A subresource, present to prove discovery skips it.
			{Name: "pods/log", Kind: "Pod", Namespaced: true, Verbs: metav1.Verbs{"get"}},
			{Name: "configmaps", SingularName: "configmap", Kind: "ConfigMap", Namespaced: true,
				Verbs: metav1.Verbs{"get", "list", "watch"}},
			{Name: "namespaces", SingularName: "namespace", Kind: "Namespace", Namespaced: false,
				Verbs: metav1.Verbs{"get", "list", "watch"}},
		}})
	})
	mux.HandleFunc("/apis/apps/v1", func(w http.ResponseWriter, _ *http.Request) {
		if f.discoveryDown(w) {
			return
		}
		writeJSON(w, metav1.APIResourceList{GroupVersion: "apps/v1", APIResources: []metav1.APIResource{
			{Name: "deployments", SingularName: "deployment", Kind: "Deployment", Namespaced: true,
				Verbs: metav1.Verbs{"get", "list", "watch"}, ShortNames: []string{"deploy"}},
		}})
	})
	mux.HandleFunc("/apis/apps/v1beta1", func(w http.ResponseWriter, _ *http.Request) {
		if f.discoveryDown(w) {
			return
		}
		writeJSON(w, metav1.APIResourceList{GroupVersion: "apps/v1beta1", APIResources: []metav1.APIResource{
			{Name: "deployments", SingularName: "deployment", Kind: "Deployment", Namespaced: true,
				Verbs: metav1.Verbs{"get", "list", "watch"}},
		}})
	})
	mux.HandleFunc("/apis/example.io/v1", func(w http.ResponseWriter, _ *http.Request) {
		if f.discoveryDown(w) {
			return
		}
		writeJSON(w, metav1.APIResourceList{GroupVersion: "example.io/v1", APIResources: []metav1.APIResource{
			{Name: "widgets", SingularName: "widget", Kind: "Widget", Namespaced: true,
				Verbs: metav1.Verbs{"get", "list", "watch"}},
		}})
	})

	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAPI) discoveryDown(w http.ResponseWriter) bool {
	f.mu.Lock()
	fail := f.failDiscovery
	f.mu.Unlock()
	if fail {
		http.Error(w, "boom", http.StatusInternalServerError)
	}
	return fail
}

func (f *fakeAPI) setReadyz(status int, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.readyzStatus, f.readyzBody = status, body
}

func (f *fakeAPI) setFailDiscovery(v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failDiscovery = v
}

func (f *fakeAPI) setServeCRD(v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.serveCRD = v
}

func (f *fakeAPI) groupRequests() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.groupHits
}

func writeJSON(w http.ResponseWriter, v any) {
	// A plain application/json content type is what makes client-go fall back
	// from aggregated discovery to the legacy per-group protocol served here.
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// writeKubeconfig writes a kubeconfig pointing at the fake server. It carries
// two contexts so context selection and wildcard expansion can be exercised;
// the second context's server is a dead address that is never dialed.
func writeKubeconfig(t *testing.T, serverURL string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kubeconfig")
	data := `apiVersion: v1
kind: Config
clusters:
- name: fake
  cluster:
    server: ` + serverURL + `
- name: other-cluster
  cluster:
    server: http://127.0.0.1:1
contexts:
- name: fake
  context:
    cluster: fake
    user: fake-user
- name: other
  context:
    cluster: other-cluster
    user: fake-user
current-context: fake
users:
- name: fake-user
  user:
    token: fake-token
`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}
	return path
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func testClusterConfig(kubeconfig string, mode config.AuthMode) config.ClusterConfig {
	return config.ClusterConfig{
		Name:       "test",
		Kubeconfig: kubeconfig,
		AuthMode:   mode,
		QPS:        50,
		Burst:      100,
	}
}

// newTestCluster builds a Cluster against the fake API server and guarantees
// its probe loop is stopped at test end.
func newTestCluster(t *testing.T, f *fakeAPI, mode config.AuthMode) *Cluster {
	t.Helper()
	c, err := New(testClusterConfig(writeKubeconfig(t, f.srv.URL), mode), config.Default(), testLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(c.Close)
	return c
}
