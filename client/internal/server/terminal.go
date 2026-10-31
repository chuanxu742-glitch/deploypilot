package server

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

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

// terminalFrameLimit bounds one browser message; the web terminal splits
// larger pastes into 32 KiB frames.
const terminalFrameLimit = 64 * 1024

// A viewer subscribes to the application-owned shell and replays its bounded history.
func (s *Server) handleTerminal(w http.ResponseWriter, r *http.Request) {
	up := newUpgrader()
	conn, err := up.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	defer s.track(r, conn)()
	conn.SetReadLimit(terminalFrameLimit)
	touch, stopSession := s.guardSession(r, conn)
	defer stopSession()
	stopPing := make(chan struct{})
	defer close(stopPing)
	keepAlive(conn, stopPing)
	sess, err := s.sharedTerminal()
	if err != nil {
		conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(1011, "terminal unavailable"), time.Now().Add(time.Second))
		return
	}
	history, feed, unsubscribeOutput := sess.Subscribe()
	defer unsubscribeOutput()
	updates, unsubscribe := s.subscribeTerminalAgent()
	defer unsubscribe()
	write := func(kind int, data []byte) error {
		conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
		return conn.WriteMessage(kind, data)
	}
	// redraw clears the browser terminal and replays chunks, on connect and
	// again whenever this viewer falls behind live output.
	redraw := func(chunks [][]byte) error {
		if err := write(websocket.TextMessage, []byte(`{"type":"terminal_reset"}`)); err != nil {
			return err
		}
		for _, chunk := range chunks {
			if err := write(websocket.BinaryMessage, chunk); err != nil {
				return err
			}
		}
		return nil
	}
	if redraw(history) != nil {
		return
	}
	done := make(chan struct{})
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		defer conn.Close()
		for {
			select {
			case <-done:
				return
			case <-sess.dead:
				return
			case data, ok := <-feed.output:
				if !ok || !touch() || write(websocket.BinaryMessage, data) != nil {
					return
				}
			case <-feed.lagged:
				if !touch() || redraw(sess.Resync(feed)) != nil {
					return
				}
			case data, ok := <-updates:
				if !ok || !touch() || write(websocket.TextMessage, data) != nil {
					return
				}
			}
		}
	}()
	defer func() { close(done); conn.Close(); <-writerDone }()
	for {
		kind, data, err := conn.ReadMessage()
		if err != nil || !touch() {
			return
		}
		switch kind {
		case websocket.BinaryMessage:
			if _, err := s.writeTerminalInput(sess, data); err != nil {
				return
			}
		case websocket.TextMessage:
			var msg controlMsg
			if json.Unmarshal(data, &msg) == nil && msg.Type == "resize" && msg.Rows > 0 && msg.Cols > 0 {
				_ = sess.Resize(msg.Rows, msg.Cols)
			}
		}
	}
}
