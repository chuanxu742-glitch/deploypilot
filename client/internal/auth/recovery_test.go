package auth

import (
	"errors"
	"testing"
	"time"
)

func TestResetPairingRevokesOldSessionAndRefreshesTTL(t *testing.T) {
	now := time.Now()
	m, err := NewManager(Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	old := m.Token()
	sid, err := m.Pair(old)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(5 * time.Minute)
	newToken, err := m.ResetPairing()
	if err != nil {
		t.Fatal(err)
	}
	if newToken == old || m.Validate(sid) {
		t.Fatal("old credentials survived reset")
	}
	if _, err = m.Pair(old); !errors.Is(err, ErrBadToken) {
		t.Fatalf("old link: %v", err)
	}
	now = now.Add(9 * time.Minute) // beyond the original token's TTL, inside the new one
	if _, err = m.Pair(newToken); err != nil {
		t.Fatalf("fresh link: %v", err)
	}
}

func TestRevokeAllRejectsUnusedPairingToken(t *testing.T) {
	m, err := NewManager(Options{})
	if err != nil {
		t.Fatal(err)
	}
	token := m.Token()
	m.RevokeAll()
	if _, err := m.Pair(token); !errors.Is(err, ErrBadToken) {
		t.Fatalf("unused old link: %v", err)
	}
	newToken, err := m.ResetPairing()
	if err != nil {
		t.Fatal(err)
	}
	sid, err := m.Pair(newToken)
	if err != nil {
		t.Fatal(err)
	}
	m.RevokeAll()
	if m.Validate(sid) {
		t.Fatal("old session survived revoke")
	}
}
