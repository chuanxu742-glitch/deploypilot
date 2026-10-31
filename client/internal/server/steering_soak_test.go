package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deploypilot/deploypilot/internal/agent"
	"github.com/deploypilot/deploypilot/internal/llm"
)

// copyHistoryStage captures exact committed file contents without simulating a
// graceful shutdown. Reopening the copy tests a crash stage with no user process.
func copyHistoryStage(t *testing.T, s *Server) string {
	t.Helper()
	dir := t.TempDir()
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	entries, err := os.ReadDir(s.historyDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if name != "tasks.json" && !((strings.HasPrefix(name, "task-") || strings.HasPrefix(name, "conversation-")) && strings.HasSuffix(name, ".json")) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.historyDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestSoakOrdinaryFollowupSurvivesCrashStage(t *testing.T) {
	var mu sync.Mutex
	var requests []string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		requests = append(requests, string(body))
		mu.Unlock()
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"saved fact\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer provider.Close()
	srv, _ := newLLMServer(t)
	srv.llm = llm.New(llm.Config{BaseURL: provider.URL, APIKey: "test"})
	srv.newExecutor = func() agent.Executor { return staticInfoExecutor{} }
	if reason := srv.startTask("original task", "", "", "initial"); reason != "" {
		t.Fatal(reason)
	}
	srv.taskWG.Wait()
	id := srv.tasks[0].ID
	entered, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	srv.newExecutor = func() agent.Executor { close(entered); <-release; return staticInfoExecutor{} }
	const accepted = "ordinary accepted instruction before process loss"
	if reason := srv.startTask(accepted, "", id, "ordinary-before-crash"); reason != "" {
		t.Fatal(reason)
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("construction gate not reached")
	}
	crashDir := copyHistoryStage(t, srv)
	srv.stopTask()
	close(release)
	srv.taskWG.Wait()
	restored := New(Options{Auth: srv.auth, LLM: srv.llm, Assets: testAssets(), HistoryDir: crashDir})
	defer restored.Shutdown(context.Background())
	restored.newExecutor = func() agent.Executor { return staticInfoExecutor{} }
	if reason := restored.startTask("explicit continuation after process loss", "", id, "after-crash"); reason != "" {
		t.Fatal(reason)
	}
	restored.taskWG.Wait()
	mu.Lock()
	last := requests[len(requests)-1]
	mu.Unlock()
	if count := strings.Count(last, accepted); count != 1 {
		t.Fatalf("accepted ordinary followup occurs %d times after crash stage, want 1", count)
	}
}

func TestSoakAppliedCheckpointDoesNotRegressAfterRestartStop(t *testing.T) {
	second, released := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	calls := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n == 1 {
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"current\",\"type\":\"function\",\"function\":{\"name\":\"run_command\",\"arguments\":\"{\\\"command\\\":\\\"current step\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n")
			return
		}
		if n == 2 {
			close(second)
			select {
			case <-released:
			case <-r.Context().Done():
				return
			}
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"continued\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer provider.Close()
	defer func() {
		select {
		case <-released:
		default:
			close(released)
		}
	}()
	srv, _ := newLLMServer(t)
	srv.llm = llm.New(llm.Config{BaseURL: provider.URL, APIKey: "test"})
	exec := &steeringExecutor{started: make(chan struct{}), release: make(chan struct{})}
	srv.newExecutor = func() agent.Executor { return exec }
	if reason := srv.startTask("checkpoint stage", "", "", "checkpoint-start"); reason != "" {
		t.Fatal(reason)
	}
	<-exec.started
	id := srv.tasks[0].ID
	for i, text := range []string{"first accepted requirement", "second accepted requirement"} {
		if reason := srv.queueSteer(id, text, fmt.Sprintf("batch-%d", i)); reason != "" {
			t.Fatal(reason)
		}
	}
	srv.taskMu.Lock()
	oldTask, err := os.ReadFile(srv.taskPath(id))
	if err != nil {
		t.Fatal(err)
	}
	oldIndex, err := os.ReadFile(filepath.Join(srv.historyDir, "tasks.json"))
	if err != nil {
		t.Fatal(err)
	}
	srv.taskMu.Unlock()
	close(exec.release)
	select {
	case <-second:
	case <-time.After(2 * time.Second):
		t.Fatal("applied model boundary not reached")
	}
	crashDir := copyHistoryStage(t, srv)
	// The checkpoint was committed first; receipts still have the preceding
	// committed task/index bytes, exactly the cross-file crash gap under review.
	if err = os.WriteFile(filepath.Join(crashDir, filepath.Base(srv.taskPath(id))), oldTask, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(crashDir, "tasks.json"), oldIndex, 0600); err != nil {
		t.Fatal(err)
	}
	srv.stopTask()
	close(released)
	srv.taskWG.Wait()
	restored := New(Options{Auth: srv.auth, LLM: srv.llm, Assets: testAssets(), HistoryDir: crashDir})
	defer restored.Shutdown(context.Background())
	entered, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	restored.newExecutor = func() agent.Executor { close(entered); <-release; return staticInfoExecutor{} }
	if reason := restored.startTask("explicit recovery then immediate stop", "", id, "recovery-stop"); reason != "" {
		t.Fatal(reason)
	}
	<-entered
	restored.stopTask()
	close(release)
	restored.taskWG.Wait()
	for _, e := range restored.tasks[0].Events {
		if strings.HasPrefix(e.RequestID, "batch-") && e.State == "steer_deferred" {
			t.Fatalf("checkpoint-applied request %s regressed to deferred after restart/stop", e.RequestID)
		}
	}
}

func TestSoakBatchStopRestartUsesDurableFIFO(t *testing.T) {
	var mu sync.Mutex
	var requests []string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		requests = append(requests, string(body))
		n := len(requests)
		mu.Unlock()
		if n == 1 {
			soakTools(w, []soakCall{{"current", "run_command", `{"command":"current step"}`}})
			return
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"continued\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer provider.Close()
	srv, _ := newLLMServer(t)
	srv.llm = llm.New(llm.Config{BaseURL: provider.URL, APIKey: "test"})
	exec := &steeringExecutor{started: make(chan struct{}), release: make(chan struct{})}
	srv.newExecutor = func() agent.Executor { return exec }
	if reason := srv.startTask("batch restart", "", "", "batch-start"); reason != "" {
		t.Fatal(reason)
	}
	<-exec.started
	id := srv.tasks[0].ID
	texts := []string{"first queued instruction", "second queued instruction", "third queued instruction"}
	for i, text := range texts {
		if reason := srv.queueSteer(id, text, fmt.Sprint("fifo-", i)); reason != "" {
			t.Fatal(reason)
		}
	}
	srv.stopTask()
	srv.taskWG.Wait()
	mu.Lock()
	before := len(requests)
	mu.Unlock()
	if before != 1 {
		t.Fatal("stop started another model request")
	}
	crashDir := copyHistoryStage(t, srv)
	restored := New(Options{Auth: srv.auth, LLM: srv.llm, Assets: testAssets(), HistoryDir: crashDir})
	defer restored.Shutdown(context.Background())
	restored.newExecutor = func() agent.Executor { return staticInfoExecutor{} }
	if len(restored.tasks[0].PendingSteers) != 3 {
		t.Fatal("batch not durably deferred")
	}
	if reason := restored.queueSteer(id, "changed text", "fifo-0"); reason == "" {
		t.Fatal("changed text reused a durable request ID")
	}
	if reason := restored.queueSteer(id, texts[0], "fifo-0"); reason != "" {
		t.Fatal(reason)
	}
	for _, followup := range []string{"explicit continuation", "later question"} {
		if reason := restored.startTask(followup, "", id, followup); reason != "" {
			t.Fatal(reason)
		}
		restored.taskWG.Wait()
		mu.Lock()
		last := requests[len(requests)-1]
		mu.Unlock()
		position := -1
		for _, text := range texts {
			next := strings.Index(last, text)
			if strings.Count(last, text) != 1 || next <= position {
				t.Fatalf("batch missing, duplicated or reordered: %q", text)
			}
			position = next
		}
		if position > strings.Index(last, followup) {
			t.Fatal("older batch was inserted after new continuation")
		}
	}
	if len(restored.tasks[0].PendingSteers) != 0 || len(restored.tasks[0].PendingFollowUps) != 0 {
		t.Fatal("consumed batch remained pending")
	}
	for i := range texts {
		count := 0
		for _, e := range restored.tasks[0].Events {
			if e.RequestID == fmt.Sprint("fifo-", i) && e.State == "steer_applied" {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("fifo-%d applied %d times", i, count)
		}
	}
}

type soakCall struct{ id, name, args string }

func soakTools(w http.ResponseWriter, calls []soakCall) {
	var deltas []map[string]any
	for i, call := range calls {
		deltas = append(deltas, map[string]any{"index": i, "id": call.id, "type": "function", "function": map[string]string{"name": call.name, "arguments": call.args}})
	}
	data, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": deltas}, "finish_reason": "tool_calls"}}})
	fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", data)
}

type soakCommandExecutor struct {
	agent.Executor
	mu       sync.Mutex
	commands []string
}

func (e *soakCommandExecutor) MachineInfo(context.Context) string { return "isolated machine" }
func (e *soakCommandExecutor) Run(_ context.Context, command string, _ time.Duration, _ func(string)) (int, string, error) {
	e.mu.Lock()
	e.commands = append(e.commands, command)
	e.mu.Unlock()
	return 0, "actual check result", nil
}

func TestSoakSteerDefersFinishAndSkipsUnexecutedToolTail(t *testing.T) {
	ready, release := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	var requests []string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		requests = append(requests, string(body))
		n := len(requests)
		mu.Unlock()
		switch n {
		case 1:
			soakTools(w, []soakCall{{"old-check", "run_command", `{"command":"initial health check"}`}})
		case 2:
			close(ready)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			soakTools(w, []soakCall{{"old-finish", "finish", `{"summary":"obsolete","success":true,"verification_call_ids":["old-check"]}`}, {"tail", "run_command", `{"command":"must not execute stale tail"}`}})
		case 3:
			soakTools(w, []soakCall{{"invalid-old-finish", "finish", `{"summary":"old evidence rejected","success":true,"verification_call_ids":["old-check"]}`}})
		case 4:
			soakTools(w, []soakCall{{"fresh-check", "run_command", `{"command":"fresh health check"}`}})
		default:
			soakTools(w, []soakCall{{"fresh-finish", "finish", `{"summary":"updated verified result","success":true,"verification_call_ids":["fresh-check"]}`}})
		}
	}))
	defer provider.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	srv, _ := newLLMServer(t)
	srv.llm = llm.New(llm.Config{BaseURL: provider.URL, APIKey: "test"})
	exec := &soakCommandExecutor{}
	srv.newExecutor = func() agent.Executor { return exec }
	if reason := srv.startTask("finish competition", "", "", "finish-start"); reason != "" {
		t.Fatal(reason)
	}
	select {
	case <-ready:
	case <-time.After(2 * time.Second):
		t.Fatal("finish response gate not reached")
	}
	id := srv.tasks[0].ID
	for i, text := range []string{"revise requirement first", "revise requirement second"} {
		if reason := srv.queueSteer(id, text, fmt.Sprint("finish-", i)); reason != "" {
			t.Fatal(reason)
		}
	}
	close(release)
	srv.taskWG.Wait()
	exec.mu.Lock()
	commands := append([]string(nil), exec.commands...)
	exec.mu.Unlock()
	if len(commands) != 2 || commands[0] != "initial health check" || commands[1] != "fresh health check" {
		t.Fatalf("stale/unverified command executed: %v", commands)
	}
	mu.Lock()
	if len(requests) != 5 {
		t.Fatalf("unexpected model turns: %d", len(requests))
	}
	third := requests[2]
	mu.Unlock()
	if !strings.Contains(third, "not executed (the task ended before this call)") || strings.Count(third, "revise requirement first") != 1 || strings.Count(third, "revise requirement second") != 1 {
		t.Fatal("next turn lost paired skipped tools or batch instructions")
	}
	finals := 0
	for _, e := range srv.tasks[0].Events {
		if e.Type == agent.EventFinal {
			finals++
			if !e.Success || !strings.Contains(e.Text, "updated verified result") {
				t.Fatalf("obsolete final escaped: %+v", e)
			}
		}
	}
	if finals != 1 {
		t.Fatalf("final events=%d", finals)
	}
}

func TestSoakStartDuplicateDoesNotReplayAcrossHistoryAndRestart(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n%2 == 1 {
			soakTools(w, []soakCall{{fmt.Sprint("command-", n), "run_command", `{"command":"one accepted command"}`}})
		} else {
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		}
	}))
	defer provider.Close()
	srv, _ := newLLMServer(t)
	srv.llm = llm.New(llm.Config{BaseURL: provider.URL, APIKey: "test"})
	exec := &soakCommandExecutor{}
	srv.newExecutor = func() agent.Executor { return exec }
	if reason := srv.startTask("original deployment", "", "", "initial-dedupe"); reason != "" {
		t.Fatal(reason)
	}
	srv.taskWG.Wait()
	id := srv.tasks[0].ID
	for _, pair := range [][2]string{{"earlier continuation", "continue-first"}, {"later continuation", "continue-second"}} {
		if reason := srv.startTask(pair[0], "", id, pair[1]); reason != "" {
			t.Fatal(reason)
		}
		srv.taskWG.Wait()
	}
	mu.Lock()
	before := calls
	mu.Unlock()
	duplicates := [][3]string{{"original deployment", "", "initial-dedupe"}, {"earlier continuation", id, "continue-first"}, {"later continuation", id, "continue-second"}}
	for _, request := range duplicates {
		reason := srv.startTask(request[0], "", request[1], request[2])
		if reason == "" {
			srv.taskWG.Wait()
			mu.Lock()
			after := calls
			mu.Unlock()
			t.Fatalf("late duplicate %s was accepted again; model requests %d -> %d", request[2], before, after)
		}
		if !strings.Contains(reason, "不会重放") {
			t.Fatalf("duplicate acceptance not identified: %s", reason)
		}
	}
	if reason := srv.startTask("changed content", "", id, "continue-first"); reason == "" {
		t.Fatal("request ID reused different content")
	}
	if reason := srv.startTask("earlier continuation", "", "", "continue-first"); reason == "" {
		t.Fatal("request ID reused different continuation identity")
	}
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	restored := New(Options{Auth: srv.auth, LLM: srv.llm, Assets: testAssets(), HistoryDir: srv.historyDir})
	defer restored.Shutdown(context.Background())
	restored.newExecutor = func() agent.Executor { return exec }
	if reason := restored.startTask("a separate task", "", "", "separate-start"); reason != "" {
		t.Fatal(reason)
	}
	restored.taskWG.Wait()
	mu.Lock()
	baseline := calls
	mu.Unlock()
	for _, request := range duplicates {
		if reason := restored.startTask(request[0], "", request[1], request[2]); reason == "" {
			restored.taskWG.Wait()
			t.Fatalf("old receipt %s lost after restart/new task", request[2])
		}
	}
	mu.Lock()
	after := calls
	mu.Unlock()
	exec.mu.Lock()
	commandCount := len(exec.commands)
	exec.mu.Unlock()
	if after != baseline || commandCount != 4 {
		t.Fatalf("duplicate replayed work: models=%d want %d; commands=%d want 4", after, baseline, commandCount)
	}
}

