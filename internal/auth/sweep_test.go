package auth

import (
	"context"
	"errors"
	"testing"
	"time"
)

// The sweep is the only thing that reclaims an abandoned session. Get evicts
// what it reads, but a session nobody comes back for is never read again — a
// user who closed the tab, a bot that logged in once — so without the sweep
// the map only grows, for as long as the process runs.

func TestEvictExpiredDropsWhatHasRunOut(t *testing.T) {
	store := NewMemoryStore(30 * time.Minute)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	now := time.Now()

	put := func(id string, expires, lastSeen time.Time) {
		t.Helper()
		if err := store.Put(ctx, &Session{ID: id, ExpiresAt: expires, LastSeen: lastSeen}); err != nil {
			t.Fatal(err)
		}
	}
	put("live", now.Add(time.Hour), now)
	put("past-deadline", now.Add(-time.Minute), now)
	put("idle-too-long", now.Add(time.Hour), now.Add(-time.Hour))

	if n := store.evictExpired(now); n != 2 {
		t.Errorf("evicted %d sessions, want 2", n)
	}

	if _, err := store.Get(ctx, "live"); err != nil {
		t.Errorf("the live session was swept: %v", err)
	}
	for _, id := range []string{"past-deadline", "idle-too-long"} {
		if _, err := store.Get(ctx, id); !errors.Is(err, ErrNoSession) {
			t.Errorf("%s survived the sweep (err = %v)", id, err)
		}
	}
}

// A store configured without an idle timeout expires on the absolute deadline
// only — otherwise a zero timeout would read as "everything is idle" and sign
// out every session on the first tick.
func TestEvictExpiredWithoutAnIdleTimeout(t *testing.T) {
	store := NewMemoryStore(0)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	now := time.Now()

	if err := store.Put(ctx, &Session{
		ID:        "long-idle",
		ExpiresAt: now.Add(time.Hour),
		LastSeen:  now.Add(-72 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	if n := store.evictExpired(now); n != 0 {
		t.Errorf("evicted %d sessions with no idle timeout configured, want 0", n)
	}
	if _, err := store.Get(ctx, "long-idle"); err != nil {
		t.Errorf("a session with no idle timeout was evicted: %v", err)
	}
}

// A session with no absolute deadline is governed by the idle timeout alone.
func TestEvictExpiredWithoutADeadline(t *testing.T) {
	store := NewMemoryStore(time.Minute)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	now := time.Now()

	if err := store.Put(ctx, &Session{ID: "no-deadline", LastSeen: now}); err != nil {
		t.Fatal(err)
	}
	if n := store.evictExpired(now); n != 0 {
		t.Fatalf("a fresh session was evicted (%d)", n)
	}
	if n := store.evictExpired(now.Add(2 * time.Minute)); n != 1 {
		t.Errorf("evicted %d, want the idle session gone", n)
	}
}

func TestEvictExpiredOnAnEmptyStore(t *testing.T) {
	store := NewMemoryStore(time.Minute)
	t.Cleanup(func() { _ = store.Close() })

	if n := store.evictExpired(time.Now()); n != 0 {
		t.Errorf("evicted %d from an empty store", n)
	}
}
