package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/daiwa-zou/orrery/internal/config"
)

func testKey(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	return key
}

func TestCookieCodecRoundTrip(t *testing.T) {
	codec, err := NewCookieCodec(testKey(t))
	if err != nil {
		t.Fatal(err)
	}

	token, err := codec.Encode("session-id-123")
	if err != nil {
		t.Fatal(err)
	}
	if token == "session-id-123" {
		t.Fatal("the session id must not appear in the cookie in the clear")
	}

	got, err := codec.Decode(token)
	if err != nil {
		t.Fatal(err)
	}
	if got != "session-id-123" {
		t.Errorf("Decode() = %q", got)
	}
}

func TestCookieCodecRejectsTampering(t *testing.T) {
	codec, err := NewCookieCodec(testKey(t))
	if err != nil {
		t.Fatal(err)
	}
	token, _ := codec.Encode("session-id-123")

	// Flip a byte in the ciphertext; GCM must refuse to open it.
	corrupted := []byte(token)
	corrupted[len(corrupted)-1] ^= 'A' ^ 'B'

	if _, err := codec.Decode(string(corrupted)); err == nil {
		t.Error("a tampered cookie was accepted")
	}
	if _, err := codec.Decode("not-base64!!"); err == nil {
		t.Error("garbage was accepted")
	}
	if _, err := codec.Decode(""); err == nil {
		t.Error("an empty cookie was accepted")
	}
}

func TestCookieCodecRejectsForeignKey(t *testing.T) {
	a, _ := NewCookieCodec(testKey(t))
	other := testKey(t)
	other[0] ^= 0xff
	b, _ := NewCookieCodec(other)

	token, _ := a.Encode("id")
	if _, err := b.Decode(token); err == nil {
		t.Error("a cookie sealed with a different key was accepted")
	}
}

func TestSessionExpiry(t *testing.T) {
	now := time.Now()
	s := &Session{
		CreatedAt: now.Add(-time.Hour),
		LastSeen:  now.Add(-time.Minute),
		ExpiresAt: now.Add(time.Hour),
	}

	if s.Expired(now, time.Hour) {
		t.Error("an active session was reported expired")
	}
	// Idle timeout shorter than the time since last use.
	if !s.Expired(now, 30*time.Second) {
		t.Error("an idle session should expire")
	}
	// Past its absolute lifetime.
	past := &Session{LastSeen: now, ExpiresAt: now.Add(-time.Second)}
	if !past.Expired(now, time.Hour) {
		t.Error("a session past its TTL should expire")
	}
	// Zero ExpiresAt means no absolute deadline.
	forever := &Session{LastSeen: now}
	if forever.Expired(now, 0) {
		t.Error("a session with no deadlines should not expire")
	}
}

func TestMemoryStoreEvictsExpired(t *testing.T) {
	store := NewMemoryStore(time.Millisecond)
	defer store.Close()

	ctx := context.Background()
	s := &Session{ID: "a", LastSeen: time.Now().Add(-time.Hour), ExpiresAt: time.Now().Add(time.Hour)}
	if err := store.Put(ctx, s); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Get(ctx, "a"); err != ErrNoSession {
		t.Errorf("expected an idle session to be gone, got %v", err)
	}
}

