package api

import (
	"errors"
	"strings"
	"testing"
)

// Watches, log follows and exec sessions all re-authorize every 60 seconds and
// close when the check does not come back yes. Closing is right — a review
// that could not be performed is not a pass — but all three used to leave the
// same sentence behind whatever had happened: "access ... was revoked".
//
// A stream open for hours is where a busy or briefly unreachable API server is
// most likely to be met, and that sentence sends its reader to whoever
// administers their RBAC to ask about a permission nobody ever withdrew. The
// answer they need is "try again".

func TestStreamClosedBecauseNamesRevocationOnlyWhenItHappened(t *testing.T) {
	revoked := "access to this resource was revoked"

	got := streamClosedBecause(&forbiddenError{verb: "watch", resource: "pods"}, revoked)
	if got != revoked {
		t.Errorf("a real denial said %q, want %q", got, revoked)
	}
}

func TestStreamClosedBecauseDoesNotBlameRBACForAnUnaskableQuestion(t *testing.T) {
	revoked := "access to this pod was revoked"
	err := errors.New("subjectaccessreview: the API server is too busy")

	got := streamClosedBecause(err, revoked)
	if got == revoked {
		t.Fatal("a review that could not be performed was reported as a revocation")
	}
	if !strings.Contains(got, "reconnecting") {
		t.Errorf("message = %q, want it to point at the action that would help", got)
	}
	// The underlying failure is worth carrying: it is what tells an operator
	// reading the console which of their components is unwell.
	if !strings.Contains(got, "too busy") {
		t.Errorf("message = %q, want it to carry what actually failed", got)
	}
}
