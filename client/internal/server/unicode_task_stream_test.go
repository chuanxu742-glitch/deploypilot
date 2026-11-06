package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/deploypilot/deploypilot/internal/agent"
	"github.com/gorilla/websocket"
)

// The task panel consumes JSON events on /ws/agent. Its live path must keep
// characters intact even when a real process writes one UTF-8 byte at a time.
// This intentionally does not start an LLM task or run environment probes.
func TestCommandOutputUTF8SurvivesTaskWebSocket(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("real Linux process/PTY reproduction")
	}
	for _, executorName := range []string{"shared", "standalone"} {
		for _, content := range []string{"ASCII", "é", "中", "🌍"} {
			t.Run(executorName+"/"+content, func(t *testing.T) {
				srv, cookie := newLLMServer(t)
				task := &taskRecord{ID: "local-utf8-task", Title: "Local UTF-8 fixture", Phase: "running", StartedAt: time.Now().UnixMilli()}
				srv.taskMu.Lock()
				srv.tasks = append([]*taskRecord{task}, srv.tasks...)
				srv.taskMu.Unlock()
				addr, err := srv.Start()
				if err != nil {
					t.Fatal(err)
				}
				header := http.Header{"Cookie": {cookie.Name + "=" + cookie.Value}}
				conn, _, err := websocket.DefaultDialer.Dial("ws://"+addr+"/ws/agent", header)
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
				read := func() agent.Event {
					t.Helper()
					kind, data, err := conn.ReadMessage()
					if err != nil {
						t.Fatal(err)
					}
					if kind != websocket.TextMessage {
						t.Fatalf("unexpected websocket message kind: %d", kind)
					}
					var event agent.Event
					if err := json.Unmarshal(data, &event); err != nil {
						t.Fatal(err)
					}
					return event
				}
				// Receipt of snapshot proves the production subscriber is installed.
				if event := read(); event.Type != "snapshot" {
					t.Fatalf("initial message: %+v", event)
				}
				if !srv.acquireAgent() {
					t.Fatal("task slot unavailable")
				}
				defer srv.releaseAgent()
				executor := srv.newExecutor()
				if executorName == "standalone" {
					executor = agent.NewShellExecutor()
				}
				var command strings.Builder
				command.WriteString("printf '[begin]'; ")
				for _, value := range []byte(content) {
					fmt.Fprintf(&command, "printf '\\%03o'; sleep 0.04; ", value)
				}
				command.WriteString("printf '[end]\\n'")
				const callID = "local-utf8-output"
				code, captured, err := executor.Run(context.Background(), command.String(), 5*time.Second, func(chunk string) {
					srv.emitTask(task, agent.Event{Type: agent.EventOutput, Text: chunk, CallID: callID})
				})
				if err != nil || code != 0 {
					t.Fatalf("execution: code=%d error=%v output=%q", code, err, captured)
				}
				srv.emitTask(task, agent.Event{Type: agent.EventToolDone, Tool: "run_command", ExitCode: code, CallID: callID})
				var live strings.Builder
				for {
					event := read()
					if event.TaskID != task.ID || event.CallID != callID {
						t.Fatalf("unexpected task event: %+v", event)
					}
					if event.Type == agent.EventOutput {
						live.WriteString(event.Text)
					}
					if event.Type == agent.EventToolDone {
						break
					}
				}
				want := "[begin]" + content + "[end]"
				// Log the exact compared substring without relying on other test helpers.
				extract := func(text string) string {
					begin := strings.Index(text, "[begin]")
					if begin < 0 {
						return text
					}
					end := strings.Index(text[begin:], "[end]")
					if end < 0 {
						return text[begin:]
					}
					return text[begin : begin+end+len("[end]")]
				}
				t.Logf("executor=%s source=%q live_task_json=%q exit=%d", executorName, extract(captured), extract(live.String()), code)
				if !strings.Contains(captured, want) {
					t.Fatal("source command did not emit expected UTF-8")
				}
				if !strings.Contains(live.String(), want) {
					t.Fatal("valid UTF-8 command output corrupted by production task WebSocket JSON")
				}
			})
		}
	}
}
