package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	oidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
	"golang.org/x/sync/singleflight"

	"github.com/daiwa-zou/orrery/internal/config"
)

// stateCookieName holds the in-flight login. Keeping the state, nonce and PKCE
// verifier in an encrypted cookie rather than server memory means any replica
// can complete a login that another replica started.
const stateCookieName = "orrery_oidc_state"

const stateTTL = 10 * time.Minute

// refreshTimeout bounds one token exchange. It outlives the request that
// started it — that is the point — so it needs a limit of its own; without one
// a provider that never answers holds a goroutine for the life of the process.
const refreshTimeout = 30 * time.Second

// loginState is the sealed payload carried across the provider round trip.
type loginState struct {
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
	ReturnTo string `json:"r"`
	IssuedAt int64  `json:"t"`
}

// Authenticator drives the OIDC authorization-code flow with PKCE.
type Authenticator struct {
	cfg      config.OIDCConfig
	sessCfg  config.SessionConfig
	provider *oidc.Provider
	verifier *oidc.IDTokenVerifier
	oauth    *oauth2.Config
	codec    *CookieCodec
	sessions *SessionManager

	// endSessionURL is taken from the discovery document when the operator did
	// not configure one.
	endSessionURL string

	// refreshSF collapses concurrent refreshes of the same session (keyed by
	// session ID); see Refresh for why that matters with rotating providers.
	refreshSF singleflight.Group
}

// NewAuthenticator performs OIDC discovery and builds the flow configuration.
func NewAuthenticator(ctx context.Context, cfg *config.Config, sessions *SessionManager) (*Authenticator, error) {
	key, err := cfg.SessionKey()
	if err != nil {
		return nil, err
	}
	codec, err := NewCookieCodec(key)
	if err != nil {
		return nil, err
	}

	pctx := ctx
	if cfg.OIDC.InsecureSkipIssuerVerify {
		pctx = oidc.InsecureIssuerURLContext(ctx, cfg.OIDC.Issuer)
	}
	provider, err := oidc.NewProvider(pctx, cfg.OIDC.Issuer)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery for %s: %w", cfg.OIDC.Issuer, err)
	}

	a := &Authenticator{
		cfg:      cfg.OIDC,
		sessCfg:  cfg.Session,
		provider: provider,
		verifier: provider.Verifier(&oidc.Config{ClientID: cfg.OIDC.ClientID}),
		oauth: &oauth2.Config{
			ClientID:     cfg.OIDC.ClientID,
			ClientSecret: cfg.OIDC.ClientSecret,
			RedirectURL:  cfg.OIDC.RedirectURL,
			Endpoint:     provider.Endpoint(),
			Scopes:       cfg.OIDC.Scopes,
		},
		codec:         codec,
		sessions:      sessions,
		endSessionURL: cfg.OIDC.EndSessionURL,
	}

	if a.endSessionURL == "" {
		var meta struct {
			EndSessionEndpoint string `json:"end_session_endpoint"`
		}
		if err := provider.Claims(&meta); err == nil {
			a.endSessionURL = meta.EndSessionEndpoint
		}
	}
	return a, nil
}

// Login starts the authorization-code flow.
func (a *Authenticator) Login(w http.ResponseWriter, r *http.Request) {
	state, err := randomToken(24)
	if err != nil {
		http.Error(w, "failed to start login", http.StatusInternalServerError)
		return
	}
	nonce, err := randomToken(24)
	if err != nil {
		http.Error(w, "failed to start login", http.StatusInternalServerError)
		return
	}
	verifier := oauth2.GenerateVerifier()

	ls := loginState{
		State:    state,
		Nonce:    nonce,
		Verifier: verifier,
		ReturnTo: safeReturnTo(r.URL.Query().Get("returnTo")),
		IssuedAt: time.Now().Unix(),
	}
	raw, err := json.Marshal(ls)
	if err != nil {
		http.Error(w, "failed to start login", http.StatusInternalServerError)
		return
	}
	sealed, err := a.codec.Encode(string(raw))
	if err != nil {
		http.Error(w, "failed to start login", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     stateCookieName,
		Value:    sealed,
		Path:     "/",
		Domain:   a.sessCfg.Domain,
		MaxAge:   int(stateTTL.Seconds()),
		HttpOnly: true,
		Secure:   a.sessCfg.Secure,
		SameSite: http.SameSiteLaxMode,
	})

	opts := []oauth2.AuthCodeOption{
		oidc.Nonce(nonce),
		oauth2.S256ChallengeOption(verifier),
	}
	// A refresh token generally requires consent to have been granted; ask for
	// it explicitly so long-lived sessions do not silently break.
	if a.cfg.OfflineAccess {
		opts = append(opts, oauth2.AccessTypeOffline)
	}
	http.Redirect(w, r, a.oauth.AuthCodeURL(state, opts...), http.StatusFound)
}

