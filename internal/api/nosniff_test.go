package api

// Every response on this surface reproduces strings taken verbatim from
// cluster objects — names, annotations, event messages, ConfigMap keys — and
// every one of them was written by whoever can write to that namespace. One of
// them can be an entire HTML document.
//
// The declared Content-Type says otherwise, and a browser that sniffs past it
// renders that document on the console's own origin, carrying the viewer's
// session, from a plain GET anyone can be sent a link to. X-Content-Type-Options
// is what holds a browser to the declared type. The log stream and the workload
// proxy had it; the JSON and YAML writers, which carry far more object content
// between them, did not.

import (
	"net/http"
	"testing"
)

func TestResponsesRefuseContentSniffing(t *testing.T) {
	rig := hndNewRig(t)

	cases := []struct {
		name        string
		path        string
		contentType string
	}{
		{"json list", "/api/v1/clusters/fake/resources/core/v1/pods", "application/json"},
		{"json object", "/api/v1/clusters/fake/resources/_/v1/pods/demo/web-1", "application/json"},
		{"yaml object", "/api/v1/clusters/fake/resources/_/v1/pods/demo/web-1?format=yaml", "application/yaml"},
		{"plain-text logs", "/api/v1/clusters/fake/pods/demo/web-1/logs", "text/plain"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := rig.get(t, c.path)
			hndWantStatus(t, rec, http.StatusOK)
			if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				t.Errorf("X-Content-Type-Options = %q, want %q (Content-Type %q)",
					got, "nosniff", rec.Header().Get("Content-Type"))
			}
		})
	}
}

// An error body is written by the same helper and reproduces object names and
// API-server messages, so it must carry the header too.
func TestErrorResponsesRefuseContentSniffing(t *testing.T) {
	rig := hndNewRig(t)

	rec := rig.get(t, "/api/v1/clusters/fake/resources/core/v1/nosuchresource")
	if rec.Code == http.StatusOK {
		t.Fatalf("expected a failure for an unknown resource, got 200: %s", rec.Body.String())
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q on an error body, want %q", got, "nosniff")
	}
}
