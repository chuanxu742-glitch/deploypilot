package server

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/deploypilot/deploypilot/internal/agent"
	"github.com/deploypilot/deploypilot/internal/auth"
	"github.com/gorilla/websocket"
)

// agentClientMsg is a control message from the browser.
type agentClientMsg struct {
	Type string `json:"type"` // start | steer | stop
	Task string `json:"task"`
	// ContinueFrom, when set on start, resumes that task's conversation.
	ContinueFrom string `json:"continueFrom,omitempty"`
	// RequestID identifies this submission so reconnecting clients can match it
	// against the accepted task record without replaying the start command.
	RequestID string `json:"requestId,omitempty"`
	TaskID    string `json:"taskId,omitempty"`
}

// agentFrameLimit bounds one browser message. startTask enforces the 64 KiB
// text limit with a readable reason. JSON escaping can grow text up to 6x
// (\u00XX), so the frame limit must stay above that, or a pasted Windows
// log closes the socket with no message.
const agentFrameLimit = 6*maxTaskTextBytes + 4096

// wsWriteTimeout bounds one WebSocket write to a slow viewer.
const wsWriteTimeout = 10 * time.Second

// The WebSocket observes the server-owned task. Closing a viewer never cancels it.
func (s *Server) handleAgent(w http.ResponseWriter, r *http.Request) {
	up := newUpgrader()
	conn, err := up.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	defer s.track(r, conn)()
	conn.SetReadLimit(agentFrameLimit)
	touch, stopSession := s.guardSession(r, conn)
	defer stopSession()
	stopPing := make(chan struct{})
	defer close(stopPing)
	keepAlive(conn, stopPing)
	owner, _ := r.Cookie(auth.CookieName)
	updates, unsubscribe := s.subscribeTasks()
	defer unsubscribe()
	done := make(chan struct{})
	writerDone := make(chan struct{})
	var writeMu sync.Mutex
	send := func(data []byte) {
		writeMu.Lock()
		defer writeMu.Unlock()
		conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
		if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
			conn.Close()
		}
	}
	sendJSON := func(v any) {
		data, _ := json.Marshal(v)
		send(data)
	}
	go func() {
		defer close(writerDone)
		for {
			select {
			case <-done:
				return
			case data, ok := <-updates:
				if !ok || !touch() {
					conn.Close()
					return
				}
				send(data)
			}
		}
	}()
	defer func() { close(done); conn.Close(); <-writerDone }()
	for {
		_, data, err := conn.ReadMessage()
		if err != nil || !touch() {
			return
		}
		var msg agentClientMsg
		if json.Unmarshal(data, &msg) != nil {
			continue
		}
		switch msg.Type {
		case "steer":
			if reason := s.queueSteer(msg.TaskID, msg.Task, msg.RequestID); reason != "" {
				sendJSON(map[string]any{"type": "error", "taskId": msg.TaskID, "requestId": msg.RequestID, "text": reason})
			} else {
				sendJSON(map[string]any{"type": "steer_ack", "taskId": msg.TaskID, "requestId": msg.RequestID, "state": "queued"})
			}
		case "stop":
			s.stopTask()
		case "start":
			if reason := s.startTask(msg.Task, owner.Value, msg.ContinueFrom, msg.RequestID); reason != "" {
				rejection := map[string]any{"type": agent.EventError, "text": reason}
				if taskID := s.acceptedStartTaskID(msg.RequestID); taskID != "" {
					rejection["taskId"] = taskID
				}
				if msg.RequestID != "" {
					rejection["requestId"] = msg.RequestID
				}
				sendJSON(rejection)
				s.taskMu.Lock()
				state := s.runStateLocked()
				s.taskMu.Unlock()
				if state == "idle" {
					sendJSON(runStateMessage("idle", nil))
				}
			}
		}
	}
}

// newAuditor returns a file auditor under ~/.deploypilot, or a no-op auditor
// when the directory is unavailable.
func (s *Server) newAuditor() agent.Auditor {
	dir, err := agent.DefaultAuditDir()
	if err != nil {
		return agent.NewNopAuditor()
	}
	a, err := agent.NewFileAuditor(dir)
	if err != nil {
		return agent.NewNopAuditor()
	}
	return a
}
