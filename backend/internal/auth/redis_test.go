package auth

import (
	"context"
	"os"
	"testing"
	"time"
)

// redisStore connects to the instance named by CLUSTERLENS_TEST_REDIS_URL and
// skips the test when there is none, so `go test ./...` stays runnable with no
// infrastructure while CI can exercise the real thing.
func redisStore(t *testing.T, idle time.Duration) *RedisStore {
	t.Helper()
	url := os.Getenv("CLUSTERLENS_TEST_REDIS_URL")
	if url == "" {
		t.Skip("set CLUSTERLENS_TEST_REDIS_URL to run the Redis session tests")
	}
	store, err := NewRedisStore(context.Background(), url, idle)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestRedisStoreRoundTrip(t *testing.T) {
	store := redisStore(t, time.Hour)
	ctx := context.Background()

	now := time.Now()
	want := &Session{
		ID:          "test-roundtrip",
		User:        User{Username: "alice@example.com", Groups: []string{"oidc:devs"}},
		IDToken:     "id-token",
		CSRFToken:   "csrf",
		CreatedAt:   now,
		LastSeen:    now,
		ExpiresAt:   now.Add(time.Hour),
		TokenExpiry: now.Add(30 * time.Minute),
	}
	if err := store.Put(ctx, want); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Delete(ctx, want.ID) })

	got, err := store.Get(ctx, want.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.User.Username != want.User.Username || len(got.User.Groups) != 1 {
		t.Errorf("identity did not survive the round trip: %+v", got.User)
	}
	// The bearer material has to survive too, or passthrough clusters and
	// token refresh break after a request lands on another replica.
	if got.IDToken != want.IDToken {
		t.Error("the id token was not persisted")
	}
	if got.CSRFToken != want.CSRFToken {
		t.Error("the CSRF token was not persisted")
	}
}

func TestRedisStoreMissingSession(t *testing.T) {
	store := redisStore(t, time.Hour)
	if _, err := store.Get(context.Background(), "does-not-exist"); err != ErrNoSession {
		t.Errorf("got %v, want ErrNoSession", err)
	}
}

func TestRedisStoreDelete(t *testing.T) {
	store := redisStore(t, time.Hour)
	ctx := context.Background()

	s := &Session{ID: "test-delete", LastSeen: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}
	if err := store.Put(ctx, s); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, s.ID); err != ErrNoSession {
		t.Errorf("session survived deletion: %v", err)
	}
	// Deleting something that is already gone is not an error; logout can be
	// racy across replicas.
	if err := store.Delete(ctx, s.ID); err != nil {
		t.Errorf("second delete errored: %v", err)
	}
}

func TestRedisStoreEnforcesIdleTimeout(t *testing.T) {
	// Redis expires on the absolute deadline; the idle timeout is enforced in
	// our code, so it needs its own coverage.
	store := redisStore(t, 50*time.Millisecond)
	ctx := context.Background()

	s := &Session{
		ID:        "test-idle",
		LastSeen:  time.Now().Add(-time.Hour),
		ExpiresAt: time.Now().Add(time.Hour),
	}
	if err := store.Put(ctx, s); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Delete(ctx, s.ID) })

	if _, err := store.Get(ctx, s.ID); err != ErrNoSession {
		t.Errorf("an idle session was served: %v", err)
	}
}

func TestRedisStoreRefusesExpiredWrites(t *testing.T) {
	store := redisStore(t, time.Hour)
	ctx := context.Background()

	s := &Session{ID: "test-expired", LastSeen: time.Now(), ExpiresAt: time.Now().Add(-time.Minute)}
	if err := store.Put(ctx, s); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, s.ID); err != ErrNoSession {
		t.Errorf("a session written past its deadline was stored: %v", err)
	}
}

func TestRedisStoreSurvivesAcrossInstances(t *testing.T) {
	// The property that matters: a session created by one process is readable
	// by another. This is what makes more than one replica possible.
	writer := redisStore(t, time.Hour)
	reader := redisStore(t, time.Hour)
	ctx := context.Background()

	s := &Session{
		ID:        "test-cross-replica",
		User:      User{Username: "alice@example.com"},
		LastSeen:  time.Now(),
		ExpiresAt: time.Now().Add(time.Hour),
	}
	if err := writer.Put(ctx, s); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writer.Delete(ctx, s.ID) })

	got, err := reader.Get(ctx, s.ID)
	if err != nil {
		t.Fatalf("a second replica could not read the session: %v", err)
	}
	if got.User.Username != "alice@example.com" {
		t.Errorf("read the wrong identity: %+v", got.User)
	}
}

func TestNewRedisStoreRejectsBadURL(t *testing.T) {
	// This must fail at startup, not on the first user's login.
	if _, err := NewRedisStore(context.Background(), "not-a-url", time.Hour); err == nil {
		t.Error("an unparseable URL was accepted")
	}
}
