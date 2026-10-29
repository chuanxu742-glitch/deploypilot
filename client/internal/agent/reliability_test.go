package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/deploypilot/deploypilot/internal/llm"
)

type cancelProbeExecutor struct {
	Executor
	cancel context.CancelFunc
	writes int
}

func (e *cancelProbeExecutor) MachineInfo(context.Context) string { return "local probe" }
func (e *cancelProbeExecutor) Run(ctx context.Context, _ string, _ time.Duration, _ func(string)) (int, string, error) {
	e.cancel()
	return -1, "", ctx.Err()
}
func (e *cancelProbeExecutor) WriteFile(string, string) error { e.writes++; return nil }

func TestCancelledBatchDoesNotWrite(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e := &cancelProbeExecutor{cancel: cancel}
	calls := []map[string]any{
		{"index": 0, "id": "c1", "type": "function", "function": map[string]string{"name": "run_command", "arguments": `{"command":"echo probe"}`}},
		{"index": 1, "id": "c2", "type": "function", "function": map[string]string{"name": "write_file", "arguments": `{"path":"probe-only","content":"changed"}`}},
	}
	payload, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": calls}, "finish_reason": "tool_calls"}}})
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", payload) }))
	defer provider.Close()
	a := New(Config{LLM: llm.New(llm.Config{BaseURL: provider.URL, APIKey: "probe"}), Executor: e})
	err := a.Run(ctx, "cancel batch")
	t.Logf("context=%v Run=%v writes_after_cancel=%d", ctx.Err(), err, e.writes)
	if e.writes != 0 {
		t.Fatal("required behavior is missing")
	}
}

func TestIncompleteStreamCannotSucceed(t *testing.T) {
	for _, ending := range []string{"eof", "length"} {
		t.Run(ending, func(t *testing.T) {
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				if ending == "eof" {
					fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"deployment in progress\"}}]}\n\n")
				} else {
					fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"deployment in progress\"},\"finish_reason\":\"length\"}]}\n\ndata: [DONE]\n\n")
				}
			}))
			defer provider.Close()
			var final Event
			a := New(Config{LLM: llm.New(llm.Config{BaseURL: provider.URL, APIKey: "probe"}), Executor: newFakeExec(), Emit: func(e Event) {
				if e.Type == EventFinal {
					final = e
				}
			}})
			err := a.Run(context.Background(), "deploy")
			t.Logf("ending=%s error=%v final_success=%v text=%q", ending, err, final.Success, final.Text)
			if err == nil || final.Success {
				t.Fatal("required behavior is missing")
			}
		})
	}
}

func TestWindowsChildStopsOnCancellation(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows only")
	}
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	command := fmt.Sprintf("$p=Start-Process powershell.exe -WindowStyle Hidden -PassThru -ArgumentList '-NoProfile','-NonInteractive','-Command','Start-Sleep -Seconds 45'; Set-Content -LiteralPath '%s' -Value $p.Id; Start-Sleep -Seconds 45", strings.ReplaceAll(pidFile, "'", "''"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, _, err := NewShellExecutor().Run(ctx, command, 20*time.Second, nil); done <- err }()
	var pid int
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(pidFile)
		if err == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
			if pid > 0 {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("child did not start")
	}
	defer func() {
		p, _ := os.FindProcess(pid)
		if p != nil {
			p.Kill()
		}
	}()
	cancel()
	select {
	case err := <-done:
		t.Logf("executor_returned=%v child_pid=%d", err, pid)
	case <-time.After(10 * time.Second):
		t.Fatal("executor did not return")
	}
	check := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", fmt.Sprintf("if (Get-Process -Id %d -ErrorAction SilentlyContinue) { 'CHILD_ALIVE' } else { 'CHILD_EXITED' }", pid))
	output, err := check.CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("after cancellation: %s", strings.TrimSpace(string(output)))
	if !strings.Contains(string(output), "CHILD_EXITED") {
		t.Fatal("required behavior is missing")
	}
}

func TestWindowsJobPreservesSuccessAndStopsTimeout(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows only")
	}
	for _, timeout := range []bool{false, true} {
		t.Run(fmt.Sprintf("timeout=%v", timeout), func(t *testing.T) {
			dir := t.TempDir()
			pidFile := filepath.Join(dir, "child.pid")
			quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
			command := "$p=Start-Process powershell.exe -WindowStyle Hidden -PassThru -ArgumentList '-NoProfile','-NonInteractive','-Command','Start-Sleep -Seconds 45' -RedirectStandardOutput " + quote(filepath.Join(dir, "out.txt")) + " -RedirectStandardError " + quote(filepath.Join(dir, "err.txt")) + "; Set-Content -LiteralPath " + quote(pidFile) + " -Value $p.Id"
			duration := 10 * time.Second
			if timeout {
				command += "; Start-Sleep -Seconds 45"
				duration = 2 * time.Second
			}
			exit, _, err := NewShellExecutor().Run(context.Background(), command, duration, nil)
			data, readErr := os.ReadFile(pidFile)
			if readErr != nil {
				t.Fatalf("child pid unavailable: %v; run=%v", readErr, err)
			}
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
			if parseErr != nil {
				t.Fatal(parseErr)
			}
			defer func() {
				p, _ := os.FindProcess(pid)
				if p != nil {
					p.Kill()
				}
			}()
			check := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", fmt.Sprintf("if (Get-Process -Id %d -ErrorAction SilentlyContinue) { 'alive' } else { 'exited' }", pid))
			state, checkErr := check.Output()
			if checkErr != nil {
				t.Fatal(checkErr)
			}
			if timeout {
				if err == nil || !strings.Contains(string(state), "exited") {
					t.Fatalf("timeout did not clean child: error=%v state=%s", err, state)
				}
			} else if err != nil || exit != 0 || !strings.Contains(string(state), "alive") {
				t.Fatalf("successful background service lost: exit=%d error=%v state=%s", exit, err, state)
			}
		})
	}
}
