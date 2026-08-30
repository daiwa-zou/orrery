package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

// newRefreshAuthenticator builds an Authenticator whose refresh path talks to
// an httptest token endpoint. There is no discovery and no verifier, so
// responses must not carry an id_token. calls counts provider round trips.
func newRefreshAuthenticator(t *testing.T, sessions *SessionManager, handler http.HandlerFunc) (*Authenticator, *atomic.Int64) {
	t.Helper()
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	return &Authenticator{
		sessions: sessions,
		oauth: &oauth2.Config{
			ClientID: "orrery",
			Endpoint: oauth2.Endpoint{TokenURL: srv.URL},
		},
	}, &calls
}

func writeToken(w http.ResponseWriter, access string) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"access_token":%q,"token_type":"bearer","expires_in":3600,"refresh_token":"rotated"}`, access)
}

// staleSession seeds the store with a session whose token is about to expire.
func staleSession(t *testing.T, m *SessionManager) *Session {
	t.Helper()
	sess, err := m.NewSession(context.Background(), User{Username: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	sess.AccessToken = "old"
	sess.RefreshToken = "orig"
	sess.TokenExpiry = time.Now().Add(10 * time.Second)
	if err := m.Save(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	return sess
}

func TestRefreshUpdatesTheStoredSession(t *testing.T) {
	m := newTestManager(t)
	sess := staleSession(t, m)
	a, calls := newRefreshAuthenticator(t, m, func(w http.ResponseWriter, _ *http.Request) {
		writeToken(w, "new-access")
	})

	if err := a.Refresh(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Errorf("provider called %d times", calls.Load())
	}
	if sess.AccessToken != "new-access" || sess.RefreshToken != "rotated" {
		t.Errorf("caller's copy not updated: %+v", sess)
	}
	stored, err := m.Get(context.Background(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.AccessToken != "new-access" || stored.RefreshToken != "rotated" {
		t.Errorf("store not updated: access=%q refresh=%q", stored.AccessToken, stored.RefreshToken)
	}
	if time.Until(stored.TokenExpiry) < 30*time.Minute {
		t.Errorf("expiry not advanced: %v", stored.TokenExpiry)
	}
}

func TestRefreshCollapsesConcurrentCallsForOneSession(t *testing.T) {
	// Presenting one refresh token twice trips rotation reuse detection at
	// real providers, so concurrent refreshes of a session must share a single
	// provider round trip.
	m := newTestManager(t)
	sess := staleSession(t, m)
	a, calls := newRefreshAuthenticator(t, m, func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(50 * time.Millisecond) // widen the collapse window
		writeToken(w, "new-access")
	})

	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			copy := *sess
			errs[i] = a.Refresh(context.Background(), &copy)
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("refresh %d: %v", i, err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("provider called %d times, want 1", got)
	}
}

func TestRefreshSkipsWhenAnotherRequestAlreadyDid(t *testing.T) {
	m := newTestManager(t)
	sess := staleSession(t, m)

	// Simulate another replica or request having refreshed already: the store
	// holds a fresh token even though this caller's copy is stale.
	stored, _ := m.Get(context.Background(), sess.ID)
	stored.AccessToken = "already-fresh"
	stored.TokenExpiry = time.Now().Add(time.Hour)
	_ = m.Save(context.Background(), stored)

	a, calls := newRefreshAuthenticator(t, m, func(w http.ResponseWriter, _ *http.Request) {
		writeToken(w, "should-not-happen")
	})
	if err := a.Refresh(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Errorf("provider called %d times for an already-fresh session", calls.Load())
	}
	if sess.AccessToken != "already-fresh" {
		t.Errorf("caller should adopt the store's fresh token, got %q", sess.AccessToken)
	}
}

func TestRefreshFatalClassification(t *testing.T) {
	m := newTestManager(t)

	fatal := func(status int, body string) error {
		t.Helper()
		sess := staleSession(t, m)
		a, _ := newRefreshAuthenticator(t, m, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			fmt.Fprint(w, body)
		})
		err := a.Refresh(context.Background(), sess)
		if err == nil {
			t.Fatal("expected a refresh error")
		}
		return err
	}

	if !RefreshFatal(fatal(http.StatusBadRequest, `{"error":"invalid_grant"}`)) {
		t.Error("invalid_grant is definitive and must be fatal")
	}
	if RefreshFatal(fatal(http.StatusServiceUnavailable, `upstream down`)) {
		t.Error("a 503 is transient and must not end the session")
	}
	if RefreshFatal(fatal(http.StatusTooManyRequests, `slow down`)) {
		t.Error("throttling is transient and must not end the session")
	}
	if RefreshFatal(nil) {
		t.Error("nil is not a failure")
	}
}

// authedRequest builds a request carrying sess's cookies.
func authedRequest(t *testing.T, m *SessionManager, sess *Session) *http.Request {
	t.Helper()
	rec := httptest.NewRecorder()
	if err := m.SetCookies(rec, sess); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/clusters", nil)
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}
	return req
}

func TestMiddlewareToleratesTransientRefreshFailure(t *testing.T) {
	// An identity provider blip must not sign users out: the current token is
	// still valid when NeedsRefresh fires, so the request is served.
	m := newTestManager(t)
	sess := staleSession(t, m)
	a, _ := newRefreshAuthenticator(t, m, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	mw := NewMiddleware(m, a, nil)

	served := false
	h := mw.Authenticated(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		served = true
		if u, ok := UserFrom(r.Context()); !ok || u.Username != "alice" {
			t.Errorf("wrong identity: %+v", u)
		}
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedRequest(t, m, sess))

	if !served || rec.Code != http.StatusOK {
		t.Fatalf("request not served: code=%d served=%v", rec.Code, served)
	}
	if _, err := m.Get(context.Background(), sess.ID); err != nil {
		t.Error("a transient refresh failure must not destroy the session")
	}
}

func TestMiddlewareEndsSessionOnFatalRefresh(t *testing.T) {
	m := newTestManager(t)
	sess := staleSession(t, m)
	a, _ := newRefreshAuthenticator(t, m, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":"invalid_grant"}`)
	})
	mw := NewMiddleware(m, a, nil)

	h := mw.Authenticated(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("the handler must not run once the grant is gone")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedRequest(t, m, sess))

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("code = %d, want 401", rec.Code)
	}
	if _, err := m.Get(context.Background(), sess.ID); err == nil {
		t.Error("a revoked grant must destroy the session")
	}
}

