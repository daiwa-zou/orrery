package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// A watch's INIT frame is a snapshot, and a snapshot that quietly left a
// namespace out is the one answer a reader takes as reassurance: nothing
// further will ever arrive for the missing rows, so the table stays wrong for
// as long as the socket stays open, and looks like a quiet cluster.
//
// The list endpoint has said which namespaces it dropped, and why, since
// namespaceAccess.warnings was written. The stream computed exactly the same
// scope through the same helper and threw the sentences away.
func dialWatch(t *testing.T, rig *hndRig, path string) *websocket.Conn {
	t.Helper()
	srv := httptest.NewServer(rig.router)
	t.Cleanup(srv.Close)

	url := "ws" + strings.TrimPrefix(srv.URL, "http") + path
	conn, resp, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		status := "no response"
		if resp != nil {
			status = resp.Status
		}
		t.Fatalf("dialing %s: %v (%s)", path, err, status)
	}
	t.Cleanup(func() { conn.Close() })
	if err := conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	return conn
}

type watchFrame struct {
	Type     string           `json:"type"`
	Items    []map[string]any `json:"items"`
	Warnings []string         `json:"warnings"`
}

func readFrame(t *testing.T, conn *websocket.Conn) watchFrame {
	t.Helper()
	var f watchFrame
	if err := conn.ReadJSON(&f); err != nil {
		t.Fatalf("reading frame: %v", err)
	}
	return f
}

// Asking for two namespaces and being allowed one is a narrower stream, not a
// refused one — and the narrowing has to be said out loud.
func TestWatchInitNamesTheNamespaceItWasDenied(t *testing.T) {
	rig := hndNewRig(t)
	rig.fake.denyNamespace = "kube-system"

	conn := dialWatch(t, rig,
		"/api/v1/clusters/fake/ws/watch/_/v1/pods?namespace=demo&namespace=kube-system")
	f := readFrame(t, conn)

	if f.Type != "INIT" {
		t.Fatalf("first frame = %q, want INIT", f.Type)
	}
	if len(f.Warnings) == 0 {
		t.Fatal("INIT carried no warnings for a stream that dropped a namespace")
	}
	joined := strings.Join(f.Warnings, " ")
	if !strings.Contains(joined, "kube-system") {
		t.Errorf("warnings = %q, want the dropped namespace named", joined)
	}
}

// A denial and a review that could not be performed are different facts, and
// only one of them is worth taking to whoever administers your RBAC. The
// stream has to keep them apart the way the list does.
func TestWatchInitSaysWhenAccessCouldNotBeChecked(t *testing.T) {
	rig := hndNewRig(t)
	rig.fake.failReviewNamespace = "kube-system"

	conn := dialWatch(t, rig,
		"/api/v1/clusters/fake/ws/watch/_/v1/pods?namespace=demo&namespace=kube-system")
	f := readFrame(t, conn)

	if f.Type != "INIT" {
		t.Fatalf("first frame = %q, want INIT", f.Type)
	}
	joined := strings.Join(f.Warnings, " ")
	if !strings.Contains(joined, "not a permission problem") {
		t.Errorf("warnings = %q, want the sentence that says this is not RBAC", joined)
	}
}

// A complete answer says nothing, so a client that renders warnings does not
// grow a permanent banner.
func TestWatchInitIsSilentWhenNothingWasDropped(t *testing.T) {
	rig := hndNewRig(t)

	conn := dialWatch(t, rig, "/api/v1/clusters/fake/ws/watch/_/v1/pods?namespace=demo")
	f := readFrame(t, conn)

	if f.Type != "INIT" {
		t.Fatalf("first frame = %q, want INIT", f.Type)
	}
	if len(f.Warnings) != 0 {
		t.Errorf("warnings = %q on a complete answer", f.Warnings)
	}
}

// sameAs is what the re-authorization tick asks before adopting a new scope,
// because a scope that has changed cannot be repaired by adopting a filter.
// Narrower leaves rows on the page that will never be retracted; wider means
// the next edit to a newly-visible object arrives as MODIFIED for a row the
// client has never seen. Both need a reload, which is what OVERFLOW asks for.
func TestWatchVisibilitySameAs(t *testing.T) {
	ns := func(names ...string) watchVisibility {
		v := watchVisibility{namespaced: true, namespaces: map[string]struct{}{}}
		for _, n := range names {
			v.namespaces[n] = struct{}{}
		}
		return v
	}

	cases := []struct {
		name string
		a, b watchVisibility
		want bool
	}{
		{"identical", ns("demo", "payments"), ns("demo", "payments"), true},
		{"order is not identity", ns("demo", "payments"), ns("payments", "demo"), true},
		{"narrowed", ns("demo", "payments"), ns("demo"), false},
		{"widened", ns("demo"), ns("demo", "payments"), false},
		{"swapped", ns("demo"), ns("payments"), false},
		{
			"cluster-wide is not the same as every namespace named",
			watchVisibility{namespaced: true, all: true}, ns("demo"), false,
		},
	}
	for _, c := range cases {
		if got := c.a.sameAs(c.b); got != c.want {
			t.Errorf("%s: sameAs = %v, want %v", c.name, got, c.want)
		}
	}
}

// The pre-upgrade refusals must keep working: nothing was allowed is still a
// plain HTTP error rather than a socket that opens and immediately closes.
func TestWatchStillRefusesBeforeUpgradingWhenNothingIsAllowed(t *testing.T) {
	rig := hndNewRig(t)
	rig.fake.denyNamespace = "demo"

	rec := rig.get(t, "/api/v1/clusters/fake/ws/watch/_/v1/pods?namespace=demo")
	if rec.Code == http.StatusOK {
		t.Fatalf("status = %d, want a refusal before the upgrade", rec.Code)
	}
}
