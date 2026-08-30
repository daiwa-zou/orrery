package cluster

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func newTestDiscovery(t *testing.T, f *fakeAPI, ttl time.Duration) *DiscoveryCache {
	t.Helper()
	cs, err := kubernetes.NewForConfig(&rest.Config{Host: f.srv.URL})
	if err != nil {
		t.Fatalf("clientset: %v", err)
	}
	return NewDiscoveryCache(cs.Discovery(), ttl)
}

// expireDiscovery ages the cache in place instead of sleeping through a TTL.
func expireDiscovery(d *DiscoveryCache) {
	d.mu.Lock()
	d.fetchedAt = time.Now().Add(-time.Hour)
	d.mu.Unlock()
}

func TestAPIResourceGVRAndSupports(t *testing.T) {
	ar := APIResource{Group: "apps", Version: "v1", Name: "deployments", Verbs: []string{"get", "list"}}
	want := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	if ar.GVR() != want {
		t.Errorf("GVR() = %v, want %v", ar.GVR(), want)
	}
	if !ar.Supports("list") || ar.Supports("delete") {
		t.Error("Supports must reflect the advertised verb list exactly")
	}
}

func TestNormalizeGroup(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"core", ""}, {"_", ""}, {"-", ""}, {"", ""}, {"apps", "apps"}, {"example.io", "example.io"},
	} {
		if got := NormalizeGroup(tc.in); got != tc.want {
			t.Errorf("NormalizeGroup(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestDiscoveryResources(t *testing.T) {
	d := newTestDiscovery(t, newFakeAPI(t), time.Minute)
	res, err := d.Resources(context.Background())
	if err != nil {
		t.Fatalf("Resources: %v", err)
	}

	byName := map[string]APIResource{}
	for _, r := range res {
		byName[r.Group+"/"+r.Version+"/"+r.Name] = r
		if strings.Contains(r.Name, "/") {
			t.Errorf("subresource %q leaked into the resource list", r.Name)
		}
	}

	pods, ok := byName["/v1/pods"]
	if !ok {
		t.Fatalf("pods missing from %v", res)
	}
	if !pods.Namespaced || !pods.Preferred || len(pods.ShortNames) != 1 || pods.ShortNames[0] != "po" {
		t.Errorf("pods = %+v", pods)
	}
	if dep := byName["apps/v1/deployments"]; !dep.Preferred {
		t.Errorf("apps/v1 deployments should be the preferred version: %+v", dep)
	}
	if dep := byName["apps/v1beta1/deployments"]; dep.Preferred {
		t.Errorf("apps/v1beta1 deployments must not be preferred: %+v", dep)
	}

	if !sort.SliceIsSorted(res, func(i, j int) bool {
		if res[i].Group != res[j].Group {
			return res[i].Group < res[j].Group
		}
		if res[i].Name != res[j].Name {
			return res[i].Name < res[j].Name
		}
		return res[i].Version < res[j].Version
	}) {
		t.Error("resource list is not sorted by group/name/version")
	}
}

func TestDiscoveryResolveSpellings(t *testing.T) {
	d := newTestDiscovery(t, newFakeAPI(t), time.Minute)
	ctx := context.Background()

	tests := []struct {
		name                     string
		group, version, resource string
		wantGroup, wantVersion   string
		wantResource             string
	}{
		{"exact core GVR", "", "v1", "pods", "", "v1", "pods"},
		{"core placeholder group", "core", "", "pods", "", "v1", "pods"},
		{"underscore version placeholder", "core", "_", "pods", "", "v1", "pods"},
		{"short name", "", "", "po", "", "v1", "pods"},
		{"singular name", "", "", "pod", "", "v1", "pods"},
		{"lowercase kind", "", "", "configmap", "", "v1", "configmaps"},
		{"group short name", "apps", "", "deploy", "apps", "v1", "deployments"},
		{"unqualified plural picks preferred", "", "", "deployments", "apps", "v1", "deployments"},
		{"explicit non-preferred version", "apps", "v1beta1", "deployments", "apps", "v1beta1", "deployments"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ar, err := d.Resolve(ctx, tc.group, tc.version, tc.resource)
			if err != nil {
				t.Fatalf("Resolve(%q,%q,%q): %v", tc.group, tc.version, tc.resource, err)
			}
			if ar.Group != tc.wantGroup || ar.Version != tc.wantVersion || ar.Name != tc.wantResource {
				t.Errorf("resolved to %s/%s %s, want %s/%s %s",
					ar.Group, ar.Version, ar.Name, tc.wantGroup, tc.wantVersion, tc.wantResource)
			}
		})
	}
}

func TestDiscoveryResolveUnknown(t *testing.T) {
	d := newTestDiscovery(t, newFakeAPI(t), time.Minute)
	_, err := d.Resolve(context.Background(), "example.io", "v9", "gizmos")
	var unknown *UnknownResourceError
	if !errors.As(err, &unknown) {
		t.Fatalf("err = %v, want UnknownResourceError", err)
	}
	if unknown.Resource != "gizmos" {
		t.Errorf("error carries %+v", unknown)
	}
}

func TestUnknownResourceErrorMessage(t *testing.T) {
	for _, tc := range []struct {
		err  UnknownResourceError
		want []string
	}{
		// The empty group must read as "core", not vanish from the message.
		{UnknownResourceError{Resource: "pods"}, []string{`"pods"`, "core"}},
		{UnknownResourceError{Group: "apps", Version: "v1", Resource: "x"}, []string{`"x"`, "apps/v1"}},
	} {
		got := tc.err.Error()
		for _, w := range tc.want {
			if !strings.Contains(got, w) {
				t.Errorf("Error() = %q, want it to contain %q", got, w)
			}
		}
	}
}

func TestDiscoveryCachesWithinTTL(t *testing.T) {
	f := newFakeAPI(t)
	d := newTestDiscovery(t, f, time.Minute)
	ctx := context.Background()

	if _, err := d.Resources(ctx); err != nil {
		t.Fatalf("first Resources: %v", err)
	}
	first := f.groupRequests()
	if _, err := d.Resources(ctx); err != nil {
		t.Fatalf("second Resources: %v", err)
	}
	if got := f.groupRequests(); got != first {
		t.Errorf("a fresh cache still hit the server (%d -> %d requests)", first, got)
	}

	expireDiscovery(d)
	if _, err := d.Resources(ctx); err != nil {
		t.Fatalf("post-expiry Resources: %v", err)
	}
	if got := f.groupRequests(); got != first+1 {
		t.Errorf("an expired cache should refresh exactly once, requests %d -> %d", first, got)
	}
}

func TestDiscoveryInvalidateForcesRefresh(t *testing.T) {
	f := newFakeAPI(t)
	d := newTestDiscovery(t, f, time.Hour)
	ctx := context.Background()

	if _, err := d.Resources(ctx); err != nil {
		t.Fatalf("Resources: %v", err)
	}
	before := f.groupRequests()
	d.Invalidate()
	if _, err := d.Resources(ctx); err != nil {
		t.Fatalf("Resources after Invalidate: %v", err)
	}
	if got := f.groupRequests(); got != before+1 {
		t.Errorf("Invalidate should force one refresh, requests %d -> %d", before, got)
	}
}

func TestDiscoveryServesStaleOnFailure(t *testing.T) {
	f := newFakeAPI(t)
	d := newTestDiscovery(t, f, time.Minute)
	ctx := context.Background()

	res, err := d.Resources(ctx)
	if err != nil {
		t.Fatalf("Resources: %v", err)
	}

	f.setFailDiscovery(true)
	expireDiscovery(d)
	stale, err := d.Resources(ctx)
	if err != nil {
		t.Fatalf("a failed refresh must serve stale data, got error %v", err)
	}
	if len(stale) != len(res) {
		t.Errorf("stale list has %d resources, want the original %d", len(stale), len(res))
	}
}

func TestDiscoveryFailsWithNothingCached(t *testing.T) {
	f := newFakeAPI(t)
	f.setFailDiscovery(true)
	d := newTestDiscovery(t, f, time.Minute)
	if _, err := d.Resources(context.Background()); err == nil {
		t.Fatal("with no cache and a dead server there is nothing to serve; want an error")
	}
}

func TestDiscoveryRefreshOnMiss(t *testing.T) {
	f := newFakeAPI(t)
	d := newTestDiscovery(t, f, time.Hour)
	ctx := context.Background()

	// First miss consumes the rate-limited refresh slot and still fails: the
	// CRD is not being served yet.
	if _, err := d.Resolve(ctx, "example.io", "", "widgets"); err == nil {
		t.Fatal("widgets should be unknown before the CRD exists")
	}

	// The CRD appears, but the previous miss claimed the refresh slot within
	// its rate-limit window, so a stale bookmark cannot hot-loop discovery.
	f.setServeCRD(true)
	before := f.groupRequests()
	if _, err := d.Resolve(ctx, "example.io", "", "widgets"); err == nil {
		t.Fatal("a rate-limited miss must not refresh again")
	}
	if got := f.groupRequests(); got != before {
		t.Errorf("rate-limited miss still refreshed (%d -> %d requests)", before, got)
	}

	// Once the window passes (aged in place), the miss triggers one refresh
	// and the just-installed CRD resolves.
	d.mu.Lock()
	d.missRefreshAt = time.Time{}
	d.mu.Unlock()
	ar, err := d.Resolve(ctx, "example.io", "", "widgets")
	if err != nil {
		t.Fatalf("Resolve after refresh window: %v", err)
	}
	if ar.Group != "example.io" || ar.Name != "widgets" {
		t.Errorf("resolved %+v", ar)
	}
}

func TestNewDiscoveryCacheDefaultTTL(t *testing.T) {
	if d := NewDiscoveryCache(nil, 0); d.ttl != 5*time.Minute {
		t.Errorf("ttl = %s, want the 5m default for a zero TTL", d.ttl)
	}
}

// A miss whose refresh fails is covered in resolve_failure_test.go. This test
// used to live here asserting the opposite — that the answer should still be a
// clean unknown-resource error — and a clean 404 is exactly what misleads: it
// says the cluster does not serve the resource, on the strength of a cache
// already known to be stale plus a lookup that never happened.

func TestDiscoveryServerVersion(t *testing.T) {
	f := newFakeAPI(t)
	d := newTestDiscovery(t, f, time.Minute)
	v, err := d.ServerVersion()
	if err != nil {
		t.Fatalf("ServerVersion: %v", err)
	}
	if v != fakeGitVersion {
		t.Errorf("version = %q, want %q", v, fakeGitVersion)
	}

	f.srv.Close()
	if _, err := d.ServerVersion(); err == nil {
		t.Error("a dead server must surface a version error")
	}
}