func TestFreshSessionRefreshesAndTouches(t *testing.T) {
	m := newTestManager(t)
	sess := staleSession(t, m)

	// Backdate LastSeen so the stream's liveness stamp is observable.
	stored, _ := m.Get(context.Background(), sess.ID)
	stored.LastSeen = time.Now().Add(-30 * time.Minute)
	_ = m.Save(context.Background(), stored)

	a, calls := newRefreshAuthenticator(t, m, func(w http.ResponseWriter, _ *http.Request) {
		writeToken(w, "new-access")
	})
	mw := NewMiddleware(m, a, nil)

	got, err := mw.FreshSession(context.Background(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != "new-access" || calls.Load() != 1 {
		t.Errorf("stream refresh did not happen: token=%q calls=%d", got.AccessToken, calls.Load())
	}
	after, _ := m.Get(context.Background(), sess.ID)
	if time.Since(after.LastSeen) > time.Minute {
		t.Error("an open stream must count as session activity")
	}
}

func TestFreshSessionFailsWhenSessionGone(t *testing.T) {
	// A watch or shell must not outlive a sign-out.
	m := newTestManager(t)
	sess := staleSession(t, m)
	mw := NewMiddleware(m, nil, nil)

	_ = m.Destroy(context.Background(), sess.ID)
	if _, err := mw.FreshSession(context.Background(), sess.ID); err == nil {
		t.Error("a destroyed session must end the stream")
	}
}

// A token exchange must not be abandoned because the request that started it
// went away, and the caller must not be held by an exchange it has stopped
// caring about. Those pull in opposite directions, which is why the exchange
// runs on its own context and each caller waits on its own.
//
// The cost of getting it wrong is not a lost round trip. A provider that
// rotates refresh tokens has already spent the old one by the time the
// cancellation lands; if the new one never reaches the store, the session goes
// on presenting a token the provider has retired, and the next refresh comes
// back invalid_grant — which is fatal, and signs the user out. One closing tab
// is enough, and the session it ends may belong to a different tab entirely.
func TestRefreshOutlivesTheRequestThatStartedIt(t *testing.T) {
	m := newTestManager(t)
	sess := staleSession(t, m)

	entered := make(chan struct{})
	release := make(chan struct{})
	// The httptest server's Close waits for handlers to return, so the gate has
	// to open on every path out of this test, t.Fatal included.
	releaseOnce := sync.OnceFunc(func() { close(release) })
	defer releaseOnce()

	var enteredOnce sync.Once
	a, calls := newRefreshAuthenticator(t, m, func(w http.ResponseWriter, _ *http.Request) {
		enteredOnce.Do(func() { close(entered) })
		<-release
		writeToken(w, "rotated-access")
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Refresh(ctx, sess) }()

	<-entered
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Refresh returned %v, want the caller's own cancellation", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a caller was held by an exchange it had already given up on")
	}

	releaseOnce()

	// The exchange finishes on its own and the rotated token lands in the
	// store, where the next request will find it.
	deadline := time.Now().Add(5 * time.Second)
	for {
		stored, err := m.Get(context.Background(), sess.ID)
		if err == nil && stored.AccessToken == "rotated-access" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the abandoned exchange never reached the store (session = %+v, err = %v)", stored, err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	if n := calls.Load(); n != 1 {
		t.Errorf("provider round trips = %d, want exactly 1", n)
	}
}
