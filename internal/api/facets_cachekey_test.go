package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// facetLabelKeys reads the label vocabulary out of a facets response.
func facetLabelKeys(t *testing.T, rec *httptest.ResponseRecorder) map[string]bool {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("facets = %d: %s", rec.Code, rec.Body.String())
	}
	var body facetsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	keys := map[string]bool{}
	for _, f := range body.Labels {
		keys[f.Key] = true
	}
	return keys
}

// The facets cache key folds in the active search, because a vocabulary
// harvested under one filter is the wrong answer under another — the file
// says so where searchKey is defined, and the dropdown "confidently offering
// keys harvested from somebody else's filter" is the failure it names.
//
// It folded q, labelSelector and fieldSelector. It did not fold `where`, and
// listFacets narrows by `where` like every other read does, so two `where`
// clauses over the same resource and scope collided on one entry: whichever
// ran first within the TTL answered for both.
func TestFacetsDoNotShareAVocabularyAcrossWhereClauses(t *testing.T) {
	rig := hndNewRig(t)
	const path = "/api/v1/clusters/fake/resources/_/v1/pods/facets"

	// Only the finished pod, which carries no labels at all.
	succeeded := facetLabelKeys(t, rig.get(t, path+"?where=status%3D~Succeeded"))
	if succeeded["app"] {
		t.Fatalf("the finished pod has no labels, yet the vocabulary offered %v", succeeded)
	}

	// The running pods, which are labelled app=web. Served from the entry
	// above if `where` is missing from the key.
	running := facetLabelKeys(t, rig.get(t, path+"?where=status%3D~Running"))
	if !running["app"] {
		t.Errorf("vocabulary = %v, want the running pods' app label — "+
			"a different where clause was answered from another one's cache entry", running)
	}
}

// searchKey is what the entry above turns on, so state the requirement
// directly too: any parameter listFacets narrows by has to change the key.
func TestSearchKeySeparatesWhereClauses(t *testing.T) {
	key := func(query string) string {
		return searchKey(httptest.NewRequest(http.MethodGet, "/x?"+query, nil))
	}
	if a, b := key("where=restarts%3E5"), key("where=restarts%3E0"); a == b {
		t.Errorf("two where clauses share a key: %q", a)
	}
	if a, b := key("where=restarts%3E5"), key(""); a == b {
		t.Errorf("a where clause shares a key with no filter at all: %q", a)
	}
	// Repeated terms are part of the filter and so part of the key.
	if a, b := key("where=a%3D~x"), key("where=a%3D~x&where=b%3D~y"); a == b {
		t.Errorf("a second where term did not change the key: %q", a)
	}
}
