package server

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/deploypilot/deploypilot/internal/agent"
)

// Exercise decoder cleanup through real process cancellation rather than merely
// closing a decoder directly. The next command must have a fresh decoder.
func TestPartialUTF8CancellationDoesNotLeakIntoNextCommand(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("real Linux process/PTY cancellation")
	}
	for _, executorName := range []string{"standalone", "shared"} {
		for _, stopMode := range []string{"cancel", "timeout"} {
			t.Run(executorName+"/"+stopMode, func(t *testing.T) {
				var run func(context.Context, string, func(string)) (int, string, error)
				if executorName == "shared" {
					shell, err := newSharedShell(t.TempDir())
					if err != nil {
						t.Fatal(err)
					}
					defer shell.Close()
					run = shell.Run
				} else {
					executor := agent.NewShellExecutor()
					run = func(ctx context.Context, command string, callback func(string)) (int, string, error) {
						return executor.Run(ctx, command, 5*time.Second, callback)
					}
				}
				var ctx context.Context
				var cancel context.CancelFunc
				if stopMode == "timeout" {
					ctx, cancel = context.WithTimeout(context.Background(), 600*time.Millisecond)
				} else {
					ctx, cancel = context.WithCancel(context.Background())
				}
				defer cancel()
				var mu sync.Mutex
				var output strings.Builder
				returned := false
				callback := func(chunk string) {
					mu.Lock()
					defer mu.Unlock()
					if returned {
						t.Error("callback after Run returned")
					}
					if !utf8.ValidString(chunk) {
						t.Errorf("invalid UTF-8 callback: %q", chunk)
					}
					output.WriteString(chunk)
				}
				// A printable prefix confirms output delivery; the following incomplete
				// character is held until cancellation closes the stream (or the PTY
				// supplies interrupt/prompt bytes, which establish its invalidity).
				command := "printf '[partial]\\344\\270'; sleep 30"
				// A cooperative SIGINT handler lets the shared shell finish its
				// status side channel and remain available for the next command.
				// Untrapped interruption may intentionally close this terminal.
				if executorName == "shared" {
					command = "trap ':' INT; " + command + "; trap - INT"
				}
				if stopMode == "cancel" {
					go func() {
						ticker := time.NewTicker(5 * time.Millisecond)
						defer ticker.Stop()
						deadline := time.NewTimer(3 * time.Second)
						defer deadline.Stop()
						for {
							select {
							case <-ctx.Done():
								return
							case <-deadline.C:
								cancel()
								return
							case <-ticker.C:
								mu.Lock()
								ready := strings.Contains(output.String(), "[partial]")
								mu.Unlock()
								if ready {
									time.Sleep(40 * time.Millisecond)
									cancel()
									return
								}
							}
						}
					}()
				}
				started := time.Now()
				_, _, err := run(ctx, command, callback)
				if err == nil {
					t.Fatal("cancelled command succeeded")
				}
				if elapsed := time.Since(started); elapsed > 4*time.Second {
					t.Fatalf("slow cancellation: %s", elapsed)
				}
				mu.Lock()
				returned = true
				got := output.String()
				mu.Unlock()
				if !strings.Contains(got, "[partial]�") {
					t.Fatalf("incomplete character not flushed: %q", got)
				}
				nextCtx, nextCancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer nextCancel()
				var next strings.Builder
				code, captured, err := run(nextCtx, "printf '[next]中[end]\\n'", func(chunk string) {
					if !utf8.ValidString(chunk) {
						t.Errorf("invalid next-command UTF-8: %q", chunk)
					}
					next.WriteString(chunk)
				})
				if err != nil || code != 0 {
					t.Fatalf("next command failed: code=%d err=%v captured=%q", code, err, captured)
				}
				if !strings.Contains(next.String(), "[next]中[end]") || strings.Contains(next.String(), "�") {
					t.Fatalf("next command inherited partial UTF-8: %q", next.String())
				}
			})
		}
	}
}
