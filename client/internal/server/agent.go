package server

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"

	"github.com/deploypilot/deploypilot/internal/agent"
)

// agentClientMsg is a control message from the browser.
type agentClientMsg struct {
	Type string `json:"type"` // start | stop
	Task string `json:"task"`
}

// handleAgent bridges a WebSocket to an autonomous agent run. At most one run
// is active per connection. When the connection closes or the server shuts
// down, the run is cancelled and the handler waits for it to clean up.
func (s *Server) handleAgent(w http.ResponseWriter, r *http.Request) {
	up := newUpgrader()
	conn, err := up.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	defer s.track(r, conn)()
	stopPing := make(chan struct{})
	defer close(stopPing)
	keepAlive(conn, stopPing)

	var writeMu sync.Mutex
	send := func(v any) {
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = conn.WriteJSON(v)
	}

	var (
		mu      sync.Mutex
		cancel  context.CancelFunc
		runID   int
		running sync.WaitGroup
	)
	stop := func() {
		mu.Lock()
		if cancel != nil {
			cancel()
			cancel = nil
		}
		mu.Unlock()
	}
	// On exit: cancel any run, then wait for it so its commands are killed
	// and the audit log is closed before the handler returns.
	defer running.Wait()
	defer stop()

	for {
		_, data, readErr := conn.ReadMessage()
		if readErr != nil {
			return
		}
		var msg agentClientMsg
		if json.Unmarshal(data, &msg) != nil {
			continue
		}
		switch msg.Type {
		case "stop":
			stop()
		case "start":
			if !s.llm.HasKey() {
				send(agent.Event{Type: agent.EventError, Text: "尚未配置模型 API Key，请先在设置中填写。"})
				continue
			}
			mu.Lock()
			if cancel != nil {
				mu.Unlock()
				send(agent.Event{Type: agent.EventError, Text: "已有任务在运行，请先停止。"})
				continue
			}
			ctx, c := context.WithCancel(r.Context())
			cancel = c
			runID++
			id := runID
			mu.Unlock()

			auditor := s.newAuditor()
			ag := agent.New(agent.Config{
				LLM:      s.llm,
				Executor: agent.NewShellExecutor(),
				Auditor:  auditor,
				Emit:     func(e agent.Event) { send(e) },
			})
			running.Add(1)
			go func(task string) {
				defer running.Done()
				defer auditor.Close()
				_ = ag.Run(ctx, task)
				mu.Lock()
				if runID == id && cancel != nil {
					cancel()
					cancel = nil
				}
				mu.Unlock()
			}(msg.Task)
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
