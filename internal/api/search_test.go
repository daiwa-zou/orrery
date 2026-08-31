package api

import (
	"net/http"
	"strings"
	"testing"
)

func search(t *testing.T, rig *hndRig, query string) searchResponse {
	t.Helper()
	rec := rig.get(t, "/api/v1/search?"+query)
	hndWantStatus(t, rec, http.StatusOK)
	var body searchResponse
	hndDecode(t, rec, &body)
	return body
}

func (sr searchResponse) names() []string {
	out := make([]string, 0, len(sr.Hits))
	for _, h := range sr.Hits {
		out = append(out, h.Kind+"/"+h.Name)
	}
	return out
}

func (sr searchResponse) hit(kind, name string) (searchHit, bool) {
	for _, h := range sr.Hits {
		if h.Kind == kind && h.Name == name {
			return h, true
		}
	}
	return searchHit{}, false
}

func TestSearchFindsObjectsByName(t *testing.T) {
	rig := hndNewRig(t)
	got := search(t, rig, "q=web")

	dep, ok := got.hit("Deployment", "web")
	if !ok {
		t.Fatalf("the deployment is missing: %v", got.names())
	}
	if !got.has("Pod", "web-1") || !got.has("Pod", "web-2") {
		t.Errorf("pods are missing: %v", got.names())
	}

	// An exact name match is what the caller meant; anything else is noise
	// that happens to contain the string.
	if got.Hits[0].Name != "web" || got.Hits[0].Kind != "Deployment" {
		t.Errorf("first hit = %s/%s, want the exact match first",
			got.Hits[0].Kind, got.Hits[0].Name)
	}
	if dep.Score != 100 {
		t.Errorf("exact match scored %d", dep.Score)
	}
	pod, _ := got.hit("Pod", "web-1")
	if pod.Score >= dep.Score {
		t.Errorf("a prefix match (%d) outranked the exact one (%d)", pod.Score, dep.Score)
	}

	if dep.Cluster != "fake" {
		t.Errorf("cluster = %q", dep.Cluster)
	}
	if want := "/api/v1/clusters/fake/resources/apps/v1/deployments/demo/web"; dep.Path != want {
		t.Errorf("path = %q, want %q", dep.Path, want)
	}
	if dep.Status == "" {
		t.Error("the deployment came back with no status")
	}
	if len(got.Scanned) == 0 {
		t.Error("nothing reported as scanned; a caller cannot tell where it looked")
	}
}

func (sr searchResponse) has(kind, name string) bool {
	_, ok := sr.hit(kind, name)
	return ok
}

func TestSearchNarrowsToNamedResources(t *testing.T) {
	rig := hndNewRig(t)
	got := search(t, rig, "q=web&resource=pods")

	if len(got.Hits) != 2 {
		t.Fatalf("hits = %v, want the two pods", got.names())
	}
	for _, h := range got.Hits {
		if h.Kind != "Pod" {
			t.Errorf("%s/%s slipped past resource=pods", h.Kind, h.Name)
		}
	}
	if len(got.Scanned) != 1 || got.Scanned[0] != "pods" {
		t.Errorf("scanned = %v, want only pods", got.Scanned)
	}

	// The same three spellings the neighbourhood endpoint takes.
	for _, spelling := range []string{"v1/pods", "core/v1/pods", "Pod"} {
		alt := search(t, rig, "q=web&resource="+spelling)
		if len(alt.Hits) != 2 {
			t.Errorf("resource=%s returned %v", spelling, alt.names())
		}
	}
}

func TestSearchNarrowsByClusterAndNamespace(t *testing.T) {
	rig := hndNewRig(t)

	if got := search(t, rig, "q=web&cluster=fake"); len(got.Hits) == 0 {
		t.Error("naming the only cluster excluded it")
	}
	// A cluster nobody configured is not an error; it is an empty answer —
	// carrying a warning that says so, which
	// TestSearchNamesAClusterItDoesNotHave covers.
	other := search(t, rig, "q=web&cluster=elsewhere")
	if len(other.Hits) != 0 {
		t.Errorf("hits from an unconfigured cluster: %v", other.names())
	}

	scoped := search(t, rig, "q=web&resource=pods&namespace=demo")
	if len(scoped.Hits) != 2 {
		t.Errorf("namespace=demo returned %v", scoped.names())
	}
	empty := search(t, rig, "q=web&resource=pods&namespace=kube-system")
	if len(empty.Hits) != 0 {
		t.Errorf("kube-system holds no web pods, got %v", empty.names())
	}
}