// Callback completes the flow and establishes a session.
func (a *Authenticator) Callback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if errCode := q.Get("error"); errCode != "" {
		a.fail(w, r, fmt.Sprintf("%s: %s", errCode, q.Get("error_description")))
		return
	}

	c, err := r.Cookie(stateCookieName)
	if err != nil {
		a.fail(w, r, "login state missing or expired; please try again")
		return
	}
	// The state cookie is single-use.
	http.SetCookie(w, &http.Cookie{
		Name: stateCookieName, Value: "", Path: "/", Domain: a.sessCfg.Domain,
		MaxAge: -1, HttpOnly: true, Secure: a.sessCfg.Secure, SameSite: http.SameSiteLaxMode,
	})

	plain, err := a.codec.Decode(c.Value)
	if err != nil {
		a.fail(w, r, "login state is not valid")
		return
	}
	var ls loginState
	if err := json.Unmarshal([]byte(plain), &ls); err != nil {
		a.fail(w, r, "login state is not valid")
		return
	}
	if time.Since(time.Unix(ls.IssuedAt, 0)) > stateTTL {
		a.fail(w, r, "login took too long; please try again")
		return
	}
	if !subtleCompare(ls.State, q.Get("state")) {
		a.fail(w, r, "state mismatch")
		return
	}

	token, err := a.oauth.Exchange(r.Context(), q.Get("code"), oauth2.VerifierOption(ls.Verifier))
	if err != nil {
		a.fail(w, r, "could not exchange authorization code")
		return
	}
	rawID, ok := token.Extra("id_token").(string)
	if !ok || rawID == "" {
		a.fail(w, r, "provider returned no id_token")
		return
	}
	idToken, err := a.verifier.Verify(r.Context(), rawID)
	if err != nil {
		a.fail(w, r, "id_token verification failed")
		return
	}
	if idToken.Nonce != ls.Nonce {
		a.fail(w, r, "nonce mismatch")
		return
	}

	user, err := a.userFromToken(idToken)
	if err != nil {
		a.fail(w, r, err.Error())
		return
	}

	sess, err := a.sessions.NewSession(r.Context(), *user)
	if err != nil {
		a.fail(w, r, "could not create session")
		return
	}
	sess.IDToken = rawID
	sess.AccessToken = token.AccessToken
	sess.RefreshToken = token.RefreshToken
	sess.TokenExpiry = token.Expiry
	if err := a.sessions.Save(r.Context(), sess); err != nil {
		a.fail(w, r, "could not persist session")
		return
	}
	if err := a.sessions.SetCookies(w, sess); err != nil {
		a.fail(w, r, "could not set session cookie")
		return
	}
	http.Redirect(w, r, ls.ReturnTo, http.StatusFound)
}

