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

type steeringExecutor struct {
	agent.Executor
	started chan struct{}
	release chan struct{}
}

func (e *steeringExecutor) MachineInfo(context.Context) string { return "isolated test machine" }
func (e *steeringExecutor) Run(ctx context.Context, _ string, _ time.Duration, _ func(string)) (int, string, error) {
	close(e.started)
	select {
	case <-e.release:
		return 0, "current step checked", nil
	case <-ctx.Done():
		return -1, "stopped", ctx.Err()
	}
}

func steeringMessage(t *testing.T, conn *websocket.Conn, kind, request string) map[string]any {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	for i := 0; i < 100; i++ {
		var event map[string]any
		if err := conn.ReadJSON(&event); err != nil {
			t.Fatalf("waiting for %s/%s: %v", kind, request, err)
		}
		if event["type"] == kind && (request == "" || event["requestId"] == request) {
			return event
		}
	}
	t.Fatalf("no matching %s/%s", kind, request)
	return nil
}

func sendSteer(t *testing.T, conn *websocket.Conn, id, text, request string) {
	t.Helper()
	if err := conn.WriteJSON(map[string]string{"type": "steer", "taskId": id, "task": text, "requestId": request}); err != nil {
		t.Fatal(err)
	}
}

func TestSteerQueuedDuringToolSurvivesDisconnectAndDeduplicates(t *testing.T) {
	var mu sync.Mutex
	var requests []string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		requests = append(requests, string(body))
		n := len(requests)
		mu.Unlock()
		if n == 1 {
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"check\",\"type\":\"function\",\"function\":{\"name\":\"run_command\",\"arguments\":\"{\\\"command\\\":\\\"current step\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n")
			return
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"updated reply\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer provider.Close()
	srv, cookie := newLLMServer(t)
	srv.llm = llm.New(llm.Config{BaseURL: provider.URL, APIKey: "test"})
	exec := &steeringExecutor{started: make(chan struct{}), release: make(chan struct{})}
	srv.newExecutor = func() agent.Executor { return exec }
	conn := agentSocket(t, srv, cookie)
	conn.WriteJSON(agentClientMsg{Type: "start", Task: "original task"})
	running := steeringMessage(t, conn, "run_state", "")
	id := running["taskId"].(string)
	select {
	case <-exec.started:
	case <-time.After(2 * time.Second):
		t.Fatal("tool did not start")
	}
	sendSteer(t, conn, id, "change port to 9001", "steer-one")
	if ack := steeringMessage(t, conn, "steer_ack", "steer-one"); ack["state"] != "queued" || ack["taskId"] != id {
		t.Fatalf("bad acknowledgement: %v", ack)
	}
	sendSteer(t, conn, id, "change port to 9001", "steer-one")
	steeringMessage(t, conn, "steer_ack", "steer-one")
	sendSteer(t, conn, id, "different text", "steer-one")
	steeringMessage(t, conn, "error", "steer-one")
	sendSteer(t, conn, "another-task", "wrong task", "steer-other")
	steeringMessage(t, conn, "error", "steer-other")
	srv.taskMu.Lock()
	if len(srv.tasks[0].PendingSteers) != 1 {
		t.Fatal("duplicate instruction queued")
	}
	srv.taskMu.Unlock()
	conn.Close()
	reconnected := agentSocket(t, srv, cookie)
	close(exec.release)
	readUntilIdle(t, reconnected)
	mu.Lock()
	last := requests[len(requests)-1]
	mu.Unlock()
	if strings.Count(last, "change port to 9001") != 1 {
		t.Fatalf("instruction absent or duplicated: %s", last)
	}
	srv.taskMu.Lock()
	defer srv.taskMu.Unlock()
	users, applied := 0, 0
	for _, event := range srv.tasks[0].Events {
		if event.RequestID == "steer-one" {
			if event.Type == agent.EventUser {
				users++
			}
			if event.State == "steer_applied" {
				applied++
			}
		}
	}
	if users != 1 || applied != 1 || len(srv.tasks[0].PendingSteers) != 0 {
		t.Fatalf("users=%d applied=%d pending=%d", users, applied, len(srv.tasks[0].PendingSteers))
	}
}

func TestSteerWinsRaceAgainstFinalReply(t *testing.T) {
	first := make(chan struct{})
	release := make(chan struct{})
	var mu sync.Mutex
	calls := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		text := "updated final"
		if n == 1 {
			close(first)
			<-release
			text = "obsolete final"
		}
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", text)
	}))
	defer provider.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	srv, cookie := newLLMServer(t)
	srv.llm = llm.New(llm.Config{BaseURL: provider.URL, APIKey: "test"})
	srv.newExecutor = func() agent.Executor { return staticInfoExecutor{} }
	conn := agentSocket(t, srv, cookie)
	conn.WriteJSON(agentClientMsg{Type: "start", Task: "finish race"})
	running := steeringMessage(t, conn, "run_state", "")
	id := running["taskId"].(string)
	<-first
	sendSteer(t, conn, id, "new requirement", "last-moment")
	steeringMessage(t, conn, "steer_ack", "last-moment")
	close(release)
	finals := readUntilIdle(t, conn)
	if len(finals) != 1 || finals[0]["text"] != "updated final" {
		t.Fatalf("accepted instruction lost at final: %v", finals)
	}
	sendSteer(t, conn, id, "too late", "after-final")
	steeringMessage(t, conn, "error", "after-final")
}

