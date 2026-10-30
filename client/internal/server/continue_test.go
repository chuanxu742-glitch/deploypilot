package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deploypilot/deploypilot/internal/agent"
	"github.com/deploypilot/deploypilot/internal/llm"
	"github.com/gorilla/websocket"
)

// commandExecutor answers every command instantly, without a real shell.
type commandExecutor struct{ agent.Executor }

func (commandExecutor) MachineInfo(context.Context) string { return "test environment" }
func (commandExecutor) Run(_ context.Context, command string, _ time.Duration, _ func(string)) (int, string, error) {
	return 0, "ran " + command, nil
}

func readUntilIdle(t *testing.T, conn *websocket.Conn) (finals []map[string]any) {
	t.Helper()
	for {
		event := readAgentMessage(t, conn)
		if event["type"] == "final" {
			finals = append(finals, event)
		}
		if event["type"] == "run_state" && event["state"] == "idle" {
			return finals
		}
	}
}

func latestSnapshot(t *testing.T, srv *Server, cookie *http.Cookie) map[string]any {
	t.Helper()
	conn := agentSocket(t, srv, cookie)
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var snapshot map[string]any
	if err := conn.ReadJSON(&snapshot); err != nil {
		t.Fatal(err)
	}
	conn.Close()
	return snapshot
}

func TestContinueResumesLastTaskConversation(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(body))
		n := len(bodies)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		call := `{"command":"pip install vllm"}`
		name := "run_command"
		if n == 2 {
			call, name = `{"summary":"vllm ready on 8001","success":true}`, "finish"
		}
		args, _ := json.Marshal(call)
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c%d\",\"type\":\"function\",\"function\":{\"name\":%q,\"arguments\":%s}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n", n, name, args)
	}))
	defer provider.Close()
	srv, cookie := newLLMServer(t)
	srv.llm = llm.New(llm.Config{BaseURL: provider.URL, APIKey: "test"})
	srv.agentMaxSteps = 1
	srv.newExecutor = func() agent.Executor { return commandExecutor{} }

	conn := agentSocket(t, srv, cookie)
	conn.WriteJSON(agentClientMsg{Type: "start", Task: "deploy vllm"})
	finals := readUntilIdle(t, conn)
	if len(finals) != 1 || finals[0]["success"] != false {
		t.Fatalf("first run should stop at the step budget: %v", finals)
	}
	snapshot := latestSnapshot(t, srv, cookie)
	firstID := snapshot["task"].(map[string]any)["id"].(string)
	if snapshot["continuable"] != firstID {
		t.Fatalf("finished task not offered for continuation: %v", snapshot["continuable"])
	}

	conn.WriteJSON(agentClientMsg{Type: "start", Task: "继续，用 8001 端口", ContinueFrom: firstID})
	finals = readUntilIdle(t, conn)
	if len(finals) != 1 || finals[0]["success"] != true {
		t.Fatalf("continued run did not finish: %v", finals)
	}
	mu.Lock()
	second := bodies[1]
	mu.Unlock()
	for _, want := range []string{"deploy vllm", "pip install vllm", "8001"} {
		if !strings.Contains(second, want) {
			t.Fatalf("continued request lost %q", want)
		}
	}
	// A follow-up stays in the same session: one history entry, original title,
	// and the operator's message recorded in its timeline.
	snapshot = latestSnapshot(t, srv, cookie)
	record := snapshot["task"].(map[string]any)
	if record["id"] != firstID || record["title"] != "deploy vllm" {
		t.Fatalf("follow-up created a new session: id=%v title=%v", record["id"], record["title"])
	}
	if tasks := snapshot["tasks"].([]any); len(tasks) != 1 {
		t.Fatalf("history has %d entries, want 1", len(tasks))
	}
	sawUser := false
	for _, e := range record["events"].([]any) {
		if ev := e.(map[string]any); ev["type"] == "user" && ev["text"] == "继续，用 8001 端口" {
			sawUser = true
		}
	}
	if !sawUser {
		t.Fatal("operator follow-up missing from the session timeline")
	}
	if snapshot["continuable"] != firstID {
		t.Fatal("session should remain continuable")
	}
}

func TestContinueRejectsUnknownTask(t *testing.T) {
	srv, cookie := newLLMServer(t)
	srv.llm.SetAPIKey("test")
	conn := agentSocket(t, srv, cookie)
	conn.WriteJSON(agentClientMsg{Type: "start", Task: "go on", ContinueFrom: "not-a-task"})
	event := readAgentMessage(t, conn)
	if event["type"] != "error" || !strings.Contains(event["text"].(string), "无法继续") {
		t.Fatalf("want continuation rejection, got %v", event)
	}
}