// userFromToken applies the configured claim mapping and access rules.
func (a *Authenticator) userFromToken(idToken *oidc.IDToken) (*User, error) {
	var claims map[string]any
	if err := idToken.Claims(&claims); err != nil {
		return nil, errors.New("could not read token claims")
	}

	// The three ways a required claim can refuse a login are three different
	// facts, and this text is what the person sees on the login page — it is
	// all they get, since they cannot read the server's logs. It said "claim
	// %q does not permit access" for every one of them.
	//
	// The type mismatch is the one that mattered most. requiredClaims compares
	// text, the way kube-apiserver's --oidc-required-claim does, so a perfectly
	// reasonable `email_verified: "true"` against a boolean claim locked
	// everybody out permanently and blamed each of them for it in turn. No
	// claim values appear here: the key, and the shape, are enough to fix it.
	for k, want := range a.cfg.RequiredClaims {
		raw, present := claims[k]
		got, isText := raw.(string)
		switch {
		case !present:
			return nil, fmt.Errorf("your token does not carry the %q claim this dashboard requires", k)
		case !isText:
			return nil, fmt.Errorf(
				"the %q claim is a %T and requiredClaims compares text — "+
					"this is a configuration problem, not a permission one", k, raw)
		case got != want:
			return nil, fmt.Errorf("the %q claim does not permit access", k)
		}
	}

	rawName := stringClaim(claims, a.cfg.UsernameClaim)
	if rawName == "" {
		rawName = idToken.Subject
	}
	groups := stringSliceClaim(claims, a.cfg.GroupsClaim)

	if len(a.cfg.AllowedGroups) > 0 && !anyOverlap(groups, a.cfg.AllowedGroups) {
		return nil, errors.New("you are not a member of a group permitted to sign in")
	}

	u := &User{
		Subject: idToken.Subject,
		// Mirrors kube-apiserver's mapper: a configured prefix always applies,
		// "-" explicitly disables prefixing, and only the *default* differs by
		// claim — no prefix for email, issuer# for anything else. Skipping a
		// configured prefix for email claims would impersonate the wrong
		// identity against RBAC bindings written for the prefixed form.
		Username: a.mapUsername(rawName),
		Email:    stringClaim(claims, "email"),
		Name:     stringClaim(claims, "name"),
		Picture:  stringClaim(claims, "picture"),
	}
	for _, g := range groups {
		u.Groups = append(u.Groups, applyPrefix(a.cfg.GroupsPrefix, g))
	}
	return u, nil
}

// applyPrefix mirrors kube-apiserver claim-prefix semantics: an explicitly
// configured prefix always applies, and "-" means "no prefix".
func applyPrefix(prefix, value string) string {
	if prefix == "" || prefix == "-" {
		return value
	}
	return prefix + value
}

// mapUsername applies the configured username prefix, defaulting the way the
// apiserver does when none is set: bare for the email claim, issuer-qualified
// for every other claim so usernames from different issuers cannot collide.
func (a *Authenticator) mapUsername(rawName string) string {
	if a.cfg.UsernamePrefix != "" {
		return applyPrefix(a.cfg.UsernamePrefix, rawName)
	}
	if a.cfg.UsernameClaim == "email" {
		return rawName
	}
	return a.cfg.Issuer + "#" + rawName
}