func TestSoakStartReceiptCapacityRejectsWithoutForgettingOldIDs(t *testing.T) {
	srv, _ := newLLMServer(t)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("capacity rejection reached the model")
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer provider.Close()
	srv.llm = llm.New(llm.Config{BaseURL: provider.URL, APIKey: "test"})
	task := &taskRecord{ID: "receipt-capacity", Title: "bounded task", Phase: "finished", RequestID: "capacity-1023", Events: []agent.Event{}, StartReceipts: map[string]startReceipt{}}
	for i := 0; i < maxStartReceipts; i++ {
		task.StartReceipts[fmt.Sprint("capacity-", i)] = startReceipt{ContentHash: steerDigest(fmt.Sprint("accepted-", i)), ContinueFrom: task.ID}
	}
	srv.tasks = []*taskRecord{task}
	srv.persistTasksLocked()
	if reason := srv.startTask("new input", "", task.ID, "new-over-capacity"); !strings.Contains(reason, "上限") {
		t.Fatalf("capacity not explicit: %q", reason)
	}
	if srv.taskCancel != nil || srv.tasks[0].Phase != "finished" {
		t.Fatal("capacity rejection mutated task lifecycle")
	}
	if reason := srv.startTask("accepted-0", "", task.ID, "capacity-0"); !strings.Contains(reason, "不会重放") {
		t.Fatalf("old idempotency receipt evicted: %q", reason)
	}
	if len(task.StartReceipts) != maxStartReceipts {
		t.Fatal("capacity failure discarded old receipts")
	}
}

