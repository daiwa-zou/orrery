package auth

// Refresh deduplicates concurrent refreshes of one session, so singleflight
// hands every waiter the *same* *Session. Copying that struct into each
// caller's own session leaves them all pointing at one User.Groups backing
// array — the arrangement Clone exists to prevent, and its comment says why it
// matters here: Groups becomes the impersonation header and the
// SubjectAccessReview subject, so a group written through an alias is a group
// granted to whoever else holds a copy.
//
// The reachable hazard is narrower than "an append could cross sessions".
// Clone and both Stores build Groups with append([]string(nil), ...), so
// capacity equals length everywhere and an append always reallocates. What
// aliasing still exposes is writing through an index and sorting in place —
// neither of which anything does today, which is exactly why the invariant is
// worth stating rather than left to be rediscovered.
//
// So this asserts Clone's own contract directly — "a session that shares
// nothing with this one" — rather than a scenario that happens to be safe by
// an accident of capacity.

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"
)

func TestConcurrentRefreshesDoNotShareGroups(t *testing.T) {
	m := newTestManager(t)

	sess, err := m.NewSession(context.Background(), User{
		Username: "alice",
		Groups:   []string{"team-a", "team-b"},
	})
	if err != nil {
		t.Fatal(err)
	}
	sess.RefreshToken = "orig"
	sess.TokenExpiry = time.Now().Add(10 * time.Second)
	if err := m.Save(context.Background(), sess); err != nil {
		t.Fatal(err)
	}

	a, _ := newRefreshAuthenticator(t, m, func(w http.ResponseWriter, _ *http.Request) {
		writeToken(w, "new-access")
	})

	// Several callers refresh the same session at once, which is what puts
	// them all on one singleflight result.
	const callers = 8
	copies := make([]*Session, callers)
	var wg sync.WaitGroup
	for i := range callers {
		copies[i] = sess.Clone()
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := a.Refresh(context.Background(), copies[i]); err != nil {
				t.Errorf("caller %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	for i, c := range copies {
		if len(c.User.Groups) != 2 {
			t.Fatalf("caller %d has groups %v, want the two it started with", i, c.User.Groups)
		}
	}

	// Written through an index, which is what a shared backing array actually
	// exposes: no reallocation can hide it.
	copies[0].User.Groups[0] = "rewritten-by-caller-0"

	for i, c := range copies[1:] {
		if c.User.Groups[0] != "team-a" {
			t.Errorf("caller %d sees %q after another session's write — "+
				"the refreshed sessions share one Groups array",
				i+1, c.User.Groups[0])
		}
	}
}
