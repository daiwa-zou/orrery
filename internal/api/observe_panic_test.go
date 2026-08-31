package api

// A panicking request is the one an operator most wants to find in
// orrery_http_requests_total, and it was the one status the counter never
// carried.
//
// chi wraps middleware in registration order, so registering the recoverer
// before the observer put the recovery *outside* the metrics: the panic unwound
// past observe's recording lines, and the 500 the recoverer wrote afterwards
// went to a ResponseWriter observe had stopped watching. Nothing was counted at
// all — not the request, not its latency — so a handler panicking on every call
// left a graph that looked idle rather than broken.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// requestCount reads one series of orrery_http_requests_total.
//
// Read off the default gatherer rather than through prometheus/testutil: that
// package pulls kylelemons/godebug into the module graph for a diff helper
// nothing here needs, and a new dependency is a poor price for one assertion.
// The metric family's own accessors do the job, and naming no type from
// client_model keeps that one indirect too.
func requestCount(t *testing.T, route, method, status string) float64 {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, f := range families {
		if f.GetName() != "orrery_http_requests_total" {
			continue
		}
		for _, m := range f.GetMetric() {
			got := map[string]string{}
			for _, l := range m.GetLabel() {
				got[l.GetName()] = l.GetValue()
			}
			if got["route"] == route && got["method"] == method && got["status"] == status {
				return m.GetCounter().GetValue()
			}
		}
	}
	// Absent is zero: the series is created on first observation.
	return 0
}

// chain wraps h in the real middleware stack, outermost first, so these tests
// exercise the order Router installs rather than an order they chose. Composing
// observe and recoverer by hand would pass just as happily with the two
// registered the wrong way round, which is the mistake being pinned.
func chain(a *API, h http.Handler) http.Handler {
	mws := a.middlewares()
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

func TestAPanickingRequestIsCountedAsA500(t *testing.T) {
	rig := hndNewRig(t)

	panicking := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	})
	handler := chain(rig.api, panicking)

	// No chi route context, so observe labels it "unmatched".
	before := requestCount(t, "unmatched", http.MethodGet, "500")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/boom", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body %s", rec.Code, rec.Body.String())
	}
	if got := requestCount(t, "unmatched", http.MethodGet, "500"); got != before+1 {
		t.Errorf("500 count went %v -> %v, want +1; a panicking request must be "+
			"counted rather than lost with the panic", before, got)
	}
}

// An aborted connection has no reply and so no status, and must not be
// invented as a 200. It travels through both middlewares.
func TestAnAbortedRequestIsNotCountedAsSuccess(t *testing.T) {
	rig := hndNewRig(t)

	aborting := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	})
	handler := chain(rig.api, aborting)

	before := requestCount(t, "unmatched", http.MethodGet, "200")

	func() {
		defer func() {
			if rec := recover(); rec != http.ErrAbortHandler {
				t.Errorf("recovered %v, want ErrAbortHandler to reach the server", rec)
			}
		}()
		handler.ServeHTTP(httptest.NewRecorder(),
			httptest.NewRequest(http.MethodGet, "/api/v1/abort", nil))
	}()

	if got := requestCount(t, "unmatched", http.MethodGet, "200"); got != before {
		t.Errorf("200 count went %v -> %v; a dropped connection was recorded "+
			"as a successful reply", before, got)
	}
}

// The ordinary path still records the status it actually served, labelled by
// the chi pattern rather than the concrete path.
func TestAnOrdinaryRequestIsCountedWithItsStatus(t *testing.T) {
	rig := hndNewRig(t)

	const route = "/api/v1/healthz"
	before := requestCount(t, route, http.MethodGet, "200")

	rec := rig.get(t, route)
	hndWantStatus(t, rec, http.StatusOK)

	if got := requestCount(t, route, http.MethodGet, "200"); got != before+1 {
		t.Errorf("200 count for %s went %v -> %v, want +1", route, before, got)
	}
	if !strings.Contains(rec.Body.String(), "ok") {
		t.Errorf("healthz body = %q", rec.Body.String())
	}
}
