package auth

import (
	"context"
	"testing"
	"time"
)

// Groups is what becomes the impersonation header and the SubjectAccessReview
// subject, so a Groups slice shared between two "copies" of a session is not a
// tidiness problem — it is a group one request can write into another's
// identity. MemoryStore copied the struct, which copies the slice header and
// not the array behind it; RedisStore decodes JSON and so never had the
// problem. Behaviour that changes when a deployment grows a second replica is
// behaviour nobody can reproduce locally.

func TestMemoryStoreHandsBackIndependentSessions(t *testing.T) {
	store := NewMemoryStore(time.Hour)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()

	// Spare capacity is what makes the aliasing reachable: appending within
	// capacity writes into the shared array rather than allocating a new one.
	groups := make([]string, 1, 4)
	groups[0] = "oidc:dev"

	if err := store.Put(ctx, &Session{
		ID:        "s1",
		User:      User{Username: "alice", Groups: groups},
		LastSeen:  time.Now(),
		ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	// The caller's own slice must no longer reach the store.
	groups[0] = "oidc:admin"

	first, err := store.Get(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if first.User.Groups[0] != "oidc:dev" {
		t.Errorf("the stored session followed the caller's slice to %q", first.User.Groups[0])
	}

	// And one reader appending a group must not grant it to the next.
	first.User.Groups = append(first.User.Groups, "oidc:admin")

	second, err := store.Get(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(second.User.Groups) != 1 || second.User.Groups[0] != "oidc:dev" {
		t.Errorf("groups = %v, want just [oidc:dev]; one reader's append reached another", second.User.Groups)
	}
}

func TestSessionCloneSharesNothing(t *testing.T) {
	s := &Session{ID: "s1", User: User{Username: "alice", Groups: []string{"a", "b"}}}

	cp := s.Clone()
	cp.User.Groups[0] = "changed"

	if s.User.Groups[0] != "a" {
		t.Errorf("the original's groups became %v", s.User.Groups)
	}
	if (*Session)(nil).Clone() != nil {
		t.Error("cloning nothing should give nothing, not a panic-in-waiting")
	}
}
