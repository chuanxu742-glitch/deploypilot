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

func TestContinueAnySavedTaskAcrossRestart(t *testing.T) {
	var mu sync.Mutex
	var requests []string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		requests = append(requests, string(body))
		mu.Unlock()
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"saved assistant fact\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer provider.Close()
	srv, cookie := newLLMServer(t)
	srv.llm = llm.New(llm.Config{BaseURL: provider.URL, APIKey: "test"})
	srv.newExecutor = func() agent.Executor { return staticInfoExecutor{} }
	if reason := srv.startTask("first deployment", "", "", "first-request"); reason != "" {
		t.Fatal(reason)
	}
	srv.taskWG.Wait()
	firstID := srv.tasks[0].ID
	if reason := srv.startTask("second independent deployment", "", "", "second-request"); reason != "" {
		t.Fatal(reason)
	}
	srv.taskWG.Wait()
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	restored := New(Options{Auth: srv.auth, LLM: srv.llm, HistoryDir: srv.historyDir, Assets: testAssets()})
	defer restored.Shutdown(context.Background())
	restored.newExecutor = func() agent.Executor { return staticInfoExecutor{} }
	if reason := restored.startTask("continue original safely", "", firstID, "continue-request"); reason != "" {
		t.Fatalf("older saved task was not resumable: %s", reason)
	}
	restored.taskWG.Wait()
	mu.Lock()
	last := requests[len(requests)-1]
	mu.Unlock()
	for _, want := range []string{"first deployment", "saved assistant fact", "continue original safely"} {
		if !strings.Contains(last, want) {
			t.Fatalf("saved conversation lost %q: %s", want, last)
		}
	}
	if strings.Contains(last, "second independent deployment") {
		t.Fatal("continuation mixed another task into context")
	}
	response := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/tasks/"+firstID, nil)
	req.AddCookie(cookie)
	restored.Handler().ServeHTTP(response, req)
	var record map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record["id"] != firstID || record["canContinue"] != true || len(restored.tasks) != 2 {
		t.Fatalf("continuation changed identity/capability: %v", record)
	}
}

func TestContinueLegacyRecordReportsSummaryRecovery(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"checking historical facts\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer provider.Close()
	srv, _ := newLLMServer(t)
	srv.llm = llm.New(llm.Config{BaseURL: provider.URL, APIKey: "test"})
	srv.newExecutor = func() agent.Executor { return staticInfoExecutor{} }
	srv.tasks = []*taskRecord{{ID: "legacy-task", Title: "old deployment", Phase: "finished", Events: []agent.Event{{Type: agent.EventThinking, Text: "old observed output"}}}}
	srv.persistTasksLocked()
	if reason := srv.startTask("continue but verify", "", "legacy-task", "legacy-request"); reason != "" {
		t.Fatal(reason)
	}
	srv.taskWG.Wait()
	var announced bool
	for _, e := range srv.tasks[0].Events {
		announced = announced || e.Type == agent.EventStatus && strings.Contains(e.Text, "历史摘要恢复") && strings.Contains(e.Text, "重新检查")
	}
	if !announced {
		t.Fatal("legacy recovery was not disclosed as a historical summary")
	}
}

func TestContinueRejectsCorruptOrMismatchedConversation(t *testing.T) {
	for _, payload := range []string{"broken-json", `{"version":1,"taskId":"another-task"}`} {
		t.Run(payload, func(t *testing.T) {
			srv, _ := newLLMServer(t)
			srv.llm.SetAPIKey("test")
			srv.tasks = []*taskRecord{{ID: "saved-task", Title: "original", Phase: "finished", Events: []agent.Event{{Type: agent.EventThinking, Text: "history"}}}}
			srv.persistTasksLocked()
			path := filepath.Join(srv.historyDir, "conversation-saved-task.json")
			if err := os.WriteFile(path, []byte(payload), 0600); err != nil {
				t.Fatal(err)
			}
			if reason := srv.startTask("go", "", "saved-task", "reject-request"); reason == "" || !strings.Contains(reason, "无法继续") {
				t.Fatalf("invalid conversation was silently accepted: %q", reason)
			}
			if srv.taskCancel != nil || srv.tasks[0].Phase != "finished" {
				t.Fatal("invalid context changed task lifecycle")
			}
		})
	}
}

func TestAcceptedContinuationSurvivesStopBeforeAgentRun(t *testing.T) {
	var mu sync.Mutex
	var requests []string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		requests = append(requests, string(body))
		mu.Unlock()
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"saved response\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer provider.Close()
	srv, _ := newLLMServer(t)
	srv.llm = llm.New(llm.Config{BaseURL: provider.URL, APIKey: "test"})
	srv.newExecutor = func() agent.Executor { return staticInfoExecutor{} }
	if reason := srv.startTask("original deployment", "", "", "initial-run"); reason != "" {
		t.Fatal(reason)
	}
	srv.taskWG.Wait()
	id := srv.tasks[0].ID
	const older = "older deferred instruction before ordinary followup"
	srv.taskMu.Lock()
	task := srv.tasks[0]
	task.PendingSteers = []agent.Instruction{{RequestID: "older-deferred", Text: older}}
	task.SteerReceipts = map[string]string{"older-deferred": steerDigest(older)}
	srv.appendEventLocked(task, agent.Event{Type: agent.EventUser, RequestID: "older-deferred", State: "queued", Text: older})
	srv.appendEventLocked(task, agent.Event{Type: agent.EventActivity, RequestID: "older-deferred", State: "steer_deferred", Text: "pending explicit continuation"})
	srv.persistTasksLocked()
	srv.taskMu.Unlock()
	entered, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	srv.newExecutor = func() agent.Executor { close(entered); <-release; return staticInfoExecutor{} }
	const accepted = "accepted before stop; preserve this ordinary followup"
	if reason := srv.startTask(accepted, "", id, "early-stopped-run"); reason != "" {
		t.Fatal(reason)
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("agent construction gate not reached")
	}
	srv.stopTask()
	close(release)
	srv.taskWG.Wait()
	mu.Lock()
	if len(requests) != 1 {
		t.Fatal("early stop unexpectedly sent a model request")
	}
	mu.Unlock()
	found := false
	for _, e := range srv.tasks[0].Events {
		found = found || e.Type == agent.EventUser && e.Text == accepted
	}
	if !found {
		t.Fatal("accepted ordinary followup was not recorded")
	}
	for _, e := range srv.tasks[0].Events {
		if e.RequestID == "older-deferred" && e.State == "steer_applied" {
			t.Fatal("cancelled run falsely announced pending instruction applied")
		}
	}
	srv.newExecutor = func() agent.Executor { return staticInfoExecutor{} }
	for _, next := range []string{"resume explicitly after early stop", "continue one more time"} {
		if reason := srv.startTask(next, "", id, next); reason != "" {
			t.Fatal(reason)
		}
		srv.taskWG.Wait()
		mu.Lock()
		last := requests[len(requests)-1]
		mu.Unlock()
		if strings.Count(last, accepted) != 1 {
			t.Fatalf("accepted followup absent or duplicated after %q: %s", next, last)
		}
		if strings.Count(last, older) != 1 || strings.Index(last, older) > strings.Index(last, accepted) || strings.Index(last, accepted) > strings.Index(last, next) {
			t.Fatalf("deferred/ordinary continuation order was lost: %s", last)
		}
	}
	applied := 0
	for _, e := range srv.tasks[0].Events {
		if e.RequestID == "older-deferred" && e.State == "steer_applied" {
			applied++
		}
	}
	if applied != 1 {
		t.Fatalf("deferred instruction applied activity count=%d", applied)
	}
}
