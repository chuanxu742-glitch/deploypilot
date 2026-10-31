package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/deploypilot/deploypilot/internal/agent"
)

// An antivirus scan or backup briefly opening tasks.json must not leave the
// history permanently unwritable. On Windows the open handle blocks the
// replacing rename until it closes.
func TestHistorySurvivesABriefOutsideReader(t *testing.T) {
	dir := t.TempDir()
	srv := New(Options{HistoryDir: dir})
	defer srv.historyLock.Close()
	f, err := os.Open(filepath.Join(dir, "tasks.json"))
	if err != nil {
		t.Fatal(err)
	}
	release := time.AfterFunc(300*time.Millisecond, func() { f.Close() })
	defer release.Stop()
	srv.taskMu.Lock()
	defer srv.taskMu.Unlock()
	srv.tasks = append(srv.tasks, &taskRecord{ID: "abc", Title: "t", Phase: "finished", Events: []agent.Event{}})
	srv.persistTasksLocked()
	if srv.historyErr != nil {
		t.Fatalf("a 300ms outside reader disabled history: %v", srv.historyErr)
	}
	data, err := os.ReadFile(filepath.Join(dir, "tasks.json"))
	if err != nil || !strings.Contains(string(data), `"abc"`) {
		t.Fatalf("tasks.json not updated: %s %v", data, err)
	}
}
