package server

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"
)

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
