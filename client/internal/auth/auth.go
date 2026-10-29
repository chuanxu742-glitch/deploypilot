// Package auth implements one-time pairing tokens and single-session
// cookie authentication for the control plane.
//
// Security model:
//   - A single pairing token is minted at startup and embedded in the QR
//     code fragment. It is 32 random bytes, usable once, and expires after
//     a short TTL.
//   - Pairing exchanges the token for an opaque session id delivered as an
//     HttpOnly, Secure, SameSite=Strict cookie.
//   - Only one session may be active at a time; a session expires after an
//     idle timeout. Any authenticated activity refreshes the deadline.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"sync"
	"time"
)

// CookieName is the session cookie name.
const CookieName = "dp_session"

var (
	// ErrBadToken is returned when a pairing token is unknown, already used,
	// or expired.
	ErrBadToken = errors.New("invalid or expired pairing token")
	// ErrSessionActive is returned when a session already holds the single
	// available slot.
	ErrSessionActive = errors.New("a session is already active")
)

// Manager coordinates pairing tokens and the single active session.
type Manager struct {
	tokenTTL    time.Duration
	idleTimeout time.Duration
	now         func() time.Time

	mu          sync.Mutex
	token       string
	tokenExpiry time.Time
	tokenUsed   bool
	sessionID   string
	sessionSeen time.Time
}

// Options configures a Manager.
type Options struct {
	TokenTTL    time.Duration
	IdleTimeout time.Duration
	// Now is injectable for tests; defaults to time.Now.
	Now func() time.Time
}

// NewManager creates a Manager and mints the initial pairing token.
func NewManager(opts Options) (*Manager, error) {
	if opts.TokenTTL <= 0 {
		opts.TokenTTL = 10 * time.Minute
	}
	if opts.IdleTimeout <= 0 {
		opts.IdleTimeout = 30 * time.Minute
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	m := &Manager{
		tokenTTL:    opts.TokenTTL,
		idleTimeout: opts.IdleTimeout,
		now:         opts.Now,
	}
	if _, err := m.mintToken(); err != nil {
		return nil, err
	}
	return m, nil
}

// Token returns the current pairing token.
func (m *Manager) Token() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.token
}

func (m *Manager) mintToken() (string, error) {
	tok, err := randToken()
	if err != nil {
		return "", err
	}
	m.token = tok
	m.tokenExpiry = m.now().Add(m.tokenTTL)
	m.tokenUsed = false
	return tok, nil
}

// Pair validates the token and, if a session slot is free, creates a new
// session. The returned session id must be delivered to the client as a
// cookie. The token is single-use regardless of outcome once matched.
func (m *Manager) Pair(token string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := m.now()
	if m.token == "" || m.tokenUsed || now.After(m.tokenExpiry) {
		return "", ErrBadToken
	}
	if subtle.ConstantTimeCompare([]byte(token), []byte(m.token)) != 1 {
		return "", ErrBadToken
	}

	// Token matched: consume it so it cannot be reused.
	m.tokenUsed = true

	// Enforce single active session (unless the current one is idle-expired).
	if m.sessionID != "" && !m.sessionIdleExpiredLocked(now) {
		return "", ErrSessionActive
	}

	sid, err := randToken()
	if err != nil {
		return "", err
	}
	m.sessionID = sid
	m.sessionSeen = now
	return sid, nil
}

// Validate reports whether sid is the active, non-expired session. On
// success it refreshes the idle deadline.
func (m *Manager) Validate(sid string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	if m.sessionID == "" || sid == "" {
		return false
	}
	if subtle.ConstantTimeCompare([]byte(sid), []byte(m.sessionID)) != 1 {
		return false
	}
	if m.sessionIdleExpiredLocked(now) {
		m.sessionID = ""
		return false
	}
	m.sessionSeen = now
	return true
}

// Revoke clears the active session (e.g. on explicit logout or shutdown).
func (m *Manager) Revoke() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessionID = ""
}

func (m *Manager) sessionIdleExpiredLocked(now time.Time) bool {
	return now.Sub(m.sessionSeen) > m.idleTimeout
}

// SetCookie writes the session cookie for sid onto w.
func SetCookie(w http.ResponseWriter, sid string, idleTimeout time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    sid,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(idleTimeout.Seconds()),
	})
}

// IdleTimeout exposes the configured idle timeout.
func (m *Manager) IdleTimeout() time.Duration { return m.idleTimeout }

// Middleware rejects requests that do not carry a valid session cookie.
func (m *Manager) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(CookieName)
		if err != nil || !m.Validate(c.Value) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func randToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