func TestSearchTruncatesLoudly(t *testing.T) {
	rig := hndNewRig(t)
	got := search(t, rig, "q=web&limit=1")

	if len(got.Hits) != 1 {
		t.Fatalf("limit=1 returned %d hits", len(got.Hits))
	}
	if !got.Truncated {
		t.Error("the answer was cut without saying so")
	}
	// Total counts what was found, not what was returned, so a caller knows
	// to narrow rather than believing it saw everything.
	if got.Total < 3 {
		t.Errorf("total = %d, want the full count of matches", got.Total)
	}
}

// A resource the caller may not list is a hole in the answer, and a search
// that quietly skipped it would report "not found" for something that is there.
func TestSearchReportsForbiddenScans(t *testing.T) {
	rig := hndNewRig(t)
	rig.fake.denyResource = "pods"

	got := search(t, rig, "q=web")
	if got.has("Pod", "web-1") {
		t.Error("a denied resource was still searched")
	}
	if !got.has("Deployment", "web") {
		t.Errorf("one denial sank the whole search: %v", got.names())
	}
	joined := strings.Join(got.Warnings, "; ")
	if !strings.Contains(joined, "pods") {
		t.Errorf("warnings = %q, want them to name pods", joined)
	}
}

func TestSearchMatchesLabelsAndNamespaces(t *testing.T) {
	rig := hndNewRig(t)

	// The label form the list endpoint's free text also accepts.
	byLabel := search(t, rig, "q=app=web&resource=pods")
	if !byLabel.has("Pod", "web-1") {
		t.Errorf("app=web found %v", byLabel.names())
	}
	for _, h := range byLabel.Hits {
		if h.Score != 10 {
			t.Errorf("%s matched only on a label but scored %d", h.Name, h.Score)
		}
	}

	byNamespace := search(t, rig, "q=kube-system")
	ns, ok := byNamespace.hit("Namespace", "kube-system")
	if !ok {
		t.Fatalf("the namespace itself is missing: %v", byNamespace.names())
	}
	if ns.Score != 100 {
		t.Errorf("the namespace's own name scored %d, want an exact match", ns.Score)
	}
}

func TestSearchRejections(t *testing.T) {
	rig := hndNewRig(t)
	cases := []struct {
		name, query string
		want        int
	}{
		{"no query", "", http.StatusBadRequest},
		{"blank query", "q=%20%20", http.StatusBadRequest},
		{"malformed resource", "q=web&resource=a/b/c/d", http.StatusBadRequest},
		{
			"too many resources",
			"q=web&resource=a,b,c,d,e,f,g,h,i,j,k,l,m",
			http.StatusBadRequest,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hndWantStatus(t, rig.get(t, "/api/v1/search?"+tc.query), tc.want)
		})
	}
}

func TestSearchTargets(t *testing.T) {
	got, err := searchTargets(nil)
	if err != nil || len(got) != len(defaultSearchResources) {
		t.Errorf("no resources = %v, %v; want the default set", got, err)
	}

	got, err = searchTargets([]string{"pods,apps/v1/deployments", "v1/services"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %v, want three targets", got)
	}
	if got[1].Group != "apps" || got[1].Resource != "deployments" {
		t.Errorf("second target = %+v", got[1])
	}
	if got[2].Version != "v1" || got[2].Resource != "services" {
		t.Errorf("third target = %+v", got[2])
	}

	if _, err := searchTargets([]string{"a,b,c,d,e,f,g,h,i,j,k,l,m"}); err == nil {
		t.Error("thirteen resources were accepted")
	}
}
