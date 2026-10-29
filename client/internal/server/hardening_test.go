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