// Refresh renews an access/ID token that is close to expiry. Concurrent
// refreshes of the same session collapse into one provider round trip —
// presenting the same refresh token twice trips providers with rotation reuse
// detection, which invalidates the whole token family and logs the user out.
// Different sessions refresh independently, so one user's slow provider call
// does not queue everyone else's.
func (a *Authenticator) Refresh(ctx context.Context, s *Session) error {
	if s.RefreshToken == "" {
		return errors.New("session has no refresh token")
	}

	// The exchange runs on a context detached from whichever request happened
	// to start it, because abandoning a token exchange half-way is the exact
	// thing the note above says must not happen. The provider may already have
	// rotated the refresh token when the cancellation lands; the new one then
	// never reaches the store, the session keeps the spent one, and the next
	// refresh is answered with invalid_grant — which RefreshFatal correctly
	// reads as "the grant is gone" and signs the user out. A browser tab
	// closing during a refresh is enough, and the session it ends may belong
	// to another tab entirely.
	//
	// Callers still wait on their own context, so nobody is held by a request
	// they no longer care about.
	ch := a.refreshSF.DoChan(s.ID, func() (any, error) {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), refreshTimeout)
		defer cancel()

		// Re-read from the store: s is this caller's private copy, so checking
		// it again learns nothing. Another request may have refreshed while we
		// waited to enter the group.
		fresh, err := a.sessions.Get(rctx, s.ID)
		if err != nil {
			return nil, fmt.Errorf("session gone: %w", err)
		}
		if !fresh.TokenExpiry.IsZero() && time.Until(fresh.TokenExpiry) > time.Minute {
			return fresh, nil
		}

		src := a.oauth.TokenSource(rctx, &oauth2.Token{RefreshToken: fresh.RefreshToken})
		tok, err := src.Token()
		if err != nil {
			return nil, fmt.Errorf("refresh token: %w", err)
		}
		if raw, ok := tok.Extra("id_token").(string); ok && raw != "" {
			if _, err := a.verifier.Verify(rctx, raw); err != nil {
				return nil, fmt.Errorf("refreshed id_token invalid: %w", err)
			}
			fresh.IDToken = raw
		}
		fresh.AccessToken = tok.AccessToken
		if tok.RefreshToken != "" {
			fresh.RefreshToken = tok.RefreshToken
		}
		fresh.TokenExpiry = tok.Expiry
		if err := a.sessions.Save(rctx, fresh); err != nil {
			return nil, err
		}
		return fresh, nil
	})

	select {
	case <-ctx.Done():
		return ctx.Err()
	case r := <-ch:
		if r.Err != nil {
			return r.Err
		}
		// Cloned, not struct-copied. Deduplication means every caller waiting
		// on this key is handed the *same* *Session, so a plain copy leaves
		// each of their sessions pointing at one User.Groups backing array —
		// which is the precise arrangement Clone exists to prevent, and its
		// comment says why it matters here: Groups becomes the impersonation
		// header and the SubjectAccessReview subject, so a group written
		// through an alias is a group granted to whoever else holds a copy.
		//
		// A hazard rather than a live bug, and worth being exact about which.
		// An append cannot cross sessions here: Clone and both Stores build
		// Groups with append([]string(nil), ...), so capacity equals length
		// and appending always reallocates. What sharing still exposes is a
		// write through an index and a sort in place — neither of which
		// anything does today.
		//
		// That is the reason to close it now rather than when it bites. It
		// takes two concurrent refreshes of one session to arise at all, every
		// other path through the store hands back independent copies, and the
		// capacity that makes appends safe is an accident of how the slices
		// happen to be built rather than anything stated. A future sort would
		// look correct everywhere it was tested.
		*s = *r.Val.(*Session).Clone()
		return nil
	}
}

// RefreshFatal reports whether a refresh failure is definitive — the grant
// itself is gone — rather than a transient provider hiccup. Callers destroy
// the session on a fatal failure and keep serving on a transient one, so an
// identity provider blip does not sign everyone out.
func RefreshFatal(err error) bool {
	if err == nil {
		return false
	}
	var rerr *oauth2.RetrieveError
	if errors.As(err, &rerr) {
		// invalid_grant is the spec's "this refresh token is expired, revoked
		// or already rotated out". Any other 4xx except 429 is a request the
		// provider understood and refused; retrying will not change its mind.
		if rerr.ErrorCode == "invalid_grant" {
			return true
		}
		if rerr.Response != nil {
			code := rerr.Response.StatusCode
			return code >= 400 && code < 500 && code != http.StatusTooManyRequests
		}
	}
	return false
}

// NeedsRefresh reports whether the session's tokens are near or past expiry.
func NeedsRefresh(s *Session) bool {
	return s.RefreshToken != "" && !s.TokenExpiry.IsZero() && time.Until(s.TokenExpiry) < time.Minute
}