func TestPendingSteerRemainsVisibleWhenHistoryIsBounded(t *testing.T) {
	srv, _ := newLLMServer(t)
	task := &taskRecord{ID: "visible-pending", Title: "original", Phase: "running", Events: []agent.Event{}, PendingSteers: []agent.Instruction{{RequestID: "queued-one", Text: "must remain visible"}}}
	srv.taskMu.Lock()
	defer srv.taskMu.Unlock()
	srv.tasks = []*taskRecord{task}
	srv.appendEventLocked(task, agent.Event{Type: agent.EventUser, State: "queued", RequestID: "queued-one", Text: "must remain visible"})
	for i := 0; i < 600; i++ {
		srv.appendEventLocked(task, agent.Event{Type: agent.EventStatus, Text: fmt.Sprintf("progress %d", i)})
	}
	found := false
	for _, e := range task.Events {
		found = found || e.RequestID == "queued-one"
	}
	if !found {
		t.Fatal("accepted pending user message was trimmed out of the snapshot")
	}
	if len(task.Events) > 512 {
		t.Fatal("history event budget exceeded")
	}
}

func TestSteerStopDefersUntilExplicitRestartContinuation(t *testing.T) {
	var mu sync.Mutex
	var requests []string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		requests = append(requests, string(body))
		n := len(requests)
		mu.Unlock()
		if n == 1 {
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"check\",\"type\":\"function\",\"function\":{\"name\":\"run_command\",\"arguments\":\"{\\\"command\\\":\\\"current step\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n")
			return
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"continued safely\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer provider.Close()
	srv, cookie := newLLMServer(t)
	srv.llm = llm.New(llm.Config{BaseURL: provider.URL, APIKey: "test"})
	exec := &steeringExecutor{started: make(chan struct{}), release: make(chan struct{})}
	srv.newExecutor = func() agent.Executor { return exec }
	conn := agentSocket(t, srv, cookie)
	conn.WriteJSON(agentClientMsg{Type: "start", Task: "stop race"})
	id := steeringMessage(t, conn, "run_state", "")["taskId"].(string)
	<-exec.started
	sendSteer(t, conn, id, "earlier queued requirement", "deferred-one")
	steeringMessage(t, conn, "steer_ack", "deferred-one")
	conn.WriteJSON(agentClientMsg{Type: "stop"})
	readUntilIdle(t, conn)
	mu.Lock()
	if len(requests) != 1 {
		t.Fatal("stop automatically started another model request")
	}
	mu.Unlock()
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	restored := New(Options{Auth: srv.auth, LLM: srv.llm, HistoryDir: srv.historyDir, Assets: testAssets()})
	defer restored.Shutdown(context.Background())
	restored.newExecutor = func() agent.Executor { return staticInfoExecutor{} }
	if reason := restored.startTask("new explicit continuation", "", id, "continue-after-stop"); reason != "" {
		t.Fatal(reason)
	}
	restored.taskWG.Wait()
	mu.Lock()
	last := requests[len(requests)-1]
	mu.Unlock()
	if strings.Index(last, "earlier queued requirement") < 0 || strings.Index(last, "earlier queued requirement") > strings.Index(last, "new explicit continuation") {
		t.Fatalf("deferred messages lost or reordered: %s", last)
	}
	if reason := restored.queueSteer(id, "earlier queued requirement", "deferred-one"); reason != "" {
		t.Fatalf("durable retry receipt lost: %s", reason)
	}
	if len(restored.tasks[0].PendingSteers) != 0 {
		t.Fatal("old request replayed after restart")
	}
	encoded, _ := json.Marshal(restored.tasks[0])
	if !strings.Contains(string(encoded), "steer_deferred") {
		t.Fatal("unapplied stop status was not saved")
	}
}

func TestSteerQueueEnforcesInputAndCapacityBounds(t *testing.T) {
	for _, size := range []int{1, maxSteerTextBytes} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			srv, _ := newLLMServer(t)
			ctx, cancel := context.WithCancel(srv.baseCtx)
			defer cancel()
			_ = ctx
			srv.taskCancel = cancel
			srv.taskAcceptingSteers = true
			srv.tasks = []*taskRecord{{ID: "queue-bounds", Title: "task", Phase: "running", Events: []agent.Event{}}}
			limit := min(maxQueuedSteers, maxQueuedSteerBytes/size)
			for i := 0; i < limit; i++ {
				if reason := srv.queueSteer("queue-bounds", strings.Repeat("x", size), fmt.Sprint(i)); reason != "" {
					t.Fatal(reason)
				}
			}
			if reason := srv.queueSteer("queue-bounds", strings.Repeat("x", size), "overflow"); reason == "" {
				t.Fatal("queue overflow was accepted")
			}
			if reason := srv.queueSteer("queue-bounds", "", "empty"); reason == "" {
				t.Fatal("empty message was accepted")
			}
			if reason := srv.queueSteer("queue-bounds", "valid", ""); reason == "" {
				t.Fatal("missing request identity was accepted")
			}
			if reason := srv.queueSteer("queue-bounds", strings.Repeat("x", maxSteerTextBytes+1), "too-long"); reason == "" {
				t.Fatal("oversized message was accepted")
			}
		})
	}
}
