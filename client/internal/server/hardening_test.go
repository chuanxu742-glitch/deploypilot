package server

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/deploypilot/deploypilot/internal/agent"
	"github.com/gorilla/websocket"
)

func TestIdleSessionSendsExpiryCloseCode(t *testing.T) {
	srv, cookie := shortSessionServer(t)
	c := agentSocket(t, srv, cookie)
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, _ = c.ReadMessage() // initial snapshot
	_, _, err := c.ReadMessage()
	ce, ok := err.(*websocket.CloseError)
	if !ok {
		t.Fatalf("expected a websocket close frame on expiry, got %T: %v", err, err)
	}
	if ce.Code != 4401 {
		t.Fatalf("expiry close code = %d, want 4401 (session expired)", ce.Code)
	}
}

func TestCorruptScrollbackDoesNotDisableSession(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "linux" {
		t.Skip("PTY unavailable")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "terminal.json"), []byte("{ not valid json"), 0600); err != nil {
		t.Fatal(err)
	}
	shell, err := newSharedShell(dir)
	if err != nil {
		t.Fatalf("corrupt scrollback must not fail the session: %v", err)
	}
	defer shell.Close()
	if _, err := os.Stat(filepath.Join(dir, "terminal.json.corrupt")); err != nil {
		t.Fatal("corrupt scrollback was not preserved for inspection")
	}
}

func TestBoundedOutputKeepsHeadAndTail(t *testing.T) {
	b := &boundedOutput{halfLimit: 4}
	b.WriteString("ABCDEFGHIJ") // head ABCD, middle EF, tail GHIJ
	got := b.String()
	if !strings.HasPrefix(got, "ABCD") {
		t.Fatalf("head not kept: %q", got)
	}
	if !strings.HasSuffix(got, "GHIJ") {
		t.Fatalf("tail (where failures appear) not kept: %q", got)
	}
	if !strings.Contains(got, "truncated") {
		t.Fatalf("truncation not marked: %q", got)
	}
	if strings.Contains(got, "EF") {
		t.Fatalf("middle should be omitted: %q", got)
	}
}

func TestBoundedOutputShortIsVerbatim(t *testing.T) {
	b := &boundedOutput{halfLimit: 16}
	b.WriteString("hello")
	if got := b.String(); got != "hello" {
		t.Fatalf("short output altered: %q", got)
	}
}

func TestBoundedOutputRuneSafeBoundaries(t *testing.T) {
	// 300 bytes of 3-byte runes, halfLimit forces a cut mid-rune.
	b := &boundedOutput{halfLimit: 10}
	b.WriteString(strings.Repeat("世", 100))
	if got := b.String(); !utf8.ValidString(got) {
		t.Fatalf("result is not valid UTF-8: %q", got)
	}
}

func TestHistoryStartupErrorNamesDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tasks.json"), []byte("broken {"), 0600); err != nil {
		t.Fatal(err)
	}
	srv := New(Options{HistoryDir: dir})
	defer func() { srv.historyDir = ""; srv.Shutdown(context.Background()) }()
	_, err := srv.Start()
	if err == nil {
		t.Fatal("corrupt history should fail startup")
	}
	if !strings.Contains(err.Error(), dir) {
		t.Fatalf("startup error does not name the history directory: %v", err)
	}
}

func TestTruncateUTF8RuneBoundary(t *testing.T) {
	// A string of multi-byte runes (each "世" is 3 bytes).
	s := strings.Repeat("世", 100) // 300 bytes
	for _, max := range []int{0, 1, 2, 3, 4, 100, 299, 300, 301} {
		got := truncateUTF8(s, max)
		if len(got) > max {
			t.Fatalf("max=%d: result %d bytes exceeds limit", max, len(got))
		}
		if !utf8.ValidString(got) {
			t.Fatalf("max=%d: result is not valid UTF-8: %q", max, got)
		}
		if max >= len(s) && got != s {
			t.Fatalf("max=%d: should return the whole string unchanged", max)
		}
	}
}

func TestTruncateUTF8ASCIIUnchanged(t *testing.T) {
	s := "hello world"
	if got := truncateUTF8(s, 100); got != s {
		t.Fatalf("short ASCII string was altered: %q", got)
	}
	if got := truncateUTF8(s, 5); got != "hello" {
		t.Fatalf("ASCII truncation = %q, want %q", got, "hello")
	}
}

func TestStreamingDeltasAreStoredAsOneEvent(t *testing.T) {
	srv := New(Options{})
	task := &taskRecord{ID: "t", Events: []agent.Event{}}
	srv.tasks = []*taskRecord{task}
	for _, part := range []string{"有的", "，当前", "电脑已安装 Pyth", "on 3.11.9。"} {
		srv.emitTask(task, agent.Event{Type: agent.EventThinking, Text: part})
	}
	srv.emitTask(task, agent.Event{Type: agent.EventToolCall, Tool: "run_command", CallID: "c1"})
	srv.emitTask(task, agent.Event{Type: agent.EventOutput, Text: "line 1\n", CallID: "c1"})
	srv.emitTask(task, agent.Event{Type: agent.EventOutput, Text: "line 2\n", CallID: "c1"})
	srv.emitTask(task, agent.Event{Type: agent.EventThinking, Text: "下一段"})
	if len(task.Events) != 4 {
		t.Fatalf("events=%d, want 4 (reply, call, output, reply): %+v", len(task.Events), task.Events)
	}
	if task.Events[0].Text != "有的，当前电脑已安装 Python 3.11.9。" {
		t.Fatalf("reply not merged: %q", task.Events[0].Text)
	}
	if task.Events[2].Text != "line 1\nline 2\n" {
		t.Fatalf("output not merged: %q", task.Events[2].Text)
	}
	if task.Events[3].Text != "下一段" {
		t.Fatal("a new reply after a tool call must stay separate")
	}
}

func TestMergedOutputKeepsTailWithinLimit(t *testing.T) {
	srv := New(Options{})
	task := &taskRecord{ID: "t", Events: []agent.Event{}}
	srv.tasks = []*taskRecord{task}
	chunk := strings.Repeat("构建日志", 1000) // 12 KB per delta
	for i := 0; i < 5; i++ {
		srv.emitTask(task, agent.Event{Type: agent.EventOutput, Text: chunk})
	}
	srv.emitTask(task, agent.Event{Type: agent.EventOutput, Text: "FATAL_END"})
	got := task.Events[0].Text
	if len(task.Events) != 1 || len(got) > maxStoredEventText {
		t.Fatalf("events=%d len=%d", len(task.Events), len(got))
	}
	if !strings.HasSuffix(got, "FATAL_END") || !strings.HasPrefix(got, truncatedMarker) || !utf8.ValidString(got) {
		t.Fatal("merged output lost its tail, marker or UTF-8 validity")
	}
}
