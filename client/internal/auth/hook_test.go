package auth

import (
	"testing"
	"time"
)

func TestSessionEndedHookFiresOnceOnIdleExpiry(t *testing.T) {
	now := time.Now()
	m, err := NewManager(Options{IdleTimeout: time.Minute, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	fired := make(chan struct{}, 4)
	m.SetSessionEndedHook(func() { fired <- struct{}{} })

	sid, err := m.Pair(m.Token())
	if err != nil {
		t.Fatal(err)
	}
	if !m.Validate(sid) {
		t.Fatal("fresh session should be valid")
	}
	// No hook while the session is live.
	select {
	case <-fired:
		t.Fatal("hook fired for a live session")
	case <-time.After(100 * time.Millisecond):
	}

	now = now.Add(2 * time.Minute) // idle past the timeout
	if m.Validate(sid) {
		t.Fatal("session should have expired")
	}
	select {
	case <-fired:
	case <-time.After(2 * time.Second):
		t.Fatal("session-ended hook did not fire on idle expiry")
	}
	// Further checks on the already-cleared session must not re-fire.
	_ = m.Validate(sid)
	_ = m.IsValid(sid)
	select {
	case <-fired:
		t.Fatal("hook fired more than once for a single expiry")
	case <-time.After(150 * time.Millisecond):
	}
}
