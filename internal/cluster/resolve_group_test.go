package cluster

// Naming a group means naming one.
//
// Resolve falls back to an unqualified alias table that answers "which group
// serves a thing called this?", and it used to consult that table even when the
// caller had said which group they meant. So Resolve("example.io", "v1",
// "deployments") came back apps/v1 deployments: a real object, served
// successfully, from a group nobody asked about. The route is
// /resources/{group}/{version}/{resource} and the caller spelled all three.
//
// The forgiving cases this fallback exists for are still forgiving. An
// unqualified ask picks the preferred version, the placeholders still mean
// "unqualified", and a stale *version* under the right group still resolves —
// that one is handled by the group-qualified alias, which is a different line.

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestResolveWillNotCrossGroups(t *testing.T) {
	d := newTestDiscovery(t, newFakeAPI(t), time.Minute)
	ctx := context.Background()

	cases := []struct {
		name                     string
		group, version, resource string
	}{
		// apps/v1 serves deployments; example.io does not.
		{"named group does not serve it", "example.io", "v1", "deployments"},
		// Still a miss with no version: the group was named either way.
		{"named group, no version", "example.io", "", "deployments"},
		// The core group does not serve deployments either, and "core" is an
		// explicit spelling of it rather than an absence of one.
		{"core does not serve it", "apps", "v1", "pods"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ar, err := d.Resolve(ctx, c.group, c.version, c.resource)
			if err == nil {
				t.Fatalf("resolved to %s/%s %s; a group that does not serve the "+
					"resource is a miss, not a redirect", ar.Group, ar.Version, ar.Name)
			}
			var unknown *UnknownResourceError
			if !errors.As(err, &unknown) {
				t.Errorf("err = %v, want UnknownResourceError", err)
			}
		})
	}
}

// The leniency that was actually wanted is untouched.
func TestResolveStaysForgivingWhereItShould(t *testing.T) {
	d := newTestDiscovery(t, newFakeAPI(t), time.Minute)
	ctx := context.Background()

	cases := []struct {
		name                     string
		group, version, resource string
		wantGroup, wantVersion   string
	}{
		// No group given: the unqualified table is exactly the right answer.
		{"unqualified plural", "", "", "deployments", "apps", "v1"},
		{"unqualified short name", "", "", "deploy", "apps", "v1"},
		// A stale bookmark carrying a version that has been removed still
		// resolves, because the group it names does serve the resource.
		{"stale version under the right group", "apps", "v9", "deployments", "apps", "v1"},
		// The core placeholders mean "unqualified", as NormalizeGroup defines.
		{"core placeholder", "core", "", "pods", "", "v1"},
		{"underscore placeholder", "_", "", "pods", "", "v1"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ar, err := d.Resolve(ctx, c.group, c.version, c.resource)
			if err != nil {
				t.Fatalf("Resolve(%q,%q,%q): %v", c.group, c.version, c.resource, err)
			}
			if ar.Group != c.wantGroup || ar.Version != c.wantVersion {
				t.Errorf("resolved to %s/%s, want %s/%s",
					ar.Group, ar.Version, c.wantGroup, c.wantVersion)
			}
		})
	}
}
