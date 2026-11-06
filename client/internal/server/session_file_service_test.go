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

	"github.com/deploypilot/deploypilot/internal/llm"
	"github.com/gorilla/websocket"
)

func TestAgentRelativeFileErrorsWhenTerminalUnavailable(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "linux" {
		t.Skip("PTY unavailable")
	}
	t.Chdir(t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	const original = "launch-directory sentinel"
	for _, name := range []string{"read", "write", "append", "edit"} {
		if err := os.WriteFile(name+".txt", []byte(original), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var turns atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		calls := []map[string]any{}
		add := func(name string, arguments map[string]any) {
			args, _ := json.Marshal(arguments)
			calls = append(calls, map[string]any{
				"index": len(calls), "id": fmt.Sprintf("call-%d-%d", turns.Load(), len(calls)), "type": "function",
				"function": map[string]any{"name": name, "arguments": string(args)},
			})
		}
		if turns.Add(1) == 1 {
			add("read_file", map[string]any{"path": "read.txt"})
			add("write_file", map[string]any{"path": "write.txt", "content": "replacement"})
			add("append_file", map[string]any{"path": "append.txt", "content": "+append"})
			add("edit_file", map[string]any{"path": "edit.txt", "old_string": "sentinel", "new_string": "changed"})
		} else {
			add("finish", map[string]any{"summary": "Local error-propagation test completed", "success": false})
		}
		payload, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{
			"delta": map[string]any{"tool_calls": calls}, "finish_reason": "tool_calls",
		}}})
		_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", payload)
	}))
	defer provider.Close()

	// Keep the production constructor and executor. Only the model endpoint is
	// replaced with the local deterministic provider above.
	srv, cookie := newLLMServer(t)
	srv.llm = llm.New(llm.Config{BaseURL: provider.URL, APIKey: "test-only"})
	workspace := t.TempDir()
	const workspaceOriginal = "shared-shell workspace sentinel"
	for _, name := range []string{"read", "write", "append", "edit"} {
		if err := os.WriteFile(filepath.Join(workspace, name+".txt"), []byte(workspaceOriginal), 0600); err != nil {
			t.Fatal(err)
		}
	}
	command := "cd " + shellQuote(workspace)
	if runtime.GOOS == "windows" {
		command = "Set-Location -LiteralPath " + psQuote(workspace)
	}
	if code, out, err := srv.newExecutor().Run(context.Background(), command, 10*time.Second, nil); err != nil || code != 0 {
		t.Fatalf("prepare shared-shell workspace: code=%d, error=%v, output=%q", code, err, out)
	}
	shell, err := srv.sharedTerminal()
	if err != nil {
		t.Fatal(err)
	}
	if err := shell.Close(); err != nil {
		t.Fatal(err)
	}
	terminalPath := filepath.Join(srv.historyDir, "terminal.json")
	if err := os.Remove(terminalPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(terminalPath, 0700); err != nil {
		t.Fatal(err)
	}
	t.Log("real PTY changed to a separate workspace; terminal then closed and restart snapshot made unreadable")
	addr, err := srv.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Log("production Server.Start succeeded with valid task history and an unavailable terminal")
	header := http.Header{}
	header.Add("Cookie", cookie.Name+"="+cookie.Value)
	conn, _, err := websocket.DefaultDialer.Dial("ws://"+addr+"/ws/agent", header)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.WriteJSON(agentClientMsg{Type: "start", Task: "Test relative file operations locally"}); err != nil {
		t.Fatal(err)
	}
	done := map[string]bool{}
	errors := map[string]bool{}
	for {
		event := readAgentMessage(t, conn)
		tool, _ := event["tool"].(string)
		if strings.HasSuffix(tool, "_file") {
			switch event["type"] {
			case "tool_done":
				done[tool] = true
				t.Logf("%s: tool_done success=%v", tool, event["success"])
				if event["success"] == true {
					t.Errorf("%s incorrectly reported success while terminal was unavailable", tool)
				}
			case "error":
				text, _ := event["text"].(string)
				errors[tool] = strings.Contains(text, "terminal.json")
				t.Logf("%s: %s", tool, text)
			}
		}
		if event["type"] == "run_state" && event["state"] == "idle" {
			break
		}
	}
	for _, name := range []string{"read", "write", "append", "edit"} {
		tool := name + "_file"
		if !done[tool] || !errors[tool] {
			t.Errorf("%s: want unsuccessful completion and terminal error, got completion=%v terminal_error=%v", tool, done[tool], errors[tool])
		}
		data, err := os.ReadFile(name + ".txt")
		t.Logf("%s.txt after service run: %q", name, data)
		if err != nil || string(data) != original {
			t.Errorf("%s changed launch-directory file: content=%q, error=%v", tool, data, err)
		}
		data, err = os.ReadFile(filepath.Join(workspace, name+".txt"))
		if err != nil || string(data) != workspaceOriginal {
			t.Errorf("%s changed shared-shell workspace file: content=%q, error=%v", tool, data, err)
		}
	}
}