// Logout destroys the session and, when the provider supports it, ends the
// session upstream too.
func (a *Authenticator) Logout(w http.ResponseWriter, r *http.Request) {
	var idToken string
	if s, err := a.sessions.FromRequest(r); err == nil {
		idToken = s.IDToken
		_ = a.sessions.Destroy(r.Context(), s.ID)
	}
	a.sessions.ClearCookies(w)

	if a.endSessionURL == "" {
		writeJSON(w, http.StatusOK, map[string]any{"loggedOut": true})
		return
	}
	u, err := url.Parse(a.endSessionURL)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"loggedOut": true})
		return
	}
	q := u.Query()
	if idToken != "" {
		q.Set("id_token_hint", idToken)
	}
	q.Set("client_id", a.cfg.ClientID)
	u.RawQuery = q.Encode()
	writeJSON(w, http.StatusOK, map[string]any{"loggedOut": true, "endSessionURL": u.String()})
}

// fail redirects back to the SPA with a human-readable reason rather than
// rendering a bare error page mid-flow.
func (a *Authenticator) fail(w http.ResponseWriter, r *http.Request, reason string) {
	target := "/login?error=" + url.QueryEscape(reason)
	http.Redirect(w, r, target, http.StatusFound)
}

// safeReturnTo blocks open redirects by accepting only same-site paths.
// "/\evil.example.com" counts as protocol-relative too: browsers normalise the
// backslash to a slash in the authority position.
func safeReturnTo(v string) string {
	const home = "/"
	if v == "" || !strings.HasPrefix(v, "/") {
		return home
	}
	// A backslash is a slash to a browser, so "/\\evil.example" is read as
	// "//evil.example" — a protocol-relative URL pointing off-site. Nothing
	// this console links to contains one.
	if strings.Contains(v, "\\") {
		return home
	}
	// Browsers strip ASCII tab, CR and LF out of a URL before parsing it, so a
	// value that looks like a path to this function can be something else
	// entirely by the time it is followed: "/\t/evil.example" arrives at the
	// browser as "//evil.example". Go's header writer turns CR and LF into
	// spaces on the way out, but not tab, so the check has to happen here.
	// Every other control character is equally unwelcome in a path.
	for _, c := range v {
		if c < 0x20 || c == 0x7f {
			return home
		}
	}
	// A second slash in the authority position is refused here rather than left
	// to url.Parse, because on this exact question Go and browsers disagree.
	//
	// "//evil.example" parses with Host set and was caught below. "///evil.example"
	// does not: Go reads the third slash as an empty authority and hands back
	// Host "" and Path "/evil.example", so every check below passed and the
	// value was returned unchanged. A browser does not read it that way —
	// resolved against https://console.example.com/login, Chrome makes
	// "///evil.example" into https://evil.example/, exactly as it does the
	// two-slash form. So did "////evil.example". That is the open redirect this
	// function exists to prevent, reachable by adding one character to a string
	// it already knew to refuse.
	//
	// Counting slashes is the check that does not depend on a parser agreeing
	// with a browser about where an authority begins. A same-origin path starts
	// with exactly one slash; "/.//evil.example" keeps its dot segment and stays
	// on this origin, which the browser confirms.
	if len(v) > 1 && (v[1] == '/' || v[1] == '\\') {
		return home
	}
	// Whatever is left that still parses to a bare path — no scheme, no host —
	// is same-origin by construction.
	u, err := url.Parse(v)
	if err != nil || u.Scheme != "" || u.Host != "" || !strings.HasPrefix(u.Path, "/") {
		return home
	}
	return v
}

func stringClaim(claims map[string]any, key string) string {
	if key == "" {
		return ""
	}
	v, _ := claims[key].(string)
	return v
}

// stringSliceClaim tolerates providers that emit a single string where the
// spec allows an array.
func stringSliceClaim(claims map[string]any, key string) []string {
	if key == "" {
		return nil
	}
	switch v := claims[key].(type) {
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return v
	case string:
		if v == "" {
			return nil
		}
		return []string{v}
	}
	return nil
}

func anyOverlap(have, want []string) bool {
	set := make(map[string]struct{}, len(have))
	for _, h := range have {
		set[h] = struct{}{}
	}
	for _, w := range want {
		if _, ok := set[w]; ok {
			return true
		}
	}
	return false
}
