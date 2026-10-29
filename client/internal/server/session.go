package server

import (
	"net/http"
	"time"

	"github.com/deploypilot/deploypilot/internal/auth"
	"github.com/gorilla/websocket"
)

// guardSession closes an expired/revoked socket even when no input arrives.
// Only actual input or task output calls touch; ping/pong traffic does not.
// closeSessionExpired sends a distinct application close code before closing,
// so the browser can tell an expired/revoked session apart from a transient
// network drop and prompt the operator to pair again instead of retrying
// forever. WriteControl is safe to call concurrently with the writer.
func closeSessionExpired(conn *websocket.Conn) {
	_ = conn.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(4401, "session expired"),
		time.Now().Add(time.Second))
	_ = conn.Close()
}

func (s *Server) guardSession(r *http.Request, conn *websocket.Conn) (touch func() bool, stop func()) {
	cookie, err := r.Cookie(auth.CookieName)
	if err != nil {
		closeSessionExpired(conn)
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
					closeSessionExpired(conn)
					return
				}
			}
		}
	}()
	return func() bool {
		if !s.auth.Validate(cookie.Value) {
			closeSessionExpired(conn)
			return false
		}
		return true
	}, func() { close(done) }
}