func TestSoakDuplicateStartWSEchoesAcceptedTaskIdentity(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer provider.Close()
	srv, cookie := newLLMServer(t)
	srv.llm = llm.New(llm.Config{BaseURL: provider.URL, APIKey: "test"})
	srv.newExecutor = func() agent.Executor { return staticInfoExecutor{} }
	conn := agentSocket(t, srv, cookie)
	payload := agentClientMsg{Type: "start", Task: "one WS request", RequestID: "wire-duplicate"}
	if err := conn.WriteJSON(payload); err != nil {
		t.Fatal(err)
	}
	readUntilIdle(t, conn)
	id := srv.tasks[0].ID
	for _, text := range []string{"one WS request", "different text"} {
		payload.Task = text
		if err := conn.WriteJSON(payload); err != nil {
			t.Fatal(err)
		}
		event := steeringMessage(t, conn, "error", "wire-duplicate")
		if event["taskId"] != id {
			t.Fatalf("original acceptance identity missing: %v", event)
		}
		if text == "one WS request" && !strings.Contains(event["text"].(string), "不会重放") {
			t.Fatal("duplicate acknowledgement is ambiguous")
		}
		if text != "one WS request" && !strings.Contains(event["text"].(string), "不同内容或会话归属") {
			t.Fatal("changed signature not explicitly rejected")
		}
	}
	mu.Lock()
	count := calls
	mu.Unlock()
	if count != 1 {
		t.Fatalf("duplicate WS request repeated model: %d", count)
	}
}
