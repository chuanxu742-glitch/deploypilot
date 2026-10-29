package server

import (
	"context"
	"github.com/deploypilot/deploypilot/internal/agent"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deploypilot/deploypilot/internal/llm"
	"github.com/gorilla/websocket"
)

func agentSocket(t *testing.T, srv *Server, cookie *http.Cookie) *websocket.Conn {
	t.Helper()
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	header := http.Header{}
	header.Add("Cookie", cookie.Name+"="+cookie.Value)
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+"/ws/agent", header)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

// Hold cleanup after cancellation to test restart without depending on shell timing.
type heldExecutor struct {
	agent.Executor
	started   chan struct{}
	cancelled chan struct{}
	release   chan struct{}
}

func (e *heldExecutor) MachineInfo(ctx context.Context) string {
	close(e.started)
	<-ctx.Done()
	close(e.cancelled)
	<-e.release
	return "test environment"
}

func waitAgentState(t *testing.T, conn *websocket.Conn, state string) {
	t.Helper()
	for i := 0; i < 100; i++ {
		event := readAgentMessage(t, conn)
		if event["type"] == "run_state" && event["state"] == state {
			return
		}
	}
	t.Fatalf("state %q not received", state)
}

func TestAgentStopWaitsForCleanupBeforeRestart(t *testing.T) {
	srv, cookie := newLLMServer(t)
	terminalUpdates, unsubscribe := srv.subscribeTerminalAgent()
	defer unsubscribe()
	terminalMessage(t, terminalUpdates)
	srv.llm.SetAPIKey("test")
	executor := &heldExecutor{started: make(chan struct{}), cancelled: make(chan struct{}), release: make(chan struct{})}
	var release sync.Once
	unblock := func() { release.Do(func() { close(executor.release) }) }
	var calls atomic.Int32
	srv.newExecutor = func() agent.Executor { calls.Add(1); return executor }
	conn := agentSocket(t, srv, cookie)
	defer unblock()
	conn.WriteJSON(agentClientMsg{Type: "start", Task: "test stop"})
	waitAgentState(t, conn, "running")
	if got := terminalMessage(t, terminalUpdates); got["state"] != "running" {
		t.Fatal(got)
	}
	select {
	case <-executor.started:
	case <-time.After(time.Second):
		t.Fatal("executor not started")
	}
	conn.WriteJSON(agentClientMsg{Type: "stop"})
	waitAgentState(t, conn, "stopping")
	select {
	case <-executor.cancelled:
	case <-time.After(time.Second):
		t.Fatal("stop did not cancel executor")
	}
	conn.WriteJSON(agentClientMsg{Type: "start", Task: "must not overlap"})
	if event := readAgentMessage(t, conn); event["type"] != "error" || !strings.Contains(event["text"].(string), "已有任务") {
		t.Fatalf("want busy rejection, got %v", event)
	}
	if calls.Load() != 1 {
		t.Fatal("overlapping executor started")
	}
	for {
		got := terminalMessage(t, terminalUpdates)
		if got["state"] == "idle" {
			t.Fatal("terminal released before cleanup")
		}
		if got["state"] == "stopping" {
			break
		}
	}
	unblock()
	waitAgentState(t, conn, "idle")
	for {
		if got := terminalMessage(t, terminalUpdates); got["state"] == "idle" {
			break
		}
	}
}

func TestAgentDisconnectKeepsRunAndReconnectRestores(t *testing.T) {
	srv, cookie := newLLMServer(t)
	srv.llm.SetAPIKey("test")
	executor := &heldExecutor{started: make(chan struct{}), cancelled: make(chan struct{}), release: make(chan struct{})}
	srv.newExecutor = func() agent.Executor { return executor }
	conn := agentSocket(t, srv, cookie)
	defer close(executor.release)
	conn.WriteJSON(agentClientMsg{Type: "start", Task: "disconnect test"})
	waitAgentState(t, conn, "running")
	select {
	case <-executor.started:
	case <-time.After(time.Second):
		t.Fatal("executor not started")
	}
	conn.Close()
	select {
	case <-executor.cancelled:
		t.Fatal("disconnect cancelled task")
	case <-time.After(100 * time.Millisecond):
	}
	reconnect := agentSocket(t, srv, cookie)
	var snapshot map[string]any
	reconnect.SetReadDeadline(time.Now().Add(time.Second))
	if err := reconnect.ReadJSON(&snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot["type"] != "snapshot" || snapshot["state"] != "running" || snapshot["task"].(map[string]any)["title"] != "disconnect test" {
		t.Fatal(snapshot)
	}
	reconnect.WriteJSON(agentClientMsg{Type: "stop"})
	waitAgentState(t, reconnect, "stopping")
	select {
	case <-executor.cancelled:
	case <-time.After(time.Second):
		t.Fatal("stop did not cancel")
	}

}

func readAgentMessage(t *testing.T, conn *websocket.Conn) map[string]any {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		var message map[string]any
		if err := conn.ReadJSON(&message); err != nil {
			t.Fatal(err)
		}
		if message["type"] != "snapshot" {
			return message
		}
	}
}

func TestAgentRejectsStartWithIdleAcknowledgement(t *testing.T) {
	srv, cookie := newLLMServer(t)
	conn := agentSocket(t, srv, cookie)
	conn.WriteJSON(agentClientMsg{Type: "start", Task: "check"})
	if event := readAgentMessage(t, conn); event["type"] != "error" {
		t.Fatalf("want error, got %v", event)
	}
	if event := readAgentMessage(t, conn); event["type"] != "run_state" || event["state"] != "idle" {
		t.Fatalf("want idle acknowledgement, got %v", event)
	}
}

func TestAgentCompletionAcknowledgesCleanupAndAllowsRestart(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
	}))
	defer provider.Close()
	srv, cookie := newLLMServer(t)
	srv.llm = llm.New(llm.Config{BaseURL: provider.URL, APIKey: "test"})
	srv.newExecutor = func() agent.Executor { return staticInfoExecutor{} }
	conn := agentSocket(t, srv, cookie)
	for attempt := 0; attempt < 2; attempt++ {
		conn.WriteJSON(agentClientMsg{Type: "start", Task: "report environment"})
		if event := readAgentMessage(t, conn); event["type"] != "run_state" || event["state"] != "running" {
			t.Fatalf("want running, got %v", event)
		}
		final := false
		for {
			event := readAgentMessage(t, conn)
			if event["type"] == "final" {
				final = true
			}
			if event["type"] == "run_state" && event["state"] == "idle" {
				break
			}
		}
		if !final {
			t.Fatal("cleanup acknowledged before final result")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}

type staticInfoExecutor struct{ agent.Executor }

func (staticInfoExecutor) MachineInfo(context.Context) string { return "test environment" }
