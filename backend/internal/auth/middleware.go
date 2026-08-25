package auth

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// Middleware resolves the session cookie into a request-scoped identity and
// rejects unauthenticated calls. It also refreshes tokens that are about to
// expire so long-lived tabs keep working.
type Middleware struct {
	sessions *SessionManager
	auth     *Authenticator
	// anonymous is used when OIDC is disabled: every request runs as this
	// identity. Clusters in impersonation mode will impersonate it, so it must
	// be bound in RBAC for anything to work.
	anonymous *User
}

// NewMiddleware builds the auth middleware. Pass a nil authenticator together
// with a non-nil anonymous user to run without OIDC.
func NewMiddleware(sessions *SessionManager, auth *Authenticator, anonymous *User) *Middleware {
	return &Middleware{sessions: sessions, auth: auth, anonymous: anonymous}
}

// Authenticated wraps handlers that require a signed-in user.
func (m *Middleware) Authenticated(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if m.anonymous != nil {
			ctx := WithUser(r.Context(), m.anonymous)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		sess, err := m.sessions.FromRequest(r)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]any{
				"error":  "unauthenticated",
				"reason": "no valid session; sign in again",
			})
			return
		}

		if m.auth != nil && NeedsRefresh(sess) {
			if err := m.auth.Refresh(r.Context(), sess); err != nil {
				// Only a definitive refusal ends the session. On a transient
				// provider failure the current token is still valid for a
				// little while (NeedsRefresh fires ahead of expiry), so serve
				// this request and let a later one retry — an identity
				// provider blip must not sign everyone out.
				if !RefreshFatal(err) {
					slog.Warn("token refresh failed transiently; serving with current token",
						"session", sess.ID[:8], "err", err)
				} else {
					_ = m.sessions.Destroy(r.Context(), sess.ID)
					m.sessions.ClearCookies(w)
					writeJSON(w, http.StatusUnauthorized, map[string]any{
						"error":  "session_expired",
						"reason": "could not refresh credentials; sign in again",
					})
					return
				}
			}
		}

		ctx := WithUser(r.Context(), &sess.User)
		ctx = WithSession(ctx, sess)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// CSRF enforces the double-submit cookie on state-changing requests. Combined
// with SameSite=Lax this keeps a cookie-authenticated API safe from
// cross-origin form posts.
func (m *Middleware) CSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		if m.anonymous != nil {
			next.ServeHTTP(w, r)
			return
		}
		sess, ok := SessionFrom(r.Context())
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthenticated"})
			return
		}
		presented := r.Header.Get("X-CSRF-Token")
		if presented == "" || !subtleCompare(presented, sess.CSRFToken) {
			writeJSON(w, http.StatusForbidden, map[string]any{
				"error":  "csrf",
				"reason": "missing or invalid X-CSRF-Token header",
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// FreshSession re-reads a live session by ID, renewing its tokens when they
// are near expiry. It exists for long-lived streams (watch, logs, exec), which
// authenticate once at the handshake and must notice what happens afterwards:
// a sign-out or expired session returns an error — the stream must not outlive
// the login that opened it — while a refresh keeps a passthrough stream's
// re-authorization working past the original token's lifetime. Transient
// refresh failures are tolerated for the same reason they are on the request
// path.
func (m *Middleware) FreshSession(ctx context.Context, id string) (*Session, error) {
	sess, err := m.sessions.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	// An open stream is activity: without this stamp a long log follow or
	// shell would hit the idle timeout mid-use. The absolute TTL still bounds
	// the session's total lifetime.
	if time.Since(sess.LastSeen) > time.Minute {
		sess.LastSeen = time.Now()
		_ = m.sessions.Save(ctx, sess)
	}
	if m.auth != nil && NeedsRefresh(sess) {
		if err := m.auth.Refresh(ctx, sess); err != nil {
			if RefreshFatal(err) {
				return nil, err
			}
			slog.Warn("stream token refresh failed transiently; keeping current token",
				"session", sess.ID[:8], "err", err)
		}
	}
	return sess, nil
}

// Anonymous reports whether authentication is disabled.
func (m *Middleware) Anonymous() bool { return m.anonymous != nil }

func subtleCompare(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// OriginAllowed validates the Origin header of a WebSocket handshake against
// the configured public URL and any extra allowed origins.
func OriginAllowed(origin, publicURL string, extra []string) bool {
	if origin == "" {
		// Non-browser clients (kubectl-style tooling, tests) send no Origin.
		return true
	}
	if strings.EqualFold(origin, publicURL) {
		return true
	}
	for _, e := range extra {
		// Exact origins only — "*" is rejected at config load, because the
		// Origin check is what stands in for CSRF on streaming endpoints.
		if strings.EqualFold(origin, e) {
			return true
		}
	}
	return false
}
