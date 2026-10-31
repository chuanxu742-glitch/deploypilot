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

func TestHoldSessionSurvivesLongTaskAndRestartsIdleTimeoutOnRelease(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	m := newManager(t, clk)
	sid, err := m.Pair(m.Token())
	if err != nil {
		t.Fatal(err)
	}
	release, ok := m.HoldSession(sid)
	if !ok {
		t.Fatal("valid session was not held")
	}
	releaseLast, ok := m.HoldSession(sid)
	if !ok {
		t.Fatal("second hold on valid session was rejected")
	}
	clk.add(31 * time.Minute)
	if !m.IsValid(sid) {
		t.Fatal("held session expired during a long task")
	}
	release()
	clk.add(31 * time.Minute)
	if !m.IsValid(sid) {
		t.Fatal("session expired before the last hold was released")
	}
	releaseLast()
	releaseLast() // releasing twice must not change the session twice
	clk.add(29 * time.Minute)
	if !m.IsValid(sid) {
		t.Fatal("release did not start a fresh idle period")
	}
	clk.add(2 * time.Minute)
	if m.IsValid(sid) {
		t.Fatal("released session did not expire after its fresh idle period")
	}
}

func TestHoldSessionIsReferenceCountedAndReleaseCannotAffectReplacement(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	m := newManager(t, clk)
	oldSID, err := m.Pair(m.Token())
	if err != nil {
		t.Fatal(err)
	}
	releaseOld, ok := m.HoldSession(oldSID)
	if !ok {
		t.Fatal("old session was not held")
	}
	if _, err := m.ResetPairing(); err != nil {
		t.Fatal(err)
	}
	newSID, err := m.Pair(m.Token())
	if err != nil {
		t.Fatal(err)
	}
	releaseNew, ok := m.HoldSession(newSID)
	if !ok {
		t.Fatal("new session was not held")
	}
	releaseOld()
	if m.IsValid(oldSID) {
		t.Fatal("old session was revived by its release")
	}
	clk.add(31 * time.Minute)
	if !m.IsValid(newSID) {
		t.Fatal("old release changed the new session's active hold")
	}
	releaseNew()
}

func TestHoldSessionRejectsExpiredSession(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	m := newManager(t, clk)
	sid, err := m.Pair(m.Token())
	if err != nil {
		t.Fatal(err)
	}
	clk.add(31 * time.Minute)
	if _, ok := m.HoldSession(sid); ok {
		t.Fatal("expired session was held")
	}
	if m.IsValid(sid) {
		t.Fatal("HoldSession revived an expired session")
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
