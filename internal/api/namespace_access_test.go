package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// Asking for several namespaces and being allowed some of them is a narrower
// answer, not a refused one, and the list endpoint has always said which ones
// were dropped. What it did not say is why.
//
// Every namespace whose access review returned an error was named in "You may
// not list pods in team-b, so nothing from there is shown" — a sentence about
// RBAC, and a definite one. A review the API server could not perform says
// nothing at all about permission, and putting it in that sentence sends the
// reader to change a binding that was never the problem, which will not help.

func TestListSaysWhyANamespaceIsMissing(t *testing.T) {
	rig := hndNewRig(t)
	// Reviews for pods fail rather than come back denied.
	rig.fake.failReviewResource = "pods"

	rec := rig.get(t, "/api/v1/clusters/fake/resources/core/v1/pods?namespace=demo")
	// Nothing was allowed, so this is an error rather than a partial answer —
	// and it must not be a 403.
	if rec.Code == http.StatusForbidden {
		t.Fatalf("an unperformable review was reported as a denial: %s", rec.Body.String())
	}
	if rec.Code == http.StatusOK {
		t.Fatalf("a namespace whose access could not be checked was listed anyway: %s", rec.Body.String())
	}
}

func TestNamespaceAccessWarningsKeepTheTwoReasonsApart(t *testing.T) {
	na := namespaceAccess{
		allowed:   []string{"demo"},
		denied:    []string{"team-b"},
		unchecked: []string{"team-c"},
	}

	got := na.warnings("pods")
	if len(got) != 2 {
		t.Fatalf("warnings = %v, want one sentence per kind of gap", got)
	}

	joined := strings.Join(got, "\n")
	for _, want := range []string{"You may not list pods in team-b", "team-c", "not a permission problem"} {
		if !strings.Contains(joined, want) {
			t.Errorf("warnings = %q, want them to contain %q", joined, want)
		}
	}
	// The namespace that could not be checked must never appear in the
	// sentence that claims a denial.
	for _, w := range got {
		if strings.Contains(w, "You may not") && strings.Contains(w, "team-c") {
			t.Errorf("an unchecked namespace was named as denied: %q", w)
		}
	}
}

// The events feed shares the same check and used to narrow without saying so:
// ask for three namespaces, be allowed two, and read the result as everything
// that happened.
func TestEventsFeedSaysWhenItWasNarrowed(t *testing.T) {
	rig := hndNewRig(t)
	rig.fake.denyNamespace = "kube-system"

	// One namespace the caller may read, one they may not. The feed is served,
	// narrowed, and must say so — otherwise it reads as everything that
	// happened, which is the conclusion an incident cannot afford to get
	// wrong.
	rec := rig.get(t, "/api/v1/clusters/fake/events?namespace=demo&namespace=kube-system")
	if rec.Code != http.StatusOK {
		t.Fatalf("events = %d: %s", rec.Code, rec.Body.String())
	}

	var body listResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(body.Warnings, "\n")
	if !strings.Contains(joined, "kube-system") {
		t.Errorf("warnings = %v, want the namespace that was dropped named", body.Warnings)
	}
}

func TestEventsFeedIsQuietWhenNothingWasDropped(t *testing.T) {
	rig := hndNewRig(t)

	rec := rig.get(t, "/api/v1/clusters/fake/events?namespace=demo")
	if rec.Code != http.StatusOK {
		t.Fatalf("events = %d: %s", rec.Code, rec.Body.String())
	}
	var body listResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Warnings) != 0 {
		t.Errorf("warnings = %v, want none when nothing was dropped", body.Warnings)
	}
}
