package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/deploypilot/deploypilot/internal/agent"
)

// A directory at terminal.json leaves task history valid but makes terminal
// initialization fail. Use the normal constructor and executor factory so this
// does not depend on a partially initialized Server or a fake file executor.
func sessionWithUnavailableTerminal(t *testing.T) (*Server, string) {
	t.Helper()
	history := t.TempDir()
	blocked := filepath.Join(history, "terminal.json")
	if err := os.Mkdir(blocked, 0700); err != nil {
		t.Fatal(err)
	}
	srv := New(Options{HistoryDir: history})
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	if srv.historyErr != nil {
		t.Fatalf("task history must remain usable: %v", srv.historyErr)
	}
	if _, err := srv.sharedTerminal(); err == nil {
		t.Fatal("expected terminal initialization to fail")
	}
	return srv, blocked
}

func TestSessionExecutorRelativeFilesRequireTerminal(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, operation := range []string{"read", "write", "create", "append", "edit"} {
		t.Run(operation, func(t *testing.T) {
			srv, blocked := sessionWithUnavailableTerminal(t)
			executor := srv.newExecutor()
			path := operation + ".txt"
			const original = "launch-directory sentinel"
			if operation != "create" {
				if err := os.WriteFile(path, []byte(original), 0600); err != nil {
					t.Fatal(err)
				}
			}
			var err error
			switch operation {
			case "read":
				var result agent.ReadResult
				result, err = executor.ReadFile(path, 0, 1024)
				if result != (agent.ReadResult{}) {
					t.Errorf("relative read returned launch-directory data: %+v", result)
				}
			case "write", "create":
				err = executor.WriteFile(path, "replacement")
			case "append":
				err = executor.AppendFile(path, "+append")
			case "edit":
				var count int
				count, err = executor.EditFile(path, "sentinel", "changed", false)
				if count != 0 {
					t.Errorf("relative edit reported %d replacements", count)
				}
			}
			var pathErr *os.PathError
			if !errors.As(err, &pathErr) || pathErr.Path != blocked {
				t.Errorf("must preserve terminal initialization error for %s, got %v", blocked, err)
			}
			data, readErr := os.ReadFile(path)
			t.Logf("%s: error=%v; launch-directory file=%q; read error=%v", operation, err, data, readErr)
			if operation == "create" {
				if !errors.Is(readErr, os.ErrNotExist) {
					t.Errorf("failed relative write created a file: content=%q; error=%v", data, readErr)
				}
			} else if readErr != nil || string(data) != original {
				t.Errorf("launch-directory file changed: content=%q; error=%v", data, readErr)
			}
		})
	}
}

func TestSessionExecutorAbsoluteFilesDoNotRequireTerminal(t *testing.T) {
	srv, _ := sessionWithUnavailableTerminal(t)
	executor := srv.newExecutor()
	path := filepath.Join(t.TempDir(), "absolute.txt")
	if err := executor.WriteFile(path, "initial"); err != nil {
		t.Fatal(err)
	}
	if err := executor.AppendFile(path, "+append"); err != nil {
		t.Fatal(err)
	}
	if count, err := executor.EditFile(path, "initial", "edited", false); err != nil || count != 1 {
		t.Fatalf("absolute edit: count=%d, error=%v", count, err)
	}
	if result, err := executor.ReadFile(path, 0, 1024); err != nil || result.Content != "edited+append" {
		t.Fatalf("absolute read: result=%+v, error=%v", result, err)
	}
}

func TestSessionExecutorRelativeFilesRecoverWithShellDirectory(t *testing.T) {
	t.Chdir(t.TempDir())
	if runtime.GOOS != "windows" && runtime.GOOS != "linux" {
		t.Skip("PTY unavailable")
	}
	srv, blocked := sessionWithUnavailableTerminal(t)
	executor := srv.newExecutor()
	if err := executor.WriteFile("must-not-exist.txt", "unexpected"); err == nil {
		// Clean up the original implementation's erroneous write on failure.
		_ = os.Remove("must-not-exist.txt")
		t.Fatal("write succeeded while terminal was unavailable")
	}
	if err := os.Remove(blocked); err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	command := "cd " + shellQuote(target)
	if runtime.GOOS == "windows" {
		command = "Set-Location -LiteralPath " + psQuote(target)
	}
	if code, out, err := executor.Run(context.Background(), command, 10*time.Second, nil); err != nil || code != 0 {
		t.Fatalf("change directory: code=%d, error=%v, output=%q", code, err, out)
	}
	if err := executor.WriteFile("relative.txt", "initial"); err != nil {
		t.Fatal(err)
	}
	if err := executor.AppendFile("relative.txt", "+append"); err != nil {
		t.Fatal(err)
	}
	if count, err := executor.EditFile("relative.txt", "initial", "edited", false); err != nil || count != 1 {
		t.Fatalf("relative edit: count=%d, error=%v", count, err)
	}
	if result, err := executor.ReadFile("relative.txt", 0, 1024); err != nil || result.Content != "edited+append" {
		t.Fatalf("relative read: result=%+v, error=%v", result, err)
	}
	data, err := os.ReadFile(filepath.Join(target, "relative.txt"))
	if err != nil || strings.TrimSpace(string(data)) != "edited+append" {
		t.Fatalf("file did not follow shared-shell directory: content=%q, error=%v", data, err)
	}
}
