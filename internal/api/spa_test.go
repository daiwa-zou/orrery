package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// A build as the frontend actually ships: one entry document and hashed
// output under the directory Vite owns.
func spaFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":              &fstest.MapFile{Data: []byte("<!doctype html><div id=root>")},
		"assets/index-abc123.js":  &fstest.MapFile{Data: []byte("export const a = 1")},
		"assets/index-abc123.css": &fstest.MapFile{Data: []byte(":root{}")},
		"favicon.svg":             &fstest.MapFile{Data: []byte("<svg/>")},
	}
}

// navigation is what a browser sends when someone opens or refreshes a page.
func navigation(target string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, target, nil)
	r.Header.Set("Sec-Fetch-Dest", "document")
	r.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	return r
}

// subresource is what a document sends when it pulls in a script or a style.
func subresource(target, dest string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, target, nil)
	r.Header.Set("Sec-Fetch-Dest", dest)
	r.Header.Set("Accept", "*/*")
	return r
}

func serveSPA(t *testing.T, r *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	spaHandler(spaFS())(w, r)
	return w
}

func TestSPAServesTheDocumentForClientRoutes(t *testing.T) {
	// The deep links the console actually produces, including the one the
	// related-links tests build: a dotted API group, which is why the shape of
	// the path cannot be used to tell a route from a file.
	for _, target := range []string{
		"/",
		"/c/lens-a/events",
		"/c/lens-a/r/apps/v1/deployments/demo/web",
		"/c/lens-a/r/acme.example/v1/sprocketz/demo/sp-1",
		"/c/lens-a/r/core/v1/nodes/_/node-1.internal.example.com",
	} {
		w := serveSPA(t, navigation(target))
		if w.Code != http.StatusOK {
			t.Errorf("%s: got %d, want the app document", target, w.Code)
		}
		if !strings.Contains(w.Body.String(), "id=root") {
			t.Errorf("%s: did not serve index.html", target)
		}
		if got := w.Header().Get("Cache-Control"); got != "no-cache" {
			t.Errorf("%s: index.html cached as %q; a deploy would strand users on the old bundle", target, got)
		}
	}
}

func TestSPAServesRealFiles(t *testing.T) {
	w := serveSPA(t, subresource("/assets/index-abc123.js", "script"))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "export const a") {
		t.Fatalf("hashed asset not served: %d %q", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Cache-Control"); !strings.Contains(got, "immutable") {
		t.Errorf("hashed asset Cache-Control = %q, want immutable", got)
	}
}

// The bug this guards. A missing subresource used to come back 200 with the
// app document in the body, which is how the deploy window between new HTML
// and old assets reported itself: a syntax error at the first "<" of a
// document the script never asked for, or, for a stylesheet, nothing at all
// beyond an unstyled page.
func TestSPARefusesToAnswerAMissingSubresourceWithTheDocument(t *testing.T) {
	cases := []struct {
		target, dest string
	}{
		{"/assets/index-STALE.js", "script"},
		{"/assets/index-STALE.css", "style"},
		{"/favicon-old.svg", "image"},
		{"/fonts/mono.woff2", "font"},
		{"/api-ish/thing.json", "empty"}, // fetch/XHR
	}
	for _, c := range cases {
		w := serveSPA(t, subresource(c.target, c.dest))
		if w.Code != http.StatusNotFound {
			t.Errorf("%s (%s): got %d, want 404; a subresource must not be answered with HTML",
				c.target, c.dest, w.Code)
		}
		if strings.Contains(w.Body.String(), "id=root") {
			t.Errorf("%s (%s): served the app document to a subresource request", c.target, c.dest)
		}
	}
}

// No client-side route lives under the build's own directory, so a miss there
// is a miss even when a person types it into the address bar.
func TestSPADoesNotFallBackInsideTheAssetDirectory(t *testing.T) {
	w := serveSPA(t, navigation("/assets/index-STALE.js"))
	if w.Code != http.StatusNotFound {
		t.Errorf("navigating to a missing asset returned %d, want 404", w.Code)
	}
}

// Anything that does not say what it wanted keeps getting the app, so `curl /`
// and uptime checks behave as they always have. The stale-bundle case does not
// depend on this: it lives under the asset directory, which is refused above
// without consulting a header at all.
func TestSPAStillServesClientsThatAnnounceNothing(t *testing.T) {
	for _, target := range []string{"/", "/c/lens-a/r/apps/v1/deployments/demo/web"} {
		w := serveSPA(t, httptest.NewRequest(http.MethodGet, target, nil))
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "id=root") {
			t.Errorf("%s: bare request got %d; it used to get the app and still should", target, w.Code)
		}
	}
}

func TestIsSubresource(t *testing.T) {
	cases := []struct {
		dest string
		want bool
	}{
		{"document", false},
		{"iframe", false},
		{"frame", false},
		{"", false}, // said nothing: treated as a navigation
		{"script", true},
		{"style", true},
		{"image", true},
		{"font", true},
		{"empty", true}, // fetch/XHR
	}
	for _, c := range cases {
		t.Run("dest="+c.dest, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/whatever", nil)
			if c.dest != "" {
				r.Header.Set("Sec-Fetch-Dest", c.dest)
			}
			if got := isSubresource(r); got != c.want {
				t.Errorf("isSubresource = %v, want %v", got, c.want)
			}
		})
	}
}

// A build with no index.html cannot serve the app, and saying so is better
// than a panic or an empty 200.
func TestSPAWithoutAnIndex(t *testing.T) {
	w := httptest.NewRecorder()
	spaHandler(fstest.MapFS{})(w, navigation("/c/lens-a/events"))
	if w.Code != http.StatusNotFound {
		t.Errorf("got %d, want 404", w.Code)
	}
}
