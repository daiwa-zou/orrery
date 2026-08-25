package auth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/daiwa-zou/orrery/backend/internal/config"
)

// fakeOIDC is an in-process OIDC provider: discovery, JWKS and a token
// endpoint backed by a throwaway RSA key. go-oidc talks to it exactly as it
// would to a real issuer, so NewAuthenticator, the callback and refresh run
// their real code paths with no network beyond the httptest listener.
type fakeOIDC struct {
	t   *testing.T
	key *rsa.PrivateKey
	srv *httptest.Server

	mu sync.Mutex
	// extraClaims are folded into the next id_token the token endpoint mints.
	extraClaims map[string]any
	// tokenStatus, when non-zero, makes the token endpoint fail.
	tokenStatus int
	// omitIDToken leaves the id_token out of the token response.
	omitIDToken bool
	// rawIDToken overrides the minted id_token verbatim when non-empty.
	rawIDToken string
}

func newFakeOIDC(t *testing.T) *fakeOIDC {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &fakeOIDC{t: t, key: key}

	mux := http.NewServeMux()
	p.srv = httptest.NewServer(mux)
	t.Cleanup(p.srv.Close)

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                 p.srv.URL,
			"authorization_endpoint": p.srv.URL + "/authorize",
			"token_endpoint":         p.srv.URL + "/token",
			"jwks_uri":               p.srv.URL + "/jwks",
			"end_session_endpoint":   p.srv.URL + "/logout",
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		pub := &p.key.PublicKey
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]string{{
				"kty": "RSA", "alg": "RS256", "use": "sig", "kid": "test-key",
				"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
				"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
			}},
		})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, _ *http.Request) {
		p.mu.Lock()
		status, omit, raw := p.tokenStatus, p.omitIDToken, p.rawIDToken
		extra := p.extraClaims
		p.mu.Unlock()

		if status != 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			fmt.Fprint(w, `{"error":"server_error"}`)
			return
		}
		resp := map[string]any{
			"access_token": "provider-access", "token_type": "bearer",
			"expires_in": 3600, "refresh_token": "provider-refresh",
		}
		if !omit {
			if raw == "" {
				raw = p.mintIDToken(extra)
			}
			resp["id_token"] = raw
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
	return p
}

func (p *fakeOIDC) set(f func(*fakeOIDC)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	f(p)
}

