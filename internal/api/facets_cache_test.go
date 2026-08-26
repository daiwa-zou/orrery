package api

import (
	"testing"
	"time"
)

// A cache keyed by scope must never serve one reach's vocabulary to another.
// Getting this wrong would leak label values harvested from namespaces the
// caller cannot list, which is exactly what the authorization walk prevents.
func TestScopeKeySeparatesReach(t *testing.T) {
	cases := []struct {
		name string
		a, b scopeInfo
		same bool
	}{
		{
			name: "cluster-wide is not one namespace",
			a:    scopeInfo{AllNamespaces: true},
			b:    scopeInfo{Namespace: "demo"},
		},
		{
			name: "different single namespaces",
			a:    scopeInfo{Namespace: "demo"},
			b:    scopeInfo{Namespace: "other"},
		},
		{
			name: "a subset is not the whole set",
			a:    scopeInfo{Namespaces: []string{"a", "b"}},
			b:    scopeInfo{Namespaces: []string{"a"}},
		},
		{
			name: "cluster-wide is not the same as listing every namespace",
			a:    scopeInfo{AllNamespaces: true},
			b:    scopeInfo{Namespaces: []string{"a", "b"}},
		},
		{
			name: "the same reach in a different order agrees",
			a:    scopeInfo{Namespaces: []string{"b", "a"}},
			b:    scopeInfo{Namespaces: []string{"a", "b"}},
			same: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ka, kb := scopeKey(tc.a), scopeKey(tc.b)
			if tc.same && ka != kb {
				t.Errorf("keys differ but the reach is identical: %q vs %q", ka, kb)
			}
			if !tc.same && ka == kb {
				t.Errorf("distinct reach collided on key %q", ka)
			}
		})
	}
}

func TestFacetCacheExpires(t *testing.T) {
	c := newFacetCache()
	want := facetsResponse{Labels: []facet{{Key: "app", Values: []string{"web"}}}}
	c.put("k", want)

	if got, ok := c.get("k"); !ok || len(got.Labels) != 1 {
		t.Fatalf("fresh entry not served: %+v ok=%v", got, ok)
	}
	if _, ok := c.get("absent"); ok {
		t.Error("unknown key reported a hit")
	}

	// Age the entry past the TTL in place rather than sleeping for it.
	c.mu.Lock()
	e, _ := c.entries.Get("k")
	e.computed = time.Now().Add(-facetsCacheTTL - time.Second)
	c.entries.Add("k", e)
	c.mu.Unlock()

	if _, ok := c.get("k"); ok {
		t.Error("expired entry was served")
	}
}
