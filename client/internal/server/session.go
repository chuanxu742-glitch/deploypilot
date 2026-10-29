package server

import (
	"net/http"
	"time"

	"github.com/deploypilot/deploypilot/internal/auth"
	"github.com/gorilla/websocket"
)

// guardSession closes an expired/revoked socket even when no input arrives.
// Only actual input or task output calls touch; ping/pong traffic does not.
func (s *Server) guardSession(r *http.Request, conn *websocket.Conn) (touch func() bool, stop func()) {
	cookie, err := r.Cookie(auth.CookieName)
	if err != nil {
		_ = conn.Close()
		return func() bool { return false }, func() {}
	}
	done := make(chan struct{})
	interval := s.auth.IdleTimeout() / 4
	if interval > time.Second {
		interval = time.Second
	}
	if interval <= 0 {
		interval = time.Millisecond
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if !s.auth.IsValid(cookie.Value) {
					_ = conn.Close()
					return
				}
			}
		}
	}()
	return func() bool {
		if !s.auth.Validate(cookie.Value) {
			_ = conn.Close()
			return false
		}
		return true
	}, func() { close(done) }
}