// mintIDToken signs an RS256 JWT the way the provider's key set can verify.
func (p *fakeOIDC) mintIDToken(extra map[string]any) string {
	claims := map[string]any{
		"iss": p.srv.URL,
		"aud": "orrery",
		"sub": "sub-1",
		"iat": time.Now().Unix(),
		"exp": time.Now().Add(time.Hour).Unix(),
	}
	for k, v := range extra {
		claims[k] = v
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","kid":"test-key"}`))
	payload, err := json.Marshal(claims)
	if err != nil {
		p.t.Fatal(err)
	}
	signing := header + "." + base64.RawURLEncoding.EncodeToString(payload)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, p.key, crypto.SHA256, sum[:])
	if err != nil {
		p.t.Fatal(err)
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func fakeOIDCConfig(issuer string) *config.Config {
	cfg := config.Default()
	cfg.Session.EncryptionKey = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="
	cfg.Session.Secure = false
	cfg.OIDC.Enabled = true
	cfg.OIDC.Issuer = issuer
	cfg.OIDC.ClientID = "orrery"
	cfg.OIDC.RedirectURL = "http://localhost:8080/api/v1/auth/callback"
	return cfg
}

// newFakeAuthenticator runs real OIDC discovery against the fake provider.
func newFakeAuthenticator(t *testing.T, mutate func(*config.Config)) (*Authenticator, *fakeOIDC, *SessionManager) {
	t.Helper()
	p := newFakeOIDC(t)
	cfg := fakeOIDCConfig(p.srv.URL)
	if mutate != nil {
		mutate(cfg)
	}
	store := NewMemoryStore(time.Hour)
	t.Cleanup(func() { _ = store.Close() })
	sessions, err := NewSessionManager(cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	a, err := NewAuthenticator(context.Background(), cfg, sessions)
	if err != nil {
		t.Fatal(err)
	}
	return a, p, sessions
}

func TestNewAuthenticatorAdoptsDiscoveredEndSession(t *testing.T) {
	a, p, _ := newFakeAuthenticator(t, nil)
	if a.endSessionURL != p.srv.URL+"/logout" {
		t.Errorf("endSessionURL = %q", a.endSessionURL)
	}

	// An operator-configured URL must win over discovery.
	b, _, _ := newFakeAuthenticator(t, func(cfg *config.Config) {
		cfg.OIDC.EndSessionURL = "https://logout.example/end"
	})
	if b.endSessionURL != "https://logout.example/end" {
		t.Errorf("configured endSessionURL was overridden: %q", b.endSessionURL)
	}
}

func TestNewAuthenticatorFailsWithoutDiscovery(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()

	cfg := fakeOIDCConfig(srv.URL)
	sessions := newTestManager(t)
	if _, err := NewAuthenticator(context.Background(), cfg, sessions); err == nil {
		t.Error("an issuer with no discovery document was accepted")
	}
}

// startLogin drives Login and hands back the state cookie plus the query the
// provider would receive.
func startLogin(t *testing.T, a *Authenticator, returnTo string) (*http.Cookie, url.Values) {
	t.Helper()
	target := "/api/v1/auth/login"
	if returnTo != "" {
		target += "?returnTo=" + url.QueryEscape(returnTo)
	}
	rec := httptest.NewRecorder()
	a.Login(rec, httptest.NewRequest(http.MethodGet, target, nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("login did not redirect: %d", rec.Code)
	}
	loc, err := url.Parse(rec.Result().Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	var state *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == stateCookieName {
			state = c
		}
	}
	if state == nil {
		t.Fatal("login set no state cookie")
	}
	return state, loc.Query()
}

func TestLoginRedirectsToProviderWithPKCE(t *testing.T) {
	a, _, _ := newFakeAuthenticator(t, nil)
	cookie, q := startLogin(t, a, "/c/prod/pods")

	if !cookie.HttpOnly {
		t.Error("the state cookie must be HttpOnly")
	}
	if q.Get("client_id") != "orrery" || q.Get("response_type") != "code" {
		t.Errorf("core parameters wrong: %v", q)
	}
	if q.Get("code_challenge") == "" || q.Get("code_challenge_method") != "S256" {
		t.Error("PKCE challenge missing; a stolen code would be replayable")
	}
	// Default config asks for offline access so sessions outlive ID tokens.
	if q.Get("access_type") != "offline" {
		t.Errorf("access_type = %q", q.Get("access_type"))
	}
	if q.Get("nonce") == "" {
		t.Error("no nonce; the id_token could be replayed across logins")
	}

	// The cookie payload must carry the same state the provider sees, or the
	// callback could never bind the two halves of the flow together.
	plain, err := a.codec.Decode(cookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	var ls loginState
	if err := json.Unmarshal([]byte(plain), &ls); err != nil {
		t.Fatal(err)
	}
	if ls.State != q.Get("state") || ls.Nonce != q.Get("nonce") {
		t.Error("cookie state does not match the authorize request")
	}
	if ls.ReturnTo != "/c/prod/pods" {
		t.Errorf("ReturnTo = %q", ls.ReturnTo)
	}
}

// completeCallback plays the provider redirect back into Callback.
func completeCallback(t *testing.T, a *Authenticator, cookie *http.Cookie, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/callback?"+query, nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	a.Callback(rec, req)
	return rec
}

func TestLoginCallbackEstablishesSession(t *testing.T) {
	a, p, sessions := newFakeAuthenticator(t, nil)
	cookie, q := startLogin(t, a, "/c/prod/pods")

	p.set(func(p *fakeOIDC) {
		p.extraClaims = map[string]any{
			"nonce": q.Get("nonce"), "email": "alice@example.com",
			"name": "Alice", "groups": []any{"devs"},
		}
	})
	rec := completeCallback(t, a, cookie,
		"code=good&state="+url.QueryEscape(q.Get("state")))

	if rec.Code != http.StatusFound {
		t.Fatalf("callback did not redirect: %d %s", rec.Code, rec.Result().Header.Get("Location"))
	}
	if loc := rec.Result().Header.Get("Location"); loc != "/c/prod/pods" {
		t.Errorf("redirected to %q, want the returnTo target", loc)
	}

	var sessCookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == "orrery_session" {
			sessCookie = c
		}
	}
	if sessCookie == nil {
		t.Fatal("no session cookie was set")
	}
	id, err := a.codec.Decode(sessCookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := sessions.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	// Default mapping: email claim → bare address, groups get the oidc: prefix.
	if sess.User.Username != "alice@example.com" {
		t.Errorf("username = %q", sess.User.Username)
	}
	if len(sess.User.Groups) != 1 || sess.User.Groups[0] != "oidc:devs" {
		t.Errorf("groups = %v", sess.User.Groups)
	}
	if sess.IDToken == "" || sess.AccessToken != "provider-access" || sess.RefreshToken != "provider-refresh" {
		t.Error("the bearer material must be persisted for refresh and passthrough")
	}
}

func failReason(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	loc := rec.Result().Header.Get("Location")
	if rec.Code != http.StatusFound || !strings.HasPrefix(loc, "/login?error=") {
		t.Fatalf("expected an error redirect, got %d %q", rec.Code, loc)
	}
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query().Get("error")
}

func TestCallbackRejectsBrokenState(t *testing.T) {
	// These paths run before any provider round trip, so a bare authenticator
	// with just the cookie codec exercises them.
	codec, err := NewCookieCodec(testKey(t))
	if err != nil {
		t.Fatal(err)
	}
	a := &Authenticator{codec: codec, sessCfg: config.Default().Session}

	sealed := func(v string) *http.Cookie {
		s, err := codec.Encode(v)
		if err != nil {
			t.Fatal(err)
		}
		return &http.Cookie{Name: stateCookieName, Value: s}
	}
	stale, _ := json.Marshal(loginState{
		State: "s", IssuedAt: time.Now().Add(-time.Hour).Unix(),
	})
	fresh, _ := json.Marshal(loginState{
		State: "expected", IssuedAt: time.Now().Unix(),
	})

	cases := []struct {
		name   string
		cookie *http.Cookie
		query  string
		want   string
	}{
		{"provider reported an error", nil, "error=access_denied&error_description=nope", "access_denied: nope"},
		{"missing state cookie", nil, "code=x&state=s", "login state missing or expired; please try again"},
		{"undecodable state cookie", &http.Cookie{Name: stateCookieName, Value: "garbage"}, "code=x&state=s", "login state is not valid"},
		{"state cookie is not JSON", sealed("not json"), "code=x&state=s", "login state is not valid"},
		{"login took too long", sealed(string(stale)), "code=x&state=s", "login took too long; please try again"},
		{"state mismatch", sealed(string(fresh)), "code=x&state=forged", "state mismatch"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := completeCallback(t, a, tc.cookie, tc.query)
			if got := failReason(t, rec); got != tc.want {
				t.Errorf("reason = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCallbackRejectsBadTokenResponses(t *testing.T) {
	a, p, _ := newFakeAuthenticator(t, nil)

	cases := []struct {
		name  string
		setup func(*fakeOIDC)
		want  string
	}{
		{"exchange fails", func(p *fakeOIDC) { p.tokenStatus = http.StatusBadRequest },
			"could not exchange authorization code"},
		{"no id_token", func(p *fakeOIDC) { p.omitIDToken = true },
			"provider returned no id_token"},
		{"unverifiable id_token", func(p *fakeOIDC) { p.rawIDToken = "garbage" },
			"id_token verification failed"},
		{"nonce mismatch", func(p *fakeOIDC) {
			p.extraClaims = map[string]any{"nonce": "not-the-login-nonce"}
		}, "nonce mismatch"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cookie, q := startLogin(t, a, "")
			p.set(func(p *fakeOIDC) {
				p.tokenStatus, p.omitIDToken, p.rawIDToken, p.extraClaims = 0, false, "", nil
				tc.setup(p)
			})
			rec := completeCallback(t, a, cookie,
				"code=good&state="+url.QueryEscape(q.Get("state")))
			if got := failReason(t, rec); got != tc.want {
				t.Errorf("reason = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestUserFromTokenMapping(t *testing.T) {
	// One real verifier mints *oidc.IDToken values; the mapping itself is then
	// tested against lightweight per-case configurations.
	a, p, _ := newFakeAuthenticator(t, nil)
	verify := func(extra map[string]any) *User {
		t.Helper()
		idTok, err := a.verifier.Verify(context.Background(), p.mintIDToken(extra))
		if err != nil {
			t.Fatal(err)
		}
		u, err := (&Authenticator{cfg: config.OIDCConfig{
			UsernameClaim: "email", GroupsClaim: "groups", GroupsPrefix: "oidc:",
		}}).userFromToken(idTok)
		if err != nil {
			t.Fatal(err)
		}
		return u
	}

	u := verify(map[string]any{
		"email": "alice@example.com", "name": "Alice",
		"picture": "https://img.example/a.png", "groups": []any{"devs", "sre"},
	})
	if u.Username != "alice@example.com" || u.Name != "Alice" || u.Picture == "" {
		t.Errorf("profile claims not mapped: %+v", u)
	}
	if len(u.Groups) != 2 || u.Groups[0] != "oidc:devs" {
		t.Errorf("groups not prefixed: %v", u.Groups)
	}

	// No username claim in the token: fall back to the stable subject.
	if u := verify(nil); u.Username != "sub-1" {
		t.Errorf("missing claim should fall back to sub, got %q", u.Username)
	}
}

func TestUserFromTokenEnforcesAccessRules(t *testing.T) {
	a, p, _ := newFakeAuthenticator(t, nil)
	idTok, err := a.verifier.Verify(context.Background(),
		p.mintIDToken(map[string]any{"hd": "other.example", "groups": []any{"guests"}}))
	if err != nil {
		t.Fatal(err)
	}

	gate := &Authenticator{cfg: config.OIDCConfig{
		GroupsClaim:    "groups",
		RequiredClaims: map[string]string{"hd": "example.com"},
	}}
	if _, err := gate.userFromToken(idTok); err == nil {
		t.Error("a mismatched required claim must refuse access")
	}

	groups := &Authenticator{cfg: config.OIDCConfig{
		GroupsClaim:   "groups",
		AllowedGroups: []string{"devs"},
	}}
	if _, err := groups.userFromToken(idTok); err == nil {
		t.Error("a user outside every allowed group must be refused")
	}
}

func TestRefreshRequiresRefreshToken(t *testing.T) {
	a := &Authenticator{}
	if err := a.Refresh(context.Background(), &Session{}); err == nil {
		t.Error("a session without a refresh token cannot be refreshed")
	}
}

func TestRefreshFailsWhenSessionGone(t *testing.T) {
	// Refresh re-reads the store; a signed-out session must not come back.
	m := newTestManager(t)
	a := &Authenticator{sessions: m}
	err := a.Refresh(context.Background(), &Session{ID: "gone", RefreshToken: "r"})
	if err == nil || !strings.Contains(err.Error(), "session gone") {
		t.Errorf("err = %v", err)
	}
}

func TestRefreshVerifiesAndAdoptsNewIDToken(t *testing.T) {
	a, p, m := newFakeAuthenticator(t, nil)
	sess := staleSession(t, m)

	if err := a.Refresh(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	if sess.IDToken == "" {
		t.Error("a refreshed id_token must replace the stored one")
	}
	if sess.AccessToken != "provider-access" {
		t.Errorf("access token not adopted: %q", sess.AccessToken)
	}

	// A provider handing back a token we cannot verify must fail the refresh
	// rather than store an id_token passthrough clusters would then trust.
	sess2 := staleSession(t, m)
	p.set(func(p *fakeOIDC) { p.rawIDToken = "garbage" })
	if err := a.Refresh(context.Background(), sess2); err == nil {
		t.Error("an unverifiable refreshed id_token was accepted")
	}
}

func TestLogoutDestroysSessionAndBuildsEndSessionURL(t *testing.T) {
	a, p, m := newFakeAuthenticator(t, nil)
	sess, err := m.NewSession(context.Background(), User{Username: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	sess.IDToken = "the-id-token"
	if err := m.Save(context.Background(), sess); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	a.Logout(rec, authedRequest(t, m, sess))

	if _, err := m.Get(context.Background(), sess.ID); err == nil {
		t.Error("logout must destroy the session")
	}
	var body struct {
		LoggedOut     bool   `json:"loggedOut"`
		EndSessionURL string `json:"endSessionURL"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.LoggedOut {
		t.Error("loggedOut missing")
	}
	u, err := url.Parse(body.EndSessionURL)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(body.EndSessionURL, p.srv.URL+"/logout") {
		t.Errorf("endSessionURL = %q", body.EndSessionURL)
	}
	if u.Query().Get("id_token_hint") != "the-id-token" || u.Query().Get("client_id") != "orrery" {
		t.Errorf("RP-initiated logout parameters wrong: %q", body.EndSessionURL)
	}
}

func TestLogoutWithoutEndSessionEndpoint(t *testing.T) {
	m := newTestManager(t)
	for name, endSession := range map[string]string{
		"none configured": "",
		// A broken URL degrades to a local-only logout instead of failing.
		"unparseable": "://bad",
	} {
		t.Run(name, func(t *testing.T) {
			a := &Authenticator{sessions: m, endSessionURL: endSession}
			rec := httptest.NewRecorder()
			a.Logout(rec, httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil))

			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body["loggedOut"] != true {
				t.Error("loggedOut missing")
			}
			if _, ok := body["endSessionURL"]; ok {
				t.Error("no endSessionURL should be offered")
			}
		})
	}
}

func TestStringClaim(t *testing.T) {
	claims := map[string]any{"email": "a@example.com", "count": 3}
	if got := stringClaim(claims, "email"); got != "a@example.com" {
		t.Errorf("got %q", got)
	}
	if got := stringClaim(claims, ""); got != "" {
		t.Errorf("empty key must map to nothing, got %q", got)
	}
	if got := stringClaim(claims, "count"); got != "" {
		t.Errorf("non-string claim must map to nothing, got %q", got)
	}
	if got := stringClaim(claims, "absent"); got != "" {
		t.Errorf("absent claim must map to nothing, got %q", got)
	}
}

func TestAnyOverlap(t *testing.T) {
	if !anyOverlap([]string{"a", "b"}, []string{"b", "c"}) {
		t.Error("shared member not detected")
	}
	if anyOverlap([]string{"a"}, []string{"b"}) {
		t.Error("disjoint sets reported as overlapping")
	}
	if anyOverlap(nil, []string{"b"}) {
		t.Error("empty membership cannot overlap")
	}
}
