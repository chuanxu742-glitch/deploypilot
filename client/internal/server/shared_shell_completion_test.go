package server

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// Completion must use the controlling PTY, not the shell's mutable stdout.
// Keep the redirected stdout persistent so later commands honor shell state.
func TestSharedShellCompletesWithRedirectedStdout(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux PTY regression")
	}
	for _, destination := range []string{"file", "closed"} {
		t.Run(destination, func(t *testing.T) {
			shell, err := newSharedShell(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer shell.Close()
			log := filepath.Join(t.TempDir(), "command.log")
			redirect := "exec >" + shellQuote(log) + " 2>&1; printf first"
			if destination == "closed" {
				redirect = "exec 1>&- 2>&-; :"
			}
			run := func(command string, wantCode int) string {
				t.Helper()
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				code, out, err := shell.Run(ctx, command, nil)
				if err != nil || code != wantCode {
					t.Fatalf("command %q: code=%d want=%d err=%v output=%s", command, code, wantCode, err, out)
				}
				return out
			}
			run(redirect, 0)
			run("false", 1)
			if destination == "file" {
				run("printf second", 0)
				got, err := os.ReadFile(log)
				if err != nil || !strings.Contains(string(got), "first") || !strings.Contains(string(got), "second") || strings.Contains(string(got), "\x1b]777;") {
					t.Fatalf("redirected stdout changed or contains completion marker: %q err=%v", got, err)
				}
			}
			run("exec >/dev/tty 2>&1", 0)
			if out := run("printf restored-output", 0); !strings.Contains(out, "restored-output") {
				t.Fatalf("stdout restoration failed: %q", out)
			}
		})
	}
}

func TestSharedShellCompletionFollowsLastOutput(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux PTY regression")
	}
	shell, err := newSharedShell(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer shell.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var streamMu sync.Mutex
	var streamed strings.Builder
	code, out, err := shell.Run(ctx, "printf before-redirect; exec >/dev/null 2>&1; :", func(chunk string) { streamMu.Lock(); defer streamMu.Unlock(); streamed.WriteString(chunk) })
	streamMu.Lock()
	defer streamMu.Unlock()
	if err != nil || code != 0 || !strings.Contains(out, "before-redirect") || !strings.Contains(streamed.String(), "before-redirect") {
		t.Fatalf("completion lost earlier PTY output: code=%d err=%v output=%q streamed=%q", code, err, out, streamed.String())
	}
}

func TestSharedShellCancellationAfterStdoutRedirection(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux PTY regression")
	}
	shell, err := newSharedShell(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer shell.Close()
	setup, setupCancel := context.WithTimeout(context.Background(), time.Second)
	code, out, err := shell.Run(setup, "exec >/dev/null 2>&1", nil)
	setupCancel()
	if err != nil || code != 0 {
		t.Fatalf("setup code=%d err=%v output=%q", code, err, out)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	code, _, err = shell.Run(ctx, "sleep 10", nil)
	if err == nil || code == 0 {
		t.Fatalf("cancelled command reported success: code=%d err=%v", code, err)
	}
	if time.Since(started) > 4*time.Second {
		t.Fatal("cancellation exceeded cleanup grace")
	}
}