func TestMemoryStoreReturnsCopies(t *testing.T) {
	// Handlers mutate the session they are given (token refresh); that must
	// not race with another request reading the same session.
	store := NewMemoryStore(time.Hour)
	defer store.Close()

	ctx := context.Background()
	_ = store.Put(ctx, &Session{ID: "a", LastSeen: time.Now(), User: User{Username: "alice"}})

	got, err := store.Get(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	got.User.Username = "mallory"

	again, _ := store.Get(ctx, "a")
	if again.User.Username != "alice" {
		t.Error("mutating a returned session leaked into the store")
	}
}

func newTestManager(t *testing.T) *SessionManager {
	t.Helper()
	cfg := config.Default()
	cfg.Session.EncryptionKey = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="
	cfg.Session.Secure = false
	m, err := NewSessionManager(cfg, NewMemoryStore(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestSessionCookiesAreScopedCorrectly(t *testing.T) {
	m := newTestManager(t)
	sess, err := m.NewSession(context.Background(), User{Username: "alice"})
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	if err := m.SetCookies(rec, sess); err != nil {
		t.Fatal(err)
	}

	cookies := rec.Result().Cookies()
	byName := map[string]*http.Cookie{}
	for _, c := range cookies {
		byName[c.Name] = c
	}

	session := byName["orrery_session"]
	if session == nil {
		t.Fatal("no session cookie was set")
	}
	if !session.HttpOnly {
		t.Error("the session cookie must be HttpOnly so script cannot read it")
	}
	if session.Value == sess.ID {
		t.Error("the raw session id must not be the cookie value")
	}

	csrf := byName[CSRFCookieName]
	if csrf == nil {
		t.Fatal("no CSRF cookie was set")
	}
	if csrf.HttpOnly {
		t.Error("the CSRF cookie must be readable by script for double-submit to work")
	}
	if csrf.Value != sess.CSRFToken {
		t.Error("the CSRF cookie must carry the session's token")
	}
}

func TestFromRequestRejectsUnknownAndMissingCookies(t *testing.T) {
	m := newTestManager(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/clusters", nil)
	if _, err := m.FromRequest(req); err != ErrNoSession {
		t.Errorf("a request with no cookie gave %v", err)
	}

	req.AddCookie(&http.Cookie{Name: "orrery_session", Value: "bogus"})
	if _, err := m.FromRequest(req); err != ErrNoSession {
		t.Errorf("a request with a bogus cookie gave %v", err)
	}
}

func TestFromRequestResolvesRealSession(t *testing.T) {
	m := newTestManager(t)
	sess, _ := m.NewSession(context.Background(), User{Username: "alice", Groups: []string{"oidc:devs"}})

	rec := httptest.NewRecorder()
	_ = m.SetCookies(rec, sess)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/clusters", nil)
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}

	got, err := m.FromRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if got.User.Username != "alice" || len(got.User.Groups) != 1 {
		t.Errorf("resolved the wrong identity: %+v", got.User)
	}
}

func TestSessionsGetDistinctTokens(t *testing.T) {
	m := newTestManager(t)
	a, _ := m.NewSession(context.Background(), User{Username: "a"})
	b, _ := m.NewSession(context.Background(), User{Username: "b"})

	if a.ID == b.ID {
		t.Error("session ids collided")
	}
	if a.CSRFToken == b.CSRFToken {
		t.Error("CSRF tokens collided")
	}
}

func TestApplyPrefixMirrorsApiserverSemantics(t *testing.T) {
	if got := applyPrefix("oidc:", "alice"); got != "oidc:alice" {
		t.Errorf("prefix not applied: %q", got)
	}
	if got := applyPrefix("-", "alice"); got != "alice" {
		t.Errorf(`"-" should disable prefixing, got %q`, got)
	}
	if got := applyPrefix("", "alice"); got != "alice" {
		t.Errorf("empty prefix changed the value: %q", got)
	}
}

func TestMapUsernameMirrorsApiserverSemantics(t *testing.T) {
	mk := func(claim, prefix string) *Authenticator {
		return &Authenticator{cfg: config.OIDCConfig{
			Issuer: "https://issuer.example", UsernameClaim: claim, UsernamePrefix: prefix,
		}}
	}
	// A configured prefix ALWAYS applies — including to the email claim. The
	// apiserver's email special case only changes the default when no prefix
	// is set. Skipping a configured prefix would impersonate an identity no
	// RBAC binding names.
	if got := mk("email", "oidc:").mapUsername("alice@example.com"); got != "oidc:alice@example.com" {
		t.Errorf("configured prefix was not applied to email claim: %q", got)
	}
	if got := mk("email", "").mapUsername("alice@example.com"); got != "alice@example.com" {
		t.Errorf("default for email claim should be the bare address: %q", got)
	}
	if got := mk("sub", "").mapUsername("alice"); got != "https://issuer.example#alice" {
		t.Errorf("default for non-email claims should be issuer-qualified: %q", got)
	}
	if got := mk("sub", "-").mapUsername("alice"); got != "alice" {
		t.Errorf(`"-" should disable prefixing entirely: %q`, got)
	}
}

func TestStringSliceClaimToleratesProviderVariation(t *testing.T) {
	cases := []struct {
		name   string
		claims map[string]any
		want   int
	}{
		{"array of strings", map[string]any{"groups": []any{"a", "b"}}, 2},
		{"single string", map[string]any{"groups": "a"}, 1},
		{"native slice", map[string]any{"groups": []string{"a", "b", "c"}}, 3},
		{"absent", map[string]any{}, 0},
		{"empty string", map[string]any{"groups": ""}, 0},
		{"wrong type", map[string]any{"groups": 42}, 0},
		{"mixed junk is filtered", map[string]any{"groups": []any{"a", 7, "", "b"}}, 2},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := stringSliceClaim(tc.claims, "groups"); len(got) != tc.want {
				t.Errorf("got %v, want %d entries", got, tc.want)
			}
		})
	}
}

func TestSafeReturnToBlocksOpenRedirects(t *testing.T) {
	cases := map[string]string{
		"/c/prod/r/core/v1/pods":                "/c/prod/r/core/v1/pods",
		"/c/prod/r/core/v1/pods?namespace=demo": "/c/prod/r/core/v1/pods?namespace=demo",
		"/c/prod#tab":                           "/c/prod#tab",
		"":                                      "/",
		"https://evil.example.com":              "/",
		"//evil.example.com":                    "/",
		`/\evil.example.com`:                    "/",
		"javascript:alert(1)":                   "/",
		// Not a path at all, however it is dressed up.
		"http:/evil.example.com": "/",
		"c/prod":                 "/",
	}
	for in, want := range cases {
		if got := safeReturnTo(in); got != want {
			t.Errorf("safeReturnTo(%q) = %q, want %q", in, got, want)
		}
	}
}

// Browsers remove ASCII tab, CR and LF from a URL before parsing it, so a
// value that reads as a path here is a different URL by the time it is
// followed: "/\t/evil.example" arrives at the browser as "//evil.example",
// which is protocol-relative and lands off-site.
//
// Go's header writer replaces CR and LF with spaces on the way out, which
// happens to defuse those two, but it leaves tab alone — the tab reaches the
// Location header intact. So this cannot be left to the transport.
func TestSafeReturnToRejectsControlCharacters(t *testing.T) {
	for _, in := range []string{
		"/\t/evil.example", // the live one: tab, then the second slash
		"/\tevil.example",
		"/\n//evil.example",
		"/\r\n/evil.example",
		"/c/prod\t/../..//evil.example",
		"/\x00/evil.example",
		"/\x7f//evil.example",
		"/c/prod\u000b//evil.example", // vertical tab
	} {
		if got := safeReturnTo(in); got != "/" {
			t.Errorf("safeReturnTo(%q) = %q, want %q", in, got, "/")
		}
	}
}

// The Location header a real server writes must not carry anything a browser
// would strip: that is the property the check above exists to guarantee, and
// asserting it here means a future relaxation of safeReturnTo is caught by
// what actually goes on the wire.
func TestReturnToNeverReachesTheWireStrippable(t *testing.T) {
	for _, payload := range []string{"/\t/evil.example", "/\n//evil.example", "//evil.example", "/c/ok"} {
		target := safeReturnTo(payload)
		rec := httptest.NewRecorder()
		http.Redirect(rec, httptest.NewRequest(http.MethodGet, "/", nil), target, http.StatusFound)

		loc := rec.Header().Get("Location")
		if strings.ContainsAny(loc, "\t\r\n") {
			t.Errorf("payload %q produced Location %q, which a browser would rewrite", payload, loc)
		}
		if strings.HasPrefix(loc, "//") {
			t.Errorf("payload %q produced a protocol-relative Location %q", payload, loc)
		}
	}
}

func TestOriginAllowed(t *testing.T) {
	public := "https://orrery.example.com"

	if !OriginAllowed("", public, nil) {
		t.Error("a handshake with no Origin (non-browser client) should be allowed")
	}
	if !OriginAllowed(public, public, nil) {
		t.Error("the configured public URL must be allowed")
	}
	if OriginAllowed("https://evil.example.com", public, nil) {
		t.Error("an unrelated origin was allowed")
	}
	if !OriginAllowed("http://localhost:5173", public, []string{"http://localhost:5173"}) {
		t.Error("an explicitly configured extra origin should be allowed")
	}
}

func TestNeedsRefresh(t *testing.T) {
	if NeedsRefresh(&Session{}) {
		t.Error("a session with no refresh token cannot be refreshed")
	}
	if NeedsRefresh(&Session{RefreshToken: "r", TokenExpiry: time.Now().Add(time.Hour)}) {
		t.Error("a token valid for another hour does not need refreshing")
	}
	if !NeedsRefresh(&Session{RefreshToken: "r", TokenExpiry: time.Now().Add(10 * time.Second)}) {
		t.Error("a token about to expire should be refreshed")
	}
}
