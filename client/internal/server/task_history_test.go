package server

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/deploypilot/deploypilot/internal/agent"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTaskHistorySurvivesRestartAndMarksInterrupted(t *testing.T) {
	srv, cookie := newLLMServer(t)
	task := &taskRecord{ID: "history-one", Title: "check deployment", StartedAt: 123, Phase: "running", Events: []agent.Event{}}
	srv.tasks = []*taskRecord{task}
	srv.emitTask(task, agent.Event{Type: agent.EventOutput, Text: "real command result"})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	restored := New(Options{Assets: testAssets(), Auth: srv.auth, LLM: srv.llm, HistoryDir: srv.historyDir})
	defer restored.Shutdown(context.Background())
	if restored.historyErr != nil {
		t.Fatal(restored.historyErr)
	}
	req := httptest.NewRequest("GET", "/api/tasks/history-one", nil)
	req.AddCookie(cookie)
	response := httptest.NewRecorder()
	restored.Handler().ServeHTTP(response, req)
	var got taskRecord
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Phase != "interrupted" || got.ID != task.ID || len(got.Events) != 1 || got.Events[0].Text != "real command result" || got.Events[0].ID != 1 {
		t.Fatalf("restored=%+v", got)
	}
	if restored.taskCancel != nil {
		t.Fatal("restart must not re-execute task")
	}
	response = httptest.NewRecorder()
	restored.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/tasks", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatal("history exposed without authentication", response.Code)
	}
	// This instance already relinquished its lock; avoid a second write during test cleanup.
	srv.historyDir = ""
}

func TestCorruptHistoryIsNotOverwritten(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tasks.json")
	original := []byte("broken history { ")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	srv := New(Options{HistoryDir: dir})
	defer srv.Shutdown(context.Background())
	if srv.historyErr == nil {
		t.Fatal("corruption ignored")
	}
	data, _ := os.ReadFile(path)
	if string(data) != string(original) {
		t.Fatal("corrupt history overwritten")
	}
}

func TestHistoryRejectsConcurrentWriters(t *testing.T) {
	dir := t.TempDir()
	one := New(Options{HistoryDir: dir})
	defer one.Shutdown(context.Background())
	two := New(Options{HistoryDir: dir})
	defer two.Shutdown(context.Background())
	if one.historyErr != nil || two.historyErr == nil {
		t.Fatalf("locks: %v / %v", one.historyErr, two.historyErr)
	}
}

func TestLegacyAuditRestoresOnlyRecordedFacts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit-20261029-120000.jsonl")
	entries := []agent.AuditEntry{
		{Time: time.Unix(100, 0), Kind: "task", Note: "switch drive"},
		{Time: time.Unix(101, 0), Kind: "command", Command: "D:", ExitCode: 0, Output: "D:\\"},
		{Time: time.Unix(102, 0), Kind: "final", Note: "drive changed"},
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if err = json.NewEncoder(file).Encode(entry); err != nil {
			t.Fatal(err)
		}
	}
	file.Close()
	srv := New(Options{HistoryDir: t.TempDir(), LegacyAuditDir: dir})
	defer srv.Shutdown(context.Background())
	if len(srv.tasks) != 1 || srv.tasks[0].Title != "switch drive" || len(srv.tasks[0].Events) != 5 || srv.tasks[0].Events[3].ExitCode != 0 {
		t.Fatalf("legacy tasks: %+v", srv.tasks)
	}
	if _, err = os.Stat(path); err != nil {
		t.Fatal("original audit removed", err)
	}
}

func TestTaskHistoryDoesNotDeleteEarlierSessions(t *testing.T) {
	srv, cookie := newLLMServer(t)
	for i := 0; i < 55; i++ {
		record := &taskRecord{ID: fmt.Sprintf("task-%d", i), Title: fmt.Sprintf("history %d", i), Phase: "finished", Events: []agent.Event{{Type: agent.EventStatus, Text: "preserved result"}}}
		srv.tasks = append([]*taskRecord{record}, srv.tasks...)
		srv.persistTasksLocked()
	}
	if len(srv.tasks) != 55 {
		t.Fatal("old session deleted")
	}
	if srv.tasks[len(srv.tasks)-1].Events != nil {
		t.Fatal("old output should be loaded on demand")
	}
	req := httptest.NewRequest("GET", "/api/tasks/task-0", nil)
	req.AddCookie(cookie)
	response := httptest.NewRecorder()
	srv.Handler().ServeHTTP(response, req)
	var got taskRecord
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Title != "history 0" || len(got.Events) != 1 || got.Events[0].Text != "preserved result" {
		t.Fatal("old history not readable", response.Body.String())
	}
}
