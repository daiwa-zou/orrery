package cluster

import (
	"errors"
	"strings"
	"testing"

	"github.com/daiwa-zou/orrery/internal/config"
)

// newRegistryForRevival builds a registry with one entry that failed to start,
// without needing an API server: the revival path is decided entirely by
// registry state, and New is what talks to a cluster.
func newRegistryForRevival(t *testing.T, name string) *Registry {
	t.Helper()
	return &Registry{
		appCfg: config.Default(),
		log:    testLogger(),
		entries: map[string]*Entry{
			name: {Name: name, Err: errors.New("kubeconfig not found")},
		},
		order: []string{name},
		stop:  make(chan struct{}),
	}
}

func TestRecordFailureKeepsTheReasonCurrent(t *testing.T) {
	r := newRegistryForRevival(t, "prod")

	// The kubeconfig was fixed; the API server is now the problem.
	r.recordFailure("prod", errors.New("dial tcp: connection refused"))

	_, err := r.Get("prod")
	if err == nil {
		t.Fatal("a broken cluster came back as usable")
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("Get said %q; it is still reporting the failure from startup", err)
	}
}

func TestRecordFailureLeavesARecoveredClusterAlone(t *testing.T) {
	r := newRegistryForRevival(t, "prod")
	if !r.adopt("prod", &Cluster{}) {
		t.Fatal("a broken entry refused a revived cluster")
	}

	// A retry already in flight when the cluster came back must not stamp its
	// stale error over a working entry.
	r.recordFailure("prod", errors.New("dial tcp: connection refused"))

	if _, err := r.Get("prod"); err != nil {
		t.Errorf("Get = %v, want the recovered cluster", err)
	}
}

// New dials an API server, so it runs outside the registry lock, and the
// registry can be closed while it is running. Storing the result then leaves a
// cluster's informers, watches and goroutines running with nothing left that
// would ever shut them down.
func TestAdoptRefusesAClusterBuiltAfterClose(t *testing.T) {
	r := newRegistryForRevival(t, "prod")
	r.Close()

	if r.adopt("prod", &Cluster{}) {
		t.Fatal("a closed registry adopted a cluster, which nothing would ever close")
	}
}

func TestAdoptRefusesAnEntryThatIsAlreadyRunning(t *testing.T) {
	r := newRegistryForRevival(t, "prod")
	if !r.adopt("prod", &Cluster{}) {
		t.Fatal("a broken entry refused a revived cluster")
	}
	if r.adopt("prod", &Cluster{}) {
		t.Error("a second cluster displaced the running one, orphaning it")
	}
	if r.adopt("absent", &Cluster{}) {
		t.Error("a cluster was adopted for an entry that does not exist")
	}
}
