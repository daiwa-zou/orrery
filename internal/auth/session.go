package auth

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/daiwa-zou/orrery/internal/config"
)

// ErrNoSession is returned when a request carries no usable session.
var ErrNoSession = errors.New("no session")

// Store persists sessions. The memory implementation suits a single replica;
// swapping in Redis is what makes the dashboard horizontally scalable, since
// nothing else in the request path holds per-user state.
type Store interface {
	Get(ctx context.Context, id string) (*Session, error)
	Put(ctx context.Context, s *Session) error
	Delete(ctx context.Context, id string) error
	Close() error
}

// MemoryStore is an in-process Store with periodic expiry sweeps.
type MemoryStore struct {
	mu       sync.RWMutex
	sessions map[string]*Session
	idle     time.Duration
	stop     chan struct{}
	stopOnce sync.Once
}

// NewMemoryStore starts a store that evicts expired sessions every minute.
func NewMemoryStore(idle time.Duration) *MemoryStore {
	s := &MemoryStore{
		sessions: make(map[string]*Session),
		idle:     idle,
		stop:     make(chan struct{}),
	}
	go s.sweep()
	return s
}

func (m *MemoryStore) sweep() {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-m.stop:
			return
		case now := <-t.C:
			m.mu.Lock()
			for id, s := range m.sessions {
				if s.Expired(now, m.idle) {
					delete(m.sessions, id)
				}
			}
			m.mu.Unlock()
		}
	}
}

func (m *MemoryStore) Get(_ context.Context, id string) (*Session, error) {
	m.mu.RLock()
	s, ok := m.sessions[id]
	m.mu.RUnlock()
	if !ok {
		return nil, ErrNoSession
	}
	if s.Expired(time.Now(), m.idle) {
		_ = m.Delete(context.Background(), id)
		return nil, ErrNoSession
	}
	return s.Clone(), nil
}

func (m *MemoryStore) Put(_ context.Context, s *Session) error {
	cp := s.Clone()
	m.mu.Lock()
	m.sessions[s.ID] = cp
	m.mu.Unlock()
	return nil
}

func (m *MemoryStore) Delete(_ context.Context, id string) error {
	m.mu.Lock()
	delete(m.sessions, id)
	m.mu.Unlock()
	return nil
}

func (m *MemoryStore) Close() error {
	m.stopOnce.Do(func() { close(m.stop) })
	return nil
}

// CookieCodec seals the session ID into the cookie with AES-GCM. Encrypting
// rather than signing keeps session IDs out of browser storage and logs.
type CookieCodec struct {
	aead cipher.AEAD
}

// NewCookieCodec builds a codec from a 32-byte key.
func NewCookieCodec(key []byte) (*CookieCodec, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("session cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("session aead: %w", err)
	}
	return &CookieCodec{aead: aead}, nil
}

// Encode seals a value into a URL-safe token.
func (c *CookieCodec) Encode(value string) (string, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := c.aead.Seal(nonce, nonce, []byte(value), nil)
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

// Decode opens a token produced by Encode.
func (c *CookieCodec) Decode(token string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return "", ErrNoSession
	}
	ns := c.aead.NonceSize()
	if len(raw) < ns {
		return "", ErrNoSession
	}
	plain, err := c.aead.Open(nil, raw[:ns], raw[ns:], nil)
	if err != nil {
		return "", ErrNoSession
	}
	return string(plain), nil
}

// SessionManager ties the store, the cookie codec and the cookie policy
// together. It is the only place that knows how a browser presents identity.
type SessionManager struct {
	store Store
	codec *CookieCodec
	cfg   config.SessionConfig
}

// CSRFCookieName is readable by the SPA, which echoes it back in a header.
// Pairing that with SameSite=Lax defends mutating calls against cross-site use.
const CSRFCookieName = "orrery_csrf"

