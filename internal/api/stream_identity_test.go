package api

import (
	"context"
	"encoding/base64"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/daiwa-zou/orrery/internal/auth"
	"github.com/daiwa-zou/orrery/internal/cluster"
	"github.com/daiwa-zou/orrery/internal/config"
)

// refreshStreamIdentity is what stops a watch, a log follow or a shell from
// outliving the login that opened it. The API server checked permission once,
// at handshake; every re-authorization cycle after that goes through here. It
// had no tests at all, which for the one function standing between a signed-out
// user and a still-flowing stream is the wrong number.

// hndSessionRig swaps the rig's anonymous middleware for a session-backed one,
// and returns a manager the test can plant and revoke sessions in.
func hndSessionRig(t *testing.T, rig *hndRig) (*auth.SessionManager, auth.Store) {
	t.Helper()
	cfg := config.Default()
	cfg.Session.EncryptionKey = base64.StdEncoding.EncodeToString(make([]byte, 32))
	store := auth.NewMemoryStore(cfg.Session.IdleTimeout)
	t.Cleanup(func() { _ = store.Close() })

	sessions, err := auth.NewSessionManager(cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	// No anonymous user: that is what makes the session load-bearing.
	rig.api.mw = auth.NewMiddleware(sessions, nil, nil)
	return sessions, store
}

func hndSession(t *testing.T, sessions *auth.SessionManager, id, username, token string) *auth.Session {
	t.Helper()
	s := &auth.Session{
		ID:        id,
		User:      auth.User{Username: username, Groups: []string{"system:authenticated"}},
		IDToken:   token,
		CSRFToken: "csrf",
		CreatedAt: time.Now(),
		LastSeen:  time.Now(),
		ExpiresAt: time.Now().Add(time.Hour),
	}
	if err := sessions.Save(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	return s
}

// hndResolved builds the stream context a handler would be holding.
func hndResolved(t *testing.T, rig *hndRig, id cluster.Identity) *resolved {
	t.Helper()
	c, err := rig.api.registry.Get("fake")
	if err != nil {
		t.Fatal(err)
	}
	clients, err := c.ClientsFor(id)
	if err != nil {
		t.Fatal(err)
	}
	return &resolved{cluster: c, clients: clients, identity: id}
}

// With OIDC off there is no session to go stale, and a stream must not be torn
// down for the absence of one.
func TestRefreshStreamIdentityAnonymousIsANoOp(t *testing.T) {
	rig := hndNewRig(t)
	before := cluster.Identity{Username: "orrery:anonymous"}
	res := hndResolved(t, rig, before)

	r := httptest.NewRequest("GET", "/", nil)
	if err := rig.api.refreshStreamIdentity(context.Background(), r, res); err != nil {
		t.Fatalf("anonymous refresh returned %v, want nil", err)
	}
	if res.identity.Username != before.Username {
		t.Errorf("identity changed to %q", res.identity.Username)
	}
}

func TestRefreshStreamIdentityPicksUpTheLiveSession(t *testing.T) {
	rig := hndNewRig(t)
	sessions, _ := hndSessionRig(t, rig)
	sess := hndSession(t, sessions, "sess-live", "alice@example.com", "token-1")

	// The stream opened under a stale copy of the identity, which is exactly
	// the situation after an hour of following logs.
	res := hndResolved(t, rig, cluster.Identity{Username: "stale", BearerToken: "token-0"})

	r := httptest.NewRequest("GET", "/", nil)
	r = r.WithContext(auth.WithSession(r.Context(), sess))

	if err := rig.api.refreshStreamIdentity(context.Background(), r, res); err != nil {
		t.Fatalf("refresh returned %v", err)
	}
	if res.identity.Username != "alice@example.com" {
		t.Errorf("username = %q, want the session's", res.identity.Username)
	}
	if res.clients == nil {
		t.Error("clients were not rebuilt for the refreshed identity")
	}
}

// The security property. A signed-out or expired session must end the stream,
// not merely fail to update it: leaving the old identity in place would keep a
// revoked login reading the cluster until the socket happened to close.
func TestRefreshStreamIdentityFailsWhenTheSessionIsGone(t *testing.T) {
	rig := hndNewRig(t)
	sessions, store := hndSessionRig(t, rig)
	sess := hndSession(t, sessions, "sess-doomed", "bob@example.com", "token-1")

	res := hndResolved(t, rig, cluster.Identity{Username: "bob@example.com", BearerToken: "token-1"})
	r := httptest.NewRequest("GET", "/", nil)
	r = r.WithContext(auth.WithSession(r.Context(), sess))

	// Signed out from another tab, or the absolute TTL ran out.
	if err := store.Delete(context.Background(), sess.ID); err != nil {
		t.Fatal(err)
	}

	err := rig.api.refreshStreamIdentity(context.Background(), r, res)
	if err == nil {
		t.Fatal("refresh succeeded against a deleted session; the stream would keep flowing")
	}
	// The caller tears the stream down on error, so the identity it leaves
	// behind should not be a usable one either.
	if res.identity.Username != "bob@example.com" {
		t.Errorf("identity was mutated on a failed refresh: %+v", res.identity)
	}
}

// Passthrough clusters present the user's own token to the API server. If a
// renewed token does not reach the stream, re-authorization starts presenting
// an expired one and healthy streams die an hour in.
func TestRefreshStreamIdentityCarriesARenewedToken(t *testing.T) {
	rig := hndNewRig(t)
	sessions, _ := hndSessionRig(t, rig)
	sess := hndSession(t, sessions, "sess-rotating", "carol@example.com", "token-1")

	res := hndResolved(t, rig, cluster.Identity{Username: "carol@example.com", BearerToken: "token-1"})
	r := httptest.NewRequest("GET", "/", nil)
	r = r.WithContext(auth.WithSession(r.Context(), sess))

	// The provider rotated the token and the refresh path stored the new one.
	stored, err := sessions.Get(context.Background(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	stored.IDToken = "token-2"
	if err := sessions.Save(context.Background(), stored); err != nil {
		t.Fatal(err)
	}

	if err := rig.api.refreshStreamIdentity(context.Background(), r, res); err != nil {
		t.Fatalf("refresh returned %v", err)
	}
	if res.identity.BearerToken != "token-2" {
		t.Errorf("bearer token = %q, want the renewed one", res.identity.BearerToken)
	}
}

// A stream is activity. Without the stamp, a log follow longer than the idle
// timeout expires the session out from under itself.
func TestRefreshStreamIdentityStampsActivity(t *testing.T) {
	rig := hndNewRig(t)
	sessions, _ := hndSessionRig(t, rig)
	sess := hndSession(t, sessions, "sess-idle", "dana@example.com", "token-1")

	// Older than the one-minute threshold FreshSession stamps on.
	stale := time.Now().Add(-10 * time.Minute)
	stored, err := sessions.Get(context.Background(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	stored.LastSeen = stale
	if err := sessions.Save(context.Background(), stored); err != nil {
		t.Fatal(err)
	}

	res := hndResolved(t, rig, cluster.Identity{Username: "dana@example.com"})
	r := httptest.NewRequest("GET", "/", nil)
	r = r.WithContext(auth.WithSession(r.Context(), sess))

	if err := rig.api.refreshStreamIdentity(context.Background(), r, res); err != nil {
		t.Fatalf("refresh returned %v", err)
	}

	after, err := sessions.Get(context.Background(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !after.LastSeen.After(stale) {
		t.Errorf("LastSeen was not stamped: still %v", after.LastSeen)
	}
}
