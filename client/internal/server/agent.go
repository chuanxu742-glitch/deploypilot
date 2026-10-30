package server

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/deploypilot/deploypilot/internal/agent"
	"github.com/deploypilot/deploypilot/internal/auth"
)

// agentClientMsg is a control message from the browser.
type agentClientMsg struct {
	Type string `json:"type"` // start | stop
	Task string `json:"task"`
	// ContinueFrom, when set on start, resumes that task's conversation.
	ContinueFrom string `json:"continueFrom,omitempty"`
	// RequestID identifies this submission so reconnecting clients can match it
	// against the accepted task record without replaying the start command.
	RequestID string `json:"requestId,omitempty"`
}

// The WebSocket observes the server-owned task. Closing a viewer never cancels it.
func (s *Server) handleAgent(w http.ResponseWriter, r *http.Request) {
	up := newUpgrader()
	conn, err := up.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	defer s.track(r, conn)()
	conn.SetReadLimit(64 * 1024)
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
		conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if err := conn.WriteMessage(1, data); err != nil {
			conn.Close()
		}
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
		case "stop":
			s.stopTask()
		case "start":
			if reason := s.startTask(msg.Task, owner.Value, msg.ContinueFrom, msg.RequestID); reason != "" {
				rejection := map[string]any{"type": agent.EventError, "text": reason}
				if msg.RequestID != "" {
					rejection["requestId"] = msg.RequestID
				}
				data, _ := json.Marshal(rejection)
				send(data)
				s.taskMu.Lock()
				state := "idle"
				if s.taskCancel != nil {
					state = s.tasks[0].Phase
				}
				s.taskMu.Unlock()
				if state == "idle" {
					data, _ := json.Marshal(runStateMessage("idle", nil))
					send(data)
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
