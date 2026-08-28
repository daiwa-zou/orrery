package api

import "testing"

// tableFor caches what it resolved for a resource, and used to cache "I could
// not read the CustomResourceDefinition" as though it were "this resource has
// no printer columns". Both arrived as an empty columnSet; only one of them is
// an answer.
//
// The cost is quiet. The reader gets the generic name/age table, which is
// exactly what a CRD that never defined printer columns produces, so there is
// nothing on screen to suggest anything went wrong — and it stays that way for
// the cache's whole TTL, long after the informer recovered.

func TestTableForDoesNotCacheACRDItCouldNotRead(t *testing.T) {
	rig := hndNewRig(t)
	rig.fake.breakCacheResource = "customresourcedefinitions"

	c, err := rig.api.registry.Get("fake")
	if err != nil {
		t.Fatal(err)
	}
	ar, err := c.Discovery.Resolve(t.Context(), "example.com", "v1", "widgets")
	if err != nil {
		t.Fatal(err)
	}

	// The CRD is unreadable, so this falls back to the generic table.
	if set := rig.api.tableFor(t.Context(), c, ar); hasColumn(set, "x_color") {
		t.Fatal("the CRD's columns resolved while its cache was broken")
	}

	// The informer recovers. The next request must ask again rather than be
	// served the fallback that the failure produced.
	rig.fake.breakCacheResource = ""

	set := rig.api.tableFor(t.Context(), c, ar)
	if !hasColumn(set, "x_color") {
		t.Errorf("columns = %v, want the CRD's printer columns once it could be read", keysOf(set))
	}
}

// The ordinary answer is still cached: a resource whose CRD says nothing about
// printer columns must not pay for the lookup on every list.
func TestTableForCachesAResourceWithNoPrinterColumns(t *testing.T) {
	rig := hndNewRig(t)

	c, err := rig.api.registry.Get("fake")
	if err != nil {
		t.Fatal(err)
	}
	ar, err := c.Discovery.Resolve(t.Context(), "example.com", "v1", "widgets")
	if err != nil {
		t.Fatal(err)
	}

	first := rig.api.tableFor(t.Context(), c, ar)
	if !hasColumn(first, "x_color") {
		t.Fatalf("columns = %v, want the CRD's printer columns", keysOf(first))
	}

	// Breaking the CRD cache now must not change the answer, because the
	// successful resolution is remembered.
	rig.fake.breakCacheResource = "customresourcedefinitions"
	if second := rig.api.tableFor(t.Context(), c, ar); !hasColumn(second, "x_color") {
		t.Errorf("columns = %v, want the cached answer", keysOf(second))
	}
}

func hasColumn(set columnSet, key string) bool {
	for _, c := range set.columns {
		if c.Key == key {
			return true
		}
	}
	return false
}

func keysOf(set columnSet) []string {
	out := make([]string, 0, len(set.columns))
	for _, c := range set.columns {
		out = append(out, c.Key)
	}
	return out
}
