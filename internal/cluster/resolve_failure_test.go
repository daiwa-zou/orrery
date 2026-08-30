package cluster

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// Resolve refreshes discovery once when a name misses, so a CRD installed by
// anything other than this dashboard does not stay invisible until the TTL
// expires. When that refresh fails, nothing has been established about the
// name — and the old code answered UnknownResourceError, which says "this
// cluster does not serve X" and reaches the browser as a 404.
//
// It would be saying that on the strength of a cache already known to be
// stale, which is the reason the refresh was attempted, plus a lookup that
// never happened. Whoever just ran kubectl apply then spends the outage
// debugging a CRD they installed perfectly well — the several confusing
// minutes this whole path exists to prevent.

func TestResolveDoesNotDeclareAResourceAbsentWhenDiscoveryIsDown(t *testing.T) {
	f := newFakeAPI(t)
	d := newTestDiscovery(t, f, time.Minute)

	// Warm the cache while the API server is answering.
	if _, err := d.Resources(context.Background()); err != nil {
		t.Fatal(err)
	}

	f.setFailDiscovery(true)

	_, err := d.Resolve(context.Background(), "example.com", "v1", "gizmos")
	if err == nil {
		t.Fatal("a name that is not in the cache resolved anyway")
	}

	var unknown *UnknownResourceError
	if errors.As(err, &unknown) {
		t.Fatalf("an unreachable discovery was reported as a definite absence: %v", err)
	}
	if !strings.Contains(err.Error(), "gizmos") {
		t.Errorf("error = %q, want it to name what could not be confirmed", err)
	}
}

// The ordinary miss is unchanged: discovery answered, the name is not there,
// and saying so is the correct 404.
func TestResolveStillReportsAGenuineAbsence(t *testing.T) {
	f := newFakeAPI(t)
	d := newTestDiscovery(t, f, time.Minute)

	_, err := d.Resolve(context.Background(), "example.com", "v1", "gizmos")

	var unknown *UnknownResourceError
	if !errors.As(err, &unknown) {
		t.Fatalf("a resource this cluster genuinely does not serve gave %v", err)
	}
}
