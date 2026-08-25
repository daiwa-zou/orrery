package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/daiwa-zou/orrery/internal/config"
)

func TestAuthenticatedRejectsWithoutSession(t *testing.T) {
	m := newTestManager(t)
	mw := NewMiddleware(m, nil, nil)

	h := mw.Authenticated(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("the handler must not run without a session")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/clusters", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("code = %d, want 401", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("the rejection must be JSON the SPA can render: %v", err)
	}
	if body["error"] != "unauthenticated" {
		t.Errorf("error = %v", body["error"])
	}
}

func TestAuthenticatedAnonymousMode(t *testing.T) {
	anon := &User{Username: "orrery:anonymous", Groups: []string{"system:authenticated"}}
	mw := NewMiddleware(nil, nil, anon)

	if !mw.Anonymous() {
		t.Error("Anonymous() must report that auth is disabled")
	}

	served := false
	h := mw.Authenticated(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		served = true
		u, ok := UserFrom(r.Context())
		if !ok || u.Username != "orrery:anonymous" {
			t.Errorf("wrong identity: %+v", u)
		}
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/clusters", nil))

	if !served || rec.Code != http.StatusOK {
		t.Errorf("anonymous request not served: code=%d served=%v", rec.Code, served)
	}
}

func TestAnonymousReportsFalseWithAuth(t *testing.T) {
	if NewMiddleware(newTestManager(t), nil, nil).Anonymous() {
		t.Error("Anonymous() must be false when no anonymous user is configured")
	}
}

func TestCSRF(t *testing.T) {
	m := newTestManager(t)
	sess, err := m.NewSession(context.Background(), User{Username: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	mw := NewMiddleware(m, nil, nil)

	cases := []struct {
		name    string
		method  string
		session *Session
		token   string
		want    int
	}{
		// Safe methods never need the token; that is what lets the SPA load.
		{"GET passes without token", http.MethodGet, nil, "", http.StatusOK},
		{"HEAD passes without token", http.MethodHead, nil, "", http.StatusOK},
		{"POST without session", http.MethodPost, nil, "", http.StatusUnauthorized},
		{"POST without token", http.MethodPost, sess, "", http.StatusForbidden},
		{"POST with wrong token", http.MethodPost, sess, "not-the-token", http.StatusForbidden},
		{"POST with the session's token", http.MethodPost, sess, sess.CSRFToken, http.StatusOK},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := mw.CSRF(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))
			req := httptest.NewRequest(tc.method, "/api/v1/clusters", nil)
			if tc.session != nil {
				req = req.WithContext(WithSession(req.Context(), tc.session))
			}
			if tc.token != "" {
				req.Header.Set("X-CSRF-Token", tc.token)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Errorf("code = %d, want %d", rec.Code, tc.want)
			}
		})
	}
}

func TestCSRFSkippedInAnonymousMode(t *testing.T) {
	// Without cookies there is nothing for a cross-site form post to ride on.
	mw := NewMiddleware(nil, nil, &User{Username: "orrery:anonymous"})
	h := mw.CSRF(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/clusters", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("code = %d, want 200", rec.Code)
	}
}

func TestContextHelpersOnEmptyContext(t *testing.T) {
	if _, ok := UserFrom(context.Background()); ok {
		t.Error("UserFrom on a bare context must report absence")
	}
	if _, ok := SessionFrom(context.Background()); ok {
		t.Error("SessionFrom on a bare context must report absence")
	}
}

func TestSessionFromRoundTrip(t *testing.T) {
	s := &Session{ID: "abc", User: User{Username: "alice"}}
	ctx := WithSession(context.Background(), s)
	got, ok := SessionFrom(ctx)
	if !ok || got.ID != "abc" {
		t.Errorf("SessionFrom = %+v, %v", got, ok)
	}
}

func TestFreshSessionToleratesTransientRefreshFailure(t *testing.T) {
	// A provider blip must not sever a running log follow or shell: the current
	// token is still valid when NeedsRefresh fires.
	m := newTestManager(t)
	sess := staleSession(t, m)
	a, _ := newRefreshAuthenticator(t, m, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	mw := NewMiddleware(m, a, nil)

	got, err := mw.FreshSession(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("a transient failure must not end the stream: %v", err)
	}
	if got.AccessToken != "old" {
		t.Errorf("the current token should keep serving, got %q", got.AccessToken)
	}
}

func TestClearCookiesExpiresBoth(t *testing.T) {
	m := newTestManager(t)
	rec := httptest.NewRecorder()
	m.ClearCookies(rec)

	cookies := rec.Result().Cookies()
	byName := map[string]*http.Cookie{}
	for _, c := range cookies {
		byName[c.Name] = c
	}
	for _, name := range []string{"orrery_session", CSRFCookieName} {
		c := byName[name]
		if c == nil {
			t.Fatalf("cookie %q was not cleared", name)
		}
		if c.MaxAge >= 0 {
			t.Errorf("cookie %q must be expired, MaxAge=%d", name, c.MaxAge)
		}
	}
	if !byName["orrery_session"].HttpOnly {
		t.Error("the cleared session cookie must stay HttpOnly")
	}
	if byName[CSRFCookieName].HttpOnly {
		t.Error("the cleared CSRF cookie must stay script-readable")
	}
}

func TestSameSiteMapping(t *testing.T) {
	cases := map[string]http.SameSite{
		"strict":  http.SameSiteStrictMode,
		"none":    http.SameSiteNoneMode,
		"lax":     http.SameSiteLaxMode,
		"":        http.SameSiteLaxMode, // anything unrecognised falls back to Lax
		"bizarre": http.SameSiteLaxMode,
	}
	for in, want := range cases {
		m := &SessionManager{cfg: config.SessionConfig{SameSite: in}}
		if got := m.sameSite(); got != want {
			t.Errorf("sameSite(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestNewSessionManagerRejectsBadKey(t *testing.T) {
	store := NewMemoryStore(time.Hour)
	defer store.Close()

	cfg := config.Default()
	cfg.Session.EncryptionKey = "!!not base64!!"
	if _, err := NewSessionManager(cfg, store); err == nil {
		t.Error("a non-base64 key was accepted")
	}

	cfg.Session.EncryptionKey = "c2hvcnQ=" // "short": valid base64, wrong length
	if _, err := NewSessionManager(cfg, store); err == nil {
		t.Error("a short key was accepted")
	}
}

func TestNewCookieCodecRejectsShortKey(t *testing.T) {
	if _, err := NewCookieCodec([]byte("short")); err == nil {
		t.Error("AES must refuse a 5-byte key")
	}
}

func TestMemoryStoreCloseIsIdempotent(t *testing.T) {
	store := NewMemoryStore(time.Hour)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}
