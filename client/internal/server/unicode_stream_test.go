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

func outputBetweenMarkers(text string) string {
	start := strings.Index(text, "[begin]")
	if start < 0 {
		return text
	}
	end := strings.Index(text[start:], "[end]")
	if end < 0 {
		return text[start:]
	}
	return text[start : start+end+len("[end]")]
}

func TestCommandOutputUTF8SurvivesTerminalWebSocket(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("real Linux process/PTY reproduction")
	}
	for _, executorName := range []string{"shared", "standalone"} {
		for _, content := range []string{"ASCII", "é", "中", "🌍"} {
			t.Run(executorName+"/"+content, func(t *testing.T) {
				srv, cookie := newLLMServer(t)
				addr, err := srv.Start()
				if err != nil {
					t.Fatal(err)
				}
				header := http.Header{"Cookie": {cookie.Name + "=" + cookie.Value}}
				conn, _, err := websocket.DefaultDialer.Dial("ws://"+addr+"/ws/term", header)
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
				read := func() (string, agent.Event) {
					t.Helper()
					for {
						kind, data, err := conn.ReadMessage()
						if err != nil {
							t.Fatal(err)
						}
						if kind != websocket.TextMessage {
							continue
						}
						var message struct {
							Type  string      `json:"type"`
							State string      `json:"state"`
							Event agent.Event `json:"event"`
						}
						if err := json.Unmarshal(data, &message); err != nil {
							t.Fatal(err)
						}
						if message.Type == "terminal_reset" {
							continue
						}
						return message.Type + ":" + message.State, message.Event
					}
				}
				if kind, _ := read(); kind != "agent_state:idle" {
					t.Fatalf("initial state %s", kind)
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
				code, captured, err := executor.Run(context.Background(), command.String(), 5*time.Second, func(chunk string) {
					srv.publishTerminalAgent(agent.Event{Type: agent.EventOutput, Text: chunk, CallID: "synthetic-local-output"})
				})
				if err != nil || code != 0 {
					t.Fatalf("execution: code=%d error=%v output=%q", code, err, captured)
				}
				srv.publishTerminalAgent(agent.Event{Type: agent.EventToolDone, Tool: "run_command", ExitCode: code, CallID: "synthetic-local-output"})
				var live strings.Builder
				for {
					_, event := read()
					if event.Type == agent.EventOutput {
						live.WriteString(event.Text)
					}
					if event.Type == agent.EventToolDone {
						break
					}
				}
				want := "[begin]" + content + "[end]"
				t.Logf("executor=%s source=%q terminal=%q exit=%d", executorName, outputBetweenMarkers(captured), outputBetweenMarkers(live.String()), code)
				if !strings.Contains(captured, want) {
					t.Fatal("source command did not emit expected UTF-8")
				}
				if !strings.Contains(live.String(), want) {
					t.Fatal("valid UTF-8 command output corrupted by production terminal WebSocket")
				}
			})
		}
	}
}
