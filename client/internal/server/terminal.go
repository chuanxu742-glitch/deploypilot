package server

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/deploypilot/deploypilot/internal/pty"
	"github.com/gorilla/websocket"
)

// controlMsg is a JSON control frame sent by the browser (text frames).
// Binary frames are raw terminal input.
type controlMsg struct {
	Type string `json:"type"`
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

func newUpgrader() websocket.Upgrader {
	return websocket.Upgrader{
		ReadBufferSize:  4096,
		WriteBufferSize: 4096,
		CheckOrigin:     sameOrigin,
	}
}

// sameOrigin allows WebSocket upgrades only from the same host the request
// targets (or when no Origin header is present, e.g. non-browser clients).
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

// handleTerminal upgrades to a WebSocket and bridges it to a PTY-backed shell.
func (s *Server) handleTerminal(w http.ResponseWriter, r *http.Request) {
	up := newUpgrader()
	conn, err := up.Upgrade(w, r, nil)
	if err != nil {
		return // Upgrade already wrote an error response.
	}
	defer conn.Close()
	defer s.track(r, conn)()
	stopPing := make(chan struct{})
	defer close(stopPing)
	keepAlive(conn, stopPing)

	sess, err := pty.Start(pty.Config{Rows: 24, Cols: 80})
	if err != nil {
		_ = conn.WriteControl(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "terminal unavailable: "+err.Error()),
			time.Now().Add(time.Second),
		)
		return
	}
	defer sess.Close()

	done := make(chan struct{})

	// PTY -> WebSocket.
	go func() {
		defer close(done)
		buf := make([]byte, 16*1024)
		for {
			n, readErr := sess.Read(buf)
			if n > 0 {
				if werr := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); werr != nil {
					return
				}
			}
			if readErr != nil {
				_ = conn.WriteControl(
					websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.CloseNormalClosure, "shell exited"),
					time.Now().Add(time.Second),
				)
				return
			}
		}
	}()

	// WebSocket -> PTY.
	for {
		mt, data, readErr := conn.ReadMessage()
		if readErr != nil {
			break
		}
		switch mt {
		case websocket.BinaryMessage:
			if _, werr := sess.Write(data); werr != nil {
				goto cleanup
			}
		case websocket.TextMessage:
			var msg controlMsg
			if json.Unmarshal(data, &msg) == nil && msg.Type == "resize" {
				_ = sess.Resize(msg.Rows, msg.Cols)
			}
		}
	}

cleanup:
	sess.Close()
	<-done
}
