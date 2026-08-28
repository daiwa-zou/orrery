package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestQueryNamespaces(t *testing.T) {
	cases := []struct {
		query string
		want  []string
	}{
		{"", nil},
		{"?namespace=demo", []string{"demo"}},
		{"?namespace=demo&namespace=kube-system", []string{"demo", "kube-system"}},
		// A cleared filter arrives as an empty value, and must not narrow to a
		// namespace called "".
		{"?namespace=", nil},
		{"?namespace=demo&namespace=", []string{"demo"}},
		// A repeat is not a second request for the same objects; listing it
		// twice would return every object in it twice.
		{"?namespace=demo&namespace=demo", []string{"demo"}},
		{"?namespace=%20demo%20", []string{"demo"}},
	}

	for _, c := range cases {
		r := httptest.NewRequest(http.MethodGet, "/x"+c.query, nil)
		got := queryNamespaces(r)
		if len(got) != len(c.want) {
			t.Errorf("queryNamespaces(%q) = %v, want %v", c.query, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("queryNamespaces(%q) = %v, want %v", c.query, got, c.want)
				break
			}
		}
	}
}

// Several namespaces are one question, and the answer is their union — with
// the scope saying which namespaces it actually covers, because "these two"
// and "everywhere" are different answers that would otherwise look identical.
func TestListAcrossSeveralNamespaces(t *testing.T) {
	rig := hndNewRig(t)

	page := func(query string) (int, struct {
		AllNamespaces bool     `json:"allNamespaces"`
		Namespaces    []string `json:"namespaces"`
		Namespace     string   `json:"namespace"`
	}) {
		t.Helper()
		rec := rig.get(t, "/api/v1/clusters/fake/resources/core/v1/pods"+query)
		hndWantStatus(t, rec, http.StatusOK)
		var body struct {
			Items []map[string]any `json:"items"`
			Scope struct {
				AllNamespaces bool     `json:"allNamespaces"`
				Namespaces    []string `json:"namespaces"`
				Namespace     string   `json:"namespace"`
			} `json:"scope"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode list: %v", err)
		}
		return len(body.Items), body.Scope
	}

	one, oneScope := page("?namespace=demo")
	if one == 0 {
		t.Fatal("the fixture has no pods in demo; this test is checking nothing")
	}
	if oneScope.Namespace != "demo" || len(oneScope.Namespaces) != 0 {
		t.Errorf("one namespace should report itself singly: %+v", oneScope)
	}

	// kube-system holds no pods in the fixture, so the union is still demo's —
	// what changes is that the scope now names both.
	both, bothScope := page("?namespace=demo&namespace=kube-system")
	if both != one {
		t.Errorf("demo+kube-system = %d pods, want the %d in demo alone", both, one)
	}
	if bothScope.AllNamespaces {
		t.Error("a narrowed scope must not report itself as every namespace")
	}
	if len(bothScope.Namespaces) != 2 {
		t.Errorf("scope should name both namespaces: %+v", bothScope)
	}

	// And asking twice for one namespace is asking once.
	twice, _ := page("?namespace=demo&namespace=demo")
	if twice != one {
		t.Errorf("demo twice = %d pods, want %d — a repeat must not duplicate rows", twice, one)
	}
}

func TestEventsAcrossSeveralNamespaces(t *testing.T) {
	rig := hndNewRig(t)

	total := func(query string) int {
		t.Helper()
		rec := rig.get(t, "/api/v1/clusters/fake/events"+query)
		hndWantStatus(t, rec, http.StatusOK)
		var body listResponse
		hndDecode(t, rec, &body)
		return body.Total
	}

	demo := total("?namespace=demo")
	if demo == 0 {
		t.Fatal("the fixture has no events in demo; this test is checking nothing")
	}
	if both := total("?namespace=demo&namespace=kube-system"); both != demo {
		t.Errorf("demo+kube-system = %d events, want the %d in demo alone", both, demo)
	}
	if twice := total("?namespace=demo&namespace=demo"); twice != demo {
		t.Errorf("demo twice = %d events, want %d", twice, demo)
	}
}
