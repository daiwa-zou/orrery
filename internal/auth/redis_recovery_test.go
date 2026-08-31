package auth

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A stored value the dashboard cannot decode is worse than no value at all: it
// is a session ID the browser will keep presenting and the server will keep
// failing on, which is a user locked out until they find the cookie themselves.
// Dropping it turns a permanent error into one clean re-login.
func TestRedisStoreDiscardsAValueItCannotDecode(t *testing.T) {
	store := redisStore(t, time.Hour)
	ctx := context.Background()

	const id = "corrupt-session"
	// Whatever wrote this — an older encoding, another workload sharing the
	// instance, a truncated write — the reader's position is the same.
	if err := store.client.Set(ctx, store.key(id), "{not json", time.Hour).Err(); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Get(ctx, id); !errors.Is(err, ErrNoSession) {
		t.Fatalf("Get on an undecodable value returned %v, want ErrNoSession", err)
	}

	// And it is gone, so the next request does not repeat the failure.
	n, err := store.client.Exists(ctx, store.key(id)).Result()
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("the undecodable value was left in place for the next request to trip over")
	}
}

// A session with no absolute deadline still gets a TTL, so an instance that is
// restarted or scaled to zero does not leave sessions in Redis forever.
func TestRedisStoreGivesADeadlinelessSessionATTL(t *testing.T) {
	store := redisStore(t, time.Hour)
	ctx := context.Background()

	const id = "no-deadline"
	if err := store.Put(ctx, &Session{ID: id, LastSeen: time.Now()}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Delete(ctx, id) })

	ttl, err := store.client.TTL(ctx, store.key(id)).Result()
	if err != nil {
		t.Fatal(err)
	}
	if ttl <= 0 {
		t.Fatalf("TTL = %v: a session with no deadline was stored without one", ttl)
	}
	if ttl > 24*time.Hour {
		t.Errorf("TTL = %v, want at most the 24h fallback", ttl)
	}
}

// Deleting something that is not there is not an error. Logout runs this on a
// session Redis may already have expired, and a failure there would report a
// sign-out that did happen as one that did not.
func TestRedisStoreDeleteIsSilentOnAMissingKey(t *testing.T) {
	store := redisStore(t, time.Hour)

	if err := store.Delete(context.Background(), "never-existed"); err != nil {
		t.Errorf("Delete on a missing key returned %v", err)
	}
}
