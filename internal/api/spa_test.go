package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/daiwa-zou/orrery/internal/webfs"
)

// spaHandler is generic over fs.FS so the embedded release bundle and the
// on-disk webRoot share one code path; this exercises it over an in-memory FS
// the way the bundle build uses it.
func TestSPAHandlerOverFS(t *testing.T) {
	fsys := fstest.MapFS{
		"index.html":    {Data: []byte("<html>spa-index</html>")},
		"assets/app.js": {Data: []byte("console.log(1)")},
	}
	h := spaHandler(fsys)

	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}

	rec := get("/assets/app.js")
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Errorf("asset: code %d, cache %q", rec.Code, rec.Header().Get("Cache-Control"))
	}

	// Deep links fall back to index.html, uncached.
	for _, path := range []string{"/", "/c/prod/r/core/v1/pods", "/missing.txt"} {
		rec = get(path)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "spa-index") {
			t.Errorf("%s: code %d body %q, want the SPA index", path, rec.Code, rec.Body.String())
		}
		if rec.Header().Get("Cache-Control") != "no-cache" {
			t.Errorf("%s: fallback must be no-cache", path)
		}
	}

	// fs.FS path rules make traversal unrepresentable; the attempt just
	// falls back to the index like any other unknown path.
	rec = get("/../secret")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "spa-index") {
		t.Errorf("traversal attempt: code %d", rec.Code)
	}
}

func TestWebFSAbsentWithoutBundleTag(t *testing.T) {
	// This test file builds without the bundleweb tag, so no bundle may be
	// present — the router must fall through to webRoot-or-nothing.
	if webfs.FS() != nil {
		t.Error("webfs.FS() should be nil without the bundleweb build tag")
	}
}
