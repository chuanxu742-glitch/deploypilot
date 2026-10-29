package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time      { return c.t }
func (c *fakeClock) add(d time.Duration) { c.t = c.t.Add(d) }

func newManager(t *testing.T, clk *fakeClock) *Manager {
	t.Helper()
	m, err := NewManager(Options{
		TokenTTL:    10 * time.Minute,
		IdleTimeout: 30 * time.Minute,
		Now:         clk.now,
	})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	return m
}

func TestPairSucceedsOnce(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	m := newManager(t, clk)
	tok := m.Token()
	if tok == "" {
		t.Fatal("empty token")
	}

	sid, err := m.Pair(tok)
	if err != nil {
		t.Fatalf("first pair: %v", err)
	}
	if sid == "" {
		t.Fatal("empty session id")
	}

	// Reusing the token must fail.
	if _, err := m.Pair(tok); err != ErrBadToken {
		t.Fatalf("reuse must fail with ErrBadToken, got %v", err)
	}
}

func TestPairWrongToken(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	m := newManager(t, clk)
	if _, err := m.Pair("not-the-token"); err != ErrBadToken {
		t.Fatalf("want ErrBadToken, got %v", err)
	}
	// A legitimate pair should still work afterwards (wrong attempt must not
	// consume the token).
	if _, err := m.Pair(m.Token()); err != nil {
		t.Fatalf("valid pair after wrong attempt: %v", err)
	}
}

func TestTokenExpiry(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	m := newManager(t, clk)
	tok := m.Token()
	clk.add(11 * time.Minute)
	if _, err := m.Pair(tok); err != ErrBadToken {
		t.Fatalf("expired token must fail, got %v", err)
	}
}

func TestSingleSession(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	m := newManager(t, clk)
	sid, err := m.Pair(m.Token())
	if err != nil {
		t.Fatalf("pair: %v", err)
	}

	// Mint a fresh token to simulate a second pairing attempt.
	if _, err := m.mintToken(); err != nil {
		t.Fatalf("mint: %v", err)
	}
	if _, err := m.Pair(m.Token()); err != ErrSessionActive {
		t.Fatalf("second session must be rejected, got %v", err)
	}

	// Original session still valid.
	if !m.Validate(sid) {
		t.Fatal("original session should still validate")
	}
}

func TestSessionIdleTimeout(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	m := newManager(t, clk)
	sid, _ := m.Pair(m.Token())

	// Activity within timeout refreshes the deadline.
	clk.add(20 * time.Minute)
	if !m.Validate(sid) {
		t.Fatal("session should be valid before timeout")
	}
	clk.add(20 * time.Minute) // 20 min since last activity, < 30
	if !m.Validate(sid) {
		t.Fatal("activity should have refreshed the deadline")
	}

	// Now go idle past the timeout.
	clk.add(31 * time.Minute)
	if m.Validate(sid) {
		t.Fatal("session should expire after idle timeout")
	}
}

func TestSessionFreedAfterIdleAllowsNewPair(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	m := newManager(t, clk)
	_, _ = m.Pair(m.Token())

	clk.add(31 * time.Minute) // first session goes idle
	_, _ = m.mintToken()
	if _, err := m.Pair(m.Token()); err != nil {
		t.Fatalf("new pair after idle expiry should succeed, got %v", err)
	}
}

func TestMiddleware(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	m := newManager(t, clk)
	sid, _ := m.Pair(m.Token())

	protected := m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))

	// No cookie -> 401.
	rec := httptest.NewRecorder()
	protected.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing cookie should be 401, got %d", rec.Code)
	}

	// Valid cookie -> passthrough.
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: sid})
	protected.ServeHTTP(rec, req)
	if rec.Code != http.StatusTeapot {
		t.Fatalf("valid cookie should pass, got %d", rec.Code)
	}

	// Wrong cookie -> 401.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/x", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: "bogus"})
	protected.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong cookie should be 401, got %d", rec.Code)
	}
}
