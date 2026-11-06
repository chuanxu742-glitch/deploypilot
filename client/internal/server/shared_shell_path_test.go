package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deploypilot/deploypilot/internal/agent"
	"github.com/deploypilot/deploypilot/internal/llm"
)

// Exercise the real persistent PTY, not a copy of the generated script. A
// command must still execute after PATH changes, including the recovery command.
func TestSharedShellRunsCommandsWithoutDecoderInPath(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux shell regression")
	}
	shell, err := newSharedShell(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer shell.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	run := func(command string, wantCode int) string {
		t.Helper()
		code, out, err := shell.Run(ctx, command, nil)
		if err != nil || code != wantCode {
			t.Fatalf("command %q: code=%d want=%d err=%v output=%s", command, code, wantCode, err, out)
		}
		return out
	}
	// Save PATH inside the child shell so the test never changes the Go process.
	run("export DP_PATH_TEST_ORIGINAL=$PATH; export PATH="+shellQuote(t.TempDir()), 0)
	file := filepath.Join(t.TempDir(), "command-executed")
	want := "literal 'quote', $HOME and `backticks`\n中文\n"
	run("printf '%s' "+shellQuote(want)+" > "+shellQuote(file), 0)
	got, err := os.ReadFile(file)
	if err != nil || string(got) != want {
		t.Fatalf("command reported success without writing the expected file: content=%q err=%v", got, err)
	}
	run("false", 1)
	dir := t.TempDir()
	run("cd "+shellQuote(dir)+"\nexport DP_PATH_TEST_VALUE='persistent value'\n", 0)
	if shell.Cwd() != dir {
		t.Fatalf("cwd=%q want=%q", shell.Cwd(), dir)
	}
	if out := run("printf '%s' \"$DP_PATH_TEST_VALUE\"", 0); !strings.Contains(out, "persistent value") {
		t.Fatalf("environment did not persist: %s", out)
	}
	run("export PATH=$DP_PATH_TEST_ORIGINAL; unset DP_PATH_TEST_ORIGINAL", 0)
	// Merely receiving exit=0 is insufficient: the shell must resolve a utility
	// after the recovery command has actually restored PATH.
	run("command -v ls >/dev/null", 0)
}

// Keep execution real while avoiding the unrelated hardware/network probes in
// MachineInfo. The provider and control-plane connections are local HTTP/WS.
type pathRegressionExecutor struct{ *sessionExecutor }

func (e pathRegressionExecutor) MachineInfo(context.Context) string { return "isolated Linux test" }

func TestAgentExecutesAfterPathChangeOverWebSocket(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux shell regression")
	}
	file := filepath.Join(t.TempDir(), "service-command-executed")
	commands := []string{
		"export PATH=" + shellQuote(t.TempDir()),
		"printf service-executed > " + shellQuote(file),
	}
	var requests atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(requests.Add(1)) - 1
		name := "finish"
		arguments := map[string]any{"summary": "test complete", "success": true}
		if n < len(commands) {
			name = "run_command"
			arguments = map[string]any{"command": commands[n], "timeout_seconds": 5}
		}
		args, _ := json.Marshal(arguments)
		encoded, _ := json.Marshal(string(args))
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"path%d\",\"type\":\"function\",\"function\":{\"name\":%q,\"arguments\":%s}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n", n, name, encoded)
	}))
	defer provider.Close()
	srv, cookie := newLLMServer(t)
	srv.llm = llm.New(llm.Config{BaseURL: provider.URL, APIKey: "local-test-only"})
	srv.newExecutor = func() agent.Executor {
		return pathRegressionExecutor{&sessionExecutor{server: srv, files: agent.NewShellExecutor()}}
	}
	conn := agentSocket(t, srv, cookie)
	if err := conn.WriteJSON(agentClientMsg{Type: "start", Task: "execute isolated PATH regression"}); err != nil {
		t.Fatal(err)
	}
	completed := 0
	for {
		event := readAgentMessage(t, conn)
		if event["type"] == "tool_done" && event["tool"] == "run_command" {
			completed++
			if event["success"] != true || event["exitCode"] != float64(0) {
				t.Fatalf("command was not reported successful: %v", event)
			}
		}
		if event["type"] == "run_state" && event["state"] == "idle" {
			break
		}
	}
	if completed != len(commands) {
		t.Fatalf("completed %d commands, want %d", completed, len(commands))
	}
	if got, err := os.ReadFile(file); err != nil || string(got) != "service-executed" {
		t.Fatalf("service reported success without executing: content=%q err=%v", got, err)
	}
}

func TestSharedShellDoesNotInvokeShadowedDecoder(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux shell regression")
	}
	shell, err := newSharedShell(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer shell.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if code, out, err := shell.Run(ctx, "base64() { return 73; }", nil); err != nil || code != 0 {
		t.Fatalf("setup code=%d err=%v output=%s", code, err, out)
	}
	file := filepath.Join(t.TempDir(), "executed")
	if code, out, err := shell.Run(ctx, "printf expected > "+shellQuote(file), nil); err != nil || code != 0 {
		t.Fatalf("execution code=%d err=%v output=%s", code, err, out)
	}
	if got, err := os.ReadFile(file); err != nil || string(got) != "expected" {
		t.Fatalf("shadowed decoder prevented execution: content=%q err=%v", got, err)
	}
}