// NewSessionManager wires a manager from configuration.
func NewSessionManager(cfg *config.Config, store Store) (*SessionManager, error) {
	key, err := cfg.SessionKey()
	if err != nil {
		return nil, err
	}
	codec, err := NewCookieCodec(key)
	if err != nil {
		return nil, err
	}
	return &SessionManager{store: store, codec: codec, cfg: cfg.Session}, nil
}

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// NewSession mints and persists a session for a freshly authenticated user.
func (m *SessionManager) NewSession(ctx context.Context, u User) (*Session, error) {
	id, err := randomToken(32)
	if err != nil {
		return nil, err
	}
	csrf, err := randomToken(24)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	s := &Session{
		ID:        id,
		User:      u,
		CSRFToken: csrf,
		CreatedAt: now,
		LastSeen:  now,
		ExpiresAt: now.Add(m.cfg.TTL),
	}
	if err := m.store.Put(ctx, s); err != nil {
		return nil, err
	}
	return s, nil
}

// Save persists changes to an existing session (token refresh, last seen).
func (m *SessionManager) Save(ctx context.Context, s *Session) error {
	return m.store.Put(ctx, s)
}

// Get re-reads a session from the backing store by id.
func (m *SessionManager) Get(ctx context.Context, id string) (*Session, error) {
	return m.store.Get(ctx, id)
}

func (m *SessionManager) sameSite() http.SameSite {
	switch m.cfg.SameSite {
	case "strict":
		return http.SameSiteStrictMode
	case "none":
		return http.SameSiteNoneMode
	default:
		return http.SameSiteLaxMode
	}
}

// SetCookies writes the session and CSRF cookies onto a response.
func (m *SessionManager) SetCookies(w http.ResponseWriter, s *Session) error {
	sealed, err := m.codec.Encode(s.ID)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     m.cfg.CookieName,
		Value:    sealed,
		Path:     "/",
		Domain:   m.cfg.Domain,
		Expires:  s.ExpiresAt,
		HttpOnly: true,
		Secure:   m.cfg.Secure,
		SameSite: m.sameSite(),
	})
	// Deliberately not HttpOnly: the SPA must read it to set the header.
	http.SetCookie(w, &http.Cookie{
		Name:     CSRFCookieName,
		Value:    s.CSRFToken,
		Path:     "/",
		Domain:   m.cfg.Domain,
		Expires:  s.ExpiresAt,
		HttpOnly: false,
		Secure:   m.cfg.Secure,
		SameSite: m.sameSite(),
	})
	return nil
}

// ClearCookies expires both cookies on logout.
func (m *SessionManager) ClearCookies(w http.ResponseWriter) {
	for _, name := range []string{m.cfg.CookieName, CSRFCookieName} {
		http.SetCookie(w, &http.Cookie{
			Name:     name,
			Value:    "",
			Path:     "/",
			Domain:   m.cfg.Domain,
			MaxAge:   -1,
			HttpOnly: name == m.cfg.CookieName,
			Secure:   m.cfg.Secure,
			SameSite: m.sameSite(),
		})
	}
}

// FromRequest resolves the session referenced by a request's cookie and
// refreshes its last-seen stamp.
func (m *SessionManager) FromRequest(r *http.Request) (*Session, error) {
	c, err := r.Cookie(m.cfg.CookieName)
	if err != nil {
		return nil, ErrNoSession
	}
	id, err := m.codec.Decode(c.Value)
	if err != nil {
		return nil, ErrNoSession
	}
	s, err := m.store.Get(r.Context(), id)
	if err != nil {
		return nil, err
	}
	// Persist the touch at most once a minute; sessions are read on every
	// request and writing each time would make Redis the bottleneck.
	if time.Since(s.LastSeen) > time.Minute {
		s.LastSeen = time.Now()
		_ = m.store.Put(r.Context(), s)
	}
	return s, nil
}

// Destroy removes a session from the store.
func (m *SessionManager) Destroy(ctx context.Context, id string) error {
	return m.store.Delete(ctx, id)
}
