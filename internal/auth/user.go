// Package auth implements OIDC login, server-side sessions and the request
// middleware that attaches an identity to every API call.
package auth

import (
	"context"
	"time"
)

// User is the identity Orrery carries through a request. Username and
// Groups are the mapped, prefixed values handed to the API server as
// impersonation headers, so they must match what the cluster's own OIDC
// configuration would have produced for the same token.
type User struct {
	Subject  string   `json:"subject"`
	Username string   `json:"username"`
	Groups   []string `json:"groups"`
	Email    string   `json:"email,omitempty"`
	Name     string   `json:"name,omitempty"`
	Picture  string   `json:"picture,omitempty"`
}

// Session is the server-side record backing a login. The bearer material never
// reaches the browser; the cookie only carries an opaque, encrypted ID.
type Session struct {
	ID           string    `json:"id"`
	User         User      `json:"user"`
	IDToken      string    `json:"idToken,omitempty"`
	AccessToken  string    `json:"accessToken,omitempty"`
	RefreshToken string    `json:"refreshToken,omitempty"`
	TokenExpiry  time.Time `json:"tokenExpiry,omitempty"`
	CSRFToken    string    `json:"csrfToken"`
	// CreatedAt is not read by any code path; it is kept because a session
	// record in the store should say when it was minted (audit, debugging).
	CreatedAt time.Time `json:"createdAt"`
	LastSeen  time.Time `json:"lastSeen"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// Expired reports whether the session is past its absolute lifetime or has
// been idle longer than allowed.
func (s *Session) Expired(now time.Time, idleTimeout time.Duration) bool {
	if !s.ExpiresAt.IsZero() && now.After(s.ExpiresAt) {
		return true
	}
	if idleTimeout > 0 && now.Sub(s.LastSeen) > idleTimeout {
		return true
	}
	return false
}

type ctxKey int

const (
	ctxKeyUser ctxKey = iota
	ctxKeySession
)

// WithUser returns a context carrying the authenticated identity.
func WithUser(ctx context.Context, u *User) context.Context {
	return context.WithValue(ctx, ctxKeyUser, u)
}

// UserFrom extracts the identity attached by the auth middleware.
func UserFrom(ctx context.Context) (*User, bool) {
	u, ok := ctx.Value(ctxKeyUser).(*User)
	return u, ok
}

// WithSession returns a context carrying the full session record.
func WithSession(ctx context.Context, s *Session) context.Context {
	return context.WithValue(ctx, ctxKeySession, s)
}

// SessionFrom extracts the session attached by the auth middleware. Handlers
// that need the raw ID token (passthrough clusters) read it from here.
func SessionFrom(ctx context.Context) (*Session, bool) {
	s, ok := ctx.Value(ctxKeySession).(*Session)
	return s, ok
}
