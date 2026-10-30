// Package auth implements one-time pairing tokens and single-session
// cookie authentication for the control plane.
//
// Security model:
//   - A single pairing token is minted for each new tunnel and embedded in the QR
//     code fragment. It is 32 random bytes, usable once, and expires after
//     a short TTL.
//   - Pairing exchanges the token for an opaque session id delivered as an
//     HttpOnly, Secure, SameSite=Strict cookie.
//   - Only one session may be active at a time; a session expires after an
//     idle timeout. A running task holds its owning session until cleanup;
//     explicit revocation remains effective. Otherwise authenticated activity
//     refreshes the deadline.
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

	mu           sync.Mutex
	token        string
	tokenExpiry  time.Time
	tokenUsed    bool
	sessionID    string
	sessionSeen  time.Time
	sessionHolds int
	sessionEpoch uint64
	sessionEnded func()
}

// SetSessionEndedHook registers a callback fired (once per occurrence, in its
// own goroutine) when the active session ends by idle expiry. The console uses
// it to reprint a fresh pairing link so the operator can reconnect.
func (m *Manager) SetSessionEndedHook(fn func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessionEnded = fn
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
	m.sessionHolds = 0
	m.sessionEpoch++
	return sid, nil
}

// HoldSession prevents an otherwise-idle active session from expiring while
// background work is running. The returned release function is idempotent;
// releasing the final hold starts a fresh idle timeout. A hold cannot revive
// an expired or revoked session.
func (m *Manager) HoldSession(sid string) (release func(), ok bool) {
	m.mu.Lock()
	now := m.now()
	if sid == "" || m.sessionID == "" || subtle.ConstantTimeCompare([]byte(sid), []byte(m.sessionID)) != 1 {
		m.mu.Unlock()
		return nil, false
	}
	if m.sessionIdleExpiredLocked(now) {
		m.expireSessionLocked()
		m.mu.Unlock()
		return nil, false
	}
	m.sessionHolds++
	epoch := m.sessionEpoch
	m.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			m.mu.Lock()
			defer m.mu.Unlock()
			if m.sessionEpoch != epoch || subtle.ConstantTimeCompare([]byte(sid), []byte(m.sessionID)) != 1 || m.sessionHolds == 0 {
				return
			}
			m.sessionHolds--
			if m.sessionHolds == 0 {
				m.sessionSeen = m.now()
			}
		})
	}, true
}

// Validate reports whether sid is the active, non-expired session. On
// success it refreshes the idle deadline.
func (m *Manager) Validate(sid string) bool {
	return m.validate(sid, true)
}

// IsValid checks expiry without extending it; automatic socket heartbeats
// must not turn an abandoned connection into a perpetual session.
func (m *Manager) IsValid(sid string) bool {
	return m.validate(sid, false)
}

func (m *Manager) validate(sid string, touch bool) bool {
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
		m.expireSessionLocked()
		return false
	}
	if touch {
		m.sessionSeen = now
	}
	return true
}

// Revoke clears the active session (e.g. on explicit logout or shutdown).
func (m *Manager) Revoke() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.clearSessionLocked()
}

// RevokeAll also invalidates unused pairing links while the tunnel is down.
func (m *Manager) RevokeAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.clearSessionLocked()
	m.token = ""
	m.tokenUsed = true
}

// ResetPairing starts a fresh pairing window after a new tunnel is connected.
// Old links and sessions stay revoked even if random token generation fails.
func (m *Manager) ResetPairing() (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.clearSessionLocked()
	m.token = ""
	m.tokenUsed = true
	return m.mintToken()
}

func (m *Manager) sessionIdleExpiredLocked(now time.Time) bool {
	return m.sessionHolds == 0 && now.Sub(m.sessionSeen) > m.idleTimeout
}

func (m *Manager) clearSessionLocked() {
	m.sessionID = ""
	m.sessionHolds = 0
	m.sessionEpoch++
}

func (m *Manager) expireSessionLocked() {
	m.clearSessionLocked()
	// Edge-triggered: the cleared sessionID means later calls return early
	// above, so the hook fires exactly once per expiry. Run it off the lock.
	if m.sessionEnded != nil {
		go m.sessionEnded()
	}
}

// SetCookie uses a browser-session cookie. The server owns the sliding idle
// deadline, including activity over WebSockets which cannot renew HTTP cookies.
func SetCookie(w http.ResponseWriter, sid string, _ time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    sid,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
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
